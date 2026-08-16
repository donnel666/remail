package kitesim

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

	"github.com/donnel666/remail/internal/platform"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type OperationKind string
type OperationStatus string

const (
	OperationPurchase OperationKind = "purchase"
	OperationRecharge OperationKind = "recharge"
	OperationRenew    OperationKind = "renew"

	OperationQueued         OperationStatus = "queued"
	OperationRunning        OperationStatus = "running"
	OperationSucceeded      OperationStatus = "succeeded"
	OperationFailed         OperationStatus = "failed"
	OperationUncertain      OperationStatus = "uncertain"
	OperationRequiresAction OperationStatus = "requires_action"
)

var (
	ErrOperationMissing    = errors.New("kitesim: operation not found")
	ErrOperationState      = errors.New("kitesim: operation state conflict")
	ErrIdempotencyRequired = errors.New("kitesim: idempotency key required")
	ErrIdempotencyConflict = errors.New("kitesim: idempotency key conflict")
	ErrPriceChanged        = errors.New("kitesim: upstream price exceeds confirmed limit")
	ErrPaymentUncertain    = errors.New("kitesim: payment result uncertain")
	ErrThreeDSRequired     = errors.New("kitesim: 3DS verification required")
	errOperationExpired    = errors.New("kitesim: queued recharge expired")
)

type operationModel struct {
	ID                   uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	Kind                 string     `gorm:"column:kind"`
	AccountID            uint       `gorm:"column:account_id"`
	PhoneID              *uint      `gorm:"column:phone_id"`
	CountryCode          string     `gorm:"column:country_code"`
	PackageID            string     `gorm:"column:package_id"`
	RequestedCount       int        `gorm:"column:requested_count"`
	CompletedCount       int        `gorm:"column:completed_count"`
	Amount               string     `gorm:"column:amount"`
	Currency             string     `gorm:"column:currency"`
	CardRevision         uint64     `gorm:"column:card_revision"`
	Status               string     `gorm:"column:status"`
	Attempts             int        `gorm:"column:attempts"`
	ProviderOrderNos     jsonText   `gorm:"column:provider_order_nos;type:json"`
	SecretPayload        jsonText   `gorm:"column:secret_payload;type:json"`
	LastSafeError        string     `gorm:"column:last_safe_error"`
	OperatorUserID       uint       `gorm:"column:operator_user_id;uniqueIndex:uk_kitesim_operations_idempotency"`
	IdempotencyKey       string     `gorm:"column:idempotency_key;uniqueIndex:uk_kitesim_operations_idempotency"`
	RequestFingerprint   string     `gorm:"column:request_fingerprint"`
	RequestID            string     `gorm:"column:request_id"`
	Path                 string     `gorm:"column:path"`
	ReconcileRequestedAt *time.Time `gorm:"column:reconcile_requested_at"`
	LastReconciledAt     *time.Time `gorm:"column:last_reconciled_at"`
	ReconcileAttempts    int        `gorm:"column:reconcile_attempts"`
	ResolutionSource     string     `gorm:"column:resolution_source"`
	ResolutionNote       string     `gorm:"column:resolution_note"`
	ResolvedByUserID     *uint      `gorm:"column:resolved_by_user_id"`
	ResolvedAt           *time.Time `gorm:"column:resolved_at"`
	QueuedAt             time.Time  `gorm:"column:queued_at"`
	StartedAt            *time.Time `gorm:"column:started_at"`
	FinishedAt           *time.Time `gorm:"column:finished_at"`
	CreatedAt            time.Time  `gorm:"column:created_at"`
	UpdatedAt            time.Time  `gorm:"column:updated_at"`
}

func (operationModel) TableName() string { return "kitesim_operations" }

