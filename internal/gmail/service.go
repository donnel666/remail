package gmail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/platform"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
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

func stableDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
