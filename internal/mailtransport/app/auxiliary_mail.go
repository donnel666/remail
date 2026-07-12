package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/mailtransport/domain"
)

const (
	AuxiliaryMailDefaultLimit = 20
	AuxiliaryMailMaxLimit     = 100
	AuxiliaryMailMaxSearch    = 120
)

type AuxiliaryMailRepository interface {
	MicrosoftResourceExists(ctx context.Context, resourceID uint) (bool, error)
	ListByMicrosoftResource(ctx context.Context, filter AuxiliaryMailFilter) ([]domain.InboundMail, int64, error)
	FindByMicrosoftResource(ctx context.Context, resourceID, messageID uint) (*domain.InboundMail, error)
}

type MicrosoftBindingQueryRepository interface {
	FindByResourceIDs(ctx context.Context, resourceIDs []uint) (map[uint]domain.MicrosoftBindingMailbox, error)
}

type AuxiliaryMailQueryPort interface {
	List(ctx context.Context, filter AuxiliaryMailFilter) (*AuxiliaryMailPage, error)
	Get(ctx context.Context, request AuxiliaryMailDetailRequest) (*AuxiliaryMessageDetail, error)
}

type AuxiliaryMailFilter struct {
	ResourceID uint
	Search     string
	Offset     int
	Limit      int
}

type AuxiliaryBindingSummary struct {
	ID           uint
	EmailAddress string
	Status       domain.MicrosoftBindingStatus
	UpdatedAt    time.Time
}

type AuxiliaryMessageStatus string

const (
	AuxiliaryMessageReceived AuxiliaryMessageStatus = "received"
	AuxiliaryMessageMatched  AuxiliaryMessageStatus = "matched"
	AuxiliaryMessageIgnored  AuxiliaryMessageStatus = "ignored"
)

type AuxiliaryMessageSummary struct {
	ID               uint
	Recipient        string
	Sender           string
	Subject          string
	Preview          string
	Status           AuxiliaryMessageStatus
	VerificationCode *string
	OrderNo          *string
	ReceivedAt       time.Time
}

type AuxiliaryMessageDetail struct {
	AuxiliaryMessageSummary
	Body            string
	MatchDiagnostic *string
}

type AuxiliaryMailPage struct {
	Binding *AuxiliaryBindingSummary
	Items   []AuxiliaryMessageSummary
	Total   int64
	Offset  int
	Limit   int
}

type AuxiliaryMailDetailRequest struct {
	ResourceID     uint
	MessageID      uint
	OperatorUserID uint
	RequestID      string
	Path           string
}

type AuxiliaryMailQueryService struct {
	repo          AuxiliaryMailRepository
	bindings      MicrosoftBindingQueryRepository
	files         governanceapp.FilePort
	operationLogs governanceapp.OperationLogPort
	systemLogs    SystemLogPort
}

func NewAuxiliaryMailQueryService(
	repo AuxiliaryMailRepository,
	bindings MicrosoftBindingQueryRepository,
	files governanceapp.FilePort,
	operationLogs governanceapp.OperationLogPort,
	systemLogs SystemLogPort,
) *AuxiliaryMailQueryService {
	return &AuxiliaryMailQueryService{
		repo:          repo,
		bindings:      bindings,
		files:         files,
		operationLogs: operationLogs,
		systemLogs:    systemLogs,
	}
}

func (s *AuxiliaryMailQueryService) List(ctx context.Context, filter AuxiliaryMailFilter) (*AuxiliaryMailPage, error) {
	if s == nil || s.repo == nil || s.bindings == nil || filter.ResourceID == 0 || filter.Offset < 0 {
		return nil, domain.ErrInvalidAuxiliaryMailQuery
	}
	filter.Search = strings.TrimSpace(filter.Search)
	if utf8.RuneCountInString(filter.Search) > AuxiliaryMailMaxSearch {
		return nil, domain.ErrInvalidAuxiliaryMailQuery
	}
	if filter.Limit <= 0 {
		filter.Limit = AuxiliaryMailDefaultLimit
	}
	if filter.Limit > AuxiliaryMailMaxLimit {
		return nil, domain.ErrInvalidAuxiliaryMailQuery
	}

	exists, err := s.repo.MicrosoftResourceExists(ctx, filter.ResourceID)
	if err != nil {
		return nil, fmt.Errorf("%w: check auxiliary mail resource: %w", domain.ErrAuxiliaryMailUnavailable, err)
	}
	if !exists {
		return nil, domain.ErrAuxiliaryResourceNotFound
	}

	bindings, err := s.bindings.FindByResourceIDs(ctx, []uint{filter.ResourceID})
	if err != nil {
		return nil, fmt.Errorf("%w: load auxiliary mailbox binding: %w", domain.ErrAuxiliaryMailUnavailable, err)
	}
	var binding *AuxiliaryBindingSummary
	if current, ok := bindings[filter.ResourceID]; ok {
		binding = &AuxiliaryBindingSummary{
			ID:           current.ID,
			EmailAddress: current.BindingAddress,
			Status:       current.Status,
			UpdatedAt:    current.UpdatedAt,
		}
	}

	rows, total, err := s.repo.ListByMicrosoftResource(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("%w: list auxiliary mailbox messages: %w", domain.ErrAuxiliaryMailUnavailable, err)
	}
	items := make([]AuxiliaryMessageSummary, len(rows))
	for i := range rows {
		items[i] = auxiliaryMessageSummary(rows[i])
	}
	return &AuxiliaryMailPage{
		Binding: binding,
		Items:   items,
		Total:   total,
		Offset:  filter.Offset,
		Limit:   filter.Limit,
	}, nil
}

