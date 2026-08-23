package kitesim

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/donnel666/remail/internal/businessday"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxImportAccounts = 100

var (
	ErrInvalidInput   = errors.New("kitesim: invalid input")
	ErrAccountMissing = errors.New("kitesim: account not found")
	ErrPhoneMissing   = errors.New("kitesim: phone not found")
)

type AdminPhoneStatus string

const (
	AdminPhoneActive     AdminPhoneStatus = "active"
	AdminPhonePending    AdminPhoneStatus = "pending"
	AdminPhoneActivating AdminPhoneStatus = "activating"
	AdminPhoneExpired    AdminPhoneStatus = "expired"
	AdminPhoneRefunded   AdminPhoneStatus = "refunded"
	AdminPhoneUnsynced   AdminPhoneStatus = "unsynced"
	AdminPhoneDisabled   AdminPhoneStatus = "disabled"
	AdminPhoneExclusive  AdminPhoneStatus = "exclusive"
)

type SyncTaskStatus string

const (
	SyncTaskIdle      SyncTaskStatus = "idle"
	SyncTaskQueued    SyncTaskStatus = "queued"
	SyncTaskRunning   SyncTaskStatus = "running"
	SyncTaskSucceeded SyncTaskStatus = "succeeded"
	SyncTaskFailed    SyncTaskStatus = "failed"
)

type accountModel struct {
	ID             uint       `gorm:"column:id;primaryKey;autoIncrement"`
	Account        string     `gorm:"column:account;uniqueIndex"`
	Password       string     `gorm:"column:password"`
	Token          string     `gorm:"column:token"`
	TokenUpdated   *time.Time `gorm:"column:token_updated_at"`
	LastSafeError  string     `gorm:"column:last_safe_error"`
	LastSyncedAt   *time.Time `gorm:"column:last_synced_at"`
	SyncStatus     string     `gorm:"column:sync_status"`
	SyncQueuedAt   *time.Time `gorm:"column:sync_queued_at"`
	SyncStartedAt  *time.Time `gorm:"column:sync_started_at"`
	SyncFinishedAt *time.Time `gorm:"column:sync_finished_at"`
	SyncAttempts   int        `gorm:"column:sync_attempts"`
	DeletedAt      *time.Time `gorm:"column:deleted_at"`
	CreatedAt      time.Time  `gorm:"column:created_at"`
	UpdatedAt      time.Time  `gorm:"column:updated_at"`
}

func (accountModel) TableName() string { return "kitesim_accounts" }

type phoneModel struct {
	ID                     uint       `gorm:"column:id;primaryKey;autoIncrement"`
	AccountID              uint       `gorm:"column:account_id;uniqueIndex:uk_kitesim_phones_account_order"`
	ProviderOrderID        string     `gorm:"column:provider_order_id;size:128;uniqueIndex:uk_kitesim_phones_account_order"`
	OrderNo                string     `gorm:"column:order_no"`
	PhoneCode              string     `gorm:"column:phone_code"`
	PhoneNumber            string     `gorm:"column:phone_number"`
	CountryCode            string     `gorm:"column:country_code"`
	Status                 int        `gorm:"column:status"`
	OrderStatus            int        `gorm:"column:order_status"`
	PackageID              string     `gorm:"column:package_id"`
	DurationType           int        `gorm:"column:duration_type"`
	DurationValue          int        `gorm:"column:duration_value"`
	AutoRenew              bool       `gorm:"column:auto_renew"`
	Currency               string     `gorm:"column:currency"`
	OriginalAmount         string     `gorm:"column:original_amount"`
	PaidAmount             string     `gorm:"column:paid_amount"`
	AutoRenewPrice         string     `gorm:"column:auto_renew_price"`
	CreateTime             string     `gorm:"column:create_time"`
	ProviderCreated        *time.Time `gorm:"column:provider_created_at"`
	PaymentTime            string     `gorm:"column:payment_time"`
	ExpireTime             string     `gorm:"column:expire_time"`
	LatestRenewal          string     `gorm:"column:latest_renewal_time"`
	NextRenewalDate        string     `gorm:"column:next_renewal_date"`
	RefundTime             string     `gorm:"column:refund_time"`
	DisabledAt             *time.Time `gorm:"column:disabled_at"`
	DeletedAt              *time.Time `gorm:"column:deleted_at"`
	SMSCooldownUntil       *time.Time `gorm:"column:sms_cooldown_until"`
	SMSCooldownStage       uint8      `gorm:"column:sms_cooldown_stage"`
	SMSConsecutiveFailures uint       `gorm:"column:sms_consecutive_failures"`
	SMSBlacklistedUntil    *time.Time `gorm:"column:sms_blacklisted_until"`
	SMSLastUsedAt          *time.Time `gorm:"column:sms_last_used_at"`
	RawPayload             jsonText   `gorm:"column:raw_payload;type:json"`
	CreatedAt              time.Time  `gorm:"column:created_at"`
	UpdatedAt              time.Time  `gorm:"column:updated_at"`
}

func (phoneModel) TableName() string { return "kitesim_phones" }

type syncRunModel struct {
	ID            uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	AccountID     uint       `gorm:"column:account_id;index"`
	Status        string     `gorm:"column:status"`
	Attempts      int        `gorm:"column:attempts"`
	LastSafeError string     `gorm:"column:last_safe_error"`
	QueuedAt      time.Time  `gorm:"column:queued_at"`
	StartedAt     *time.Time `gorm:"column:started_at"`
	FinishedAt    *time.Time `gorm:"column:finished_at"`
	CreatedAt     time.Time  `gorm:"column:created_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at"`
}

func (syncRunModel) TableName() string { return "kitesim_sync_runs" }

type MutationMeta struct {
	OperatorUserID uint
	IdempotencyKey string
	RequestID      string
	Path           string
}

type Service struct {
	db            *gorm.DB
	client        *Client
	queue         SyncQueue
	proxies       ProxyProvider
	logs          governanceapp.OperationLogPort
	now           func() time.Time
	smsConsumedMu sync.Mutex
	smsConsumed   map[string]time.Time
}

func NewService(db *gorm.DB, queue SyncQueue) *Service {
	return &Service{
		db: db, client: NewClient(nil), queue: queue, logs: governanceinfra.NewOperationLogRepo(db),
		now: func() time.Time { return time.Now().UTC() },
	}
}

type PhoneListFilter struct {
	Search         string
	Status         AdminPhoneStatus
	AutoRenew      *bool
	TokenAvailable *bool
	SyncHealthy    *bool
	PhoneAvailable *bool
	CreatedFrom    *time.Time
	CreatedTo      *time.Time
	Offset         int
	Limit          int
}

