package gmail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
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
	"gorm.io/gorm/clause"
)

const gmailPollInterval = 5 * time.Second

type TradePort interface {
	ActivateGmailOrder(ctx context.Context, req tradeapp.ActivateGmailOrderRequest) error
	CompleteGmailOrder(ctx context.Context, orderNo, reason string) error
	FailGmailOrder(ctx context.Context, orderNo, reason string) error
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
		return nil, tradedomain.ErrUpstreamUnavailable
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
		Cost       string `gorm:"column:cost_points"`
		MainWeight int    `gorm:"column:main_weight"`
		DotWeight  int    `gorm:"column:dot_weight"`
		PlusWeight int    `gorm:"column:plus_weight"`
		Available  uint64 `gorm:"column:available"`
	}
	result := s.dbFor(ctx).Table("project_products AS pp").
		Select(`pp.`+costColumn+` AS cost_points,
	pp.main_weight, pp.dot_weight, pp.plus_weight,
	(SELECT COUNT(*) FROM gmail_resources AS gr
	 JOIN email_resources AS er ON er.id = gr.id AND er.type = 'gmail'
	 JOIN users AS owner ON owner.id = er.owner_user_id
	 WHERE gr.status IN (?, ?)
	   AND (
	     (pp.main_weight > 0
	       AND NOT EXISTS (SELECT 1 FROM gmail_allocations AS active WHERE active.source = 'local' AND active.resource_id = gr.id AND active.mailbox = 'main' AND active.status = ?)
	       AND NOT EXISTS (SELECT 1 FROM gmail_allocations AS history WHERE history.source = 'local' AND history.resource_id = gr.id AND history.project_id = pp.project_id AND history.mailbox = 'main'))
	     OR (pp.dot_weight > 0 AND gr.email LIKE '__%@%')
	     OR pp.plus_weight > 0
	   )
	   AND ((? = 'private_first' AND gr.for_sale = FALSE AND er.owner_user_id = ?)
	        OR (gr.for_sale = TRUE AND owner.status = 'active' AND owner.role IN ('supplier','admin','super_admin')))) AS available`,
			LocalResourceNormal, localResourceRollbackNormal, AllocationStatusAllocated, string(policy), buyerUserID).
		Joins("JOIN projects AS p ON p.id = pp.project_id").
		Where("pp.id = ? AND pp.project_id = ? AND pp.type = ? AND pp.status = ? AND "+modeColumn+" = ?", productID, projectID, "gmail", "enabled", true).
		Where("p.status = ?", "listed").
		Where("p.access_type = ? OR EXISTS (SELECT 1 FROM project_accesses AS pa WHERE pa.project_id = p.id AND pa.user_id = ?)", "public", buyerUserID).
		Limit(1).Scan(&row)
	if result.Error != nil {
		return nil, fmt.Errorf("load local Gmail supply: %w", result.Error)
	}
	if result.RowsAffected == 0 || row.Available == 0 || row.MainWeight+row.DotWeight+row.PlusWeight <= 0 {
		return nil, tradedomain.ErrUpstreamUnavailable
	}
	cost, err := money.Parse(row.Cost)
	if err != nil || cost.IsNegative() {
		return nil, tradedomain.ErrUpstreamUnavailable
	}
	return &tradeapp.GmailSupplyQuote{
		Source: SourceLocal, CostPoints: money.Format(cost), Available: row.Available,
	}, nil
}

func (s *Service) FindSessionID(ctx context.Context, orderNo string) (uint, error) {
	var id uint
	err := s.dbFor(ctx).Model(&sessionModel{}).Where("order_no = ?", strings.TrimSpace(orderNo)).Pluck("id", &id).Error
	if err != nil {
		return 0, fmt.Errorf("find Gmail session: %w", err)
	}
	return id, nil
}

