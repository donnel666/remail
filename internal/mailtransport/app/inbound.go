package app

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/donnel666/remail/internal/platform"
)

const inboundProcessingStaleAge = 2 * time.Minute

type InboundMailRepository interface {
	CreateMany(ctx context.Context, mails []domain.InboundMail) error
	FindByID(ctx context.Context, id uint) (*domain.InboundMail, error)
	ClaimProcessing(ctx context.Context, id uint) (bool, error)
	ClaimDispatchable(ctx context.Context, limit int, staleBefore time.Time) ([]domain.InboundMail, error)
	MarkPending(ctx context.Context, id uint, safeError string) error
	MarkStored(ctx context.Context, id uint) error
	MarkFailed(ctx context.Context, id uint, safeError string) error
}

type InboundResourceResolver interface {
	ResolveInboundRecipient(ctx context.Context, email string) (*domain.InboundRecipient, error)
}

type InboundMailQueue interface {
	EnqueueInboundProcess(ctx context.Context, task InboundProcessTask) error
	EnqueueInboundDispatch(ctx context.Context, delay time.Duration) error
}

type InboundRawMessage struct {
	EnvelopeFrom string
	Recipients   []domain.InboundRecipient
	RemoteAddr   string
	ContentBytes []byte
	Content      io.Reader
	ContentSize  int64
}

type InboundProcessTask struct {
	InboundMailID uint   `json:"inboundMailId"`
	ObjectKey     string `json:"objectKey"`
}

type InboundDispatchResult struct {
	Attempted int
	Queued    int
	Failed    int
}

type InboundService struct {
	repo     InboundMailRepository
	resolver InboundResourceResolver
	files    governanceapp.FilePort
	queue    InboundMailQueue
	logs     SystemLogPort
	now      func() time.Time
}

func NewInboundService(repo InboundMailRepository, resolver InboundResourceResolver, files governanceapp.FilePort, queue InboundMailQueue, logs SystemLogPort) *InboundService {
	return &InboundService{
		repo:     repo,
		resolver: resolver,
		files:    files,
		queue:    queue,
		logs:     logs,
		now:      time.Now,
	}
}

func (s *InboundService) ResolveRecipient(ctx context.Context, email string) (*domain.InboundRecipient, error) {
	email = normalizeEmailAddress(email)
	if email == "" {
		return nil, domain.ErrInboundRecipientRejected
	}
	return s.resolver.ResolveInboundRecipient(ctx, email)
}

func (s *InboundService) Accept(ctx context.Context, message InboundRawMessage) ([]domain.InboundMail, error) {
	if (len(message.ContentBytes) == 0 && message.Content == nil) || len(message.Recipients) == 0 {
		return nil, domain.ErrInboundRecipientRejected
	}

	now := s.now().UTC()
	objectKey := inboundObjectKey(now, platform.NewUUIDV7String())
	stored, err := s.saveRawMessage(ctx, objectKey, message)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", domain.ErrInboundStorageUnavailable, safeDiagnostic(err.Error()))
	}

	mails := make([]domain.InboundMail, 0, len(message.Recipients))
	for _, recipient := range message.Recipients {
		if normalizeEmailAddress(recipient.Email) == "" ||
			recipient.ResourceID == 0 ||
			recipient.OwnerUserID == 0 ||
			!domain.IsValidInboundResourceType(recipient.ResourceType) {
			return nil, domain.ErrInboundRecipientRejected
		}
		mails = append(mails, *domain.NewInboundMail(
			normalizeEmailAddress(message.EnvelopeFrom),
			recipient,
			stored.ObjectKey,
			now,
		))
	}
	if err := s.repo.CreateMany(ctx, mails); err != nil {
		return nil, fmt.Errorf("%w: %s", domain.ErrInboundStorageUnavailable, safeDiagnostic(err.Error()))
	}

	for _, mail := range mails {
		if err := s.enqueueInbound(ctx, InboundProcessTask{InboundMailID: mail.ID, ObjectKey: mail.SourceObjectKey}); err != nil {
			_ = s.repo.MarkPending(ctx, mail.ID, "Inbound mail task enqueue failed.")
			writeSystemLog(ctx, s.logs, "error", "mail.inbound_enqueue_failed", "", "inbound_mail", fmt.Sprintf("%d", mail.ID), "Inbound mail task could not be queued.", err)
			continue
		}
	}
	return mails, nil
}

