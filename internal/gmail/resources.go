package gmail

import (
	"context"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	stdmail "net/mail"
	"strings"
	"time"
	"unicode"

	"github.com/donnel666/remail/internal/money"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

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

	db := s.dbFor(ctx).Model(&localResourceModel{})
	if filter.Status == "" {
		db = db.Where("status <> ?", LocalResourceDeleted)
	}
	if filter.Search != "" {
		db = db.Where("email LIKE ?", "%"+filter.Search+"%")
	}
	if filter.Status != "" {
		if filter.Status == LocalResourceNormal {
			db = db.Where("status IN (?, ?)", LocalResourceNormal, LocalResourceAvailable)
		} else {
			db = db.Where("status = ?", filter.Status)
		}
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, fmt.Errorf("count local Gmail resources: %w", err)
	}
	var rows []struct {
		ID                    uint       `gorm:"column:id"`
		Email                 string     `gorm:"column:email"`
		Status                string     `gorm:"column:status"`
		PasswordConfigured    bool       `gorm:"column:password_configured"`
		TwoFactorConfigured   bool       `gorm:"column:two_factor_configured"`
		AppPasswordConfigured bool       `gorm:"column:app_password_configured"`
		LastSafeError         string     `gorm:"column:last_safe_error"`
		LastCheckedAt         *time.Time `gorm:"column:last_checked_at"`
		CreatedAt             time.Time  `gorm:"column:created_at"`
		UpdatedAt             time.Time  `gorm:"column:updated_at"`
	}
	if err := db.Select(`id, email, status, password <> '' AS password_configured,
two_factor_secret <> '' AS two_factor_configured, app_password <> '' AS app_password_configured,
last_safe_error, last_checked_at, created_at, updated_at`).
		Order("id DESC").Offset(filter.Offset).Limit(filter.Limit).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list local Gmail resources: %w", err)
	}

	items := make([]LocalResourceItem, len(rows))
	for i := range rows {
		items[i] = LocalResourceItem{
			ID: rows[i].ID, Email: rows[i].Email, Status: rows[i].Status,
			PasswordConfigured: rows[i].PasswordConfigured, TwoFactorConfigured: rows[i].TwoFactorConfigured,
			AppPasswordConfigured: rows[i].AppPasswordConfigured, LastSafeError: rows[i].LastSafeError,
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
		case LocalResourceAvailable:
			facets.Available = item.Count
		case LocalResourcePending:
			facets.Pending = item.Count
		case LocalResourceValidating:
			facets.Validating = item.Count
		case LocalResourceNormal:
			facets.Normal = item.Count
		case LocalResourceAbnormal:
			facets.Abnormal = item.Count
		case LocalResourceDisabled:
			facets.Disabled = item.Count
		case LocalResourceLeased:
			facets.Leased = item.Count
		case LocalResourceSold:
			facets.Sold = item.Count
		}
	}
	return facets, nil
}

func parseLocalResourceImportLine(raw string) (localResourceImportLine, bool) {
	parts := strings.Split(raw, "----")
	if len(parts) != 4 {
		return localResourceImportLine{}, false
	}
	email := strings.ToLower(strings.TrimSpace(parts[0]))
	password := parts[1]
	twoFactorSecret := strings.ToUpper(strings.TrimRight(removeWhitespace(parts[2]), "="))
	appPassword := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, parts[3])
	if len(email) > 320 || strings.TrimSpace(password) == "" || len(password) > 512 ||
		twoFactorSecret == "" || len(twoFactorSecret) > 512 || appPassword == "" || len(appPassword) > 128 {
		return localResourceImportLine{}, false
	}
	address, err := stdmail.ParseAddress(email)
	if err != nil || !strings.EqualFold(strings.TrimSpace(address.Address), email) {
		return localResourceImportLine{}, false
	}
	local, domain, ok := strings.Cut(email, "@")
	if !ok || domain != "gmail.com" && domain != "googlemail.com" {
		return localResourceImportLine{}, false
	}
	if strings.Contains(local, "+") {
		return localResourceImportLine{}, false
	}
	canonicalLocal := strings.ReplaceAll(local, ".", "")
	if canonicalLocal == "" {
		return localResourceImportLine{}, false
	}
	if _, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(twoFactorSecret); err != nil {
		return localResourceImportLine{}, false
	}
	return localResourceImportLine{
		email: email, identity: canonicalLocal + "@gmail.com", password: password,
		twoFactorSecret: twoFactorSecret, appPassword: appPassword,
	}, true
}

func removeWhitespace(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
}

