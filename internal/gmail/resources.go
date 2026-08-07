package gmail

import (
	"context"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	stdmail "net/mail"
	"strconv"
	"strings"
	"time"
	"unicode"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const localGmailAllocationBucketCount = 2048

type LocalResourceListFilter struct {
	Search string
	Status string
	Offset int
	Limit  int
}

type localResourceImportLine struct {
	lineNumber      int
	email           string
	identity        string
	password        string
	bindingEmail    string
	twoFactorSecret string
	appPassword     string
}

func (s *Service) ListLocalResources(ctx context.Context, filter LocalResourceListFilter) (*LocalResourceList, error) {
	filter.Search = strings.ToLower(strings.TrimSpace(filter.Search))
	filter.Status = strings.ToLower(strings.TrimSpace(filter.Status))
	if filter.Status != "" && !isLocalResourceStatus(filter.Status) {
		return nil, ErrInvalidLocalResource
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 20
	}

	db := s.dbFor(ctx).Table("gmail_resources AS gr").
		Joins("JOIN email_resources AS er ON er.id = gr.id AND er.type = ?", "gmail")
	if filter.Status == "" {
		db = db.Where("gr.status <> ?", LocalResourceDeleted)
	}
	if filter.Search != "" {
		db = db.Where("gr.email LIKE ?", "%"+filter.Search+"%")
	}
	if filter.Status != "" {
		if filter.Status == LocalResourceNormal {
			db = db.Where("gr.status IN (?, ?, ?, ?)", LocalResourceNormal, localResourceRollbackNormal, localResourceRollbackLeased, localResourceRollbackSold)
		} else {
			db = db.Where("gr.status = ?", filter.Status)
		}
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, fmt.Errorf("count local Gmail resources: %w", err)
	}
	var rows []struct {
		ID                    uint       `gorm:"column:id"`
		Version               uint64     `gorm:"column:version"`
		OwnerUserID           uint       `gorm:"column:owner_user_id"`
		Email                 string     `gorm:"column:email"`
		BindingEmail          string     `gorm:"column:binding_email"`
		Status                string     `gorm:"column:status"`
		ForSale               bool       `gorm:"column:for_sale"`
		PasswordConfigured    bool       `gorm:"column:password_configured"`
		TwoFactorConfigured   bool       `gorm:"column:two_factor_configured"`
		AppPasswordConfigured bool       `gorm:"column:app_password_configured"`
		CredentialRevision    uint64     `gorm:"column:credential_revision"`
		ValidationFailures    int        `gorm:"column:validation_failures"`
		LastAllocatedAt       *time.Time `gorm:"column:last_allocated_at"`
		LastSafeError         string     `gorm:"column:last_safe_error"`
		LastCheckedAt         *time.Time `gorm:"column:last_checked_at"`
		CreatedAt             time.Time  `gorm:"column:created_at"`
		UpdatedAt             time.Time  `gorm:"column:updated_at"`
	}
	if err := db.Select(`gr.id, er.version, er.owner_user_id, gr.email, gr.binding_email, gr.status, gr.for_sale,
gr.password <> '' AS password_configured,
gr.two_factor_secret <> '' AS two_factor_configured, gr.app_password <> '' AS app_password_configured,
gr.credential_revision, gr.validation_failures, gr.last_allocated_at,
gr.last_safe_error, gr.last_checked_at, gr.created_at, gr.updated_at`).
		Order("gr.id DESC").Offset(filter.Offset).Limit(filter.Limit).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list local Gmail resources: %w", err)
	}

	items := make([]LocalResourceItem, len(rows))
	for i := range rows {
		status := rows[i].Status
		if status == localResourceRollbackNormal || status == localResourceRollbackLeased || status == localResourceRollbackSold {
			status = LocalResourceNormal
		}
		items[i] = LocalResourceItem{
			ID: rows[i].ID, Version: rows[i].Version, OwnerUserID: rows[i].OwnerUserID,
			Email: rows[i].Email, BindingEmail: rows[i].BindingEmail, Status: status, ForSale: rows[i].ForSale,
			PasswordConfigured: rows[i].PasswordConfigured, TwoFactorConfigured: rows[i].TwoFactorConfigured,
			AppPasswordConfigured: rows[i].AppPasswordConfigured,
			CredentialRevision:    rows[i].CredentialRevision, ValidationFailures: rows[i].ValidationFailures,
			LastAllocatedAt: rows[i].LastAllocatedAt, LastSafeError: rows[i].LastSafeError,
			LastCheckedAt: rows[i].LastCheckedAt, CreatedAt: rows[i].CreatedAt, UpdatedAt: rows[i].UpdatedAt,
		}
	}
	facets, err := s.localResourceFacets(ctx, filter.Search)
	if err != nil {
		return nil, err
	}
	return &LocalResourceList{Items: items, Total: total, Offset: filter.Offset, Limit: filter.Limit, Facets: facets}, nil
}