func (s *InboundService) saveRawMessage(ctx context.Context, objectKey string, message InboundRawMessage) (*governancedomain.StoredPrivateFile, error) {
	if message.Content != nil {
		if message.ContentSize <= 0 {
			return nil, fmt.Errorf("inbound message size is required")
		}
		return s.files.SavePrivateStream(ctx, governancedomain.PrivateFileStream{
			ObjectKey:   objectKey,
			FileName:    "inbound.eml",
			ContentType: "message/rfc822",
			Content:     message.Content,
			Size:        message.ContentSize,
		})
	}
	return s.files.SavePrivate(ctx, governancedomain.PrivateFile{
		ObjectKey:    objectKey,
		FileName:     "inbound.eml",
		ContentType:  "message/rfc822",
		ContentBytes: message.ContentBytes,
	})
}

func (s *InboundService) Process(ctx context.Context, task InboundProcessTask, finalAttempt bool) error {
	if task.InboundMailID == 0 || strings.TrimSpace(task.ObjectKey) == "" {
		return fmt.Errorf("%w: inbound mail task invalid", domain.ErrInboundStorageUnavailable)
	}

	mail, err := s.repo.FindByID(ctx, task.InboundMailID)
	if err != nil {
		return err
	}
	if mail == nil {
		return fmt.Errorf("%w: inbound mail not found", domain.ErrInboundStorageUnavailable)
	}
	if mail.Status == domain.InboundStatusStored || mail.Status == domain.InboundStatusFailed {
		return nil
	}
	if mail.SourceObjectKey != task.ObjectKey {
		_ = s.repo.MarkFailed(ctx, task.InboundMailID, "Inbound mail object mismatch.")
		writeSystemLog(ctx, s.logs, "error", "mail.inbound_object_mismatch", "", "inbound_mail", fmt.Sprintf("%d", task.InboundMailID), "Inbound mail task object key mismatched.", "Inbound mail object mismatch.")
		return nil
	}

	claimed, err := s.repo.ClaimProcessing(ctx, task.InboundMailID)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	if _, err := s.files.ReadPrivate(ctx, task.ObjectKey); err != nil {
		if finalAttempt {
			_ = s.repo.MarkFailed(ctx, task.InboundMailID, "Inbound mail object unavailable.")
			writeSystemLog(ctx, s.logs, "error", "mail.inbound_failed", "", "inbound_mail", fmt.Sprintf("%d", task.InboundMailID), "Inbound mail processing failed.", err)
		} else {
			_ = s.repo.MarkPending(ctx, task.InboundMailID, "Inbound mail object unavailable.")
			writeSystemLog(ctx, s.logs, "warning", "mail.inbound_retry", "", "inbound_mail", fmt.Sprintf("%d", task.InboundMailID), "Inbound mail processing will retry.", err)
		}
		return fmt.Errorf("%w: %s", domain.ErrInboundStorageUnavailable, safeDiagnostic(err.Error()))
	}
	return s.repo.MarkStored(ctx, task.InboundMailID)
}

func (s *InboundService) DispatchPending(ctx context.Context, limit int) (*InboundDispatchResult, error) {
	if limit <= 0 {
		limit = 100
	}
	mails, err := s.repo.ClaimDispatchable(ctx, limit, s.now().Add(-inboundProcessingStaleAge))
	if err != nil {
		return nil, err
	}
	result := &InboundDispatchResult{Attempted: len(mails)}
	for _, mail := range mails {
		if err := s.enqueueInbound(ctx, InboundProcessTask{InboundMailID: mail.ID, ObjectKey: mail.SourceObjectKey}); err != nil {
			result.Failed++
			_ = s.repo.MarkPending(ctx, mail.ID, "Inbound mail task enqueue failed.")
			writeSystemLog(ctx, s.logs, "error", "mail.inbound_dispatch_failed", "", "inbound_mail", fmt.Sprintf("%d", mail.ID), "Inbound mail dispatcher could not queue task.", err)
			continue
		}
		result.Queued++
	}
	return result, nil
}

func (s *InboundService) ScheduleDispatcher(ctx context.Context, delay time.Duration) {
	if s == nil || s.queue == nil {
		return
	}
	if err := s.queue.EnqueueInboundDispatch(ctx, delay); err != nil {
		writeSystemLog(ctx, s.logs, "error", "mail.inbound_dispatcher_enqueue_failed", "", "inbound_mail", "dispatcher", "Inbound mail dispatcher could not be queued.", err)
	}
}

func (s *InboundService) enqueueInbound(ctx context.Context, task InboundProcessTask) error {
	if s.queue == nil {
		return fmt.Errorf("inbound mail queue is unavailable")
	}
	return s.queue.EnqueueInboundProcess(ctx, task)
}

func normalizeEmailAddress(value string) string {
	value = bodyValue(value)
	return strings.ToLower(value)
}

func inboundObjectKey(now time.Time, id string) string {
	return path.Join("mailtransport", "inbound", now.Format("2006"), now.Format("01"), now.Format("02"), id+".eml")
}