type OperationItem struct {
	ID                   uint64          `json:"id"`
	Kind                 OperationKind   `json:"kind"`
	Status               OperationStatus `json:"status"`
	AccountID            uint            `json:"accountId"`
	Account              string          `json:"account,omitempty"`
	PhoneID              *uint           `json:"phoneId,omitempty"`
	PhoneNumber          string          `json:"phoneNumber,omitempty"`
	CountryCode          string          `json:"countryCode,omitempty"`
	PackageID            string          `json:"packageId,omitempty"`
	RequestedCount       int             `json:"requestedCount"`
	CompletedCount       int             `json:"completedCount"`
	Amount               string          `json:"amount"`
	Currency             string          `json:"currency,omitempty"`
	Attempts             int             `json:"attempts"`
	ProviderOrderNos     []string        `json:"providerOrderNos"`
	LastSafeError        string          `json:"lastSafeError,omitempty"`
	ReconcileAttempts    int             `json:"reconcileAttempts"`
	ReconcileRequestedAt *time.Time      `json:"reconcileRequestedAt,omitempty"`
	LastReconciledAt     *time.Time      `json:"lastReconciledAt,omitempty"`
	ResolutionSource     string          `json:"resolutionSource,omitempty"`
	ResolutionNote       string          `json:"resolutionNote,omitempty"`
	ResolvedAt           *time.Time      `json:"resolvedAt,omitempty"`
	QueuedAt             time.Time       `json:"queuedAt"`
	StartedAt            *time.Time      `json:"startedAt,omitempty"`
	FinishedAt           *time.Time      `json:"finishedAt,omitempty"`
}

type operationProviderRefs struct {
	OrderNos              []string `json:"orderNos,omitempty"`
	OutTransNo            string   `json:"outTransNo,omitempty"`
	PayOrderID            string   `json:"payOrderId,omitempty"`
	PreviousExpireTime    string   `json:"previousExpireTime,omitempty"`
	PreviousLatestRenewal string   `json:"previousLatestRenewal,omitempty"`
}

func (s *Service) QueuePurchase(ctx context.Context, productID uint, count int, maxUnitPrice string, meta MutationMeta) (*OperationItem, error) {
	limit, err := normalizedPositiveMoney(maxUnitPrice)
	if productID == 0 || count < 1 || count > 100 || err != nil {
		return nil, ErrInvalidInput
	}
	settings, err := s.loadUpstreamSettings(ctx)
	if err != nil {
		return nil, err
	}
	if settings.AccountID == nil {
		return nil, ErrUpstreamNotConfigured
	}
	var product productModel
	if err := s.db.WithContext(ctx).Where("id = ? AND active = ?", productID, true).First(&product).Error; err != nil {
		return nil, ErrInvalidInput
	}
	if priceExceeds(product.BuyPrice, limit) {
		return nil, ErrPriceChanged
	}
	operation := operationModel{
		Kind: string(OperationPurchase), AccountID: *settings.AccountID,
		CountryCode: product.CountryCode, PackageID: product.PackageID,
		RequestedCount: count, Amount: limit, Currency: product.Currency,
	}
	operation.RequestFingerprint, err = operationFingerprint(struct {
		Kind         OperationKind `json:"kind"`
		AccountID    uint          `json:"accountId"`
		ProductID    uint          `json:"productId"`
		Count        int           `json:"count"`
		MaxUnitPrice string        `json:"maxUnitPrice"`
	}{OperationPurchase, operation.AccountID, productID, count, limit})
	if err != nil {
		return nil, err
	}
	return s.createAndEnqueueOperation(ctx, operation, meta)
}