func (s *AuxiliaryMailQueryService) Get(ctx context.Context, request AuxiliaryMailDetailRequest) (*AuxiliaryMessageDetail, error) {
	if s == nil || s.repo == nil || s.files == nil || s.operationLogs == nil ||
		request.ResourceID == 0 || request.MessageID == 0 || request.OperatorUserID == 0 {
		return nil, domain.ErrInvalidAuxiliaryMailQuery
	}
	row, err := s.repo.FindByMicrosoftResource(ctx, request.ResourceID, request.MessageID)
	if err != nil {
		return nil, fmt.Errorf("%w: find auxiliary mailbox message: %w", domain.ErrAuxiliaryMailUnavailable, err)
	}
	if row == nil {
		return nil, domain.ErrAuxiliaryMessageNotFound
	}
	if strings.TrimSpace(row.SourceObjectKey) == "" {
		return nil, domain.ErrAuxiliaryMailUnavailable
	}

	stored, err := s.files.ReadPrivate(ctx, row.SourceObjectKey)
	if err != nil || stored == nil {
		writeSystemLog(
			ctx,
			s.systemLogs,
			"error",
			"mail.auxiliary_message_read_failed",
			strings.TrimSpace(request.RequestID),
			"auxiliary_message",
			fmt.Sprintf("%d", row.ID),
			"Auxiliary mailbox message could not be read.",
			"private object read failed",
		)
		return nil, domain.ErrAuxiliaryMailUnavailable
	}

	parsed := parseInboundMessage(stored.ContentBytes, auxiliaryReceivedAt(*row))
	merged := mergeParsedAuxiliaryMessage(*row, parsed)
	if err := s.operationLogs.Create(ctx, &governancedomain.OperationLog{
		OperatorUserID: request.OperatorUserID,
		OperationType:  "mailtransport.auxiliary_message.read",
		ResourceType:   "auxiliary_message",
		ResourceID:     fmt.Sprintf("%d:%d", request.ResourceID, request.MessageID),
		Path:           strings.TrimSpace(request.Path),
		Result:         "success",
		SafeSummary:    "Auxiliary mailbox message body read.",
		RequestID:      strings.TrimSpace(request.RequestID),
	}); err != nil {
		writeSystemLog(
			ctx,
			s.systemLogs,
			"error",
			"mail.auxiliary_message_audit_failed",
			strings.TrimSpace(request.RequestID),
			"auxiliary_message",
			fmt.Sprintf("%d", row.ID),
			"Auxiliary mailbox message read audit could not be recorded.",
			"operation log unavailable",
		)
		return nil, domain.ErrAuxiliaryMailUnavailable
	}
	return &merged, nil
}

func auxiliaryMessageSummary(row domain.InboundMail) AuxiliaryMessageSummary {
	var code *string
	if value := strings.TrimSpace(row.VerificationCode); value != "" {
		code = &value
	}
	return AuxiliaryMessageSummary{
		ID:               row.ID,
		Recipient:        strings.TrimSpace(row.Recipient),
		Sender:           strings.TrimSpace(row.HeaderFrom),
		Subject:          strings.TrimSpace(row.Subject),
		Preview:          strings.TrimSpace(row.BodyPreview),
		Status:           auxiliaryMessageStatus(row.Status),
		VerificationCode: code,
		OrderNo:          nil,
		ReceivedAt:       auxiliaryReceivedAt(row),
	}
}

func mergeParsedAuxiliaryMessage(row domain.InboundMail, parsed parsedInboundMessage) AuxiliaryMessageDetail {
	if value := strings.TrimSpace(parsed.Summary.HeaderFrom); value != "" {
		row.HeaderFrom = value
	}
	if value := strings.TrimSpace(parsed.Summary.Subject); value != "" {
		row.Subject = value
	}
	if value := strings.TrimSpace(parsed.Summary.BodyPreview); value != "" {
		row.BodyPreview = value
	}
	if value := strings.TrimSpace(parsed.Summary.VerificationCode); value != "" {
		row.VerificationCode = value
	}
	if !parsed.Summary.ReceivedAt.IsZero() {
		receivedAt := parsed.Summary.ReceivedAt.UTC()
		row.ReceivedAt = &receivedAt
	}
	diagnostic := strings.TrimSpace(parsed.Diagnostic)
	if diagnostic == "" && row.Status == domain.InboundStatusFailed {
		diagnostic = strings.TrimSpace(row.FailureReason)
	}
	var diagnosticPtr *string
	if diagnostic != "" {
		diagnosticPtr = &diagnostic
	}
	return AuxiliaryMessageDetail{
		AuxiliaryMessageSummary: auxiliaryMessageSummary(row),
		Body:                    parsed.Body,
		MatchDiagnostic:         diagnosticPtr,
	}
}

func auxiliaryMessageStatus(status domain.InboundStatus) AuxiliaryMessageStatus {
	if status == domain.InboundStatusFailed {
		return AuxiliaryMessageIgnored
	}
	return AuxiliaryMessageReceived
}

func auxiliaryReceivedAt(row domain.InboundMail) time.Time {
	if row.ReceivedAt != nil && !row.ReceivedAt.IsZero() {
		return row.ReceivedAt.UTC()
	}
	return row.CreatedAt.UTC()
}

func IsAuxiliaryMailNotFound(err error) bool {
	return errors.Is(err, domain.ErrAuxiliaryResourceNotFound) || errors.Is(err, domain.ErrAuxiliaryMessageNotFound)
}
