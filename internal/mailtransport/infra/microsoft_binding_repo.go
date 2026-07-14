package infra

import (
	"context"
	"errors"
	"fmt"
	stdmail "net/mail"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MicrosoftBindingMailboxModel struct {
	ID             uint       `gorm:"primaryKey;autoIncrement"`
	ResourceID     uint       `gorm:"not null;column:resource_id;uniqueIndex:idx_microsoft_binding_resource"`
	ResourceType   string     `gorm:"type:varchar(32);not null;default:'microsoft';column:resource_type"`
	OwnerUserID    uint       `gorm:"not null;column:owner_user_id"`
	AccountEmail   string     `gorm:"type:varchar(255);not null;column:account_email"`
	BindingAddress string     `gorm:"type:varchar(320);not null;column:binding_address"`
	Purpose        string     `gorm:"type:varchar(64);not null;default:'validation'"`
	Status         string     `gorm:"type:varchar(32);not null;default:'pending'"`
	CodeMessageID  string     `gorm:"type:varchar(255);not null;default:'';column:code_msg_id"`
	BoundDisplay   string     `gorm:"type:varchar(255);not null;default:'';column:bound_display"`
	Category       string     `gorm:"type:varchar(64);not null;default:''"`
	LastSafeError  string     `gorm:"type:varchar(500);not null;default:'';column:last_safe_error"`
	SelectedAt     *time.Time `gorm:"column:selected_at"`
	CodeSentAt     *time.Time `gorm:"column:code_sent_at"`
	VerifiedAt     *time.Time `gorm:"column:verified_at"`
	ExpiresAt      *time.Time `gorm:"column:expires_at"`
	CreatedAt      time.Time  `gorm:"not null;autoCreateTime"`
	UpdatedAt      time.Time  `gorm:"not null;autoUpdateTime"`
}

func (MicrosoftBindingMailboxModel) TableName() string {
	return "microsoft_binding_mailboxes"
}

type MicrosoftBindingImportInput struct {
	OwnerUserID    uint
	AccountEmail   string
	BindingAddress string
}

var (
	ErrMicrosoftBindingRecoveryInvalidInput     = errors.New("microsoft binding recovery input is invalid")
	ErrMicrosoftBindingRecoveryResourceNotFound = errors.New("microsoft binding recovery resource not found")
	ErrMicrosoftBindingRecoveryResourceDeleted  = errors.New("microsoft binding recovery resource is deleted")
	ErrMicrosoftBindingRecoveryConflict         = errors.New("microsoft binding changed while recovery was running")
	ErrMicrosoftBindingAddressOccupied          = errors.New("microsoft binding address is already active on another resource")
	ErrMicrosoftBindingRecoveryTransaction      = errors.New("microsoft binding validation recovery requires caller transaction")
	ErrMicrosoftBindingRecoveryIneligible       = errors.New("microsoft binding address is not on an active binding domain")
)

// MicrosoftRecoveredBindingInput carries the optimistic snapshot captured
// before the remote Microsoft proof lookup. ExpectedBindingID == 0 means that
// no binding row existed in that snapshot. For an existing row, ID, address,
// and updated_at must all still match before the recovered fact is applied.
type MicrosoftRecoveredBindingInput struct {
	ResourceID               uint
	BindingAddress           string
	ExpectedOwnerUserID      uint
	ExpectedAccountEmail     string
	ExpectedBindingID        uint
	ExpectedBindingAddress   string
	ExpectedBindingUpdatedAt time.Time
}

// MicrosoftValidationBindingObservationInput is the ordinary binding outcome
// returned by a validation login. It may only be applied inside Core's fenced
// caller transaction.
type MicrosoftValidationBindingObservationInput struct {
	ResourceID   uint
	OwnerUserID  uint
	AccountEmail string
	Address      string
	Status       string
	BoundDisplay string
	SafeMessage  string
}

// MicrosoftRecoveredBindingResult reports the committed local fact. A result
// with Changed=false is an idempotent replay and does not advance the root
// resource version or refresh protocol timestamps.
type MicrosoftRecoveredBindingResult struct {
	BindingID       uint
	Created         bool
	Changed         bool
	VerifiedAt      time.Time
	ResourceVersion uint64
}

type MicrosoftBindingRepo struct {
	db *gorm.DB
}

func NewMicrosoftBindingRepo(db *gorm.DB) *MicrosoftBindingRepo {
	return &MicrosoftBindingRepo{db: db}
}

// ApplyRecoveredBinding atomically replaces the current recovery-mailbox fact
// with a fully resolved, locally receivable address. It locks the Core root,
// Microsoft resource, current binding, and target active-address slot; rejects
// stale snapshots and deleted resources; and advances email_resources.version
// exactly once when a row is created or repaired.
//
// If ctx contains a caller-owned GORM transaction, all work joins it. The
// caller may then add its OperationLog before committing the same transaction.
func (r *MicrosoftBindingRepo) ApplyRecoveredBinding(ctx context.Context, input MicrosoftRecoveredBindingInput) (*MicrosoftRecoveredBindingResult, error) {
	input, err := normalizeRecoveredMicrosoftBindingInput(r, input)
	if err != nil {
		return nil, ErrMicrosoftBindingRecoveryInvalidInput
	}

	var applied *MicrosoftRecoveredBindingResult
	apply := func(tx *gorm.DB) error {
		result, err := applyRecoveredMicrosoftBindingTx(tx.WithContext(ctx), input, true, true)
		if err != nil {
			return err
		}
		applied = result
		return nil
	}
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		if err := apply(tx); err != nil {
			return nil, err
		}
		return applied, nil
	}
	if err := r.db.WithContext(ctx).Transaction(apply); err != nil {
		return nil, err
	}
	return applied, nil
}