func (s *Service) QueueRecharge(ctx context.Context, amount, cvc string, meta MutationMeta) (*OperationItem, error) {
	parsed, err := decimal.NewFromString(strings.TrimSpace(amount))
	if err != nil || !parsed.IsPositive() || parsed.GreaterThan(decimal.NewFromInt(10000)) || !validCVC(cvc) {
		return nil, ErrInvalidInput
	}
	settings, err := s.loadUpstreamSettings(ctx)
	if err != nil {
		return nil, err
	}
	if settings.AccountID == nil {
		return nil, ErrUpstreamNotConfigured
	}
	if len(settings.CardData) == 0 {
		return nil, ErrCardNotConfigured
	}
	secret, err := json.Marshal(struct {
		CVC string `json:"cvc"`
	}{CVC: strings.TrimSpace(cvc)})
	if err != nil {
		return nil, err
	}
	operation := operationModel{
		Kind: string(OperationRecharge), AccountID: *settings.AccountID,
		RequestedCount: 1, Amount: parsed.String(), Currency: "HKD",
		CardRevision: settings.CardRevision, SecretPayload: jsonText(secret),
	}
	operation.RequestFingerprint, err = operationFingerprint(struct {
		Kind         OperationKind `json:"kind"`
		AccountID    uint          `json:"accountId"`
		Amount       string        `json:"amount"`
		CardRevision uint64        `json:"cardRevision"`
	}{OperationRecharge, operation.AccountID, parsed.String(), settings.CardRevision})
	if err != nil {
		return nil, err
	}
	return s.createAndEnqueueOperation(ctx, operation, meta)
}

func (s *Service) QueueRenewal(ctx context.Context, phoneID, productID uint, maxUnitPrice string, meta MutationMeta) (*OperationItem, error) {
	limit, err := normalizedPositiveMoney(maxUnitPrice)
	if phoneID == 0 || productID == 0 || err != nil {
		return nil, ErrInvalidInput
	}
	var phone phoneModel
	if err := s.db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL AND disabled_at IS NULL", phoneID).
		First(&phone).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPhoneMissing
		}
		return nil, fmt.Errorf("load Kitesim phone renewal target: %w", err)
	}
	if PhoneStatus(phone.Status) != PhoneActive && PhoneStatus(phone.Status) != PhoneExpired {
		return nil, ErrOperationState
	}
	var product productModel
	if err := s.db.WithContext(ctx).
		Where("id = ? AND active = ? AND country_code = ?", productID, true, phone.CountryCode).
		First(&product).Error; err != nil {
		return nil, ErrInvalidInput
	}
	if priceExceeds(product.BuyPrice, limit) {
		return nil, ErrPriceChanged
	}
	operation := operationModel{
		Kind: string(OperationRenew), AccountID: phone.AccountID, PhoneID: &phone.ID,
		CountryCode: phone.CountryCode, PackageID: product.PackageID,
		RequestedCount: 1, Amount: limit, Currency: product.Currency,
	}
	operation.RequestFingerprint, err = operationFingerprint(struct {
		Kind         OperationKind `json:"kind"`
		PhoneID      uint          `json:"phoneId"`
		ProductID    uint          `json:"productId"`
		MaxUnitPrice string        `json:"maxUnitPrice"`
	}{OperationRenew, phoneID, productID, limit})
	if err != nil {
		return nil, err
	}
	return s.createAndEnqueueOperation(ctx, operation, meta)
}