type PhoneItem struct {
	AccountID          uint             `json:"accountId"`
	PhoneID            *uint            `json:"phoneId"`
	LinkedAccountCount int64            `json:"linkedAccountCount"`
	ProviderOrderID    string           `json:"providerOrderId,omitempty"`
	Account            string           `json:"account"`
	PhoneNumber        string           `json:"phoneNumber"`
	Status             AdminPhoneStatus `json:"status"`
	OrderNo            string           `json:"orderNo,omitempty"`
	CountryCode        string           `json:"countryCode,omitempty"`
	OrderStatus        int              `json:"orderStatus,omitempty"`
	PackageID          string           `json:"packageId,omitempty"`
	DurationType       int              `json:"durationType,omitempty"`
	DurationValue      int              `json:"durationValue,omitempty"`
	AutoRenew          bool             `json:"autoRenew"`
	Currency           string           `json:"currency,omitempty"`
	OriginalAmount     string           `json:"originalAmount,omitempty"`
	PaidAmount         string           `json:"paidAmount,omitempty"`
	AutoRenewPrice     string           `json:"autoRenewPrice,omitempty"`
	CreateTime         string           `json:"createTime,omitempty"`
	PaymentTime        string           `json:"paymentTime,omitempty"`
	ExpireTime         string           `json:"expireTime,omitempty"`
	LatestRenewal      string           `json:"latestRenewalTime,omitempty"`
	NextRenewalDate    string           `json:"nextRenewalDate,omitempty"`
	RefundTime         string           `json:"refundTime,omitempty"`
	TokenAvailable     bool             `json:"tokenAvailable"`
	TokenUpdatedAt     *time.Time       `json:"tokenUpdatedAt,omitempty"`
	SyncHealthy        bool             `json:"syncHealthy"`
	SyncStatus         SyncTaskStatus   `json:"syncStatus"`
	SyncQueuedAt       *time.Time       `json:"syncQueuedAt,omitempty"`
	SyncStartedAt      *time.Time       `json:"syncStartedAt,omitempty"`
	SyncFinishedAt     *time.Time       `json:"syncFinishedAt,omitempty"`
	SyncAttempts       int              `json:"syncAttempts"`
	LastSafeError      string           `json:"lastSafeError,omitempty"`
	LastSyncedAt       *time.Time       `json:"lastSyncedAt,omitempty"`
	AccountCreatedAt   time.Time        `json:"createdAt"`
}

type BooleanFacets struct {
	All int64 `json:"all"`
	Yes int64 `json:"yes"`
	No  int64 `json:"no"`
}

type PhoneFacets struct {
	All            int64         `json:"all"`
	Active         int64         `json:"active"`
	Pending        int64         `json:"pending"`
	Activating     int64         `json:"activating"`
	Expired        int64         `json:"expired"`
	Refunded       int64         `json:"refunded"`
	Unsynced       int64         `json:"unsynced"`
	Disabled       int64         `json:"disabled"`
	Exclusive      int64         `json:"exclusive"`
	AutoRenew      BooleanFacets `json:"autoRenew"`
	TokenAvailable BooleanFacets `json:"tokenAvailable"`
	SyncHealthy    BooleanFacets `json:"syncHealthy"`
	PhoneAvailable BooleanFacets `json:"phoneAvailable"`
}

type PhoneList struct {
	Items  []PhoneItem `json:"items"`
	Total  int64       `json:"total"`
	Offset int         `json:"offset"`
	Limit  int         `json:"limit"`
	Facets PhoneFacets `json:"facets"`
}

