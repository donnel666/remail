package app

import (
	"context"
	"errors"
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

type InboundMailRepository interface {
	CreateMany(ctx context.Context, mails []domain.InboundMail) error
	FindByID(ctx context.Context, id uint) (*domain.InboundMail, error)
	ListPending(ctx context.Context, limit int) ([]domain.InboundMail, error)
	ActivateProcessing(ctx context.Context, id uint, generation uint64) (bool, error)
	SaveParsedSummary(ctx context.Context, id uint, generation uint64, summary domain.InboundMailSummary) (bool, error)
	ReleasePending(ctx context.Context, id uint, generation uint64, safeError string) (bool, error)
	RecordProcessFailure(ctx context.Context, id uint, generation uint64, safeError string, retryable bool) (terminal bool, applied bool, err error)
	MarkStored(ctx context.Context, id uint, generation uint64) (bool, error)
	MarkFailed(ctx context.Context, id uint, safeError string) error
}

type InboundResourceResolver interface {
	ResolveInboundRecipient(ctx context.Context, email string) (*domain.InboundRecipient, error)
}

type InboundMailQueue interface {
	EnqueueInboundProcess(ctx context.Context, task InboundProcessTask) (bool, error)
	EnqueueInboundDispatch(ctx context.Context, delay time.Duration) error
}

type InboundConsumerPort interface {
	IngestInboundMail(ctx context.Context, req InboundConsumeRequest) error
}

type InboundConsumeRequest struct {
	EmailResourceID uint
	ResourceType    domain.InboundResourceType
	Recipient       string
	EnvelopeFrom    string
	Raw             []byte
	ReceivedAt      time.Time
}

// InboundConsumeFailure marks an explicit business outcome from an inbound
// consumer. Unknown consumer errors are infrastructure failures and must not
// consume the business retry budget.
type InboundConsumeFailure struct {
	SafeMessage string
	Retryable   bool
	Cause       error
}

func (e *InboundConsumeFailure) Error() string {
	if e == nil {
		return "inbound consumer failure"
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	if strings.TrimSpace(e.SafeMessage) != "" {
		return e.SafeMessage
	}
	return "inbound consumer failure"
}

func (e *InboundConsumeFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
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
	InboundMailID     uint   `json:"inboundMailId"`
	ProcessGeneration uint64 `json:"processGeneration"`
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
	consumer InboundConsumerPort
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

func (s *InboundService) SetConsumer(consumer InboundConsumerPort) {
	if s == nil {
		return
	}
	s.consumer = consumer
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
			objectKey,
			now,
		))
	}
	if err := s.repo.CreateMany(ctx, mails); err != nil {
		return nil, fmt.Errorf("%w: %s", domain.ErrInboundStorageUnavailable, safeDiagnostic(err.Error()))
	}
	stored, err := s.saveRawMessage(ctx, objectKey, message)
	if err != nil || stored == nil || stored.ObjectKey != objectKey {
		reason := "Inbound mail object could not be stored."
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		for _, mail := range mails {
			_ = s.repo.MarkFailed(cleanupCtx, mail.ID, reason)
		}
		if err == nil {
			err = fmt.Errorf("stored inbound object key mismatch")
		}
		return nil, fmt.Errorf("%w: %s", domain.ErrInboundStorageUnavailable, safeDiagnostic(err.Error()))
	}

	for _, mail := range mails {
		accepted, err := s.enqueueInbound(ctx, InboundProcessTask{InboundMailID: mail.ID, ProcessGeneration: mail.ProcessGeneration})
		if err != nil {
			writeSystemLog(ctx, s.logs, "error", "mail.inbound_enqueue_failed", "", "inbound_mail", fmt.Sprintf("%d", mail.ID), "Inbound mail task could not be queued.", err)
			continue
		}
		if !accepted {
			continue
		}
		if _, err := s.repo.ActivateProcessing(ctx, mail.ID, mail.ProcessGeneration); err != nil {
			writeSystemLog(ctx, s.logs, "error", "mail.inbound_activation_failed", "", "inbound_mail", fmt.Sprintf("%d", mail.ID), "Inbound mail task was queued but could not be activated.", err)
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
	if task.InboundMailID == 0 || task.ProcessGeneration == 0 {
		return fmt.Errorf("%w: inbound mail task invalid", domain.ErrInboundStorageUnavailable)
	}

	mail, err := s.repo.FindByID(ctx, task.InboundMailID)
	if err != nil {
		return s.inboundInfrastructureFailure(ctx, task, finalAttempt, "Inbound mail could not be loaded.", err)
	}
	if mail == nil {
		return fmt.Errorf("%w: inbound mail not found", domain.ErrInboundStorageUnavailable)
	}
	if mail.ProcessGeneration != task.ProcessGeneration || mail.Status == domain.InboundStatusStored || mail.Status == domain.InboundStatusFailed {
		return nil
	}
	if mail.Status == domain.InboundStatusPending {
		activated, err := s.repo.ActivateProcessing(ctx, task.InboundMailID, task.ProcessGeneration)
		if err != nil {
			return s.inboundInfrastructureFailure(ctx, task, finalAttempt, "Inbound mail could not be activated.", err)
		}
		if !activated {
			mail, err = s.repo.FindByID(ctx, task.InboundMailID)
			if err != nil {
				return s.inboundInfrastructureFailure(ctx, task, finalAttempt, "Inbound mail could not be reloaded.", err)
			}
			if mail == nil || mail.ProcessGeneration != task.ProcessGeneration || mail.Status != domain.InboundStatusProcessing {
				return nil
			}
		}
	} else if mail.Status != domain.InboundStatusProcessing {
		return nil
	}
	if ctx.Err() != nil {
		return s.inboundInfrastructureFailure(ctx, task, finalAttempt, "Inbound mail processing interrupted.", ctx.Err())
	}
	file, err := s.files.ReadPrivate(ctx, mail.SourceObjectKey)
	if err != nil || file == nil {
		return s.inboundInfrastructureFailure(ctx, task, finalAttempt, "Inbound mail object unavailable.", fmt.Errorf("%w: inbound mail object unavailable", domain.ErrInboundStorageUnavailable))
	}
	if mail.ParsedAt == nil {
		parsed := parseInboundMessage(file.ContentBytes, mail.CreatedAt)
		applied, err := s.repo.SaveParsedSummary(ctx, task.InboundMailID, task.ProcessGeneration, parsed.Summary)
		if err != nil {
			return s.inboundInfrastructureFailure(ctx, task, finalAttempt, "Inbound mail summary could not be stored.", fmt.Errorf("%w: %s", domain.ErrInboundStorageUnavailable, safeDiagnostic(err.Error())))
		}
		if !applied {
			return nil
		}
	}
	if mail.ResourceType == domain.InboundResourceDomain && s.consumer != nil {
		if err := s.consumer.IngestInboundMail(ctx, InboundConsumeRequest{
			EmailResourceID: mail.ResourceID,
			ResourceType:    mail.ResourceType,
			Recipient:       mail.Recipient,
			EnvelopeFrom:    mail.EnvelopeFrom,
			Raw:             file.ContentBytes,
			ReceivedAt:      mail.CreatedAt,
		}); err != nil {
			var failure *InboundConsumeFailure
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && errors.As(err, &failure) {
				return s.inboundBusinessFailure(ctx, task, finalAttempt, failure)
			}
			return s.inboundInfrastructureFailure(ctx, task, finalAttempt, "Inbound mail consumer is temporarily unavailable.", err)
		}
	}
	if _, err = s.repo.MarkStored(ctx, task.InboundMailID, task.ProcessGeneration); err != nil {
		return s.inboundInfrastructureFailure(ctx, task, finalAttempt, "Inbound mail completion could not be stored.", err)
	}
	return nil
}

func (s *InboundService) DispatchPending(ctx context.Context, limit int) (*InboundDispatchResult, error) {
	if limit <= 0 {
		limit = 100
	}
	mails, err := s.repo.ListPending(ctx, limit)
	if err != nil {
		return nil, err
	}
	result := &InboundDispatchResult{Attempted: len(mails)}
	for _, mail := range mails {
		accepted, err := s.enqueueInbound(ctx, InboundProcessTask{InboundMailID: mail.ID, ProcessGeneration: mail.ProcessGeneration})
		if err != nil {
			result.Failed++
			writeSystemLog(ctx, s.logs, "error", "mail.inbound_dispatch_failed", "", "inbound_mail", fmt.Sprintf("%d", mail.ID), "Inbound mail dispatcher could not queue task.", err)
			continue
		}
		if !accepted {
			continue
		}
		result.Queued++
		if _, err := s.repo.ActivateProcessing(ctx, mail.ID, mail.ProcessGeneration); err != nil {
			result.Failed++
			writeSystemLog(ctx, s.logs, "error", "mail.inbound_activation_failed", "", "inbound_mail", fmt.Sprintf("%d", mail.ID), "Inbound mail task was queued but could not be activated.", err)
		}
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

func (s *InboundService) enqueueInbound(ctx context.Context, task InboundProcessTask) (bool, error) {
	if s.queue == nil {
		return false, fmt.Errorf("inbound mail queue is unavailable")
	}
	return s.queue.EnqueueInboundProcess(ctx, task)
}

func (s *InboundService) inboundInfrastructureFailure(ctx context.Context, task InboundProcessTask, finalAttempt bool, reason string, cause error) error {
	if !finalAttempt {
		return cause
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	applied, err := s.repo.ReleasePending(cleanupCtx, task.InboundMailID, task.ProcessGeneration, reason)
	if err != nil {
		return fmt.Errorf("%w: release inbound mail pending: %s", cause, safeDiagnostic(err.Error()))
	}
	if applied {
		writeSystemLog(cleanupCtx, s.logs, "warning", "mail.inbound_infrastructure_released", "", "inbound_mail", fmt.Sprintf("%d", task.InboundMailID), "Inbound mail was released for retry after infrastructure failure.", cause)
		s.ScheduleDispatcher(cleanupCtx, 0)
	}
	return nil
}

func (s *InboundService) inboundBusinessFailure(ctx context.Context, task InboundProcessTask, finalAttempt bool, failure *InboundConsumeFailure) error {
	safeMessage := "Inbound mail could not be consumed."
	if message := strings.TrimSpace(failure.SafeMessage); message != "" {
		safeMessage = message
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	terminal, applied, err := s.repo.RecordProcessFailure(persistCtx, task.InboundMailID, task.ProcessGeneration, safeMessage, failure.Retryable)
	if err != nil {
		return s.inboundInfrastructureFailure(ctx, task, finalAttempt, "Inbound mail failure state could not be stored.", errors.Join(failure, err))
	}
	if applied && !terminal {
		s.ScheduleDispatcher(persistCtx, time.Second)
	}
	return nil
}

func normalizeEmailAddress(value string) string {
	value = bodyValue(value)
	return strings.ToLower(value)
}

func inboundObjectKey(now time.Time, id string) string {
	return path.Join("mailtransport", "inbound", now.Format("2006"), now.Format("01"), now.Format("02"), id+".eml")
}
