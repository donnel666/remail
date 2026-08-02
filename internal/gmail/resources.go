package gmail

import (
	"bufio"
	"context"
	"encoding/base32"
	"errors"
	"fmt"
	stdmail "net/mail"
	"strings"
	"time"
	"unicode"

	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	localResourceImportMaxBytes     = 10 << 20
	localResourceImportBodyMaxBytes = localResourceImportMaxBytes*6 + 1024
)

type LocalResourceListFilter struct {
	Search string
	Status string
	Offset int
	Limit  int
}

type localResourceImportLine struct {
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
	if filter.Search != "" {
		db = db.Where("email LIKE ?", "%"+filter.Search+"%")
	}
	if filter.Status != "" {
		db = db.Where("status = ?", filter.Status)
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
		facets.All += item.Count
		switch item.Status {
		case LocalResourceAvailable:
			facets.Available = item.Count
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

func (s *Service) ImportLocalResources(ctx context.Context, content, strategy string) (*LocalResourceImportResult, error) {
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if strategy == "" {
		strategy = "skip"
	}
	if strategy != "skip" && strategy != "abort" || len(content) == 0 || len(content) > localResourceImportMaxBytes {
		return nil, ErrInvalidLocalResource
	}

	result := &LocalResourceImportResult{}
	lines := make([]localResourceImportLine, 0)
	seen := make(map[string]struct{})
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 64*1024), localResourceImportMaxBytes)
	for scanner.Scan() {
		raw := strings.TrimSuffix(scanner.Text(), "\r")
		if strings.TrimSpace(raw) == "" {
			continue
		}
		result.Total++
		line, ok := parseLocalResourceImportLine(raw)
		if !ok {
			result.Invalid++
			continue
		}
		if _, exists := seen[line.identity]; exists {
			result.Skipped++
			continue
		}
		seen[line.identity] = struct{}{}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil || result.Total == 0 || strategy == "abort" && result.Invalid > 0 {
		return nil, ErrInvalidLocalResource
	}

	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		identities := make([]string, len(lines))
		for i := range lines {
			identities[i] = lines[i].identity
		}
		var existing []localResourceModel
		if len(identities) > 0 {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id, identity, status").Where("identity IN ?", identities).Find(&existing).Error; err != nil {
				return err
			}
		}
		byIdentity := make(map[string]localResourceModel, len(existing))
		for _, item := range existing {
			byIdentity[item.Identity] = item
		}
		create := make([]localResourceModel, 0, len(lines))
		for _, line := range lines {
			item, exists := byIdentity[line.identity]
			if !exists {
				create = append(create, localResourceModel{
					Email: line.email, Identity: line.identity, Password: line.password, TwoFactorSecret: line.twoFactorSecret,
					AppPassword: line.appPassword, Status: LocalResourceAvailable,
				})
				continue
			}
			if item.Status == LocalResourceLeased || item.Status == LocalResourceSold {
				result.Skipped++
				continue
			}
			updated := tx.Model(&localResourceModel{}).Where("id = ? AND status IN ?", item.ID, []string{LocalResourceAvailable, LocalResourceDisabled}).Updates(map[string]any{
				"email":    line.email,
				"password": line.password, "two_factor_secret": line.twoFactorSecret,
				"app_password": line.appPassword, "status": LocalResourceAvailable, "last_safe_error": "",
			})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				result.Skipped++
				continue
			}
			result.Updated++
		}
		if len(create) > 0 {
			if err := tx.CreateInBatches(create, 500).Error; err != nil {
				return err
			}
			result.Imported += len(create)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("import local Gmail resources: %w", err)
	}
	return result, nil
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

func (s *Service) AllocateLocalPurchase(ctx context.Context, quote tradeapp.GmailSupplyQuote) (*tradeapp.GmailPurchaseDelivery, error) {
	if strings.TrimSpace(quote.Source) != SourceLocal {
		return nil, ErrInvalidRoute
	}
	db := s.dbFor(ctx)
	var resource localResourceModel
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("status = ?", LocalResourceAvailable).Order("id ASC").Take(&resource).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, tradedomain.ErrInsufficientInventory
		}
		return nil, fmt.Errorf("lock local Gmail purchase resource: %w", err)
	}
	updated := db.Model(&localResourceModel{}).Where("id = ? AND status = ?", resource.ID, LocalResourceAvailable).Update("status", LocalResourceSold)
	if updated.Error != nil {
		return nil, fmt.Errorf("sell local Gmail resource: %w", updated.Error)
	}
	if updated.RowsAffected != 1 {
		return nil, tradedomain.ErrInsufficientInventory
	}
	return &tradeapp.GmailPurchaseDelivery{
		ResourceID: resource.ID, Email: resource.Email, Password: resource.Password,
		TwoFactorSecret: resource.TwoFactorSecret, AppPassword: resource.AppPassword,
	}, nil
}

func (s *Service) FindLocalPurchase(ctx context.Context, orderNo string) (*tradeapp.GmailPurchaseDelivery, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return nil, ErrLocalResourceMissing
	}
	var resource localResourceModel
	err := s.dbFor(ctx).Table("orders AS o").
		Select("r.id, r.email, r.password, r.two_factor_secret, r.app_password, r.status").
		Joins("JOIN gmail_resources AS r ON r.id = o.gmail_resource_id").
		Where("o.order_no = ? AND o.product_type = ? AND o.service_mode = ?", orderNo, "gmail", "purchase").
		Take(&resource).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrLocalResourceMissing
	}
	if err != nil {
		return nil, fmt.Errorf("load local Gmail purchase: %w", err)
	}
	return &tradeapp.GmailPurchaseDelivery{
		ResourceID: resource.ID, Email: resource.Email, Password: resource.Password,
		TwoFactorSecret: resource.TwoFactorSecret, AppPassword: resource.AppPassword,
	}, nil
}

func (s *Service) SetLocalResourceEnabled(ctx context.Context, resourceID uint, enabled bool) error {
	if resourceID == 0 {
		return ErrLocalResourceMissing
	}
	return s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		var item localResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&item, resourceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLocalResourceMissing
			}
			return err
		}
		if item.Status == LocalResourceLeased || item.Status == LocalResourceSold {
			return ErrLocalResourceBusy
		}
		next := LocalResourceDisabled
		if enabled {
			next = LocalResourceAvailable
		}
		if item.Status == next {
			return nil
		}
		return tx.Model(&localResourceModel{}).Where("id = ?", item.ID).Updates(map[string]any{
			"status": next, "last_safe_error": "",
		}).Error
	})
}

func isLocalResourceStatus(status string) bool {
	switch status {
	case LocalResourceAvailable, LocalResourceDisabled, LocalResourceLeased, LocalResourceSold:
		return true
	default:
		return false
	}
}
