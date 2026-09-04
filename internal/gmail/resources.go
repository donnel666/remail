package gmail

import (
	"context"
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
	Search      string     `json:"search,omitempty"`
	Status      string     `json:"status,omitempty"`
	ForSale     *bool      `json:"forSale,omitempty"`
	CreatedFrom *time.Time `json:"createdFrom,omitempty"`
	CreatedTo   *time.Time `json:"createdTo,omitempty"`
	Offset      int        `json:"-"`
	Limit       int        `json:"-"`
}

type localResourceAdminRow struct {
	ID                    uint       `gorm:"column:id"`
	Version               uint64     `gorm:"column:version"`
	OwnerUserID           uint       `gorm:"column:owner_user_id"`
	OwnerEmail            string     `gorm:"column:owner_email"`
	OwnerNickname         string     `gorm:"column:owner_nickname"`
	OwnerGroupName        string     `gorm:"column:owner_group_name"`
	OwnerRole             string     `gorm:"column:owner_role"`
	OwnerStatus           string     `gorm:"column:owner_status"`
	Email                 string     `gorm:"column:email"`
	BindingEmail          string     `gorm:"column:binding_email"`
	Status                string     `gorm:"column:status"`
	ForSale               bool       `gorm:"column:for_sale"`
	PasswordConfigured    bool       `gorm:"column:password_configured"`
	TwoFactorConfigured   bool       `gorm:"column:two_factor_configured"`
	AppPasswordConfigured bool       `gorm:"column:app_password_configured"`
	CredentialRevision    uint64     `gorm:"column:credential_revision"`
	CredentialUpdatedAt   time.Time  `gorm:"column:credential_updated_at"`
	ValidationFailures    int        `gorm:"column:validation_failures"`
	LastAllocatedAt       *time.Time `gorm:"column:last_allocated_at"`
	LastSafeError         string     `gorm:"column:last_safe_error"`
	LastCheckedAt         *time.Time `gorm:"column:last_checked_at"`
	CreatedAt             time.Time  `gorm:"column:created_at"`
	UpdatedAt             time.Time  `gorm:"column:updated_at"`
}

type AdminGmailAliasItem struct {
	ID           uint64    `json:"id"`
	Kind         string    `json:"kind"`
	EmailAddress string    `json:"emailAddress"`
	CreatedAt    time.Time `json:"createdAt"`
}

type AdminGmailAliasList struct {
	Items  []AdminGmailAliasItem `json:"items"`
	Total  int64                 `json:"total"`
	Offset int                   `json:"offset"`
	Limit  int                   `json:"limit"`
}

const (
	adminGmailResourceMaxLimit = 200
	adminGmailAliasMaxLimit    = 100
)

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
	filter, err := normalizeLocalResourceListFilter(filter)
	if err != nil {
		return nil, err
	}
	db := applyLocalResourceListFilter(localResourceAdminQuery(ctx, s.dbFor(ctx)), filter, false, false)
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, fmt.Errorf("count local Gmail resources: %w", err)
	}
	var rows []localResourceAdminRow
	if err := db.Select(`gr.id, er.version, er.owner_user_id,
u.email AS owner_email, u.nickname AS owner_nickname, COALESCE(ug.name, '') AS owner_group_name,
u.role AS owner_role, u.status AS owner_status, gr.email, gr.binding_email, gr.status, gr.for_sale,
gr.password <> '' AS password_configured,
gr.two_factor_secret <> '' AS two_factor_configured, gr.app_password <> '' AS app_password_configured,
gr.credential_revision, gr.credential_updated_at, gr.validation_failures, gr.last_allocated_at,
gr.last_safe_error, gr.last_checked_at, gr.created_at, gr.updated_at`).
		Order("gr.id DESC").Offset(filter.Offset).Limit(filter.Limit).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list local Gmail resources: %w", err)
	}

	items := make([]LocalResourceItem, len(rows))
	for i := range rows {
		items[i] = localResourceItemFromRow(rows[i])
	}
	facets, err := s.localResourceFacets(ctx, filter)
	if err != nil {
		return nil, err
	}
	return &LocalResourceList{Items: items, Total: total, Offset: filter.Offset, Limit: filter.Limit, Facets: facets}, nil
}