func (s *Service) CancelGmailOrder(ctx context.Context, orderNo string) error {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return ErrSessionMissing
	}
	now := s.now()
	var session sessionModel
	finishLocal := false
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_no = ?", orderNo).Take(&session).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if session.Source != SourceLocal {
			return ErrInvalidRoute
		}
		switch session.Status {
		case SessionPending, SessionProvisioning, SessionActive, SessionCancelling:
			if err := tx.Model(&sessionModel{}).Where("id = ?", session.ID).Updates(map[string]any{
				"status": SessionCancelled, "completed_at": now,
				"next_poll_at": now, "last_safe_error": "Gmail 接码会话已取消，订单已退款。",
				"version": gorm.Expr("version + 1"),
			}).Error; err != nil {
				return err
			}
			session.Status = SessionCancelled
			session.CompletedAt = &now
			session.LastSafeError = "Gmail 接码会话已取消，订单已退款。"
			finishLocal = true
		case SessionCancelled, SessionFailed:
			finishLocal = true
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("cancel Gmail session: %w", err)
	}
	if !finishLocal {
		return nil
	}
	if err := s.finishLocalSession(ctx, session); err != nil {
		return fmt.Errorf("cancel local Gmail session: %w", err)
	}
	return nil
}

func (s *Service) CreateSession(ctx context.Context, cmd tradeapp.GmailSessionCommand) (uint, error) {
	cmd.OrderNo = strings.TrimSpace(cmd.OrderNo)
	if cmd.OrderNo == "" || cmd.ProjectID == 0 || cmd.ProductID == 0 || cmd.CodeWindowMinutes <= 0 {
		return 0, ErrInvalidRoute
	}
	var model sessionModel
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("order_no = ?", cmd.OrderNo).Take(&model).Error
		if err == nil {
			if model.Source != SourceLocal || model.ServiceMode != string(tradedomain.ServiceModeCode) {
				return ErrInvalidRoute
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var allocation allocationModel
		if err := tx.Where("order_no = ?", cmd.OrderNo).Take(&allocation).Error; err != nil {
			return fmt.Errorf("load local Gmail code allocation: %w", err)
		}
		if allocation.GuardType != "gmail" || allocation.Source != SourceLocal ||
			allocation.ServiceMode != string(tradedomain.ServiceModeCode) || allocation.ProjectID != cmd.ProjectID ||
			allocation.ProductID != cmd.ProductID || allocation.ResourceID == nil || allocation.Status != AllocationStatusAllocated ||
			!isGmailMailbox(allocation.Mailbox) ||
			(allocation.SupplyScope != AllocationSupplyOwned && allocation.SupplyScope != AllocationSupplyPublic) {
			return ErrInvalidRoute
		}
		cost, err := money.Parse(allocation.CostPointsSnapshot)
		if err != nil || cost.IsNegative() {
			return ErrInvalidRoute
		}
		model = sessionModel{
			OrderNo: cmd.OrderNo, Source: SourceLocal,
			ServiceMode: string(tradedomain.ServiceModeCode), Status: SessionPending, CodesJSON: "[]",
			CostPointsSnapshot: money.Format(cost), Version: 1,
		}
		var resource localResourceModel
		if err := tx.Where("id = ?", *allocation.ResourceID).Take(&resource).Error; err != nil {
			return fmt.Errorf("load local Gmail code resource: %w", err)
		}
		now := s.now()
		expiresAt := now.Add(time.Duration(cmd.CodeWindowMinutes) * time.Minute)
		model.SourceRef = strconv.FormatUint(uint64(allocation.ID), 10)
		model.Email = allocation.Email
		model.Status = SessionActive
		model.StartedAt = &now
		model.ExpiresAt = &expiresAt
		model.NextPollAt = &now
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model)
		if result.Error != nil {
			return fmt.Errorf("create Gmail session: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return tx.Where("order_no = ?", cmd.OrderNo).Take(&model).Error
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return model.ID, nil
}

func (s *Service) ListGmailDeliveries(ctx context.Context, orderNos []string) (map[string]tradeapp.GmailDeliverySummary, error) {
	result := make(map[string]tradeapp.GmailDeliverySummary, len(orderNos))
	if len(orderNos) == 0 {
		return result, nil
	}
	var allocations []allocationModel
	if err := s.dbFor(ctx).Where("order_no IN ? AND source = ?", orderNos, SourceLocal).Find(&allocations).Error; err != nil {
		return nil, fmt.Errorf("list Gmail allocations: %w", err)
	}
	for _, allocation := range allocations {
		result[allocation.OrderNo] = tradeapp.GmailDeliverySummary{AllocationID: allocation.ID}
	}
	var sessions []sessionModel
	if err := s.dbFor(ctx).Where("order_no IN ? AND source = ?", orderNos, SourceLocal).Find(&sessions).Error; err != nil {
		return nil, fmt.Errorf("list Gmail deliveries: %w", err)
	}
	for _, session := range sessions {
		codes, err := decodeCodes(session.CodesJSON)
		if err != nil {
			return nil, err
		}
		items := make([]tradeapp.GmailCode, len(codes))
		for i := range codes {
			items[i] = tradeapp.GmailCode{Seq: codes[i].Seq, Code: codes[i].Code, ReceivedAt: codes[i].ReceivedAt}
		}
		delivery := result[session.OrderNo]
		delivery.Codes = items
		delivery.ReceivedCount = int(session.ReceivedCount)
		delivery.MaxCodes = MaxCodes
		delivery.ExpiresAt = session.ExpiresAt
		result[session.OrderNo] = delivery
	}
	return result, nil
}

func (s *Service) PickupByOrder(ctx context.Context, orderNo, email string) (*CodeOnlyPickup, bool, error) {
	var session sessionModel
	err := s.dbFor(ctx).Where("order_no = ? AND source = ?", strings.TrimSpace(orderNo), SourceLocal).Take(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("load Gmail pickup: %w", err)
	}
	if session.Email == "" || !strings.EqualFold(strings.TrimSpace(email), session.Email) {
		return nil, true, ErrPickupInvalid
	}
	codes, err := decodeCodes(session.CodesJSON)
	if err != nil {
		return nil, true, err
	}
	return &CodeOnlyPickup{
		Email: session.Email, Codes: codes, ReceivedCount: int(session.ReceivedCount), MaxCodes: MaxCodes, ExpiresAt: session.ExpiresAt,
	}, true, nil
}

func stableDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

type sessionTaskPayload struct {
	SessionID uint `json:"sessionId"`
}

func (s *Service) ScheduleProvision(ctx context.Context, sessionID uint) error {
	if sessionID == 0 {
		return ErrSessionMissing
	}
	if s.queue == nil {
		return errors.New("gmail: task queue unavailable")
	}
	payload, _ := json.Marshal(sessionTaskPayload{SessionID: sessionID})
	_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(typeGmailProvision, payload),
		asynq.Queue(platform.QueueDefault), asynq.Unique(time.Minute), asynq.MaxRetry(0),
		asynq.Timeout(30*time.Second), asynq.Retention(0))
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	return err
}

func (s *Service) schedulePoll(ctx context.Context, sessionID uint) error {
	if sessionID == 0 || s.queue == nil {
		return errors.New("gmail: task queue unavailable")
	}
	payload, _ := json.Marshal(sessionTaskPayload{SessionID: sessionID})
	_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(typeGmailPoll, payload),
		asynq.Queue(platform.QueueMailfetch), asynq.Unique(4*time.Second), asynq.MaxRetry(2),
		asynq.Timeout(20*time.Second), asynq.Retention(0))
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	return err
}

func (s *Service) DispatchDueSessions(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 200
	}
	now := s.now()
	var provisionIDs []uint
	if err := s.dbFor(ctx).Model(&sessionModel{}).
		Where("source = ? AND (status = ? OR (status IN ? AND next_poll_at IS NOT NULL AND next_poll_at <= ?))",
			SourceLocal, SessionPending, []string{SessionProvisioning, SessionFailed}, now).
		Order("id ASC").Limit(limit).Pluck("id", &provisionIDs).Error; err != nil {
		return 0, fmt.Errorf("list Gmail provision sessions: %w", err)
	}
	queued := 0
	for _, id := range provisionIDs {
		if err := s.ScheduleProvision(ctx, id); err != nil {
			return queued, err
		}
		queued++
	}
	remaining := max(limit-len(provisionIDs), 0)
	if remaining == 0 {
		return queued, nil
	}
	var pollIDs []uint
	if err := s.dbFor(ctx).Model(&sessionModel{}).
		Where("source = ? AND status IN ? AND next_poll_at IS NOT NULL AND next_poll_at <= ?",
			SourceLocal, []string{SessionActive, SessionCompleted, SessionCancelled}, now).
		Order("id ASC").Limit(remaining).Pluck("id", &pollIDs).Error; err != nil {
		return queued, fmt.Errorf("list Gmail poll sessions: %w", err)
	}
	for _, id := range pollIDs {
		if err := s.schedulePoll(ctx, id); err != nil {
			return queued, err
		}
		queued++
	}
	return queued, nil
}