func (s *Service) ListPhones(ctx context.Context, filter PhoneListFilter) (*PhoneList, error) {
	if filter.Offset < 0 || filter.Limit < 1 || filter.Limit > 100 {
		return nil, ErrInvalidInput
	}
	usage, err := loadICloudPhoneUsage(s.db.WithContext(ctx), "")
	if err != nil {
		return nil, fmt.Errorf("load iCloud phone usage: %w", err)
	}
	exclusivePhoneIDs := iCloudExclusivePhoneIDs(usage.exclusive)
	query := s.filteredPhoneQuery(ctx, filter, exclusivePhoneIDs)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, fmt.Errorf("list Kitesim phones: %w", err)
	}
	type row struct {
		AccountID        uint       `gorm:"column:account_id"`
		PhoneID          *uint      `gorm:"column:phone_id"`
		ProviderOrderID  *string    `gorm:"column:provider_order_id"`
		Account          string     `gorm:"column:account"`
		PhoneCode        *string    `gorm:"column:phone_code"`
		PhoneNumber      *string    `gorm:"column:phone_number"`
		Status           *int       `gorm:"column:status"`
		OrderNo          *string    `gorm:"column:order_no"`
		CountryCode      *string    `gorm:"column:country_code"`
		OrderStatus      *int       `gorm:"column:order_status"`
		PackageID        *string    `gorm:"column:package_id"`
		DurationType     *int       `gorm:"column:duration_type"`
		DurationValue    *int       `gorm:"column:duration_value"`
		AutoRenew        *bool      `gorm:"column:auto_renew"`
		Currency         *string    `gorm:"column:currency"`
		OriginalAmount   *string    `gorm:"column:original_amount"`
		PaidAmount       *string    `gorm:"column:paid_amount"`
		AutoRenewPrice   *string    `gorm:"column:auto_renew_price"`
		CreateTime       *string    `gorm:"column:create_time"`
		PaymentTime      *string    `gorm:"column:payment_time"`
		ExpireTime       *string    `gorm:"column:expire_time"`
		LatestRenewal    *string    `gorm:"column:latest_renewal_time"`
		NextRenewalDate  *string    `gorm:"column:next_renewal_date"`
		RefundTime       *string    `gorm:"column:refund_time"`
		TokenAvailable   bool       `gorm:"column:token_available"`
		TokenUpdatedAt   *time.Time `gorm:"column:token_updated_at"`
		SyncHealthy      bool       `gorm:"column:sync_healthy"`
		SyncStatus       string     `gorm:"column:sync_status"`
		SyncQueuedAt     *time.Time `gorm:"column:sync_queued_at"`
		SyncStartedAt    *time.Time `gorm:"column:sync_started_at"`
		SyncFinishedAt   *time.Time `gorm:"column:sync_finished_at"`
		SyncAttempts     int        `gorm:"column:sync_attempts"`
		LastSafeError    string     `gorm:"column:last_safe_error"`
		LastSyncedAt     *time.Time `gorm:"column:last_synced_at"`
		DisabledAt       *time.Time `gorm:"column:disabled_at"`
		AccountCreatedAt time.Time  `gorm:"column:account_created"`
	}
	var rows []row
	if err := query.Select(`a.id AS account_id, p.id AS phone_id, p.provider_order_id, a.account,
p.phone_code, p.phone_number, p.status, p.order_no, p.country_code, p.order_status,
p.package_id, p.duration_type, p.duration_value, p.auto_renew, p.currency,
p.original_amount, p.paid_amount, p.auto_renew_price, p.create_time, p.payment_time,
p.expire_time, p.latest_renewal_time, p.next_renewal_date, p.refund_time, p.disabled_at,
CASE WHEN a.token IS NOT NULL AND LENGTH(a.token) > 0 THEN 1 ELSE 0 END AS token_available, a.token_updated_at,
(a.last_synced_at IS NOT NULL AND a.last_safe_error = '') AS sync_healthy,
a.sync_status, a.sync_queued_at, a.sync_started_at, a.sync_finished_at, a.sync_attempts,
a.last_safe_error, a.last_synced_at, a.created_at AS account_created`).
		Order("a.id DESC, p.id DESC").Offset(filter.Offset).Limit(filter.Limit).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list Kitesim phones: %w", err)
	}
	items := make([]PhoneItem, 0, len(rows))
	for _, row := range rows {
		item := PhoneItem{
			AccountID: row.AccountID, PhoneID: row.PhoneID, Account: row.Account,
			LinkedAccountCount: func() int64 {
				if row.PhoneID == nil {
					return 0
				}
				return usage.linked[*row.PhoneID]
			}(),
			Status: AdminPhoneUnsynced, TokenAvailable: row.TokenAvailable,
			TokenUpdatedAt: row.TokenUpdatedAt, SyncHealthy: row.SyncHealthy,
			SyncStatus: SyncTaskStatus(row.SyncStatus), SyncQueuedAt: row.SyncQueuedAt,
			SyncStartedAt: row.SyncStartedAt, SyncFinishedAt: row.SyncFinishedAt,
			SyncAttempts: row.SyncAttempts, LastSafeError: row.LastSafeError,
			LastSyncedAt: row.LastSyncedAt, AccountCreatedAt: row.AccountCreatedAt,
		}
		if item.SyncStatus == "" {
			item.SyncStatus = SyncTaskIdle
		}
		if row.ProviderOrderID != nil {
			item.ProviderOrderID = *row.ProviderOrderID
		}
		if row.Status != nil {
			item.Status = adminStatus(PhoneStatus(*row.Status))
		}
		if row.DisabledAt != nil {
			item.Status = AdminPhoneDisabled
		}
		if item.Status == AdminPhoneActive && row.PhoneID != nil {
			if _, exclusive := usage.exclusive[*row.PhoneID]; exclusive {
				item.Status = AdminPhoneExclusive
			}
		}
		if row.OrderNo != nil {
			item.OrderNo = *row.OrderNo
		}
		if row.CountryCode != nil {
			item.CountryCode = *row.CountryCode
		}
		if row.OrderStatus != nil {
			item.OrderStatus = *row.OrderStatus
		}
		if row.PackageID != nil {
			item.PackageID = *row.PackageID
		}
		if row.DurationType != nil {
			item.DurationType = *row.DurationType
		}
		if row.DurationValue != nil {
			item.DurationValue = *row.DurationValue
		}
		if row.AutoRenew != nil {
			item.AutoRenew = *row.AutoRenew
		}
		if row.Currency != nil {
			item.Currency = *row.Currency
		}
		if row.OriginalAmount != nil {
			item.OriginalAmount = *row.OriginalAmount
		}
		if row.PaidAmount != nil {
			item.PaidAmount = *row.PaidAmount
		}
		if row.AutoRenewPrice != nil {
			item.AutoRenewPrice = *row.AutoRenewPrice
		}
		if row.CreateTime != nil {
			item.CreateTime = *row.CreateTime
		}
		if row.PaymentTime != nil {
			item.PaymentTime = *row.PaymentTime
		}
		if row.ExpireTime != nil {
			item.ExpireTime = *row.ExpireTime
		}
		if row.LatestRenewal != nil {
			item.LatestRenewal = *row.LatestRenewal
		}
		if row.NextRenewalDate != nil {
			item.NextRenewalDate = *row.NextRenewalDate
		}
		if row.RefundTime != nil {
			item.RefundTime = *row.RefundTime
		}
		if row.PhoneNumber != nil {
			code := ""
			if row.PhoneCode != nil {
				code = *row.PhoneCode
			}
			item.PhoneNumber = formatPhoneNumber(code, *row.PhoneNumber)
		}
		items = append(items, item)
	}
	facets, err := s.phoneFacets(ctx, filter, exclusivePhoneIDs)
	if err != nil {
		return nil, err
	}
	return &PhoneList{Items: items, Total: total, Offset: filter.Offset, Limit: filter.Limit, Facets: facets}, nil
}

func (s *Service) filteredPhoneQuery(ctx context.Context, filter PhoneListFilter, exclusivePhoneIDs []uint) *gorm.DB {
	query := s.basePhoneQuery(ctx, filter.Search, filter.CreatedFrom, filter.CreatedTo)
	if filter.AutoRenew != nil {
		query = query.Where("p.id IS NOT NULL AND p.auto_renew = ?", *filter.AutoRenew)
	}
	if filter.TokenAvailable != nil {
		if *filter.TokenAvailable {
			query = query.Where("a.token IS NOT NULL AND LENGTH(a.token) > 0")
		} else {
			query = query.Where("a.token IS NULL OR LENGTH(a.token) = 0")
		}
	}
	if filter.SyncHealthy != nil {
		if *filter.SyncHealthy {
			query = query.Where("a.last_synced_at IS NOT NULL AND a.last_safe_error = ''")
		} else {
			query = query.Where("a.last_synced_at IS NULL OR a.last_safe_error <> ''")
		}
	}
	if filter.PhoneAvailable != nil {
		if *filter.PhoneAvailable {
			query = query.Where("p.id IS NOT NULL")
		} else {
			query = query.Where("p.id IS NULL")
		}
	}
	if filter.Status == AdminPhoneUnsynced {
		return query.Where("p.id IS NULL")
	}
	if filter.Status == AdminPhoneDisabled {
		return query.Where("p.disabled_at IS NOT NULL")
	}
	if filter.Status == AdminPhoneExclusive {
		if len(exclusivePhoneIDs) == 0 {
			return query.Where("1 = 0")
		}
		return query.Where("p.id IS NOT NULL AND p.disabled_at IS NULL AND p.status = ? AND p.id IN ?", int(PhoneActive), exclusivePhoneIDs)
	}
	if filter.Status != "" {
		status, ok := providerStatus(filter.Status)
		if !ok {
			return query.Where("1 = 0")
		}
		query = query.Where("p.disabled_at IS NULL AND p.status = ?", status)
		if status == PhoneActive && len(exclusivePhoneIDs) > 0 {
			query = query.Where("p.id NOT IN ?", exclusivePhoneIDs)
		}
	}
	return query
}

func (s *Service) basePhoneQuery(ctx context.Context, search string, createdFrom, createdTo *time.Time) *gorm.DB {
	query := s.db.WithContext(ctx).Table("kitesim_accounts AS a").
		Joins("LEFT JOIN kitesim_phones AS p ON p.account_id = a.id AND p.deleted_at IS NULL").
		Where("a.deleted_at IS NULL")
	search = strings.TrimSpace(search)
	if search != "" {
		like := "%" + search + "%"
		query = query.Where("a.account LIKE ? OR p.phone_number LIKE ? OR p.order_no LIKE ?", like, like, like)
	}
	if createdFrom != nil {
		query = query.Where("p.provider_created_at >= ?", createdFrom.UTC())
	}
	if createdTo != nil {
		query = query.Where("p.provider_created_at < ?", createdTo.UTC())
	}
	return query
}