// ApplyRecoveredBindingForValidation joins Core's already-fenced validation
// transaction. It applies the same optimistic binding snapshot and uniqueness
// checks as ApplyRecoveredBinding, but deliberately leaves email_resources.version
// to Core so the complete validation result advances the root exactly once.
func (r *MicrosoftBindingRepo) ApplyRecoveredBindingForValidation(ctx context.Context, input MicrosoftRecoveredBindingInput) (*MicrosoftRecoveredBindingResult, error) {
	input, err := normalizeRecoveredMicrosoftBindingInput(r, input)
	if err != nil {
		return nil, ErrMicrosoftBindingRecoveryInvalidInput
	}
	tx, ok := platform.GormTxFromContext(ctx)
	if !ok {
		return nil, ErrMicrosoftBindingRecoveryTransaction
	}
	return applyRecoveredMicrosoftBindingTx(tx.WithContext(ctx), input, false, true)
}

// ApplyValidationBindingObservation persists an ordinary validation binding
// outcome. It is intentionally unavailable outside Core's caller-owned
// transaction so a stale worker cannot write before the job and credential
// fences have been checked.
func (r *MicrosoftBindingRepo) ApplyValidationBindingObservation(ctx context.Context, input MicrosoftValidationBindingObservationInput) (bool, error) {
	input.AccountEmail = normalizeBindingEmail(input.AccountEmail)
	input.Address = normalizeBindingEmail(input.Address)
	input.Status = strings.ToLower(strings.TrimSpace(input.Status))
	input.BoundDisplay = strings.TrimSpace(input.BoundDisplay)
	input.SafeMessage = strings.TrimSpace(input.SafeMessage)
	if r == nil || r.db == nil || input.ResourceID == 0 || input.OwnerUserID == 0 || input.AccountEmail == "" {
		return false, ErrMicrosoftBindingRecoveryInvalidInput
	}
	if input.Address != "" && !isConcreteMicrosoftBindingAddress(input.Address) {
		maskedExternalObservation := input.BoundDisplay != "" &&
			strings.Contains(input.Address, "*") && strings.Contains(input.Address, "@")
		if !maskedExternalObservation {
			return false, ErrMicrosoftBindingRecoveryInvalidInput
		}
	}
	if !validMicrosoftBindingObservationStatus(input.Status) {
		return false, ErrMicrosoftBindingRecoveryInvalidInput
	}
	if input.Status == string(domain.MicrosoftBindingVerified) &&
		(input.Address == "" || input.BoundDisplay != "") {
		return false, ErrMicrosoftBindingRecoveryInvalidInput
	}
	tx, ok := platform.GormTxFromContext(ctx)
	if !ok {
		return false, ErrMicrosoftBindingRecoveryTransaction
	}
	if input.Address == "" && input.BoundDisplay == "" {
		return false, nil
	}
	return applyValidationBindingObservationTx(tx.WithContext(ctx), input)
}

func normalizeRecoveredMicrosoftBindingInput(r *MicrosoftBindingRepo, input MicrosoftRecoveredBindingInput) (MicrosoftRecoveredBindingInput, error) {
	input.BindingAddress = normalizeBindingEmail(input.BindingAddress)
	input.ExpectedAccountEmail = normalizeBindingEmail(input.ExpectedAccountEmail)
	input.ExpectedBindingAddress = normalizeBindingEmail(input.ExpectedBindingAddress)
	if r == nil || r.db == nil || input.ResourceID == 0 ||
		!isConcreteMicrosoftBindingAddress(input.BindingAddress) || input.ExpectedAccountEmail == "" {
		return input, ErrMicrosoftBindingRecoveryInvalidInput
	}
	if input.ExpectedBindingID == 0 {
		if input.ExpectedBindingAddress != "" || !input.ExpectedBindingUpdatedAt.IsZero() {
			return input, ErrMicrosoftBindingRecoveryInvalidInput
		}
	} else if input.ExpectedBindingAddress == "" || input.ExpectedBindingUpdatedAt.IsZero() {
		return input, ErrMicrosoftBindingRecoveryInvalidInput
	}
	return input, nil
}

func validMicrosoftBindingObservationStatus(status string) bool {
	switch domain.MicrosoftBindingStatus(status) {
	case "", domain.MicrosoftBindingPending, domain.MicrosoftBindingCodeSent,
		domain.MicrosoftBindingVerified, domain.MicrosoftBindingTimeout, domain.MicrosoftBindingFailed:
		return true
	default:
		return false
	}
}