func (s *Service) AllocateLocalPurchase(ctx context.Context, orderNo string, quote tradeapp.GmailSupplyQuote) (*tradeapp.GmailPurchaseDelivery, error) {
	orderNo = strings.TrimSpace(orderNo)
	cost, err := money.Parse(quote.CostPoints)
	if orderNo == "" || strings.TrimSpace(quote.Source) != SourceLocal || err != nil || cost.IsNegative() {
		return nil, ErrInvalidRoute
	}
	var delivery *tradeapp.GmailPurchaseDelivery
	err = s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		var resource localResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("status = ?", LocalResourceAvailable).Order("id ASC").Take(&resource).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return tradedomain.ErrInsufficientInventory
			}
			return fmt.Errorf("lock local Gmail purchase resource: %w", err)
		}
		updated := tx.Model(&localResourceModel{}).Where("id = ? AND status = ?", resource.ID, LocalResourceAvailable).Update("status", LocalResourceSold)
		if updated.Error != nil {
			return fmt.Errorf("sell local Gmail resource: %w", updated.Error)
		}
		if updated.RowsAffected != 1 {
			return tradedomain.ErrInsufficientInventory
		}
		resourceID := resource.ID
		allocation := allocationModel{
			OrderNo: orderNo, Source: SourceLocal, ServiceMode: string(tradedomain.ServiceModePurchase),
			ResourceID: &resourceID, Email: resource.Email, CostPointsSnapshot: money.Format(cost),
		}
		if err := tx.Create(&allocation).Error; err != nil {
			return fmt.Errorf("create local Gmail allocation: %w", err)
		}
		delivery = &tradeapp.GmailPurchaseDelivery{
			AllocationID: allocation.ID, ResourceID: resource.ID, Email: resource.Email, Password: resource.Password,
			TwoFactorSecret: resource.TwoFactorSecret, AppPassword: resource.AppPassword,
		}
		return nil
	})
	return delivery, err
}

func (s *Service) FindLocalPurchase(ctx context.Context, orderNo string) (*tradeapp.GmailPurchaseDelivery, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return nil, ErrLocalResourceMissing
	}
	var row struct {
		AllocationID    uint   `gorm:"column:allocation_id"`
		ResourceID      uint   `gorm:"column:resource_id"`
		Email           string `gorm:"column:email"`
		Password        string `gorm:"column:password"`
		TwoFactorSecret string `gorm:"column:two_factor_secret"`
		AppPassword     string `gorm:"column:app_password"`
	}
	err := s.dbFor(ctx).Table("gmail_allocations AS a").
		Select("a.id AS allocation_id, r.id AS resource_id, r.email, r.password, r.two_factor_secret, r.app_password").
		Joins("JOIN gmail_resources AS r ON r.id = a.resource_id").
		Where("a.order_no = ? AND a.service_mode = ?", orderNo, string(tradedomain.ServiceModePurchase)).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrLocalResourceMissing
	}
	if err != nil {
		return nil, fmt.Errorf("load local Gmail purchase: %w", err)
	}
	return &tradeapp.GmailPurchaseDelivery{
		AllocationID: row.AllocationID, ResourceID: row.ResourceID, Email: row.Email, Password: row.Password,
		TwoFactorSecret: row.TwoFactorSecret, AppPassword: row.AppPassword,
	}, nil
}

func (s *Service) SetLocalResourceEnabled(ctx context.Context, resourceID uint, enabled bool) error {
	if resourceID == 0 {
		return ErrLocalResourceMissing
	}
	shouldSchedule := false
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		item, err := lockLocalResource(tx, resourceID)
		if err != nil {
			return err
		}
		if item.Status == LocalResourceDeleted {
			return ErrLocalResourceMissing
		}
		if item.Status == LocalResourceLeased || item.Status == LocalResourceSold {
			return ErrLocalResourceBusy
		}
		if enabled {
			if item.Status != LocalResourceDisabled {
				return nil
			}
			shouldSchedule = true
			return tx.Model(&localResourceModel{}).Where("id = ?", item.ID).Updates(map[string]any{
				"status": LocalResourcePending, "last_safe_error": "", "last_checked_at": nil,
			}).Error
		}
		if item.Status == LocalResourceDisabled {
			return nil
		}
		return tx.Model(&localResourceModel{}).Where("id = ?", item.ID).
			Update("status", LocalResourceDisabled).Error
	})
	if err != nil || !shouldSchedule {
		return err
	}
	if err := s.scheduleLocalResourceValidation(ctx, resourceID); err != nil {
		slog.Warn("schedule local Gmail validation failed", "resource_id", resourceID, "error", err)
	}
	return nil
}

func isLocalResourceStatus(status string) bool {
	switch status {
	case LocalResourceAvailable, LocalResourcePending, LocalResourceValidating, LocalResourceNormal,
		LocalResourceAbnormal, LocalResourceDisabled, LocalResourceLeased, LocalResourceSold:
		return true
	default:
		return false
	}
}