func (s *Service) phoneFacets(ctx context.Context, filter PhoneListFilter, exclusivePhoneIDs []uint) (PhoneFacets, error) {
	type counts struct {
		All               int64 `gorm:"column:all_count"`
		Active            int64 `gorm:"column:active_count"`
		Pending           int64 `gorm:"column:pending_count"`
		Activating        int64 `gorm:"column:activating_count"`
		Expired           int64 `gorm:"column:expired_count"`
		Refunded          int64 `gorm:"column:refunded_count"`
		Unsynced          int64 `gorm:"column:unsynced_count"`
		Disabled          int64 `gorm:"column:disabled_count"`
		Exclusive         int64 `gorm:"column:exclusive_count"`
		AutoRenewYes      int64 `gorm:"column:auto_renew_yes"`
		TokenAvailableYes int64 `gorm:"column:token_available_yes"`
		SyncHealthyYes    int64 `gorm:"column:sync_healthy_yes"`
		PhoneAvailableYes int64 `gorm:"column:phone_available_yes"`
	}
	query := s.basePhoneQuery(ctx, filter.Search, filter.CreatedFrom, filter.CreatedTo)
	var row counts
	activeCase := "SUM(CASE WHEN p.disabled_at IS NULL AND p.status = 1 THEN 1 ELSE 0 END) AS active_count"
	exclusiveCase := "SUM(CASE WHEN 1 = 0 THEN 1 ELSE 0 END) AS exclusive_count"
	var selectArgs []any
	if len(exclusivePhoneIDs) > 0 {
		activeCase = "SUM(CASE WHEN p.disabled_at IS NULL AND p.status = 1 AND p.id NOT IN ? THEN 1 ELSE 0 END) AS active_count"
		exclusiveCase = "SUM(CASE WHEN p.disabled_at IS NULL AND p.status = 1 AND p.id IN ? THEN 1 ELSE 0 END) AS exclusive_count"
		selectArgs = []any{exclusivePhoneIDs, exclusivePhoneIDs}
	}
	selectSQL := `COUNT(*) AS all_count,
` + activeCase + `,
SUM(CASE WHEN p.disabled_at IS NULL AND p.status = 2 THEN 1 ELSE 0 END) AS pending_count,
SUM(CASE WHEN p.disabled_at IS NULL AND p.status = 3 THEN 1 ELSE 0 END) AS activating_count,
SUM(CASE WHEN p.disabled_at IS NULL AND p.status = 0 THEN 1 ELSE 0 END) AS expired_count,
SUM(CASE WHEN p.disabled_at IS NULL AND p.status = 4 THEN 1 ELSE 0 END) AS refunded_count,
SUM(CASE WHEN p.id IS NULL THEN 1 ELSE 0 END) AS unsynced_count,
SUM(CASE WHEN p.disabled_at IS NOT NULL THEN 1 ELSE 0 END) AS disabled_count,
` + exclusiveCase + `,
SUM(CASE WHEN p.id IS NOT NULL AND p.auto_renew = 1 THEN 1 ELSE 0 END) AS auto_renew_yes,
SUM(CASE WHEN a.token IS NOT NULL AND LENGTH(a.token) > 0 THEN 1 ELSE 0 END) AS token_available_yes,
SUM(CASE WHEN a.last_synced_at IS NOT NULL AND a.last_safe_error = '' THEN 1 ELSE 0 END) AS sync_healthy_yes,
SUM(CASE WHEN p.id IS NOT NULL THEN 1 ELSE 0 END) AS phone_available_yes`
	if err := query.Select(selectSQL, selectArgs...).Scan(&row).Error; err != nil {
		return PhoneFacets{}, fmt.Errorf("load Kitesim phone facets: %w", err)
	}
	boolean := func(yes int64) BooleanFacets {
		return BooleanFacets{All: row.All, Yes: yes, No: row.All - yes}
	}
	return PhoneFacets{
		All: row.All, Active: row.Active, Pending: row.Pending,
		Activating: row.Activating, Expired: row.Expired,
		Refunded: row.Refunded, Unsynced: row.Unsynced, Disabled: row.Disabled,
		Exclusive: row.Exclusive,
		AutoRenew: boolean(row.AutoRenewYes), TokenAvailable: boolean(row.TokenAvailableYes),
		SyncHealthy: boolean(row.SyncHealthyYes), PhoneAvailable: boolean(row.PhoneAvailableYes),
	}, nil
}

type ImportFailure struct {
	Account string `json:"account"`
	Message string `json:"message"`
}

type ImportResult struct {
	Imported int             `json:"imported"`
	Queued   int             `json:"queued"`
	Failed   int             `json:"failed"`
	Errors   []ImportFailure `json:"errors"`
}

type SyncTaskView struct {
	AccountID  uint           `json:"accountId"`
	Status     SyncTaskStatus `json:"status"`
	QueuedAt   *time.Time     `json:"queuedAt,omitempty"`
	StartedAt  *time.Time     `json:"startedAt,omitempty"`
	FinishedAt *time.Time     `json:"finishedAt,omitempty"`
	Attempts   int            `json:"attempts"`
}

type SyncRunItem struct {
	TaskID        string         `json:"taskId"`
	Status        SyncTaskStatus `json:"status"`
	Attempts      int            `json:"attempts"`
	LastSafeError string         `json:"lastSafeError,omitempty"`
	QueuedAt      time.Time      `json:"queuedAt"`
	StartedAt     *time.Time     `json:"startedAt,omitempty"`
	FinishedAt    *time.Time     `json:"finishedAt,omitempty"`
	UpdatedAt     time.Time      `json:"updatedAt"`
}

type SyncRunList struct {
	Items     []SyncRunItem `json:"items"`
	Total     int64         `json:"total"`
	Succeeded int64         `json:"succeeded"`
	Offset    int           `json:"offset"`
	Limit     int           `json:"limit"`
}

type importAccount struct {
	Account  string
	Password string
	ID       uint
}