func (r *MicrosoftBindingRepo) FindByResourceIDs(ctx context.Context, resourceIDs []uint) (map[uint]domain.MicrosoftBindingMailbox, error) {
	ids := uniquePositiveBindingIDs(resourceIDs)
	result := make(map[uint]domain.MicrosoftBindingMailbox, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	db := r.db.WithContext(ctx)
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		db = tx.WithContext(ctx)
	}
	var models []MicrosoftBindingMailboxModel
	if err := db.
		Select("id, resource_id, owner_user_id, account_email, binding_address, purpose, status, code_msg_id, bound_display, category, last_safe_error, selected_at, code_sent_at, verified_at, expires_at, created_at, updated_at").
		Where("resource_id IN ?", ids).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("find microsoft bindings by resources: %w", err)
	}
	for i := range models {
		model := models[i]
		result[model.ResourceID] = microsoftBindingToDomain(model)
	}
	return result, nil
}

// ReplaceAdminInput updates the current MailTransport-owned binding input in
// the caller's short transaction. addressSet=false preserves the address and,
// for an owner-only change, the protocol status. If the Microsoft account email
// changed, the address remains available as a candidate but every fact verified
// against the previous account is reset to pending. addressSet=true clears or
// replaces the current input and likewise resets protocol-derived state.
func (r *MicrosoftBindingRepo) ReplaceAdminInput(ctx context.Context, resourceID, ownerUserID uint, accountEmail string, addressSet bool, bindingAddress *string) error {
	accountEmail = normalizeBindingEmail(accountEmail)
	if r == nil || r.db == nil || resourceID == 0 || ownerUserID == 0 || accountEmail == "" {
		return fmt.Errorf("replace microsoft binding input: invalid command")
	}
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return replaceMicrosoftBindingAdminInputTx(tx.WithContext(ctx), resourceID, ownerUserID, accountEmail, addressSet, bindingAddress)
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return replaceMicrosoftBindingAdminInputTx(tx.WithContext(ctx), resourceID, ownerUserID, accountEmail, addressSet, bindingAddress)
	})
}

func replaceMicrosoftBindingAdminInputTx(db *gorm.DB, resourceID, ownerUserID uint, accountEmail string, addressSet bool, bindingAddress *string) error {
	if !addressSet {
		var current MicrosoftBindingMailboxModel
		err := db.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("resource_id = ?", resourceID).
			Take(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock microsoft binding administrator input: %w", err)
		}
		now := time.Now().UTC()
		updates := map[string]any{
			"owner_user_id": ownerUserID,
			"account_email": accountEmail,
			"updated_at":    now,
		}
		if normalizeBindingEmail(current.AccountEmail) != accountEmail {
			updates["resource_type"] = "microsoft"
			updates["purpose"] = "validation"
			updates["status"] = string(domain.MicrosoftBindingPending)
			updates["code_msg_id"] = ""
			updates["bound_display"] = ""
			updates["category"] = ""
			updates["last_safe_error"] = ""
			updates["selected_at"] = now
			updates["code_sent_at"] = nil
			updates["verified_at"] = nil
			updates["expires_at"] = nil
		}
		updated := db.Model(&MicrosoftBindingMailboxModel{}).
			Where("id = ?", current.ID).
			Updates(updates)
		if updated.Error != nil {
			return fmt.Errorf("synchronize microsoft binding administrator input: %w", updated.Error)
		}
		return nil
	}

	address := ""
	if bindingAddress != nil {
		address = normalizeBindingEmail(*bindingAddress)
	}
	if address == "" {
		if err := db.Where("resource_id = ?", resourceID).Delete(&MicrosoftBindingMailboxModel{}).Error; err != nil {
			return fmt.Errorf("clear microsoft binding input: %w", err)
		}
		return nil
	}
	return upsertMicrosoftBindingTx(db, &MicrosoftBindingMailboxModel{
		ResourceID:     resourceID,
		ResourceType:   "microsoft",
		OwnerUserID:    ownerUserID,
		AccountEmail:   accountEmail,
		BindingAddress: address,
		Purpose:        "validation",
		Status:         string(domain.MicrosoftBindingPending),
	})
}

func (r *MicrosoftBindingRepo) UpsertByEmail(ctx context.Context, inputs []MicrosoftBindingImportInput) error {
	if len(inputs) == 0 {
		return nil
	}
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return r.upsertByEmailTx(tx.WithContext(ctx), inputs)
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return r.upsertByEmailTx(tx, inputs)
	})
}