func (s *Service) localResourceFacets(ctx context.Context, search string) (LocalResourceFacets, error) {
	type row struct {
		Status string `gorm:"column:status"`
		Count  int64  `gorm:"column:count"`
	}
	db := s.dbFor(ctx).Model(&localResourceModel{}).Select("status, COUNT(*) AS count")
	if search != "" {
		db = db.Where("email LIKE ?", "%"+search+"%")
	}
	var rows []row
	if err := db.Group("status").Scan(&rows).Error; err != nil {
		return LocalResourceFacets{}, fmt.Errorf("count local Gmail resource facets: %w", err)
	}
	var facets LocalResourceFacets
	for _, item := range rows {
		if item.Status == LocalResourceDeleted {
			continue
		}
		facets.All += item.Count
		switch item.Status {
		case LocalResourcePending:
			facets.Pending = item.Count
		case LocalResourceValidating:
			facets.Validating = item.Count
		case LocalResourceIdentifying:
			facets.Identifying = item.Count
		case LocalResourceNormal, localResourceRollbackNormal, localResourceRollbackLeased, localResourceRollbackSold:
			facets.Normal += item.Count
		case LocalResourceAbnormal:
			facets.Abnormal = item.Count
		case LocalResourceDisabled:
			facets.Disabled = item.Count
		}
	}
	return facets, nil
}

func parseLocalResourceImportLine(raw string) (localResourceImportLine, bool) {
	parts := strings.Split(raw, "----")
	if len(parts) < 2 || len(parts) > 4 {
		return localResourceImportLine{}, false
	}
	email := strings.ToLower(strings.TrimSpace(parts[0]))
	password := parts[1]
	bindingEmail, twoFactorSecret, appPassword := "", "", ""
	if len(parts) >= 3 {
		if candidate := strings.ToLower(strings.TrimSpace(parts[2])); validLocalGmailEmailAddress(candidate) {
			bindingEmail = candidate
		} else {
			twoFactorSecret = strings.ToUpper(strings.TrimRight(removeWhitespace(parts[2]), "="))
		}
	}
	if len(parts) == 4 {
		if bindingEmail != "" {
			twoFactorSecret = strings.ToUpper(strings.TrimRight(removeWhitespace(parts[3]), "="))
		} else {
			appPassword = removeWhitespace(parts[3])
		}
	}
	if len(email) > 320 || strings.TrimSpace(password) == "" || len(password) > 512 ||
		len(bindingEmail) > 320 || len(twoFactorSecret) > 512 || len(appPassword) > 128 ||
		len(parts) == 3 && bindingEmail == "" && twoFactorSecret == "" ||
		len(parts) == 4 && (twoFactorSecret == "" || bindingEmail == "" && appPassword == "") {
		return localResourceImportLine{}, false
	}
	if !validLocalGmailEmailAddress(email) {
		return localResourceImportLine{}, false
	}
	identity, ok := localGmailIdentity(email)
	if !ok {
		return localResourceImportLine{}, false
	}
	if twoFactorSecret != "" {
		if _, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(twoFactorSecret); err != nil {
			return localResourceImportLine{}, false
		}
	}
	return localResourceImportLine{
		email: email, identity: identity, password: password,
		bindingEmail: bindingEmail, twoFactorSecret: twoFactorSecret, appPassword: appPassword,
	}, true
}

func localGmailIdentity(email string) (string, bool) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !validLocalGmailEmailAddress(email) {
		return "", false
	}
	local, domain, ok := strings.Cut(email, "@")
	if !ok || domain != "gmail.com" && domain != "googlemail.com" || strings.Contains(local, "+") {
		return "", false
	}
	canonicalLocal := strings.ReplaceAll(local, ".", "")
	if canonicalLocal == "" {
		return "", false
	}
	return canonicalLocal + "@gmail.com", true
}

func validLocalGmailEmailAddress(value string) bool {
	if value == "" || len(value) > 320 || strings.Count(value, "@") != 1 {
		return false
	}
	address, err := stdmail.ParseAddress(value)
	return err == nil && strings.EqualFold(strings.TrimSpace(address.Address), value)
}

func removeWhitespace(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
}

