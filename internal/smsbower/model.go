package smsbower

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/donnel666/remail/internal/upstream"
)

const (
	StatusPending      = "pending"
	StatusProvisioning = "provisioning"
	StatusActive       = "active"
	StatusCompleting   = "completing"
	StatusCompleted    = "completed"
	StatusCancelling   = "cancelling"
	StatusCancelled    = "cancelled"
	StatusFailed       = "failed"
	StatusUnknown      = "unknown"

	ActionWaitNext = "wait_next"
	ActionComplete = "complete"
	ActionCancel   = "cancel"

	MaxCodes = 3
)

var (
	ErrInvalidConfig = errors.New("smsbower: invalid config")
	ErrInvalidRoute  = errors.New("smsbower: invalid route")
	ErrRouteNotFound = errors.New("smsbower: route not found")
	ErrOrderMissing  = errors.New("smsbower: order not found")
	ErrPickupInvalid = upstream.ErrPickupInvalid
	errPaidOrderTx   = errors.New("smsbower: paid order transaction required")
)

type configModel struct {
	ID                      uint      `gorm:"column:id;primaryKey"`
	Enabled                 bool      `gorm:"column:enabled"`
	APIKey                  string    `gorm:"column:api_key"`
	Strategy                string    `gorm:"column:strategy"`
	SyncIntervalMinutes     uint      `gorm:"column:sync_interval_minutes"`
	BalanceWarningThreshold string    `gorm:"column:balance_warning_threshold"`
	PointsPerUnit           string    `gorm:"column:points_per_unit"`
	MinMarginRate           string    `gorm:"column:min_margin_rate"`
	CreatedAt               time.Time `gorm:"column:created_at"`
	UpdatedAt               time.Time `gorm:"column:updated_at"`
}

func (configModel) TableName() string { return "smsbower_config" }

type accountStateModel struct {
	ID                  uint       `gorm:"column:id;primaryKey"`
	Balance             string     `gorm:"column:balance"`
	HealthStatus        string     `gorm:"column:health_status"`
	ConsecutiveFailures uint       `gorm:"column:consecutive_failures"`
	LastSafeError       string     `gorm:"column:last_safe_error"`
	BalanceAlertActive  bool       `gorm:"column:balance_alert_active"`
	FailureAlertActive  bool       `gorm:"column:failure_alert_active"`
	Generation          uint64     `gorm:"column:generation"`
	LastSyncedAt        *time.Time `gorm:"column:last_synced_at"`
	LastSuccessAt       *time.Time `gorm:"column:last_success_at"`
	CreatedAt           time.Time  `gorm:"column:created_at"`
	UpdatedAt           time.Time  `gorm:"column:updated_at"`
}

func (accountStateModel) TableName() string { return "smsbower_account_state" }