func (r *MicrosoftBindingRepo) upsertByEmailTx(tx *gorm.DB, inputs []MicrosoftBindingImportInput) error {
	for _, input := range inputs {
		accountEmail := normalizeBindingEmail(input.AccountEmail)
		bindingAddress := normalizeBindingEmail(input.BindingAddress)
		if input.OwnerUserID == 0 || accountEmail == "" || bindingAddress == "" {
			continue
		}

		var row struct {
			ResourceID  uint
			OwnerUserID uint
		}
		err := tx.Raw(`
SELECT er.id AS resource_id, er.owner_user_id AS owner_user_id
FROM email_resources AS er
JOIN microsoft_resources AS mr ON mr.id = er.id
WHERE er.owner_user_id = ?
  AND er.type = 'microsoft'
  AND mr.email_address = ?
  AND mr.status <> 'deleted'
LIMIT 1`, input.OwnerUserID, accountEmail).Scan(&row).Error
		if err != nil {
			return fmt.Errorf("resolve microsoft binding resource: %w", err)
		}
		if row.ResourceID == 0 {
			continue
		}
		if err := upsertMicrosoftBindingTx(tx, &MicrosoftBindingMailboxModel{
			ResourceID:     row.ResourceID,
			ResourceType:   "microsoft",
			OwnerUserID:    row.OwnerUserID,
			AccountEmail:   accountEmail,
			BindingAddress: bindingAddress,
			Purpose:        "validation",
			Status:         string(domain.MicrosoftBindingPending),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r *MicrosoftBindingRepo) UpsertForResource(ctx context.Context, resourceID uint, ownerUserID uint, accountEmail string, bindingAddress string) error {
	accountEmail = normalizeBindingEmail(accountEmail)
	bindingAddress = normalizeBindingEmail(bindingAddress)
	if resourceID == 0 || ownerUserID == 0 || accountEmail == "" || bindingAddress == "" {
		return nil
	}
	db := r.db.WithContext(ctx)
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		db = tx.WithContext(ctx)
	}
	return upsertMicrosoftBindingTx(db, &MicrosoftBindingMailboxModel{
		ResourceID:     resourceID,
		ResourceType:   "microsoft",
		OwnerUserID:    ownerUserID,
		AccountEmail:   accountEmail,
		BindingAddress: bindingAddress,
		Purpose:        "validation",
		Status:         string(domain.MicrosoftBindingPending),
	})
}

func (r *MicrosoftBindingRepo) PreferredAddress(ctx context.Context, resourceID uint) (string, error) {
	if resourceID == 0 {
		return "", nil
	}
	var model MicrosoftBindingMailboxModel
	err := r.db.WithContext(ctx).
		Where("resource_id = ? AND status <> ?", resourceID, string(domain.MicrosoftBindingExpired)).
		Order("updated_at DESC").
		First(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("find microsoft binding address: %w", err)
	}
	return model.BindingAddress, nil
}

// BindingResolved reports whether the resource's recovery-mailbox binding is
// already a usable fact: either verified against a receivable project-domain
// address, or resolved to a known external mailbox (bound_display recorded).
// A pending/timeout/failed-without-display binding is NOT resolved — the
// relationship still needs to be completed before alias/OTP tasks can run.
func (r *MicrosoftBindingRepo) BindingResolved(ctx context.Context, resourceID uint) (bool, error) {
	if resourceID == 0 {
		return false, nil
	}
	var model MicrosoftBindingMailboxModel
	err := r.db.WithContext(ctx).
		Select("status, bound_display").
		Where("resource_id = ? AND status <> ?", resourceID, string(domain.MicrosoftBindingExpired)).
		Order("updated_at DESC").
		First(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("resolve microsoft binding state: %w", err)
	}
	if strings.TrimSpace(model.BoundDisplay) != "" {
		return true, nil
	}
	return model.Status == string(domain.MicrosoftBindingVerified), nil
}

func (r *MicrosoftBindingRepo) MarkStatus(ctx context.Context, resourceID uint, bindingAddress string, status domain.MicrosoftBindingStatus, safeError string) error {
	if resourceID == 0 {
		return nil
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"status":          string(status),
		"category":        bindingStatusCategory(status),
		"last_safe_error": strings.TrimSpace(safeError),
		"updated_at":      now,
	}
	switch status {
	case domain.MicrosoftBindingCodeSent:
		updates["code_sent_at"] = now
		updates["bound_display"] = "" // receivable project-domain recovery; clear any prior external mark
	case domain.MicrosoftBindingVerified:
		updates["verified_at"] = now
		updates["bound_display"] = ""
	}
	db := r.db.WithContext(ctx)
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		db = tx.WithContext(ctx)
	}
	query := db.Model(&MicrosoftBindingMailboxModel{}).Where("resource_id = ?", resourceID)
	if normalized := normalizeBindingEmail(bindingAddress); normalized != "" {
		query = query.Where("binding_address = ?", normalized)
	}
	result := query.Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("mark microsoft binding status: %w", result.Error)
	}
	return nil
}

// RecordBoundDisplay records the (masked) external recovery mailbox that a
// Microsoft account is actually bound to (e.g. a****b@qq.com) on the resource's
// binding row(s), and marks the binding failed — we cannot receive verification
// codes at an external address, but the fact is preserved for operators.
func (r *MicrosoftBindingRepo) RecordBoundDisplay(ctx context.Context, resourceID uint, boundDisplay string, safeError string) error {
	boundDisplay = strings.TrimSpace(boundDisplay)
	if resourceID == 0 || boundDisplay == "" {
		return nil
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"bound_display":   boundDisplay,
		"status":          string(domain.MicrosoftBindingFailed),
		"category":        bindingStatusCategory(domain.MicrosoftBindingFailed),
		"last_safe_error": strings.TrimSpace(safeError),
		"updated_at":      now,
	}
	db := r.db.WithContext(ctx)
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		db = tx.WithContext(ctx)
	}
	if err := db.Model(&MicrosoftBindingMailboxModel{}).Where("resource_id = ?", resourceID).Updates(updates).Error; err != nil {
		return fmt.Errorf("record microsoft binding bound_display: %w", err)
	}
	return nil
}

type recoveredBindingResourceRoot struct {
	ID          uint
	OwnerUserID uint   `gorm:"column:owner_user_id"`
	Version     uint64 `gorm:"column:version"`
}

type recoveredBindingMicrosoftResource struct {
	EmailAddress string `gorm:"column:email_address"`
	Status       string `gorm:"column:status"`
}