func (s *Service) ImportAccounts(ctx context.Context, content string, meta MutationMeta) (*ImportResult, error) {
	accounts, err := parseImportContent(content)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i := range accounts {
			model := accountModel{
				Account: accounts[i].Account, Password: accounts[i].Password,
				SyncStatus: string(SyncTaskQueued), SyncQueuedAt: &now,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "account"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"password", "token", "token_updated_at", "sync_status", "sync_queued_at",
					"sync_started_at", "sync_finished_at", "sync_attempts", "last_safe_error",
					"deleted_at", "updated_at",
				}),
			}).Create(&model).Error; err != nil {
				return fmt.Errorf("upsert Kitesim account: %w", err)
			}
			if err := tx.Select("id").First(&model, "account = ?", accounts[i].Account).Error; err != nil {
				return fmt.Errorf("load Kitesim account: %w", err)
			}
			accounts[i].ID = model.ID
			if err := tx.Model(&syncRunModel{}).
				Where("account_id = ? AND status IN ?", model.ID, []SyncTaskStatus{SyncTaskQueued, SyncTaskRunning}).
				Updates(map[string]any{
					"status": SyncTaskFailed, "finished_at": now,
					"last_safe_error": "Kitesim 同步已被重新导入替代。",
				}).Error; err != nil {
				return fmt.Errorf("supersede Kitesim sync run: %w", err)
			}
			if err := tx.Create(&syncRunModel{
				AccountID: model.ID, Status: string(SyncTaskQueued), QueuedAt: now,
			}).Error; err != nil {
				return fmt.Errorf("create Kitesim sync run: %w", err)
			}
			if err := tx.Model(&phoneModel{}).
				Where("account_id = ? AND deleted_at IS NOT NULL", model.ID).
				Updates(map[string]any{"disabled_at": nil, "deleted_at": nil}).Error; err != nil {
				return fmt.Errorf("restore Kitesim phones: %w", err)
			}
		}
		return s.createAudit(platform.WithGormTx(ctx, tx), meta, "kitesim.accounts.import", "kitesim_account", "bulk", fmt.Sprintf("imported or updated %d Kitesim accounts", len(accounts)))
	})
	if err != nil {
		return nil, err
	}
	result := &ImportResult{Imported: len(accounts), Errors: []ImportFailure{}}
	for _, account := range accounts {
		if _, err := s.queueAccountSync(ctx, account.ID, nil); err != nil {
			result.Failed++
			result.Errors = append(result.Errors, ImportFailure{Account: account.Account, Message: "Kitesim 同步任务提交失败，请稍后重试。"})
			continue
		}
		result.Queued++
	}
	return result, nil
}

type PhoneMutationResult struct {
	Affected int64 `json:"affected"`
}

func normalizeIDs(ids []uint) ([]uint, error) {
	if len(ids) == 0 || len(ids) > maxImportAccounts {
		return nil, ErrInvalidInput
	}
	seen := make(map[uint]struct{}, len(ids))
	result := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			return nil, ErrInvalidInput
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, nil
}