func (s *Service) GetAdminLocalResource(ctx context.Context, resourceID uint) (*LocalResourceItem, error) {
	if s == nil || s.db == nil || resourceID == 0 {
		return nil, ErrLocalResourceMissing
	}
	var row localResourceAdminRow
	err := localResourceAdminQuery(ctx, s.dbFor(ctx)).Select(`gr.id, er.version, er.owner_user_id,
u.email AS owner_email, u.nickname AS owner_nickname, COALESCE(ug.name, '') AS owner_group_name,
u.role AS owner_role, u.status AS owner_status, gr.email, gr.binding_email, gr.status, gr.for_sale,
gr.password <> '' AS password_configured,
gr.two_factor_secret <> '' AS two_factor_configured, gr.app_password <> '' AS app_password_configured,
gr.credential_revision, gr.credential_updated_at, gr.validation_failures, gr.last_allocated_at,
gr.last_safe_error, gr.last_checked_at, gr.created_at, gr.updated_at`).
		Where("gr.id = ?", resourceID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrLocalResourceMissing
	}
	if err != nil {
		return nil, fmt.Errorf("get local Gmail resource: %w", err)
	}
	item := localResourceItemFromRow(row)
	return &item, nil
}

func (s *Service) ListAdminGmailAliases(ctx context.Context, resourceID uint, offset, limit int) (*AdminGmailAliasList, error) {
	if s == nil || s.db == nil || resourceID == 0 || offset < 0 || limit < 1 || limit > adminGmailAliasMaxLimit {
		return nil, ErrInvalidLocalResource
	}
	db := s.dbFor(ctx)
	var exists int64
	if err := db.Table("gmail_resources").Where("id = ?", resourceID).Count(&exists).Error; err != nil {
		return nil, fmt.Errorf("find Gmail aliases resource: %w", err)
	}
	if exists == 0 {
		return nil, ErrLocalResourceMissing
	}

	var total int64
	if err := db.Raw(`
SELECT COUNT(*)
FROM (
    SELECT LOWER(TRIM(email)) AS email, mailbox
    FROM gmail_allocations
    WHERE resource_id = ? AND mailbox IN (?, ?)
    GROUP BY LOWER(TRIM(email)), mailbox
) aliases`, resourceID, GmailMailboxDot, GmailMailboxPlus).Scan(&total).Error; err != nil {
		return nil, fmt.Errorf("count Gmail aliases: %w", err)
	}

	type aliasRow struct {
		ID        uint64    `gorm:"column:id"`
		Kind      string    `gorm:"column:kind"`
		Email     string    `gorm:"column:email"`
		CreatedAt time.Time `gorm:"column:created_at"`
	}
	var rows []aliasRow
	if err := db.Raw(`
SELECT a.id, a.mailbox AS kind, LOWER(TRIM(a.email)) AS email, a.created_at
FROM gmail_allocations a
JOIN (
    SELECT MIN(id) AS id
    FROM gmail_allocations
    WHERE resource_id = ? AND mailbox IN (?, ?)
    GROUP BY LOWER(TRIM(email)), mailbox
) aliases ON aliases.id = a.id
ORDER BY a.created_at DESC, a.id DESC
LIMIT ? OFFSET ?`, resourceID, GmailMailboxDot, GmailMailboxPlus, limit, offset).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list Gmail aliases: %w", err)
	}
	items := make([]AdminGmailAliasItem, len(rows))
	for i := range rows {
		items[i] = AdminGmailAliasItem{
			ID: rows[i].ID, Kind: rows[i].Kind, EmailAddress: rows[i].Email, CreatedAt: rows[i].CreatedAt,
		}
	}
	return &AdminGmailAliasList{Items: items, Total: total, Offset: offset, Limit: limit}, nil
}

func normalizeLocalResourceListFilter(filter LocalResourceListFilter) (LocalResourceListFilter, error) {
	filter.Search = strings.ToLower(strings.TrimSpace(filter.Search))
	filter.Status = strings.ToLower(strings.TrimSpace(filter.Status))
	if len(filter.Search) > 320 || filter.Status != "" && !isLocalResourceStatus(filter.Status) ||
		filter.Offset < 0 || filter.CreatedFrom != nil && filter.CreatedTo != nil && !filter.CreatedTo.After(*filter.CreatedFrom) {
		return filter, ErrInvalidLocalResource
	}
	if filter.Limit == 0 {
		filter.Limit = 20
	}
	if filter.Limit < 1 || filter.Limit > 200 {
		return filter, ErrInvalidLocalResource
	}
	return filter, nil
}