func applyValidationBindingObservationTx(tx *gorm.DB, input MicrosoftValidationBindingObservationInput) (bool, error) {
	var root recoveredBindingResourceRoot
	err := tx.Table("email_resources").
		Select("id, owner_user_id, version").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND type = ?", input.ResourceID, "microsoft").
		Take(&root).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, ErrMicrosoftBindingRecoveryResourceNotFound
	}
	if err != nil {
		return false, fmt.Errorf("lock microsoft binding observation resource root: %w", err)
	}
	if root.OwnerUserID != input.OwnerUserID {
		return false, ErrMicrosoftBindingRecoveryConflict
	}

	var resource recoveredBindingMicrosoftResource
	err = tx.Table("microsoft_resources").
		Select("email_address, status").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", input.ResourceID).
		Take(&resource).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, ErrMicrosoftBindingRecoveryResourceNotFound
	}
	if err != nil {
		return false, fmt.Errorf("lock microsoft binding observation resource: %w", err)
	}
	if resource.Status == "deleted" {
		return false, ErrMicrosoftBindingRecoveryResourceDeleted
	}
	if resource.Status == "disabled" || normalizeBindingEmail(resource.EmailAddress) != input.AccountEmail {
		return false, ErrMicrosoftBindingRecoveryConflict
	}

	var current MicrosoftBindingMailboxModel
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("resource_id = ?", input.ResourceID).
		Take(&current).Error
	currentExists := err == nil
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, fmt.Errorf("lock current microsoft binding observation: %w", err)
	}
	status := domain.MicrosoftBindingStatus(input.Status)
	if status == "" {
		status = domain.MicrosoftBindingPending
	}
	if currentExists && input.BoundDisplay == "" && status != domain.MicrosoftBindingVerified &&
		cleanVerifiedMicrosoftBindingMatchesIdentity(current, root.OwnerUserID, input.AccountEmail) {
		// Validation observations are monotonic for a clean verified binding.
		// Password failures, OTP timeouts, and locally prepared candidates do not
		// prove that Microsoft changed the relationship. Resource health may still
		// become abnormal, but the verified fact remains until a new verified fact,
		// an explicit external BoundDisplay, or an administrator identity reset.
		return false, nil
	}

	if input.BoundDisplay != "" {
		if !currentExists && !isConcreteMicrosoftBindingAddress(input.Address) {
			return false, nil
		}
		return applyExternalBindingObservationTx(tx, root, current, currentExists, input)
	}
	if input.Address == "" {
		return false, nil
	}
	if err := lockActiveBindingDomainForRecoveryTx(tx, input.Address); err != nil {
		return false, err
	}

	var occupied struct {
		ResourceID uint `gorm:"column:resource_id"`
	}
	err = tx.Table("microsoft_binding_mailboxes").
		Select("resource_id").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("active_binding_address = ?", input.Address).
		Take(&occupied).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, fmt.Errorf("lock observed microsoft binding address: %w", err)
	}
	if err == nil && occupied.ResourceID != input.ResourceID {
		return false, ErrMicrosoftBindingAddressOccupied
	}

	category := bindingStatusCategory(status)
	safeMessage := ""
	if status == domain.MicrosoftBindingTimeout || status == domain.MicrosoftBindingFailed {
		safeMessage = input.SafeMessage
	}
	if currentExists && validationBindingObservationAlreadyApplied(current, root.OwnerUserID, input.AccountEmail, input.Address, status, category, safeMessage) {
		return false, nil
	}

	now := time.Now().UTC()
	codeSentAt := (*time.Time)(nil)
	verifiedAt := (*time.Time)(nil)
	if status == domain.MicrosoftBindingCodeSent {
		codeSentAt = &now
	}
	if status == domain.MicrosoftBindingVerified {
		verifiedAt = &now
	}
	if !currentExists {
		current = MicrosoftBindingMailboxModel{
			ResourceID:     input.ResourceID,
			ResourceType:   "microsoft",
			OwnerUserID:    root.OwnerUserID,
			AccountEmail:   input.AccountEmail,
			BindingAddress: input.Address,
			Purpose:        "validation",
			Status:         string(status),
			Category:       category,
			LastSafeError:  safeMessage,
			SelectedAt:     &now,
			CodeSentAt:     codeSentAt,
			VerifiedAt:     verifiedAt,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if err := tx.Create(&current).Error; err != nil {
			if isDuplicateKeyError(err) {
				return false, ErrMicrosoftBindingAddressOccupied
			}
			return false, fmt.Errorf("create observed microsoft binding: %w", err)
		}
		return true, nil
	}

	updated := tx.Model(&MicrosoftBindingMailboxModel{}).
		Where("id = ?", current.ID).
		Updates(map[string]any{
			"resource_type":   "microsoft",
			"owner_user_id":   root.OwnerUserID,
			"account_email":   input.AccountEmail,
			"binding_address": input.Address,
			"purpose":         "validation",
			"status":          string(status),
			"code_msg_id":     "",
			"bound_display":   "",
			"category":        category,
			"last_safe_error": safeMessage,
			"selected_at":     now,
			"code_sent_at":    codeSentAt,
			"verified_at":     verifiedAt,
			"expires_at":      nil,
			"updated_at":      now,
		})
	if updated.Error != nil {
		if isDuplicateKeyError(updated.Error) {
			return false, ErrMicrosoftBindingAddressOccupied
		}
		return false, fmt.Errorf("update observed microsoft binding: %w", updated.Error)
	}
	if updated.RowsAffected != 1 {
		return false, ErrMicrosoftBindingRecoveryConflict
	}
	return true, nil
}