func (s *Service) SetPhonesDisabled(ctx context.Context, phoneIDs []uint, disabled bool, meta MutationMeta) (*PhoneMutationResult, error) {
	ids, err := normalizeIDs(phoneIDs)
	if err != nil {
		return nil, err
	}
	result := &PhoneMutationResult{}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		accountIDs, err := s.phoneAccountIDs(tx, ids)
		if err != nil {
			return err
		}
		if err := s.ensureAccountsWithoutActiveOperations(tx, accountIDs); err != nil {
			return err
		}
		query := tx.Model(&phoneModel{}).Where("id IN ? AND deleted_at IS NULL", ids)
		var updated *gorm.DB
		if disabled {
			updated = query.Where("disabled_at IS NULL").Update("disabled_at", s.now().UTC().Truncate(time.Millisecond))
		} else {
			updated = query.Where("disabled_at IS NOT NULL").Update("disabled_at", nil)
		}
		if updated.Error != nil {
			return fmt.Errorf("update Kitesim phone disabled state: %w", updated.Error)
		}
		result.Affected = updated.RowsAffected
		operation := "disable"
		if !disabled {
			operation = "enable"
		}
		return s.createAudit(
			platform.WithGormTx(ctx, tx), meta, "kitesim.phones."+operation,
			"kitesim_phone", "bulk", fmt.Sprintf("%sd %d Kitesim phones", operation, result.Affected),
		)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) DeletePhones(ctx context.Context, phoneIDs, emptyAccountIDs []uint, meta MutationMeta) (*PhoneMutationResult, error) {
	var ids []uint
	var err error
	if len(phoneIDs) > 0 {
		ids, err = normalizeIDs(phoneIDs)
		if err != nil {
			return nil, err
		}
	}
	accountIDs := make([]uint, 0, len(ids)+len(emptyAccountIDs))
	emptyAccountSet := make(map[uint]struct{}, len(emptyAccountIDs))
	if len(emptyAccountIDs) > 0 {
		emptyAccountIDs, err = normalizeIDs(emptyAccountIDs)
		if err != nil {
			return nil, err
		}
		accountIDs = append(accountIDs, emptyAccountIDs...)
		for _, accountID := range emptyAccountIDs {
			emptyAccountSet[accountID] = struct{}{}
		}
	}
	if len(ids) == 0 && len(accountIDs) == 0 {
		return nil, ErrInvalidInput
	}
	result := &PhoneMutationResult{}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(ids) > 0 {
			phoneAccounts, err := s.phoneAccountIDs(tx, ids)
			if err != nil {
				return err
			}
			accountIDs = append(accountIDs, phoneAccounts...)
		}
		accountIDs, err = uniqueIDs(accountIDs)
		if err != nil {
			return err
		}
		if err := s.ensureAccountsWithoutActiveOperations(tx, accountIDs); err != nil {
			return err
		}
		now := s.now().UTC().Truncate(time.Millisecond)
		if len(ids) > 0 {
			updated := tx.Model(&phoneModel{}).Where("id IN ? AND deleted_at IS NULL", ids).
				Updates(map[string]any{"deleted_at": now, "disabled_at": now})
			if updated.Error != nil {
				return fmt.Errorf("delete Kitesim phones: %w", updated.Error)
			}
			result.Affected += updated.RowsAffected
		}
		for _, accountID := range accountIDs {
			var remaining int64
			if err := tx.Model(&phoneModel{}).
				Where("account_id = ? AND deleted_at IS NULL", accountID).Count(&remaining).Error; err != nil {
				return fmt.Errorf("count remaining Kitesim phones: %w", err)
			}
			if remaining > 0 {
				continue
			}
			updated := tx.Model(&accountModel{}).Where("id = ? AND deleted_at IS NULL", accountID).
				Updates(map[string]any{
					"deleted_at": now, "sync_status": SyncTaskIdle,
					"sync_queued_at": nil, "sync_started_at": nil, "sync_finished_at": nil,
				})
			if updated.Error != nil {
				return fmt.Errorf("delete empty Kitesim account: %w", updated.Error)
			}
			if err := tx.Model(&syncRunModel{}).
				Where("account_id = ? AND status IN ?", accountID, []SyncTaskStatus{SyncTaskQueued, SyncTaskRunning}).
				Updates(map[string]any{
					"status": SyncTaskFailed, "finished_at": now,
					"last_safe_error": "Kitesim 平台账号已删除，同步任务已停止。",
				}).Error; err != nil {
				return fmt.Errorf("stop deleted Kitesim account sync: %w", err)
			}
			if _, selected := emptyAccountSet[accountID]; selected {
				result.Affected += updated.RowsAffected
			}
			if err := tx.Model(&upstreamSettingsModel{}).Where("account_id = ?", accountID).
				Updates(map[string]any{
					"account_id": nil, "balance": "0", "balance_updated_at": nil,
					"refresh_status": SyncTaskIdle, "refresh_queued_at": nil,
					"refresh_started_at": nil, "refresh_finished_at": nil,
					"refresh_attempts": 0, "last_safe_error": "",
				}).Error; err != nil {
				return fmt.Errorf("clear deleted Kitesim system account: %w", err)
			}
		}
		return s.createAudit(
			platform.WithGormTx(ctx, tx), meta, "kitesim.phones.delete",
			"kitesim_phone", "bulk", fmt.Sprintf("deleted %d Kitesim rows", result.Affected),
		)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func uniqueIDs(ids []uint) ([]uint, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return normalizeIDs(ids)
}

func (s *Service) phoneAccountIDs(tx *gorm.DB, phoneIDs []uint) ([]uint, error) {
	type phoneAccount struct {
		ID        uint
		AccountID uint
	}
	var phones []phoneAccount
	if err := tx.Model(&phoneModel{}).Select("id", "account_id").
		Where("id IN ? AND deleted_at IS NULL", phoneIDs).Find(&phones).Error; err != nil {
		return nil, fmt.Errorf("load Kitesim phone accounts: %w", err)
	}
	if len(phones) != len(phoneIDs) {
		return nil, ErrPhoneMissing
	}
	accountIDs := make([]uint, 0, len(phones))
	for _, phone := range phones {
		accountIDs = append(accountIDs, phone.AccountID)
	}
	accountIDs, err := uniqueIDs(accountIDs)
	if err != nil {
		return nil, err
	}
	return accountIDs, nil
}

func (s *Service) ensureAccountsWithoutActiveOperations(tx *gorm.DB, accountIDs []uint) error {
	if len(accountIDs) == 0 {
		return nil
	}
	var accounts []accountModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Model(&accountModel{}).
		Select("id").Where("id IN ? AND deleted_at IS NULL", accountIDs).Find(&accounts).Error; err != nil {
		return fmt.Errorf("check Kitesim accounts: %w", err)
	}
	if len(accounts) != len(accountIDs) {
		return ErrAccountMissing
	}
	var activeOperations int64
	if err := tx.Model(&operationModel{}).
		Where("account_id IN ? AND status IN ?", accountIDs, []OperationStatus{
			OperationQueued, OperationRunning, OperationUncertain, OperationRequiresAction,
		}).Count(&activeOperations).Error; err != nil {
		return fmt.Errorf("check Kitesim account operations: %w", err)
	}
	if activeOperations > 0 {
		return ErrOperationBusy
	}
	return nil
}

func (s *Service) SyncAccount(ctx context.Context, accountID uint, meta MutationMeta) (*SyncTaskView, error) {
	if accountID == 0 {
		return nil, ErrAccountMissing
	}
	return s.queueAccountSync(ctx, accountID, &meta)
}

func (s *Service) ListSyncRuns(ctx context.Context, accountID uint, offset, limit int) (*SyncRunList, error) {
	if accountID == 0 || offset < 0 || limit < 1 || limit > 100 {
		return nil, ErrInvalidInput
	}
	var accountCount int64
	if err := s.db.WithContext(ctx).Model(&accountModel{}).
		Where("id = ? AND deleted_at IS NULL", accountID).Count(&accountCount).Error; err != nil {
		return nil, fmt.Errorf("load Kitesim sync account: %w", err)
	}
	if accountCount == 0 {
		return nil, ErrAccountMissing
	}
	var total, succeeded int64
	if err := s.db.WithContext(ctx).Model(&syncRunModel{}).
		Where("account_id = ?", accountID).Count(&total).Error; err != nil {
		return nil, fmt.Errorf("count Kitesim sync runs: %w", err)
	}
	if err := s.db.WithContext(ctx).Model(&syncRunModel{}).
		Where("account_id = ? AND status = ?", accountID, SyncTaskSucceeded).Count(&succeeded).Error; err != nil {
		return nil, fmt.Errorf("count successful Kitesim sync runs: %w", err)
	}
	var runs []syncRunModel
	if err := s.db.WithContext(ctx).Where("account_id = ?", accountID).
		Order("updated_at DESC, id DESC").Offset(offset).Limit(limit).Find(&runs).Error; err != nil {
		return nil, fmt.Errorf("list Kitesim sync runs: %w", err)
	}
	items := make([]SyncRunItem, 0, len(runs))
	for _, run := range runs {
		items = append(items, SyncRunItem{
			TaskID: fmt.Sprintf("kitesim-sync:%d", run.ID), Status: SyncTaskStatus(run.Status),
			Attempts: run.Attempts, LastSafeError: run.LastSafeError,
			QueuedAt: run.QueuedAt, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt,
			UpdatedAt: run.UpdatedAt,
		})
	}
	return &SyncRunList{
		Items: items, Total: total, Succeeded: succeeded, Offset: offset, Limit: limit,
	}, nil
}

func (s *Service) queueAccountSync(ctx context.Context, accountID uint, meta *MutationMeta) (*SyncTaskView, error) {
	if s.queue == nil {
		return nil, errors.New("kitesim: task queue unavailable")
	}
	var account accountModel
	shouldEnqueue := false
	now := s.now().UTC().Truncate(time.Millisecond)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "sync_status", "sync_queued_at", "sync_started_at", "sync_finished_at", "sync_attempts").
			Where("id = ? AND deleted_at IS NULL", accountID).First(&account).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAccountMissing
			}
			return fmt.Errorf("load Kitesim account: %w", err)
		}
		switch SyncTaskStatus(account.SyncStatus) {
		case SyncTaskRunning:
			return nil
		case SyncTaskQueued:
			if err := ensureQueuedSyncRun(tx, account.ID, account.SyncQueuedAt, now); err != nil {
				return err
			}
			shouldEnqueue = true
			return nil
		}
		updates := map[string]any{
			"sync_status": SyncTaskQueued, "sync_queued_at": now,
			"sync_started_at": nil, "sync_finished_at": nil, "sync_attempts": 0,
			"last_safe_error": "",
		}
		if err := tx.Model(&accountModel{}).Where("id = ?", accountID).Updates(updates).Error; err != nil {
			return fmt.Errorf("queue Kitesim sync: %w", err)
		}
		account.SyncStatus = string(SyncTaskQueued)
		account.SyncQueuedAt = &now
		account.SyncStartedAt = nil
		account.SyncFinishedAt = nil
		account.SyncAttempts = 0
		if err := tx.Create(&syncRunModel{
			AccountID: account.ID, Status: string(SyncTaskQueued), QueuedAt: now,
		}).Error; err != nil {
			return fmt.Errorf("create Kitesim sync run: %w", err)
		}
		shouldEnqueue = true
		if meta == nil {
			return nil
		}
		return s.createAudit(platform.WithGormTx(ctx, tx), *meta, "kitesim.account.sync", "kitesim_account", strconv.FormatUint(uint64(accountID), 10), "queued Kitesim account synchronization")
	})
	if err != nil {
		return nil, err
	}
	if shouldEnqueue {
		_, _ = s.queue.Enqueue(ctx, accountID)
	}
	return syncTaskView(account), nil
}