func (s *Service) createAndEnqueueOperation(ctx context.Context, operation operationModel, meta MutationMeta) (*OperationItem, error) {
	queue, ok := s.queue.(OperationQueue)
	if !ok || queue == nil {
		return nil, errors.New("kitesim: task queue unavailable")
	}
	idempotencyKey, err := normalizeOperationIdempotencyKey(meta.IdempotencyKey)
	if err != nil || meta.OperatorUserID == 0 || operation.RequestFingerprint == "" {
		return nil, ErrIdempotencyRequired
	}
	if stored, found, err := s.idempotentOperation(ctx, meta.OperatorUserID, idempotencyKey); err != nil {
		return nil, err
	} else if found {
		if stored.RequestFingerprint != operation.RequestFingerprint {
			return nil, ErrIdempotencyConflict
		}
		return operationView(stored, "", ""), nil
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	operation.Status = string(OperationQueued)
	operation.QueuedAt = now
	operation.OperatorUserID = meta.OperatorUserID
	operation.IdempotencyKey = idempotencyKey
	operation.RequestID = strings.TrimSpace(meta.RequestID)
	operation.Path = strings.TrimSpace(meta.Path)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.validateOperationLifecycle(tx, operation); err != nil {
			return err
		}
		if err := tx.Create(&operation).Error; err != nil {
			return err
		}
		return s.createAudit(
			platform.WithGormTx(ctx, tx), meta, "kitesim.operation."+operation.Kind,
			"kitesim_operation", strconv.FormatUint(operation.ID, 10),
			fmt.Sprintf("queued Kitesim %s operation", operation.Kind),
		)
	})
	if err != nil {
		if !duplicateOperationError(err) {
			return nil, fmt.Errorf("create Kitesim operation: %w", err)
		}
		stored, found, loadErr := s.idempotentOperation(ctx, meta.OperatorUserID, idempotencyKey)
		if loadErr != nil {
			return nil, loadErr
		}
		if found {
			if stored.RequestFingerprint != operation.RequestFingerprint {
				return nil, ErrIdempotencyConflict
			}
			return operationView(stored, "", ""), nil
		}
		return nil, ErrOperationBusy
	}
	_, _ = queue.EnqueueOperation(ctx, operation.ID)
	return operationView(operation, "", ""), nil
}

func (s *Service) validateOperationLifecycle(tx *gorm.DB, operation operationModel) error {
	var account accountModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").Where("id = ? AND deleted_at IS NULL", operation.AccountID).First(&account).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrAccountMissing
		}
		return fmt.Errorf("lock Kitesim operation account: %w", err)
	}

	switch OperationKind(operation.Kind) {
	case OperationPurchase, OperationRecharge:
		var settings upstreamSettingsModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("account_id", "card_profile", "card_revision").First(&settings, upstreamSettingsID).Error; err != nil {
			return fmt.Errorf("lock Kitesim upstream settings: %w", err)
		}
		if settings.AccountID == nil || *settings.AccountID != operation.AccountID {
			return ErrUpstreamNotConfigured
		}
		if OperationKind(operation.Kind) == OperationRecharge {
			if len(settings.CardData) == 0 {
				return ErrCardNotConfigured
			}
			if settings.CardRevision != operation.CardRevision {
				return ErrOperationState
			}
		}
	case OperationRenew:
		if operation.PhoneID == nil || *operation.PhoneID == 0 {
			return ErrPhoneMissing
		}
		var phone phoneModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "status").
			Where("id = ? AND account_id = ? AND deleted_at IS NULL AND disabled_at IS NULL", *operation.PhoneID, operation.AccountID).
			First(&phone).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPhoneMissing
			}
			return fmt.Errorf("lock Kitesim renewal phone: %w", err)
		}
		if PhoneStatus(phone.Status) != PhoneActive && PhoneStatus(phone.Status) != PhoneExpired {
			return ErrOperationState
		}
	default:
		return ErrInvalidInput
	}
	return nil
}

func (s *Service) idempotentOperation(ctx context.Context, operatorUserID uint, idempotencyKey string) (operationModel, bool, error) {
	var operation operationModel
	err := s.db.WithContext(ctx).
		Where("operator_user_id = ? AND idempotency_key = ?", operatorUserID, idempotencyKey).
		First(&operation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return operationModel{}, false, nil
	}
	if err != nil {
		return operationModel{}, false, fmt.Errorf("load idempotent Kitesim operation: %w", err)
	}
	return operation, true, nil
}