func cleanVerifiedMicrosoftBindingMatchesIdentity(current MicrosoftBindingMailboxModel, ownerUserID uint, accountEmail string) bool {
	return current.ResourceType == "microsoft" &&
		current.OwnerUserID == ownerUserID &&
		normalizeBindingEmail(current.AccountEmail) == accountEmail &&
		current.Status == string(domain.MicrosoftBindingVerified) &&
		isConcreteMicrosoftBindingAddress(current.BindingAddress) &&
		strings.TrimSpace(current.CodeMessageID) == "" &&
		strings.TrimSpace(current.BoundDisplay) == ""
}

func applyExternalBindingObservationTx(tx *gorm.DB, root recoveredBindingResourceRoot, current MicrosoftBindingMailboxModel, currentExists bool, input MicrosoftValidationBindingObservationInput) (bool, error) {
	if !currentExists {
		if err := lockActiveBindingDomainForRecoveryTx(tx, input.Address); err != nil {
			return false, err
		}
		now := time.Now().UTC()
		current = MicrosoftBindingMailboxModel{
			ResourceID:     input.ResourceID,
			ResourceType:   "microsoft",
			OwnerUserID:    root.OwnerUserID,
			AccountEmail:   input.AccountEmail,
			BindingAddress: input.Address,
			Purpose:        "validation",
			Status:         string(domain.MicrosoftBindingFailed),
			BoundDisplay:   input.BoundDisplay,
			Category:       bindingStatusCategory(domain.MicrosoftBindingFailed),
			LastSafeError:  input.SafeMessage,
			SelectedAt:     &now,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if err := tx.Create(&current).Error; err != nil {
			if isDuplicateKeyError(err) {
				return false, ErrMicrosoftBindingAddressOccupied
			}
			return false, fmt.Errorf("create external microsoft binding observation: %w", err)
		}
		return true, nil
	}
	category := bindingStatusCategory(domain.MicrosoftBindingFailed)
	if current.ResourceType == "microsoft" &&
		current.OwnerUserID == root.OwnerUserID &&
		normalizeBindingEmail(current.AccountEmail) == input.AccountEmail &&
		current.Purpose == "validation" &&
		current.Status == string(domain.MicrosoftBindingFailed) &&
		current.CodeMessageID == "" &&
		current.BoundDisplay == input.BoundDisplay &&
		current.Category == category &&
		current.LastSafeError == input.SafeMessage &&
		current.SelectedAt != nil && current.CodeSentAt == nil &&
		current.VerifiedAt == nil && current.ExpiresAt == nil {
		return false, nil
	}
	now := time.Now().UTC()
	updated := tx.Model(&MicrosoftBindingMailboxModel{}).
		Where("id = ?", current.ID).
		Updates(map[string]any{
			"resource_type":   "microsoft",
			"owner_user_id":   root.OwnerUserID,
			"account_email":   input.AccountEmail,
			"purpose":         "validation",
			"status":          string(domain.MicrosoftBindingFailed),
			"code_msg_id":     "",
			"bound_display":   input.BoundDisplay,
			"category":        category,
			"last_safe_error": input.SafeMessage,
			"selected_at":     now,
			"code_sent_at":    nil,
			"verified_at":     nil,
			"expires_at":      nil,
			"updated_at":      now,
		})
	if updated.Error != nil {
		return false, fmt.Errorf("update external microsoft binding observation: %w", updated.Error)
	}
	if updated.RowsAffected != 1 {
		return false, ErrMicrosoftBindingRecoveryConflict
	}
	return true, nil
}

func validationBindingObservationAlreadyApplied(current MicrosoftBindingMailboxModel, ownerUserID uint, accountEmail, address string, status domain.MicrosoftBindingStatus, category, safeMessage string) bool {
	if current.ResourceType != "microsoft" || current.OwnerUserID != ownerUserID ||
		normalizeBindingEmail(current.AccountEmail) != accountEmail ||
		normalizeBindingEmail(current.BindingAddress) != address ||
		current.Purpose != "validation" || current.Status != string(status) ||
		current.CodeMessageID != "" || current.BoundDisplay != "" || current.Category != category ||
		current.LastSafeError != safeMessage || current.SelectedAt == nil ||
		current.ExpiresAt != nil {
		return false
	}
	switch status {
	case domain.MicrosoftBindingCodeSent:
		return current.CodeSentAt != nil && current.VerifiedAt == nil
	case domain.MicrosoftBindingVerified:
		return current.CodeSentAt == nil && current.VerifiedAt != nil
	default:
		return current.CodeSentAt == nil && current.VerifiedAt == nil
	}
}

func applyRecoveredMicrosoftBindingTx(tx *gorm.DB, input MicrosoftRecoveredBindingInput, advanceResourceVersion bool, requireActiveBindingDomain bool) (*MicrosoftRecoveredBindingResult, error) {
	var root recoveredBindingResourceRoot
	err := tx.Table("email_resources").
		Select("id, owner_user_id, version").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND type = ?", input.ResourceID, "microsoft").
		Take(&root).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrMicrosoftBindingRecoveryResourceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock microsoft binding recovery resource root: %w", err)
	}
	if input.ExpectedOwnerUserID != 0 && root.OwnerUserID != input.ExpectedOwnerUserID {
		return nil, ErrMicrosoftBindingRecoveryConflict
	}

	var resource recoveredBindingMicrosoftResource
	err = tx.Table("microsoft_resources").
		Select("email_address, status").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", input.ResourceID).
		Take(&resource).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrMicrosoftBindingRecoveryResourceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock microsoft binding recovery resource: %w", err)
	}
	if resource.Status == "deleted" {
		return nil, ErrMicrosoftBindingRecoveryResourceDeleted
	}
	if resource.Status == "disabled" {
		return nil, ErrMicrosoftBindingRecoveryConflict
	}
	accountEmail := normalizeBindingEmail(resource.EmailAddress)
	if accountEmail == "" || accountEmail != input.ExpectedAccountEmail {
		return nil, ErrMicrosoftBindingRecoveryConflict
	}

	var current MicrosoftBindingMailboxModel
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("resource_id = ?", input.ResourceID).
		Take(&current).Error
	currentExists := err == nil
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("lock current microsoft binding recovery fact: %w", err)
	}
	if !recoveredBindingSnapshotMatches(input, current, currentExists) {
		return nil, ErrMicrosoftBindingRecoveryConflict
	}
	if requireActiveBindingDomain {
		if err := lockActiveBindingDomainForRecoveryTx(tx, input.BindingAddress); err != nil {
			return nil, err
		}
	}

	if currentExists && recoveredBindingAlreadyApplied(current, root.OwnerUserID, accountEmail, input.BindingAddress) {
		return &MicrosoftRecoveredBindingResult{
			BindingID:       current.ID,
			Changed:         false,
			VerifiedAt:      current.VerifiedAt.UTC(),
			ResourceVersion: root.Version,
		}, nil
	}

	var occupied struct {
		ResourceID uint `gorm:"column:resource_id"`
	}
	err = tx.Table("microsoft_binding_mailboxes").
		Select("resource_id").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("active_binding_address = ?", input.BindingAddress).
		Take(&occupied).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("lock recovered microsoft binding address: %w", err)
	}
	if err == nil && occupied.ResourceID != input.ResourceID {
		return nil, ErrMicrosoftBindingAddressOccupied
	}

	now := time.Now().UTC()
	created := !currentExists
	if created {
		current = MicrosoftBindingMailboxModel{
			ResourceID:     input.ResourceID,
			ResourceType:   "microsoft",
			OwnerUserID:    root.OwnerUserID,
			AccountEmail:   accountEmail,
			BindingAddress: input.BindingAddress,
			Purpose:        "validation",
			Status:         string(domain.MicrosoftBindingVerified),
			SelectedAt:     &now,
			VerifiedAt:     &now,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if err := tx.Create(&current).Error; err != nil {
			if isDuplicateKeyError(err) {
				return nil, ErrMicrosoftBindingAddressOccupied
			}
			return nil, fmt.Errorf("create recovered microsoft binding: %w", err)
		}
	} else {
		updates := map[string]any{
			"resource_type":   "microsoft",
			"owner_user_id":   root.OwnerUserID,
			"account_email":   accountEmail,
			"binding_address": input.BindingAddress,
			"purpose":         "validation",
			"status":          string(domain.MicrosoftBindingVerified),
			"code_msg_id":     "",
			"bound_display":   "",
			"category":        "",
			"last_safe_error": "",
			"selected_at":     now,
			"code_sent_at":    nil,
			"verified_at":     now,
			"expires_at":      nil,
			"updated_at":      now,
		}
		updated := tx.Model(&MicrosoftBindingMailboxModel{}).
			Where("id = ?", current.ID).
			Updates(updates)
		if updated.Error != nil {
			if isDuplicateKeyError(updated.Error) {
				return nil, ErrMicrosoftBindingAddressOccupied
			}
			return nil, fmt.Errorf("update recovered microsoft binding: %w", updated.Error)
		}
		if updated.RowsAffected != 1 {
			return nil, ErrMicrosoftBindingRecoveryConflict
		}
	}

	resourceVersion := root.Version
	if advanceResourceVersion {
		rootUpdate := tx.Table("email_resources").
			Where("id = ? AND type = ? AND version = ?", root.ID, "microsoft", root.Version).
			Updates(map[string]any{
				"version":    gorm.Expr("version + 1"),
				"updated_at": now,
			})
		if rootUpdate.Error != nil {
			return nil, fmt.Errorf("advance recovered microsoft binding resource version: %w", rootUpdate.Error)
		}
		if rootUpdate.RowsAffected != 1 {
			return nil, ErrMicrosoftBindingRecoveryConflict
		}
		resourceVersion++
	}
	return &MicrosoftRecoveredBindingResult{
		BindingID:       current.ID,
		Created:         created,
		Changed:         true,
		VerifiedAt:      now,
		ResourceVersion: resourceVersion,
	}, nil
}