func ensureQueuedSyncRun(tx *gorm.DB, accountID uint, queuedAt *time.Time, fallback time.Time) error {
	var count int64
	if err := tx.Model(&syncRunModel{}).
		Where("account_id = ? AND status = ?", accountID, SyncTaskQueued).Count(&count).Error; err != nil {
		return fmt.Errorf("load queued Kitesim sync run: %w", err)
	}
	if count > 0 {
		return nil
	}
	queued := fallback
	if queuedAt != nil {
		queued = *queuedAt
	}
	if err := tx.Create(&syncRunModel{
		AccountID: accountID, Status: string(SyncTaskQueued), QueuedAt: queued,
	}).Error; err != nil {
		return fmt.Errorf("create queued Kitesim sync run: %w", err)
	}
	return nil
}

func syncTaskView(account accountModel) *SyncTaskView {
	status := SyncTaskStatus(account.SyncStatus)
	if status == "" {
		status = SyncTaskIdle
	}
	return &SyncTaskView{
		AccountID: account.ID, Status: status, QueuedAt: account.SyncQueuedAt,
		StartedAt: account.SyncStartedAt, FinishedAt: account.SyncFinishedAt,
		Attempts: account.SyncAttempts,
	}
}

func (s *Service) processAccountSync(ctx context.Context, claim accountSyncClaim) error {
	if claim.AccountID == 0 || claim.StartedAt.IsZero() {
		return errSyncSuperseded
	}
	var account accountModel
	if err := s.db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL AND sync_status = ? AND sync_started_at = ?", claim.AccountID, SyncTaskRunning, claim.StartedAt).
		First(&account).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errSyncSuperseded
		}
		return fmt.Errorf("load Kitesim account: %w", err)
	}
	var (
		token          string
		orders         []PhoneOrder
		tokenRefreshed bool
	)
	if err := s.withAuthClient(ctx, account.Account, func(client *Client) error {
		var err error
		token, orders, tokenRefreshed, err = s.loadPhoneOrders(ctx, client, account)
		return err
	}); err != nil {
		return err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		phones := make([]phoneModel, 0, len(orders))
		for _, order := range orders {
			providerOrderID := strings.TrimSpace(string(order.ID))
			if providerOrderID == "" || strings.TrimSpace(order.PhoneNumber) == "" {
				continue
			}
			originalAmount, err := upstreamDecimal(order.OriginalAmount)
			if err != nil {
				return fmt.Errorf("parse Kitesim original amount: %w", err)
			}
			paidAmount, err := upstreamDecimal(order.PaidAmount)
			if err != nil {
				return fmt.Errorf("parse Kitesim paid amount: %w", err)
			}
			autoRenewPrice, err := upstreamDecimal(order.AutoRenewPrice)
			if err != nil {
				return fmt.Errorf("parse Kitesim auto-renew price: %w", err)
			}
			phones = append(phones, phoneModel{
				AccountID: claim.AccountID, ProviderOrderID: providerOrderID,
				OrderNo:     strings.TrimSpace(order.OrderNo),
				PhoneCode:   strings.TrimSpace(string(order.PhoneCode)),
				PhoneNumber: strings.TrimSpace(order.PhoneNumber),
				CountryCode: strings.TrimSpace(string(order.CountryCode)),
				Status:      int(order.Status), OrderStatus: order.OrderStatus,
				PackageID:    strings.TrimSpace(string(order.PackageID)),
				DurationType: order.DurationType, DurationValue: order.DurationValue,
				AutoRenew: bool(order.AutoRenew), Currency: strings.TrimSpace(order.Currency),
				OriginalAmount:  originalAmount,
				PaidAmount:      paidAmount,
				AutoRenewPrice:  autoRenewPrice,
				CreateTime:      strings.TrimSpace(order.CreateTime),
				ProviderCreated: parseProviderTime(order.CreateTime),
				PaymentTime:     strings.TrimSpace(order.PaymentTime),
				ExpireTime:      strings.TrimSpace(order.ExpireTime),
				LatestRenewal:   strings.TrimSpace(order.LatestRenewalTime),
				NextRenewalDate: strings.TrimSpace(string(order.NextRenewalDate)),
				RefundTime:      strings.TrimSpace(string(order.RefundTime)),
				RawPayload:      jsonText(order.RawPayload),
			})
		}
		if len(phones) > 0 {
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "account_id"}, {Name: "provider_order_id"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"order_no", "phone_code", "phone_number", "country_code", "status", "order_status",
					"package_id", "duration_type", "duration_value", "auto_renew", "currency",
					"original_amount", "paid_amount", "auto_renew_price", "create_time", "provider_created_at", "payment_time",
					"expire_time", "latest_renewal_time", "next_renewal_date", "refund_time", "raw_payload",
				}),
			}).Create(&phones).Error; err != nil {
				return fmt.Errorf("upsert Kitesim phones: %w", err)
			}
		}
		updates := map[string]any{
			"last_safe_error": "", "last_synced_at": now,
			"sync_status": SyncTaskSucceeded, "sync_finished_at": now,
		}
		if tokenRefreshed {
			updates["token"] = token
			updates["token_updated_at"] = now
		}
		result := tx.Model(&accountModel{}).
			Where("id = ? AND deleted_at IS NULL AND sync_status = ? AND sync_started_at = ?", claim.AccountID, SyncTaskRunning, claim.StartedAt).
			Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("update Kitesim sync state: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return errSyncSuperseded
		}
		result = tx.Model(&syncRunModel{}).
			Where("id = ? AND account_id = ? AND status = ? AND started_at = ?", claim.RunID, claim.AccountID, SyncTaskRunning, claim.StartedAt).
			Updates(map[string]any{
				"status": SyncTaskSucceeded, "finished_at": now, "last_safe_error": "",
			})
		if result.Error != nil {
			return fmt.Errorf("complete Kitesim sync run: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return errSyncSuperseded
		}
		return nil
	})
}

func (s *Service) loadPhoneOrders(ctx context.Context, client *Client, account accountModel) (string, []PhoneOrder, bool, error) {
	token := account.Token
	if strings.TrimSpace(token) != "" {
		orders, err := client.PhoneOrders(ctx, token)
		if err == nil {
			return token, orders, false, nil
		}
		if isProxyFailure(err) || ctx.Err() != nil {
			return "", nil, false, err
		}
	}
	token, err := client.Login(ctx, account.Account, account.Password)
	if err != nil {
		return "", nil, false, err
	}
	orders, err := client.PhoneOrders(ctx, token)
	return token, orders, true, err
}

type MessageItem struct {
	Caller  string `json:"caller"`
	Content string `json:"content"`
	Time    string `json:"time"`
}

func (s *Service) Messages(ctx context.Context, phoneID uint, meta MutationMeta) ([]MessageItem, error) {
	items, err := s.FetchSMSMessages(ctx, phoneID)
	if err != nil {
		return nil, err
	}
	if err := s.createAudit(
		ctx,
		meta,
		"kitesim.phone.messages.read",
		"kitesim_phone",
		strconv.FormatUint(uint64(phoneID), 10),
		"read Kitesim SMS message bodies",
	); err != nil {
		return nil, err
	}
	return items, nil
}

