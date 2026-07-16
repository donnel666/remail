package app

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/mailmatch/domain"
)

const (
	defaultAdminMessageLimit = 20
	maxAdminMessageLimit     = 100
	maxAdminMessageSearch    = 120
)

type AdminMessageSummary struct {
	ID               uint
	Mailbox          string
	Recipient        string
	Sender           string
	Subject          string
	Preview          string
	Status           domain.MessageStatus
	VerificationCode *string
	OrderNo          *string
	ReceivedAt       time.Time
}

type AdminMessageDetail struct {
	AdminMessageSummary
	Body            string
	MatchDiagnostic *string
}

type AdminMessageListQuery struct {
	ResourceID       uint
	ResourceType     domain.ResourceType
	Search           string
	Offset           int
	Limit            int
	BeforeReceivedAt *time.Time
	BeforeID         uint
	SkipTotal        bool
}

type AdminMessagePage struct {
	Items                []AdminMessageSummary
	Total                int64
	TotalIncluded        bool
	Offset               int
	Limit                int
	HasMore              bool
	NextBeforeReceivedAt *time.Time
	NextBeforeID         uint
}

type AdminMessageRepository interface {
	AdminMessageResourceExists(ctx context.Context, resourceID uint, resourceType domain.ResourceType) (bool, error)
	ListAdminMessageSummaries(ctx context.Context, query AdminMessageListQuery) ([]AdminMessageSummary, int64, bool, error)
	FindAdminMessageDetailWithLog(ctx context.Context, resourceID uint, resourceType domain.ResourceType, messageID uint, log *governancedomain.OperationLog) (*AdminMessageDetail, error)
}

type AdminMessageUseCase struct {
	repo AdminMessageRepository
}

func NewAdminMessageUseCase(repo AdminMessageRepository) *AdminMessageUseCase {
	return &AdminMessageUseCase{repo: repo}
}

func (uc *AdminMessageUseCase) List(ctx context.Context, query AdminMessageListQuery) (*AdminMessagePage, error) {
	if uc == nil || uc.repo == nil || query.ResourceID == 0 || query.Offset < 0 || (query.BeforeReceivedAt == nil) != (query.BeforeID == 0) || (query.BeforeReceivedAt != nil && query.Offset != 0) {
		return nil, domain.ErrInvalidRequest
	}
	query.Search = strings.TrimSpace(query.Search)
	if query.ResourceType == "" {
		query.ResourceType = domain.ResourceTypeMicrosoft
	}
	if query.ResourceType != domain.ResourceTypeMicrosoft && query.ResourceType != domain.ResourceTypeDomain {
		return nil, domain.ErrInvalidRequest
	}
	if utf8.RuneCountInString(query.Search) > maxAdminMessageSearch {
		return nil, domain.ErrInvalidRequest
	}
	if query.Limit == 0 {
		query.Limit = defaultAdminMessageLimit
	}
	if query.Limit < 1 || query.Limit > maxAdminMessageLimit {
		return nil, domain.ErrInvalidRequest
	}
	exists, err := uc.repo.AdminMessageResourceExists(ctx, query.ResourceID, query.ResourceType)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, domain.ErrAdminMessageResourceNotFound
	}
	items, total, hasMore, err := uc.repo.ListAdminMessageSummaries(ctx, query)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []AdminMessageSummary{}
	}
	page := &AdminMessagePage{
		Items:         items,
		Total:         total,
		TotalIncluded: !query.SkipTotal,
		Offset:        query.Offset,
		Limit:         query.Limit,
		HasMore:       hasMore,
	}
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		nextReceivedAt := last.ReceivedAt
		page.NextBeforeReceivedAt = &nextReceivedAt
		page.NextBeforeID = last.ID
	}
	return page, nil
}

func (uc *AdminMessageUseCase) Get(
	ctx context.Context,
	operatorUserID uint,
	resourceID uint,
	resourceType domain.ResourceType,
	messageID uint,
	requestID string,
	path string,
) (*AdminMessageDetail, error) {
	if uc == nil || uc.repo == nil || operatorUserID == 0 || resourceID == 0 || messageID == 0 || (resourceType != domain.ResourceTypeMicrosoft && resourceType != domain.ResourceTypeDomain) {
		return nil, domain.ErrInvalidRequest
	}
	resourceName := "microsoft_message"
	if resourceType == domain.ResourceTypeDomain {
		resourceName = "domain_message"
	}
	return uc.repo.FindAdminMessageDetailWithLog(ctx, resourceID, resourceType, messageID, &governancedomain.OperationLog{
		OperatorUserID: operatorUserID,
		OperationType:  "mailmatch.admin_message.body.read",
		ResourceType:   resourceName,
		ResourceID:     fmt.Sprintf("%d", messageID),
		Path:           strings.TrimSpace(path),
		Result:         "success",
		SafeSummary:    fmt.Sprintf("Primary mailbox message body read; resource=%d; message=%d.", resourceID, messageID),
		RequestID:      strings.TrimSpace(requestID),
	})
}