func lockActiveBindingDomainForRecoveryTx(tx *gorm.DB, bindingAddress string) error {
	at := strings.LastIndex(bindingAddress, "@")
	if at <= 0 || at == len(bindingAddress)-1 {
		return ErrMicrosoftBindingRecoveryInvalidInput
	}
	domainName := strings.Trim(strings.ToLower(strings.TrimSpace(bindingAddress[at+1:])), ".")
	if domainName == "" || strings.Contains(domainName, "@") {
		return ErrMicrosoftBindingRecoveryInvalidInput
	}
	var row struct {
		ID uint `gorm:"column:id"`
	}
	err := tx.Table("domain_resources").
		Select("id").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("domain = ? AND purpose = ? AND status = ?", domainName, "binding", "normal").
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrMicrosoftBindingRecoveryIneligible
	}
	if err != nil {
		return fmt.Errorf("lock active microsoft binding domain: %w", err)
	}
	return nil
}

func isConcreteMicrosoftBindingAddress(address string) bool {
	address = strings.TrimSpace(address)
	if address == "" || strings.Contains(address, "*") || strings.ContainsAny(address, "\r\n\t") {
		return false
	}
	parsed, err := stdmail.ParseAddress(address)
	if err != nil || !strings.EqualFold(strings.TrimSpace(parsed.Address), address) {
		return false
	}
	parts := strings.Split(parsed.Address, "@")
	return len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != ""
}