// FetchSMSMessages is the service-to-service polling path used by durable
// verification tasks. The administrator endpoint above remains audited.
func (s *Service) FetchSMSMessages(ctx context.Context, phoneID uint) ([]MessageItem, error) {
	if phoneID == 0 {
		return nil, ErrPhoneMissing
	}
	type row struct {
		AccountID       uint       `gorm:"column:account_id"`
		ProviderOrderID string     `gorm:"column:provider_order_id"`
		PhoneNumber     string     `gorm:"column:phone_number"`
		Status          int        `gorm:"column:status"`
		DisabledAt      *time.Time `gorm:"column:disabled_at"`
		DeletedAt       *time.Time `gorm:"column:deleted_at"`
		Account         string     `gorm:"column:account"`
		Password        string     `gorm:"column:password"`
		Token           string     `gorm:"column:token"`
	}
	var phone row
	result := s.db.WithContext(ctx).Table("kitesim_phones AS p").
		Select("a.id AS account_id, p.provider_order_id, p.phone_number, p.status, p.disabled_at, p.deleted_at, a.account, a.password, a.token").
		Joins("JOIN kitesim_accounts AS a ON a.id = p.account_id").
		Where("p.id = ? AND a.deleted_at IS NULL", phoneID).Take(&phone)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, ErrPhoneMissing
	}
	if result.Error != nil {
		return nil, fmt.Errorf("load Kitesim phone: %w", result.Error)
	}
	if phone.DeletedAt != nil || phone.DisabledAt != nil || PhoneStatus(phone.Status) != PhoneActive {
		return nil, smsInactivePhoneError(s.db.WithContext(ctx), phoneID)
	}
	token := phone.Token
	var messages []Message
	refreshedToken := ""
	if err := s.withFetchClient(ctx, phone.Account, func(client *Client) error {
		if strings.TrimSpace(token) != "" {
			var err error
			messages, err = client.Messages(ctx, token, phone.ProviderOrderID, phone.PhoneNumber)
			if err == nil {
				return nil
			}
			if isProxyFailure(err) || ctx.Err() != nil {
				return err
			}
		}
		var err error
		token, err = client.Login(ctx, phone.Account, phone.Password)
		if err != nil {
			return err
		}
		messages, err = client.Messages(ctx, token, phone.ProviderOrderID, phone.PhoneNumber)
		if err == nil {
			refreshedToken = token
		}
		return err
	}); err != nil {
		return nil, err
	}
	if refreshedToken != "" {
		now := s.now().UTC().Truncate(time.Millisecond)
		_ = s.db.WithContext(context.WithoutCancel(ctx)).Model(&accountModel{}).
			Where("id = ? AND deleted_at IS NULL AND password = ? AND COALESCE(token, '') = ?", phone.AccountID, phone.Password, phone.Token).
			Updates(map[string]any{"token": refreshedToken, "token_updated_at": now}).Error
	}
	items := make([]MessageItem, 0, len(messages))
	for _, message := range messages {
		items = append(items, MessageItem{
			Caller:  strings.TrimSpace(message.Caller),
			Content: strings.TrimSpace(message.Content),
			Time:    strings.TrimSpace(message.Time()),
		})
	}
	return items, nil
}

func parseProviderTime(value string) *time.Time {
	value = strings.ReplaceAll(strings.TrimSpace(value), "/", "-")
	value = strings.Replace(value, " ", "T", 1)
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		parsed = parsed.UTC()
		return &parsed
	}
	if parsed, err := time.ParseInLocation("2006-1-2T15:04:05.999999999", value, businessday.Shanghai); err == nil {
		parsed = parsed.UTC()
		return &parsed
	}
	if unix, err := strconv.ParseInt(value, 10, 64); err == nil {
		switch len(value) {
		case 10:
			parsed := time.Unix(unix, 0).UTC()
			return &parsed
		case 13:
			parsed := time.UnixMilli(unix).UTC()
			return &parsed
		}
	}
	return nil
}

func (s *Service) createAudit(ctx context.Context, meta MutationMeta, operationType, resourceType, resourceID, summary string) error {
	if s.logs == nil || meta.OperatorUserID == 0 {
		return errors.New("kitesim: operation log is required")
	}
	return s.logs.Create(ctx, &governancedomain.OperationLog{
		OperatorUserID: meta.OperatorUserID, OperationType: operationType,
		ResourceType: resourceType, ResourceID: resourceID,
		Path: strings.TrimSpace(meta.Path), Result: "success",
		SafeSummary: summary, RequestID: strings.TrimSpace(meta.RequestID),
	})
}

func parseImportContent(content string) ([]importAccount, error) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	accounts := make([]importAccount, 0)
	positions := map[string]int{}
	for lineNumber, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		account, password, ok := strings.Cut(line, "----")
		account, password = strings.TrimSpace(account), strings.TrimSpace(password)
		parsed, parseErr := mail.ParseAddress(account)
		if !ok || parseErr != nil || parsed.Address != account || len(account) > 320 || password == "" || len(password) > 512 {
			return nil, fmt.Errorf("%w: line %d must be account----password", ErrInvalidInput, lineNumber+1)
		}
		key := strings.ToLower(account)
		if position, exists := positions[key]; exists {
			accounts[position].Password = password
			continue
		}
		positions[key] = len(accounts)
		accounts = append(accounts, importAccount{Account: account, Password: password})
		if len(accounts) > maxImportAccounts {
			return nil, fmt.Errorf("%w: at most %d accounts", ErrInvalidInput, maxImportAccounts)
		}
	}
	if len(accounts) == 0 {
		return nil, ErrInvalidInput
	}
	return accounts, nil
}

func adminStatus(status PhoneStatus) AdminPhoneStatus {
	switch status {
	case PhoneActive:
		return AdminPhoneActive
	case PhonePending:
		return AdminPhonePending
	case PhoneActivating:
		return AdminPhoneActivating
	case PhoneExpired:
		return AdminPhoneExpired
	case PhoneRefunded:
		return AdminPhoneRefunded
	default:
		return AdminPhoneUnsynced
	}
}

func providerStatus(status AdminPhoneStatus) (PhoneStatus, bool) {
	switch status {
	case AdminPhoneActive:
		return PhoneActive, true
	case AdminPhonePending:
		return PhonePending, true
	case AdminPhoneActivating:
		return PhoneActivating, true
	case AdminPhoneExpired:
		return PhoneExpired, true
	case AdminPhoneRefunded:
		return PhoneRefunded, true
	default:
		return 0, false
	}
}

func safeSyncError(err error) string {
	if errors.Is(err, ErrLoginFailed) {
		return "Kitesim 登录失败，请检查平台账号和密码。"
	}
	return "Kitesim 同步失败，请稍后重试。"
}