type serviceModel struct {
	Code              string     `gorm:"column:code;primaryKey"`
	Name              string     `gorm:"column:name"`
	GmailPrice        string     `gorm:"column:gmail_price"`
	GmailStock        uint       `gorm:"column:gmail_stock"`
	PreviousPrice     *string    `gorm:"column:previous_price"`
	LastNotifiedPrice *string    `gorm:"column:last_notified_price"`
	Active            bool       `gorm:"column:active"`
	PriceChangedAt    *time.Time `gorm:"column:price_changed_at"`
	LastSeenAt        time.Time  `gorm:"column:last_seen_at"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
	UpdatedAt         time.Time  `gorm:"column:updated_at"`
}

func (serviceModel) TableName() string { return "smsbower_services" }

type routeModel struct {
	ProjectID   uint      `gorm:"column:project_id;primaryKey"`
	ServiceCode string    `gorm:"column:service_code"`
	Enabled     bool      `gorm:"column:enabled"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (routeModel) TableName() string { return "smsbower_project_routes" }

type orderGuardModel struct {
	OrderNo   string    `gorm:"column:order_no;primaryKey"`
	Type      string    `gorm:"column:type"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (orderGuardModel) TableName() string { return "allocation_order_guards" }

type orderModel struct {
	ID                    uint       `gorm:"column:id;primaryKey"`
	OrderNo               string     `gorm:"column:order_no"`
	ProjectID             uint       `gorm:"column:project_id"`
	ProductID             uint       `gorm:"column:product_id"`
	ServiceCode           string     `gorm:"column:service_code"`
	RemoteMailID          *uint64    `gorm:"column:remote_mail_id"`
	Email                 string     `gorm:"column:email"`
	Status                string     `gorm:"column:status"`
	ReceivedCount         uint8      `gorm:"column:received_count"`
	CodesJSON             string     `gorm:"column:codes_json"`
	UpstreamPriceSnapshot string     `gorm:"column:upstream_price_snapshot"`
	PointsPerUnitSnapshot string     `gorm:"column:points_per_unit_snapshot"`
	CostPointsSnapshot    string     `gorm:"column:cost_points_snapshot"`
	MaxPriceSnapshot      string     `gorm:"column:max_price_snapshot"`
	PendingRemoteAction   string     `gorm:"column:pending_remote_action"`
	NextPollAt            *time.Time `gorm:"column:next_poll_at"`
	LastSafeError         string     `gorm:"column:last_safe_error"`
	Version               uint       `gorm:"column:version"`
	StartedAt             *time.Time `gorm:"column:started_at"`
	ExpiresAt             *time.Time `gorm:"column:expires_at"`
	CompletedAt           *time.Time `gorm:"column:completed_at"`
	CreatedAt             time.Time  `gorm:"column:created_at"`
	UpdatedAt             time.Time  `gorm:"column:updated_at"`
}

func (orderModel) TableName() string { return "smsbower_orders" }

type Code struct {
	Seq        int       `json:"seq"`
	Code       string    `json:"code"`
	ReceivedAt time.Time `json:"receivedAt"`
}

func decodeCodes(raw string) ([]Code, error) {
	if len(raw) == 0 {
		return []Code{}, nil
	}
	var codes []Code
	if err := json.Unmarshal([]byte(raw), &codes); err != nil || len(codes) > MaxCodes {
		return nil, errors.New("smsbower: invalid stored codes")
	}
	return codes, nil
}

type Config struct {
	Enabled                    bool              `json:"enabled"`
	Configured                 bool              `json:"configured"`
	Strategy                   upstream.Strategy `json:"strategy"`
	SyncIntervalMinutes        uint              `json:"syncIntervalMinutes"`
	NoCodeRefundTimeoutMinutes uint              `json:"noCodeRefundTimeoutMinutes"`
	BalanceWarningThreshold    string            `json:"balanceWarningThreshold"`
	PointsPerUnit              string            `json:"pointsPerUnit"`
	MinMarginRate              string            `json:"minMarginRate"`
}

type ConfigUpdate struct {
	Enabled                    bool              `json:"enabled"`
	APIKey                     string            `json:"apiKey"`
	Strategy                   upstream.Strategy `json:"strategy"`
	SyncIntervalMinutes        uint              `json:"syncIntervalMinutes"`
	NoCodeRefundTimeoutMinutes *uint             `json:"noCodeRefundTimeoutMinutes,omitempty"`
	BalanceWarningThreshold    string            `json:"balanceWarningThreshold"`
	PointsPerUnit              string            `json:"pointsPerUnit"`
	MinMarginRate              string            `json:"minMarginRate"`
}

type AccountStatus struct {
	Enabled             bool       `json:"enabled"`
	Configured          bool       `json:"configured"`
	Balance             string     `json:"balance"`
	HealthStatus        string     `json:"healthStatus"`
	ConsecutiveFailures uint       `json:"consecutiveFailures"`
	LastSafeError       string     `json:"lastSafeError,omitempty"`
	LastSyncedAt        *time.Time `json:"lastSyncedAt,omitempty"`
	LastSuccessAt       *time.Time `json:"lastSuccessAt,omitempty"`
}

type ServiceItem struct {
	Code           string     `json:"code"`
	Name           string     `json:"name"`
	GmailPrice     string     `json:"gmailPrice"`
	GmailStock     uint       `json:"gmailStock"`
	PreviousPrice  *string    `json:"previousPrice,omitempty"`
	Active         bool       `json:"active"`
	PriceChangedAt *time.Time `json:"priceChangedAt,omitempty"`
	LastSeenAt     time.Time  `json:"lastSeenAt"`
}

type InventoryItem struct {
	ProjectID     uint
	ProductID     uint
	CodeAvailable int64
}

type MappingItem struct {
	ProjectID           uint   `json:"projectId"`
	ProjectName         string `json:"projectName"`
	ProviderServiceCode string `json:"providerServiceCode"`
	ProviderServiceName string `json:"providerServiceName,omitempty"`
	Enabled             bool   `json:"enabled"`
	UpstreamPrice       string `json:"upstreamPrice"`
	CostPoints          string `json:"costPoints"`
	CodePrice           string `json:"codePrice"`
	PurchasePrice       string `json:"purchasePrice"`
}

type FinanceOverview struct {
	OrderCount             int64  `json:"orderCount"`
	ActivationCount        int64  `json:"activationCount"`
	ZeroCodeCount          int64  `json:"zeroCodeCount"`
	OneCodeCount           int64  `json:"oneCodeCount"`
	TwoCodeCount           int64  `json:"twoCodeCount"`
	ThreeCodeCount         int64  `json:"threeCodeCount"`
	Sales                  string `json:"sales"`
	Refunds                string `json:"refunds"`
	NetRevenue             string `json:"netRevenue"`
	SettledCost            string `json:"settledCost"`
	ReservedCost           string `json:"reservedCost"`
	UnknownCost            string `json:"unknownCost"`
	ConservativeCost       string `json:"conservativeCost"`
	ConservativeProfit     string `json:"conservativeProfit"`
	ConservativeMarginRate string `json:"conservativeMarginRate"`
}

type FinanceBreakdown struct {
	Key        string `json:"key"`
	Name       string `json:"name"`
	OrderCount int64  `json:"orderCount"`
	NetRevenue string `json:"netRevenue"`
	Cost       string `json:"cost"`
	Profit     string `json:"profit"`
}

type FinanceReport struct {
	Overview  FinanceOverview    `json:"overview"`
	ByProject []FinanceBreakdown `json:"byProject"`
	ByService []FinanceBreakdown `json:"byService"`
	BySource  []FinanceBreakdown `json:"bySource"`
}

type ActivationItem struct {
	ID                  uint       `json:"id"`
	OrderNo             string     `json:"orderNo"`
	ProjectID           uint       `json:"projectId"`
	ProjectName         string     `json:"projectName"`
	Source              string     `json:"source"`
	ProviderServiceCode string     `json:"providerServiceCode"`
	Email               string     `json:"email"`
	Status              string     `json:"status"`
	ReceivedCount       uint8      `json:"receivedCount"`
	CostPoints          string     `json:"costPoints"`
	LastSafeError       string     `json:"lastSafeError,omitempty"`
	StartedAt           *time.Time `json:"startedAt,omitempty"`
	ExpiresAt           *time.Time `json:"expiresAt,omitempty"`
	CompletedAt         *time.Time `json:"completedAt,omitempty"`
	CreatedAt           time.Time  `json:"createdAt"`
}

type Alert struct {
	ID      string
	Subject string
	Body    string
}

type AlertNotifier interface {
	NotifySMSBower(context.Context, Alert) error
}