func isGmailMailbox(mailbox string) bool {
	return mailbox == GmailMailboxMain || mailbox == GmailMailboxDot || mailbox == GmailMailboxPlus
}

func (s *Service) FindLocalPurchase(ctx context.Context, orderNo string) (*tradeapp.GmailPurchaseDelivery, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return nil, ErrLocalResourceMissing
	}
	delivery, err := findLocalPurchaseWithDB(s.dbFor(ctx), orderNo)
	if err != nil {
		return nil, err
	}
	if delivery == nil {
		return nil, ErrLocalResourceMissing
	}
	return delivery, nil
}

func findLocalPurchaseWithDB(db *gorm.DB, orderNo string) (*tradeapp.GmailPurchaseDelivery, error) {
	var row struct {
		AllocationID    uint   `gorm:"column:allocation_id"`
		ResourceID      uint   `gorm:"column:resource_id"`
		SupplyScope     string `gorm:"column:supply_scope"`
		Email           string `gorm:"column:email"`
		Password        string `gorm:"column:password"`
		TwoFactorSecret string `gorm:"column:two_factor_secret"`
		AppPassword     string `gorm:"column:app_password"`
	}
	err := db.Table("gmail_allocations AS a").
		Select("a.id AS allocation_id, r.id AS resource_id, a.supply_scope, a.email, r.password, r.two_factor_secret, r.app_password").
		Joins("JOIN gmail_resources AS r ON r.id = a.resource_id").
		Where("a.order_no = ? AND a.source = ? AND a.service_mode = ? AND a.status = ?", orderNo, SourceLocal, string(tradedomain.ServiceModePurchase), AllocationStatusAllocated).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load local Gmail purchase: %w", err)
	}
	return &tradeapp.GmailPurchaseDelivery{
		AllocationID: row.AllocationID, ResourceID: row.ResourceID,
		SupplyScope: tradeapp.SupplyScope(row.SupplyScope), Email: row.Email, Password: row.Password,
		TwoFactorSecret: row.TwoFactorSecret, AppPassword: row.AppPassword,
	}, nil
}

func (s *Service) SetLocalResourceEnabled(ctx context.Context, resourceID uint, enabled bool) error {
	return s.setLocalResourceEnabled(ctx, resourceID, 0, enabled, "", nil, false)
}

func (s *Service) SetAdminLocalResourceEnabled(
	ctx context.Context,
	resourceID uint,
	expectedVersion uint64,
	enabled bool,
	operatorUserID uint,
	idempotencyKey, requestID, path string,
) error {
	if s == nil || s.redis == nil || resourceID == 0 || expectedVersion == 0 || operatorUserID == 0 {
		return ErrInvalidLocalResource
	}
	operation := "disable"
	if enabled {
		operation = "enable"
	}
	fingerprint := stableDigest(fmt.Sprintf("%s|%d|%d|%d", operation, operatorUserID, resourceID, expectedVersion))
	reused, err := s.claimLocalGmailCommandIdempotency(ctx, operatorUserID, idempotencyKey, fingerprint)
	if err != nil {
		return err
	}
	return s.setLocalResourceEnabled(ctx, resourceID, expectedVersion, enabled, requestID, &governancedomain.OperationLog{
		OperatorUserID: operatorUserID,
		OperationType:  "gmail.resource." + operation,
		ResourceType:   "gmail_resource",
		ResourceID:     strconv.FormatUint(uint64(resourceID), 10),
		Path:           path,
		Result:         "success",
		SafeSummary:    "Gmail resource " + operation + " command applied.",
		RequestID:      requestID,
	}, reused)
}