func (s *Service) listOperationViews(ctx context.Context, limit int) ([]OperationItem, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	var operations []operationModel
	if err := s.db.WithContext(ctx).Order("id DESC").Limit(limit).Find(&operations).Error; err != nil {
		return nil, fmt.Errorf("list Kitesim operations: %w", err)
	}
	accountNames := make(map[uint]string, len(operations))
	phoneNumbers := make(map[uint]string, len(operations))
	accountIDs := make([]uint, 0, len(operations))
	phoneIDs := make([]uint, 0, len(operations))
	for i := range operations {
		if _, loaded := accountNames[operations[i].AccountID]; !loaded {
			accountNames[operations[i].AccountID] = ""
			accountIDs = append(accountIDs, operations[i].AccountID)
		}
		if operations[i].PhoneID != nil {
			if _, loaded := phoneNumbers[*operations[i].PhoneID]; !loaded {
				phoneNumbers[*operations[i].PhoneID] = ""
				phoneIDs = append(phoneIDs, *operations[i].PhoneID)
			}
		}
	}
	type accountNameRow struct {
		ID      uint
		Account string
	}
	var accounts []accountNameRow
	if len(accountIDs) > 0 {
		if err := s.db.WithContext(ctx).Model(&accountModel{}).Select("id", "account").Where("id IN ?", accountIDs).Find(&accounts).Error; err != nil {
			return nil, fmt.Errorf("load Kitesim operation accounts: %w", err)
		}
		for _, account := range accounts {
			accountNames[account.ID] = account.Account
		}
	}
	type phoneNumberRow struct {
		ID          uint
		PhoneNumber string
	}
	var phones []phoneNumberRow
	if len(phoneIDs) > 0 {
		if err := s.db.WithContext(ctx).Model(&phoneModel{}).Select("id", "phone_number").Where("id IN ?", phoneIDs).Find(&phones).Error; err != nil {
			return nil, fmt.Errorf("load Kitesim operation phones: %w", err)
		}
		for _, phone := range phones {
			phoneNumbers[phone.ID] = phone.PhoneNumber
		}
	}
	items := make([]OperationItem, len(operations))
	for i := range operations {
		phone := ""
		if operations[i].PhoneID != nil {
			phone = phoneNumbers[*operations[i].PhoneID]
		}
		items[i] = *operationView(operations[i], accountNames[operations[i].AccountID], phone)
	}
	return items, nil
}

func operationView(operation operationModel, account, phone string) *OperationItem {
	refs, _ := decodeOperationRefs([]byte(operation.ProviderOrderNos))
	if refs.OrderNos == nil {
		refs.OrderNos = []string{}
	}
	return &OperationItem{
		ID: operation.ID, Kind: OperationKind(operation.Kind), Status: OperationStatus(operation.Status),
		AccountID: operation.AccountID, Account: account, PhoneID: operation.PhoneID,
		PhoneNumber: phone, CountryCode: operation.CountryCode, PackageID: operation.PackageID,
		RequestedCount: operation.RequestedCount, CompletedCount: operation.CompletedCount,
		Amount: normalizedDecimal(operation.Amount), Currency: operation.Currency, Attempts: operation.Attempts,
		ProviderOrderNos: refs.OrderNos, LastSafeError: operation.LastSafeError,
		ReconcileAttempts: operation.ReconcileAttempts, ReconcileRequestedAt: operation.ReconcileRequestedAt,
		LastReconciledAt: operation.LastReconciledAt,
		ResolutionSource: operation.ResolutionSource, ResolutionNote: operation.ResolutionNote,
		ResolvedAt: operation.ResolvedAt,
		QueuedAt:   operation.QueuedAt, StartedAt: operation.StartedAt, FinishedAt: operation.FinishedAt,
	}
}

func (s *Service) processOperation(ctx context.Context, operationID uint64) error {
	operation, secret, err := s.claimOperation(ctx, operationID)
	if err != nil {
		if errors.Is(err, ErrOperationMissing) || errors.Is(err, errOperationExpired) {
			return nil
		}
		return err
	}
	completed := 0
	switch OperationKind(operation.Kind) {
	case OperationPurchase:
		completed, _, err = s.executePurchase(ctx, operation)
	case OperationRecharge:
		var payload struct {
			CVC string `json:"cvc"`
		}
		if openErr := json.Unmarshal(secret, &payload); openErr != nil {
			err = openErr
		} else {
			_, err = s.executeRecharge(ctx, operation, payload.CVC)
		}
	case OperationRenew:
		_, err = s.executeRenewal(ctx, operation)
	default:
		err = ErrInvalidInput
	}
	if err != nil {
		operation.CompletedCount = completed
		if persistErr := s.recordOperationFailure(ctx, operation.ID, operationStatusForError(err), safeOperationError(operation, err), completed); persistErr != nil {
			return persistErr
		}
		s.queueOperationRefresh(operation)
		return nil
	}
	if err := s.finishOperation(ctx, operation.ID); err != nil {
		return err
	}
	s.queueOperationRefresh(operation)
	return nil
}