func recoveredBindingSnapshotMatches(input MicrosoftRecoveredBindingInput, current MicrosoftBindingMailboxModel, exists bool) bool {
	if !exists {
		return input.ExpectedBindingID == 0
	}
	if input.ExpectedBindingID == 0 || current.ID != input.ExpectedBindingID {
		return false
	}
	if normalizeBindingEmail(current.BindingAddress) != input.ExpectedBindingAddress {
		return false
	}
	return current.UpdatedAt.Equal(input.ExpectedBindingUpdatedAt)
}

func recoveredBindingAlreadyApplied(current MicrosoftBindingMailboxModel, ownerUserID uint, accountEmail, bindingAddress string) bool {
	return current.ResourceType == "microsoft" &&
		current.OwnerUserID == ownerUserID &&
		normalizeBindingEmail(current.AccountEmail) == accountEmail &&
		normalizeBindingEmail(current.BindingAddress) == bindingAddress &&
		current.Purpose == "validation" &&
		current.Status == string(domain.MicrosoftBindingVerified) &&
		current.CodeMessageID == "" &&
		current.BoundDisplay == "" &&
		current.Category == "" &&
		current.LastSafeError == "" &&
		current.SelectedAt != nil &&
		current.CodeSentAt == nil &&
		current.VerifiedAt != nil &&
		current.ExpiresAt == nil
}

func upsertMicrosoftBindingTx(tx *gorm.DB, model *MicrosoftBindingMailboxModel) error {
	if model == nil {
		return nil
	}
	now := time.Now().UTC()
	assignments := map[string]any{
		"owner_user_id":   model.OwnerUserID,
		"resource_type":   firstNonBlank(model.ResourceType, "microsoft"),
		"account_email":   model.AccountEmail,
		"binding_address": model.BindingAddress,
		"purpose":         firstNonBlank(model.Purpose, "validation"),
		"status":          model.Status,
		"code_msg_id":     "",
		"bound_display":   "",
		"category":        "",
		"last_safe_error": "",
		"selected_at":     now,
		"code_sent_at":    nil,
		"verified_at":     nil,
		"expires_at":      model.ExpiresAt,
		"updated_at":      now,
	}
	if model.Status == "" {
		model.Status = string(domain.MicrosoftBindingPending)
		assignments["status"] = model.Status
	}
	err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "resource_id"}},
		DoUpdates: clause.Assignments(assignments),
	}).Create(model).Error
	if err != nil {
		return fmt.Errorf("upsert microsoft binding mailbox: %w", err)
	}
	return nil
}

func bindingStatusCategory(status domain.MicrosoftBindingStatus) string {
	switch status {
	case domain.MicrosoftBindingTimeout:
		return "code_timeout"
	case domain.MicrosoftBindingFailed:
		return "binding_failed"
	case domain.MicrosoftBindingExpired:
		return "expired"
	default:
		return ""
	}
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func normalizeBindingEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func microsoftBindingToDomain(model MicrosoftBindingMailboxModel) domain.MicrosoftBindingMailbox {
	return domain.MicrosoftBindingMailbox{
		ID:             model.ID,
		ResourceID:     model.ResourceID,
		OwnerUserID:    model.OwnerUserID,
		AccountEmail:   model.AccountEmail,
		BindingAddress: model.BindingAddress,
		Purpose:        model.Purpose,
		Status:         domain.MicrosoftBindingStatus(model.Status),
		CodeMessageID:  model.CodeMessageID,
		BoundDisplay:   model.BoundDisplay,
		Category:       model.Category,
		LastSafeError:  model.LastSafeError,
		SelectedAt:     model.SelectedAt,
		CodeSentAt:     model.CodeSentAt,
		VerifiedAt:     model.VerifiedAt,
		ExpiresAt:      model.ExpiresAt,
		CreatedAt:      model.CreatedAt,
		UpdatedAt:      model.UpdatedAt,
	}
}

func uniquePositiveBindingIDs(values []uint) []uint {
	result := make([]uint, 0, len(values))
	seen := make(map[uint]struct{}, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