func (s *Service) setLocalResourceEnabled(
	ctx context.Context,
	resourceID uint,
	expectedVersion uint64,
	enabled bool,
	requestID string,
	log *governancedomain.OperationLog,
	reused bool,
) error {
	if resourceID == 0 {
		return ErrLocalResourceMissing
	}
	shouldSchedule := false
	changed := false
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		var root resourceRootModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND type = ?", resourceID, "gmail").Take(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLocalResourceMissing
			}
			return fmt.Errorf("lock Gmail resource state root: %w", err)
		}
		var item localResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", resourceID).Take(&item).Error; err != nil {
			return fmt.Errorf("lock Gmail resource state: %w", err)
		}
		if item.Status == LocalResourceDeleted {
			return ErrLocalResourceMissing
		}
		targetReached := enabled && item.Status != LocalResourceDisabled || !enabled && item.Status == LocalResourceDisabled
		if reused && targetReached {
			return nil
		}
		if expectedVersion != 0 && root.Version != expectedVersion {
			return ErrLocalResourceVersion
		}
		now := s.now().UTC()
		if enabled {
			if item.Status != LocalResourceDisabled {
				return ErrInvalidLocalResource
			}
			result := tx.Model(&localResourceModel{}).Where("id = ? AND status = ?", item.ID, LocalResourceDisabled).Updates(map[string]any{
				"status": LocalResourcePending, "validation_generation": gorm.Expr("validation_generation + 1"),
				"validation_failures": 0, "validation_request_id": strings.TrimSpace(requestID),
				"validation_command_hash": "", "last_safe_error": "", "last_checked_at": nil, "updated_at": now,
			})
			if result.Error != nil {
				return result.Error
			}
			changed, shouldSchedule = result.RowsAffected == 1, result.RowsAffected == 1
		} else if item.Status != LocalResourceDisabled {
			result := tx.Model(&localResourceModel{}).Where("id = ? AND status <> ?", item.ID, LocalResourceDisabled).
				Updates(map[string]any{"status": LocalResourceDisabled, "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			changed = result.RowsAffected == 1
		}
		if !changed {
			return nil
		}
		if err := tx.Model(&resourceRootModel{}).Where("id = ? AND version = ?", root.ID, root.Version).
			Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
			return err
		}
		if log != nil && s.logs != nil {
			if err := s.logs.CreateInTx(ctx, tx, log); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || !shouldSchedule {
		return err
	}
	if err := s.scheduleLocalResourceValidation(ctx, resourceID); err != nil {
		slog.Warn("schedule local Gmail validation failed", "resource_id", resourceID, "error", err)
	}
	return nil
}

func (s *Service) SetAdminLocalResourceForSale(
	ctx context.Context,
	resourceID uint,
	expectedVersion uint64,
	forSale bool,
	operatorUserID uint,
	idempotencyKey, requestID, path string,
) error {
	if s == nil || s.redis == nil || resourceID == 0 || expectedVersion == 0 || operatorUserID == 0 {
		return ErrInvalidLocalResource
	}
	operation := "unpublish"
	if forSale {
		operation = "publish"
	}
	fingerprint := stableDigest(fmt.Sprintf("%s|%d|%d|%d", operation, operatorUserID, resourceID, expectedVersion))
	reused, err := s.claimLocalGmailCommandIdempotency(ctx, operatorUserID, idempotencyKey, fingerprint)
	if err != nil {
		return err
	}
	return s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		var root resourceRootModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND type = ?", resourceID, "gmail").Take(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLocalResourceMissing
			}
			return fmt.Errorf("lock Gmail publish root: %w", err)
		}
		var resource localResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", resourceID).Take(&resource).Error; err != nil {
			return fmt.Errorf("lock Gmail publish resource: %w", err)
		}
		if resource.Status == LocalResourceDeleted {
			return ErrLocalResourceMissing
		}
		if reused && resource.ForSale == forSale {
			return nil
		}
		if root.Version != expectedVersion {
			return ErrLocalResourceVersion
		}
		if resource.ForSale == forSale {
			return nil
		}
		if forSale {
			var eligible uint
			if err := tx.Table("users").Select("id").Where("id = ? AND status = ? AND role IN ?",
				root.OwnerUserID, "active", []string{"supplier", "admin", "super_admin"}).Limit(1).Scan(&eligible).Error; err != nil {
				return fmt.Errorf("validate Gmail public owner: %w", err)
			}
			if eligible == 0 {
				return ErrInvalidLocalResource
			}
		}
		now := s.now().UTC()
		if err := tx.Model(&localResourceModel{}).Where("id = ?", resourceID).
			Updates(map[string]any{"for_sale": forSale, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&resourceRootModel{}).Where("id = ? AND version = ?", root.ID, root.Version).
			Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
			return err
		}
		if s.logs != nil {
			return s.logs.CreateInTx(ctx, tx, &governancedomain.OperationLog{
				OperatorUserID: operatorUserID, OperationType: "gmail.resource." + operation,
				ResourceType: "gmail_resource", ResourceID: strconv.FormatUint(uint64(resourceID), 10),
				Path: path, Result: "success", SafeSummary: "Gmail resource " + operation + " command applied.",
				RequestID: requestID,
			})
		}
		return nil
	})
}

func isLocalResourceStatus(status string) bool {
	switch status {
	case LocalResourcePending, LocalResourceValidating, LocalResourceIdentifying, LocalResourceNormal, LocalResourceAbnormal, LocalResourceDisabled:
		return true
	default:
		return false
	}
}