func (s *Service) queueOperationRefresh(operation operationModel) {
	ctx := context.Background()
	_, _ = s.queueAccountSync(ctx, operation.AccountID, nil)
	_, _ = s.QueueUpstreamRefresh(ctx, MutationMeta{
		OperatorUserID: operation.OperatorUserID,
		RequestID:      operation.RequestID,
		Path:           operation.Path,
	})
}

func (s *Service) claimOperation(ctx context.Context, operationID uint64) (operationModel, []byte, error) {
	var operation operationModel
	var secret []byte
	expired := false
	now := s.now().UTC().Truncate(time.Millisecond)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&operation, operationID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrOperationMissing
			}
			return err
		}
		if OperationStatus(operation.Status) != OperationQueued {
			return ErrOperationMissing
		}
		if OperationKind(operation.Kind) == OperationRecharge && !operation.QueuedAt.After(now.Add(-queuedRechargeSecretTTL)) {
			if err := tx.Model(&operationModel{}).Where("id = ? AND status = ?", operationID, OperationQueued).Updates(map[string]any{
				"status": OperationFailed, "secret_payload": nil, "finished_at": now,
				"last_safe_error": expiredRechargeSafeError,
			}).Error; err != nil {
				return err
			}
			expired = true
			return nil
		}
		secret = append(secret, operation.SecretPayload...)
		if err := tx.Model(&operationModel{}).Where("id = ?", operationID).Updates(map[string]any{
			"status": OperationRunning, "started_at": now, "finished_at": nil,
			"attempts": gorm.Expr("attempts + 1"), "secret_payload": nil,
		}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return operationModel{}, nil, err
	}
	if expired {
		return operationModel{}, nil, errOperationExpired
	}
	operation.Status = string(OperationRunning)
	operation.StartedAt = &now
	operation.Attempts++
	operation.SecretPayload = ""
	return operation, secret, nil
}