func localResourceAdminQuery(ctx context.Context, db *gorm.DB) *gorm.DB {
	return db.WithContext(ctx).Table("gmail_resources AS gr").
		Joins("JOIN email_resources AS er ON er.id = gr.id AND er.type = ?", "gmail").
		Joins("JOIN users AS u ON u.id = er.owner_user_id").
		Joins("LEFT JOIN user_groups AS ug ON ug.id = u.user_group_id")
}

func applyLocalResourceListFilter(db *gorm.DB, filter LocalResourceListFilter, ignoreStatus, ignoreForSale bool) *gorm.DB {
	if filter.Search != "" {
		like := "%" + filter.Search + "%"
		db = db.Where(`LOWER(gr.email) LIKE ? OR LOWER(gr.binding_email) LIKE ? OR LOWER(u.email) LIKE ? OR
LOWER(u.nickname) LIKE ? OR CAST(gr.id AS CHAR) LIKE ? OR CAST(er.owner_user_id AS CHAR) LIKE ? OR EXISTS (
    SELECT 1 FROM gmail_allocations search_alias
    WHERE search_alias.resource_id = gr.id
      AND search_alias.mailbox IN (?, ?)
      AND LOWER(search_alias.email) LIKE ?
)`, like, like, like, like, like, like, GmailMailboxDot, GmailMailboxPlus, like)
	}
	if !ignoreStatus {
		switch filter.Status {
		case "":
			db = db.Where("gr.status <> ?", LocalResourceDeleted)
		case LocalResourceNormal:
			db = db.Where("gr.status IN (?, ?, ?, ?)", LocalResourceNormal, localResourceRollbackNormal, localResourceRollbackLeased, localResourceRollbackSold)
		default:
			db = db.Where("gr.status = ?", filter.Status)
		}
	}
	if !ignoreForSale && filter.ForSale != nil {
		db = db.Where("gr.for_sale = ?", *filter.ForSale)
	}
	if filter.CreatedFrom != nil {
		db = db.Where("gr.created_at >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		db = db.Where("gr.created_at < ?", *filter.CreatedTo)
	}
	return db
}

func localResourceItemFromRow(row localResourceAdminRow) LocalResourceItem {
	status := row.Status
	if status == localResourceRollbackNormal || status == localResourceRollbackLeased || status == localResourceRollbackSold {
		status = LocalResourceNormal
	}
	return LocalResourceItem{
		ID: row.ID, Version: row.Version, OwnerUserID: row.OwnerUserID,
		Owner: LocalResourceOwner{ID: row.OwnerUserID, Email: row.OwnerEmail, Nickname: row.OwnerNickname,
			GroupName: row.OwnerGroupName, Role: row.OwnerRole, Enabled: row.OwnerStatus == "active"},
		Email: row.Email, BindingEmail: row.BindingEmail, Status: status, ForSale: row.ForSale,
		PasswordConfigured: row.PasswordConfigured, TwoFactorConfigured: row.TwoFactorConfigured,
		AppPasswordConfigured: row.AppPasswordConfigured, CredentialRevision: row.CredentialRevision,
		CredentialUpdatedAt: row.CredentialUpdatedAt, ValidationFailures: row.ValidationFailures,
		LastAllocatedAt: row.LastAllocatedAt, LastSafeError: row.LastSafeError,
		LastCheckedAt: row.LastCheckedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func (s *Service) localResourceFacets(ctx context.Context, filter LocalResourceListFilter) (LocalResourceFacets, error) {
	type row struct {
		Status string `gorm:"column:status"`
		Count  int64  `gorm:"column:count"`
	}
	var rows []row
	statusQuery := applyLocalResourceListFilter(localResourceAdminQuery(ctx, s.dbFor(ctx)), filter, true, false)
	if err := statusQuery.Select("gr.status, COUNT(*) AS count").Group("gr.status").Scan(&rows).Error; err != nil {
		return LocalResourceFacets{}, fmt.Errorf("count local Gmail resource facets: %w", err)
	}
	var facets LocalResourceFacets
	for _, item := range rows {
		if item.Status != LocalResourceDeleted {
			facets.All += item.Count
		}
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
		case LocalResourceDeleted:
			facets.Deleted = item.Count
		}
	}
	var saleRows []struct {
		ForSale bool  `gorm:"column:for_sale"`
		Count   int64 `gorm:"column:count"`
	}
	saleQuery := applyLocalResourceListFilter(localResourceAdminQuery(ctx, s.dbFor(ctx)), filter, false, true)
	if err := saleQuery.Select("gr.for_sale, COUNT(*) AS count").Group("gr.for_sale").Scan(&saleRows).Error; err != nil {
		return LocalResourceFacets{}, fmt.Errorf("count local Gmail sale facets: %w", err)
	}
	for _, item := range saleRows {
		facets.ForSale.All += item.Count
		if item.ForSale {
			facets.ForSale.Yes += item.Count
		} else {
			facets.ForSale.No += item.Count
		}
	}
	return facets, nil
}

func parseLocalResourceImportLine(raw string) (localResourceImportLine, bool) {
	parts := splitLocalResourceImportFields(raw)
	if len(parts) < 2 || len(parts) > 5 {
		return localResourceImportLine{}, false
	}
	email := strings.ToLower(strings.TrimSpace(parts[0]))
	password, bindingEmail, twoFactorSecret := "", "", ""
	appPassword := removeWhitespace(parts[len(parts)-1])
	switch len(parts) {
	case 3:
		password = parts[1]
	case 4:
		password = parts[1]
		candidate := strings.ToLower(strings.TrimSpace(parts[2]))
		if validLocalGmailEmailAddress(candidate) {
			bindingEmail = candidate
		} else {
			twoFactorSecret = strings.ToUpper(strings.TrimRight(removeWhitespace(parts[2]), "="))
		}
	case 5:
		password = parts[1]
		bindingEmail = strings.ToLower(strings.TrimSpace(parts[2]))
		twoFactorSecret = strings.ToUpper(strings.TrimRight(removeWhitespace(parts[3]), "="))
	}
	if len(email) > 320 || len(password) > 512 || len(bindingEmail) > 320 || len(twoFactorSecret) > 512 ||
		len(parts) > 2 && strings.TrimSpace(password) == "" ||
		bindingEmail != "" && !validLocalGmailEmailAddress(bindingEmail) ||
		len(parts) == 5 && bindingEmail == "" ||
		(len(parts) == 4 && bindingEmail == "" || len(parts) == 5) && twoFactorSecret == "" ||
		!validLocalGmailAppPassword(appPassword) {
		return localResourceImportLine{}, false
	}
	if !validLocalGmailEmailAddress(email) {
		return localResourceImportLine{}, false
	}
	identity, ok := localGmailIdentity(email)
	if !ok {
		return localResourceImportLine{}, false
	}
	if twoFactorSecret != "" && !validLocalGmailTOTPSecret(twoFactorSecret) {
		return localResourceImportLine{}, false
	}
	return localResourceImportLine{
		email: email, identity: identity, password: password,
		bindingEmail: bindingEmail, twoFactorSecret: twoFactorSecret, appPassword: appPassword,
	}, true
}

func splitLocalResourceImportFields(raw string) []string {
	separator := "----"
	if !strings.Contains(raw, separator) {
		separator = ";"
	}
	return strings.Split(raw, separator)
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
			nextGeneration := nextAdminLocalResourceGeneration(item.ValidationGeneration)
			result := tx.Model(&localResourceModel{}).Where("id = ? AND status = ?", item.ID, LocalResourceDisabled).Updates(map[string]any{
				"status": LocalResourcePending, "validation_generation": nextGeneration,
				"validation_failures": 0, "validation_request_id": strings.TrimSpace(requestID),
				"validation_command_hash": "", "last_safe_error": "", "last_checked_at": nil, "updated_at": now,
			})
			if result.Error != nil {
				return result.Error
			}
			changed, shouldSchedule = result.RowsAffected == 1, result.RowsAffected == 1
			if changed {
				if _, err := ensureGmailMaintenanceRunTx(
					ctx, tx, item.ID, nextGeneration, gmailMaintenanceValidation,
					item.CredentialRevision, 0, now,
				); err != nil {
					return err
				}
			}
		} else if item.Status != LocalResourceDisabled {
			result := tx.Model(&localResourceModel{}).Where("id = ? AND status <> ?", item.ID, LocalResourceDisabled).
				Updates(map[string]any{"status": LocalResourceDisabled, "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			changed = result.RowsAffected == 1
			if changed {
				if err := cancelActiveGmailMaintenanceRunsTx(
					ctx, tx, item.ID, now, "Gmail maintenance was canceled because the resource was disabled.",
				); err != nil {
					return err
				}
			}
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
	case LocalResourcePending, LocalResourceValidating, LocalResourceIdentifying, LocalResourceNormal, LocalResourceAbnormal, LocalResourceDisabled, LocalResourceDeleted:
		return true
	default:
		return false
	}
}
