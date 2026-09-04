package gmail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/money"
	"github.com/donnel666/remail/internal/platform"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type TradePort interface {
	RefundUnavailableGmailOrders(ctx context.Context, resourceID uint, requestID string) (int, error)
	ImportHistoricalGmailUsage(ctx context.Context, history []tradeapp.HistoricalGmailUsage) error
}

type Service struct {
	db                  *gorm.DB
	queue               *asynq.Client
	trade               TradePort
	mail                MailIngestPort
	redis               redis.UniversalClient
	files               governanceapp.FilePort
	logs                *governanceinfra.OperationLogRepo
	systemLogs          *governanceinfra.SystemLogRepo
	backgroundExecution *platform.BackgroundLoadController
	validateImportOwner func(context.Context, uint) (bool, error)
	now                 func() time.Time
	fetch               localGmailFetchFunc
	pickup              *localGmailPickupClient
	validationProxies   localGmailPickupProxyProvider
}

func NewService(db *gorm.DB, queue *asynq.Client) *Service {
	pickup := newLocalGmailPickupClient(nil)
	service := &Service{
		db: db, queue: queue,
		now:   func() time.Time { return time.Now().UTC() },
		fetch: pickup.Fetch, pickup: pickup,
	}
	if db != nil {
		service.logs = governanceinfra.NewOperationLogRepo(db)
		service.systemLogs = governanceinfra.NewSystemLogRepo(db)
	}
	return service
}

func (s *Service) SetTrade(port TradePort)           { s.trade = port }
func (s *Service) SetMailIngest(port MailIngestPort) { s.mail = port }
func (s *Service) SetResourceImportDependencies(redisClient redis.UniversalClient, files governanceapp.FilePort) {
	if s != nil {
		s.redis, s.files = redisClient, files
	}
}
func (s *Service) SetImportOwnerValidator(validate func(context.Context, uint) (bool, error)) {
	if s != nil {
		s.validateImportOwner = validate
	}
}

func (s *Service) SetBackgroundExecutionGate(gate *platform.BackgroundLoadController) {
	if s != nil {
		s.backgroundExecution = gate
	}
}

func (s *Service) dbFor(ctx context.Context) *gorm.DB {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return tx.WithContext(ctx)
	}
	return s.db.WithContext(ctx)
}

func (s *Service) CheckSupply(
	ctx context.Context,
	projectID, productID, buyerUserID uint,
	mode tradedomain.ServiceMode,
	policy tradedomain.SupplyPolicy,
	payAmount string,
) (*tradeapp.GmailSupplyQuote, error) {
	if projectID == 0 || productID == 0 || buyerUserID == 0 ||
		(mode != tradedomain.ServiceModeCode && mode != tradedomain.ServiceModePurchase) ||
		(policy != tradedomain.SupplyPolicyPrivateFirst && policy != tradedomain.SupplyPolicyPublicOnly) {
		return nil, tradedomain.ErrInsufficientInventory
	}
	pay, err := money.Parse(payAmount)
	if err != nil || pay.IsNegative() {
		return nil, tradedomain.ErrInvalidOrderRequest
	}
	return s.checkLocalSupply(ctx, projectID, productID, buyerUserID, mode, policy)
}

func (s *Service) checkLocalSupply(
	ctx context.Context,
	projectID, productID, buyerUserID uint,
	mode tradedomain.ServiceMode,
	policy tradedomain.SupplyPolicy,
) (*tradeapp.GmailSupplyQuote, error) {
	modeColumn, costColumn := "pp.code_enabled", "code_supplier_price"
	if mode == tradedomain.ServiceModePurchase {
		modeColumn, costColumn = "pp.purchase_enabled", "purchase_supplier_price"
	}
	var row struct {
		Cost      string `gorm:"column:cost_points"`
		Available uint64 `gorm:"column:available"`
	}
	dotLocalLength := "LENGTH(REPLACE(SUBSTR(gr.email, 1, INSTR(gr.email, '@') - 1), '.', ''))"
	dotCapacity := "((1 << (" + dotLocalLength + " - 1)) - 1)"
	result := s.dbFor(ctx).Table("project_products AS pp").
		Select(`pp.`+costColumn+` AS cost_points,
	(SELECT COUNT(*) FROM gmail_resources AS gr
	 JOIN email_resources AS er ON er.id = gr.id AND er.type = 'gmail'
	 JOIN users AS owner ON owner.id = er.owner_user_id
	 WHERE gr.status IN (?, ?)
	   AND (
	     pp.type = 'gmail_variant'
	     OR (pp.type = 'gmail'
	       AND `+dotLocalLength+` BETWEEN 2 AND 30
	       AND (SELECT COUNT(*) FROM gmail_allocations AS history
	            WHERE history.source = 'local'
	              AND history.resource_id = gr.id
	              AND history.project_id = pp.project_id
	              AND history.mailbox = 'dot') < `+dotCapacity+`)
	   )
	   AND ((? = 'private_first' AND gr.for_sale = FALSE AND er.owner_user_id = ?)
	        OR (gr.for_sale = TRUE AND owner.status = 'active' AND owner.role IN ('supplier','admin','super_admin')))) AS available`,
			LocalResourceNormal, localResourceRollbackNormal, string(policy), buyerUserID).
		Joins("JOIN projects AS p ON p.id = pp.project_id").
		Where("pp.id = ? AND pp.project_id = ? AND pp.type IN ? AND pp.status = ? AND "+modeColumn+" = ?", productID, projectID, []string{"gmail", "gmail_variant"}, "enabled", true).
		Where("p.status = ?", "listed").
		Where("p.access_type = ? OR EXISTS (SELECT 1 FROM project_accesses AS pa WHERE pa.project_id = p.id AND pa.user_id = ?)", "public", buyerUserID).
		Limit(1).Scan(&row)
	if result.Error != nil {
		return nil, fmt.Errorf("load local Gmail supply: %w", result.Error)
	}
	if result.RowsAffected == 0 || row.Available == 0 {
		return nil, tradedomain.ErrInsufficientInventory
	}
	cost, err := money.Parse(row.Cost)
	if err != nil || cost.IsNegative() {
		return nil, tradedomain.ErrInsufficientInventory
	}
	return &tradeapp.GmailSupplyQuote{
		Source: SourceLocal, CostPoints: money.Format(cost), Available: row.Available,
	}, nil
}

func stableDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