func (s *Service) recordOperationProgress(ctx context.Context, operationID uint64, completed int, refs operationProviderRefs) error {
	encoded, err := json.Marshal(refs)
	if err != nil {
		return fmt.Errorf("marshal Kitesim operation references: %w", err)
	}
	result := s.db.WithContext(context.WithoutCancel(ctx)).Model(&operationModel{}).
		Where("id = ? AND status = ?", operationID, OperationRunning).
		Updates(map[string]any{"completed_count": completed, "provider_order_nos": jsonText(encoded)})
	if result.Error != nil {
		return fmt.Errorf("save Kitesim operation references: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrOperationState
	}
	return nil
}

func (s *Service) finishOperation(ctx context.Context, operationID uint64) error {
	now := s.now().UTC().Truncate(time.Millisecond)
	result := s.db.WithContext(context.WithoutCancel(ctx)).Model(&operationModel{}).
		Where("id = ? AND status = ?", operationID, OperationRunning).
		Updates(map[string]any{
			"status": OperationSucceeded, "completed_count": gorm.Expr("requested_count"),
			"last_safe_error": "", "finished_at": now,
		})
	if result.Error != nil {
		return fmt.Errorf("finish Kitesim operation: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrOperationState
	}
	return nil
}

func (s *Service) recordOperationFailure(ctx context.Context, operationID uint64, status OperationStatus, message string, completed int) error {
	now := s.now().UTC().Truncate(time.Millisecond)
	updates := map[string]any{
		"status": status, "last_safe_error": strings.TrimSpace(message),
		"completed_count": completed, "secret_payload": nil, "finished_at": now,
	}
	if status == OperationUncertain || status == OperationRequiresAction {
		updates["reconcile_requested_at"] = now
	}
	result := s.db.WithContext(context.WithoutCancel(ctx)).Model(&operationModel{}).
		Where("id = ? AND status = ?", operationID, OperationRunning).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("record Kitesim operation failure: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrOperationState
	}
	return nil
}

func operationStatusForError(err error) OperationStatus {
	if errors.Is(err, ErrThreeDSRequired) {
		return OperationFailed
	}
	if errors.Is(err, ErrPaymentUncertain) {
		return OperationUncertain
	}
	return OperationFailed
}

func safeOperationError(operation operationModel, err error) string {
	if errors.Is(err, ErrThreeDSRequired) {
		return "银行卡支付需要 3DS 验证，本次充值失败。"
	}
	if errors.Is(err, ErrPaymentUncertain) {
		return "上游支付结果不确定，请先核对余额和订单，勿直接重试。"
	}
	if operation.Kind == string(OperationPurchase) && operation.CompletedCount > 0 {
		return fmt.Sprintf("已补 %d/%d 个号码，后续购买失败；请刷新余额和号码列表后检查。", operation.CompletedCount, operation.RequestedCount)
	}
	if errors.Is(err, ErrLoginFailed) {
		return "Kitesim 登录失败，请检查系统平台账号。"
	}
	switch OperationKind(operation.Kind) {
	case OperationPurchase:
		return "Kitesim 补号失败，请检查余额、产品价格和号码库存。"
	case OperationRecharge:
		return "Kitesim 充值失败，请检查卡片、账单信息和支付状态。"
	case OperationRenew:
		return "Kitesim 续期失败，请检查余额、套餐和号码状态。"
	default:
		return "Kitesim 操作失败，请稍后检查。"
	}
}

func normalizeOperationIdempotencyKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return "", ErrIdempotencyRequired
	}
	return value, nil
}

func operationFingerprint(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal Kitesim operation fingerprint: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func normalizedPositiveMoney(value string) (string, error) {
	amount, err := decimal.NewFromString(strings.TrimSpace(value))
	if err != nil || !amount.IsPositive() || amount.GreaterThan(decimal.NewFromInt(10000)) {
		return "", ErrInvalidInput
	}
	return amount.String(), nil
}

func priceExceeds(current, limit string) bool {
	currentAmount, currentErr := decimal.NewFromString(strings.TrimSpace(current))
	limitAmount, limitErr := decimal.NewFromString(strings.TrimSpace(limit))
	return currentErr != nil || limitErr != nil || currentAmount.IsNegative() || currentAmount.GreaterThan(limitAmount)
}

func decodeOperationRefs(encoded []byte) (operationProviderRefs, error) {
	var refs operationProviderRefs
	if len(encoded) == 0 {
		return refs, nil
	}
	if err := json.Unmarshal(encoded, &refs); err == nil {
		return refs, nil
	}
	var legacy []string
	if err := json.Unmarshal(encoded, &legacy); err != nil {
		return operationProviderRefs{}, fmt.Errorf("decode Kitesim operation references: %w", err)
	}
	for _, value := range legacy {
		switch {
		case strings.HasPrefix(value, "outTransNo:"):
			refs.OutTransNo = strings.TrimPrefix(value, "outTransNo:")
		case strings.HasPrefix(value, "payOrderId:"):
			refs.PayOrderID = strings.TrimPrefix(value, "payOrderId:")
		default:
			refs.OrderNos = append(refs.OrderNos, value)
		}
	}
	return refs, nil
}

func validCVC(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 3 || len(value) > 4 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func duplicateOperationError(err error) bool {
	var mysqlErr *mysqldriver.MySQLError
	return errors.Is(err, gorm.ErrDuplicatedKey) ||
		errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 ||
		strings.Contains(err.Error(), "UNIQUE constraint failed")
}