func (s *Service) Provision(ctx context.Context, sessionID uint) error {
	var session sessionModel
	if err := s.dbFor(ctx).First(&session, sessionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSessionMissing
		}
		return err
	}
	if session.Source != SourceLocal {
		return ErrInvalidRoute
	}
	switch session.Status {
	case SessionActive:
		if err := s.ensureTradeActivation(ctx, session); err != nil {
			return err
		}
		return s.schedulePoll(context.WithoutCancel(ctx), session.ID)
	case SessionCompleted, SessionCancelled, SessionFailed:
		return s.finishLocalSession(ctx, session)
	default:
		return nil
	}
}
func (s *Service) ensureTradeActivation(ctx context.Context, session sessionModel) error {
	if s.trade == nil || session.StartedAt == nil || session.ExpiresAt == nil || session.Email == "" {
		return errors.New("gmail: activation callback unavailable")
	}
	allocationID, err := s.ensureCodeAllocation(ctx, session)
	if err != nil {
		return err
	}
	return s.trade.ActivateGmailOrder(ctx, tradeapp.ActivateGmailOrderRequest{
		OrderNo: session.OrderNo, AllocationID: allocationID, SessionID: session.ID, Email: session.Email,
		StartedAt: session.StartedAt.UTC(), ExpiresAt: session.ExpiresAt.UTC(),
	})
}

func (s *Service) ensureCodeAllocation(ctx context.Context, session sessionModel) (uint, error) {
	if session.ID == 0 || session.Source != SourceLocal || strings.TrimSpace(session.OrderNo) == "" ||
		strings.TrimSpace(session.Email) == "" || session.ServiceMode != string(tradedomain.ServiceModeCode) {
		return 0, ErrInvalidRoute
	}
	var model allocationModel
	if err := s.dbFor(ctx).Where("order_no = ?", session.OrderNo).Take(&model).Error; err != nil {
		return 0, fmt.Errorf("load local Gmail code allocation: %w", err)
	}
	if model.ResourceID == nil || model.Source != SourceLocal || model.ServiceMode != string(tradedomain.ServiceModeCode) ||
		model.GuardType != "gmail" || model.ProjectID == 0 || model.ProductID == 0 ||
		(model.SupplyScope != AllocationSupplyOwned && model.SupplyScope != AllocationSupplyPublic) ||
		!isGmailMailbox(model.Mailbox) || model.Status != AllocationStatusAllocated ||
		!strings.EqualFold(model.Email, session.Email) {
		return 0, errors.New("gmail: allocation conflict")
	}
	if sourceID, err := strconv.ParseUint(session.SourceRef, 10, 64); err != nil || sourceID != uint64(model.ID) {
		return 0, errors.New("gmail: allocation conflict")
	}
	return model.ID, nil
}
func (s *Service) Poll(ctx context.Context, sessionID uint) error {
	var session sessionModel
	if err := s.dbFor(ctx).First(&session, sessionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSessionMissing
		}
		return err
	}
	if session.Source != SourceLocal {
		return ErrInvalidRoute
	}
	return s.pollLocalSession(ctx, session)
}
func gmailCompletionReason(session sessionModel) string {
	if session.ReceivedCount >= MaxCodes {
		return "Gmail 已接收 3 个验证码，接码会话完成。"
	}
	return fmt.Sprintf("Gmail 接码窗口结束，共接收 %d 个验证码。", session.ReceivedCount)
}

func (s *Service) deferPoll(ctx context.Context, sessionID uint, safeMessage string, cause error) error {
	updates := map[string]any{"next_poll_at": s.now().Add(gmailPollInterval)}
	if safeMessage != "" {
		updates["last_safe_error"] = safeMessage
	}
	if err := s.dbFor(ctx).Model(&sessionModel{}).Where("id = ?", sessionID).Updates(updates).Error; err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func (s *Service) clearNextPoll(ctx context.Context, sessionID uint) error {
	return s.dbFor(ctx).Model(&sessionModel{}).Where("id = ?", sessionID).Update("next_poll_at", nil).Error
}
