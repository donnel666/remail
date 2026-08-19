package kitesim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/donnel666/remail/internal/platform"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const upstreamSettingsID = 1

var (
	ErrUpstreamNotConfigured = errors.New("kitesim: upstream account not configured")
	ErrCardNotConfigured     = errors.New("kitesim: credit card not configured")
	ErrOperationBusy         = errors.New("kitesim: operation already queued")
	errRefreshSuperseded     = errors.New("kitesim: upstream refresh superseded")
)

type upstreamSettingsModel struct {
	ID              uint8      `gorm:"column:id;primaryKey"`
	AccountID       *uint      `gorm:"column:account_id"`
	CardData        jsonText   `gorm:"column:card_profile;type:json"`
	CardBrand       string     `gorm:"column:card_brand"`
	CardLast4       string     `gorm:"column:card_last4"`
	CardExpiryMonth int        `gorm:"column:card_expiry_month"`
	CardExpiryYear  int        `gorm:"column:card_expiry_year"`
	CardRevision    uint64     `gorm:"column:card_revision"`
	Balance         string     `gorm:"column:balance"`
	BalanceUpdated  *time.Time `gorm:"column:balance_updated_at"`
	RefreshStatus   string     `gorm:"column:refresh_status"`
	RefreshQueued   *time.Time `gorm:"column:refresh_queued_at"`
	RefreshStarted  *time.Time `gorm:"column:refresh_started_at"`
	RefreshFinished *time.Time `gorm:"column:refresh_finished_at"`
	RefreshAttempts int        `gorm:"column:refresh_attempts"`
	LastSafeError   string     `gorm:"column:last_safe_error"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
}

func (upstreamSettingsModel) TableName() string { return "kitesim_upstream_settings" }

type productModel struct {
	ID             uint      `gorm:"column:id;primaryKey;autoIncrement"`
	CountryCode    string    `gorm:"column:country_code;uniqueIndex:uk_kitesim_products_country_package"`
	PackageID      string    `gorm:"column:package_id;uniqueIndex:uk_kitesim_products_country_package"`
	DurationType   int       `gorm:"column:duration_type"`
	DurationValue  int       `gorm:"column:duration_value"`
	Currency       string    `gorm:"column:currency"`
	BuyPrice       string    `gorm:"column:buy_price"`
	OriginalPrice  string    `gorm:"column:original_price"`
	AutoRenewPrice string    `gorm:"column:auto_renew_price"`
	Active         bool      `gorm:"column:active"`
	RawPayload     jsonText  `gorm:"column:raw_payload;type:json"`
	LastSeenAt     time.Time `gorm:"column:last_seen_at"`
	CreatedAt      time.Time `gorm:"column:created_at"`
	UpdatedAt      time.Time `gorm:"column:updated_at"`
}

func (productModel) TableName() string { return "kitesim_products" }

type CardProfile struct {
	Number       string `json:"number"`
	ExpiryMonth  int    `json:"expiryMonth"`
	ExpiryYear   int    `json:"expiryYear"`
	Holder       string `json:"holder"`
	BillingEmail string `json:"billingEmail"`
	FirstName    string `json:"firstName"`
	LastName     string `json:"lastName"`
	Phone        string `json:"phone"`
	Country      string `json:"country"`
	City         string `json:"city"`
	Address      string `json:"address"`
}

const (
	kitesimDefaultFirstName = "noreal"
	kitesimDefaultLastName  = "name"
	kitesimDefaultPhone     = "6505438765"
	kitesimDefaultCountry   = "US"
	kitesimDefaultCity      = "Mountain View"
	kitesimDefaultAddress   = "1295 Charleston Rd"
)

func applyKitesimCardDefaults(card *CardProfile, billingEmail string) {
	if strings.TrimSpace(card.BillingEmail) == "" {
		card.BillingEmail = strings.TrimSpace(billingEmail)
	}
	if strings.TrimSpace(card.FirstName) == "" {
		card.FirstName = kitesimDefaultFirstName
	}
	if strings.TrimSpace(card.LastName) == "" {
		card.LastName = kitesimDefaultLastName
	}
	if strings.TrimSpace(card.Phone) == "" {
		card.Phone = kitesimDefaultPhone
	}
	if strings.TrimSpace(card.Country) == "" {
		card.Country = kitesimDefaultCountry
	}
	if strings.TrimSpace(card.City) == "" {
		card.City = kitesimDefaultCity
	}
	if strings.TrimSpace(card.Address) == "" {
		card.Address = kitesimDefaultAddress
	}
}

type UpstreamConfigUpdate struct {
	AccountID uint
	Card      *CardProfile
	ClearCard bool
}

type UpstreamAccountItem struct {
	ID             uint           `json:"id"`
	Account        string         `json:"account"`
	TokenAvailable bool           `json:"tokenAvailable"`
	SyncStatus     SyncTaskStatus `json:"syncStatus"`
	LastSyncedAt   *time.Time     `json:"lastSyncedAt,omitempty"`
}

type ProductItem struct {
	ID             uint      `json:"id"`
	CountryCode    string    `json:"countryCode"`
	PackageID      string    `json:"packageId"`
	DurationType   int       `json:"durationType"`
	DurationValue  int       `json:"durationValue"`
	Currency       string    `json:"currency"`
	BuyPrice       string    `json:"buyPrice"`
	OriginalPrice  string    `json:"originalPrice"`
	AutoRenewPrice string    `json:"autoRenewPrice"`
	Active         bool      `json:"active"`
	LastSeenAt     time.Time `json:"lastSeenAt"`
}

type UpstreamView struct {
	AccountID         *uint                 `json:"accountId"`
	Account           string                `json:"account,omitempty"`
	Accounts          []UpstreamAccountItem `json:"accounts"`
	CardConfigured    bool                  `json:"cardConfigured"`
	CardBrand         string                `json:"cardBrand,omitempty"`
	CardLast4         string                `json:"cardLast4,omitempty"`
	CardExpiryMonth   int                   `json:"cardExpiryMonth,omitempty"`
	CardExpiryYear    int                   `json:"cardExpiryYear,omitempty"`
	Balance           string                `json:"balance"`
	BalanceUpdatedAt  *time.Time            `json:"balanceUpdatedAt,omitempty"`
	RefreshStatus     SyncTaskStatus        `json:"refreshStatus"`
	RefreshQueuedAt   *time.Time            `json:"refreshQueuedAt,omitempty"`
	RefreshStartedAt  *time.Time            `json:"refreshStartedAt,omitempty"`
	RefreshFinishedAt *time.Time            `json:"refreshFinishedAt,omitempty"`
	RefreshAttempts   int                   `json:"refreshAttempts"`
	LastSafeError     string                `json:"lastSafeError,omitempty"`
	Products          []ProductItem         `json:"products"`
	Operations        []OperationItem       `json:"operations"`
}

func (s *Service) GetUpstream(ctx context.Context) (*UpstreamView, error) {
	settings, err := s.loadUpstreamSettings(ctx)
	if err != nil {
		return nil, err
	}
	type accountRow struct {
		ID             uint       `gorm:"column:id"`
		Account        string     `gorm:"column:account"`
		TokenAvailable bool       `gorm:"column:token_available"`
		SyncStatus     string     `gorm:"column:sync_status"`
		LastSyncedAt   *time.Time `gorm:"column:last_synced_at"`
	}
	var accounts []accountRow
	if err := s.db.WithContext(ctx).Model(&accountModel{}).
		Select("id, account, CASE WHEN token IS NOT NULL AND LENGTH(token) > 0 THEN 1 ELSE 0 END AS token_available, sync_status, last_synced_at").
		Where("deleted_at IS NULL").
		Order("id DESC").Scan(&accounts).Error; err != nil {
		return nil, fmt.Errorf("list Kitesim upstream accounts: %w", err)
	}
	accountItems := make([]UpstreamAccountItem, len(accounts))
	accountName := ""
	for i := range accounts {
		status := SyncTaskStatus(accounts[i].SyncStatus)
		if status == "" {
			status = SyncTaskIdle
		}
		accountItems[i] = UpstreamAccountItem{
			ID: accounts[i].ID, Account: accounts[i].Account,
			TokenAvailable: accounts[i].TokenAvailable,
			SyncStatus:     status, LastSyncedAt: accounts[i].LastSyncedAt,
		}
		if settings.AccountID != nil && accounts[i].ID == *settings.AccountID {
			accountName = accounts[i].Account
		}
	}
	productItems, err := s.ListProducts(ctx)
	if err != nil {
		return nil, err
	}
	operations, err := s.listOperationViews(ctx, 20)
	if err != nil {
		return nil, err
	}
	refreshStatus := SyncTaskStatus(settings.RefreshStatus)
	if refreshStatus == "" {
		refreshStatus = SyncTaskIdle
	}
	return &UpstreamView{
		AccountID: settings.AccountID, Account: accountName, Accounts: accountItems,
		CardConfigured: len(settings.CardData) > 0,
		CardBrand:      settings.CardBrand, CardLast4: settings.CardLast4,
		CardExpiryMonth: settings.CardExpiryMonth, CardExpiryYear: settings.CardExpiryYear,
		Balance: normalizedDecimal(settings.Balance), BalanceUpdatedAt: settings.BalanceUpdated,
		RefreshStatus: refreshStatus, RefreshQueuedAt: settings.RefreshQueued,
		RefreshStartedAt: settings.RefreshStarted, RefreshFinishedAt: settings.RefreshFinished,
		RefreshAttempts: settings.RefreshAttempts, LastSafeError: settings.LastSafeError,
		Products: productItems, Operations: operations,
	}, nil
}

func (s *Service) ListProducts(ctx context.Context) ([]ProductItem, error) {
	var products []productModel
	if err := s.db.WithContext(ctx).Order("active DESC, country_code ASC, buy_price ASC, id ASC").Find(&products).Error; err != nil {
		return nil, fmt.Errorf("list Kitesim products: %w", err)
	}
	items := make([]ProductItem, len(products))
	for i := range products {
		items[i] = productView(products[i])
	}
	return items, nil
}

func productView(product productModel) ProductItem {
	return ProductItem{
		ID: product.ID, CountryCode: product.CountryCode, PackageID: product.PackageID,
		DurationType: product.DurationType, DurationValue: product.DurationValue,
		Currency: product.Currency, BuyPrice: normalizedDecimal(product.BuyPrice),
		OriginalPrice:  normalizedDecimal(product.OriginalPrice),
		AutoRenewPrice: normalizedDecimal(product.AutoRenewPrice),
		Active:         product.Active, LastSeenAt: product.LastSeenAt,
	}
}

func (s *Service) SaveUpstream(ctx context.Context, update UpstreamConfigUpdate, meta MutationMeta) error {
	if update.AccountID == 0 {
		return ErrInvalidInput
	}
	updates := map[string]any{"account_id": update.AccountID}
	if update.ClearCard {
		updates["card_profile"] = nil
		updates["card_brand"] = ""
		updates["card_last4"] = ""
		updates["card_expiry_month"] = 0
		updates["card_expiry_year"] = 0
		updates["card_revision"] = gorm.Expr("card_revision + 1")
	}
	var card *CardProfile
	if update.Card != nil {
		cardValue := *update.Card
		card = &cardValue
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account accountModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id, account").Where("id = ? AND deleted_at IS NULL", update.AccountID).First(&account).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAccountMissing
			}
			return fmt.Errorf("load Kitesim upstream account: %w", err)
		}
		if card != nil {
			applyKitesimCardDefaults(card, account.Account)
			brand, err := normalizeCard(card, s.now())
			if err != nil {
				return err
			}
			encoded, err := json.Marshal(card)
			if err != nil {
				return err
			}
			updates["card_profile"] = jsonText(encoded)
			updates["card_brand"] = brand
			updates["card_last4"] = card.Number[len(card.Number)-4:]
			updates["card_expiry_month"] = card.ExpiryMonth
			updates["card_expiry_year"] = card.ExpiryYear
			updates["card_revision"] = gorm.Expr("card_revision + 1")
		}
		if err := ensureUpstreamSettings(tx); err != nil {
			return err
		}
		var current upstreamSettingsModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, upstreamSettingsID).Error; err != nil {
			return fmt.Errorf("lock Kitesim upstream settings: %w", err)
		}
		if current.AccountID == nil || *current.AccountID != update.AccountID {
			updates["balance"] = "0"
			updates["balance_updated_at"] = nil
			updates["refresh_status"] = SyncTaskIdle
			updates["refresh_queued_at"] = nil
			updates["refresh_started_at"] = nil
			updates["refresh_finished_at"] = nil
			updates["refresh_attempts"] = 0
			updates["last_safe_error"] = ""
		}
		if err := tx.Model(&upstreamSettingsModel{}).Where("id = ?", upstreamSettingsID).Updates(updates).Error; err != nil {
			return fmt.Errorf("save Kitesim upstream settings: %w", err)
		}
		return s.createAudit(
			platform.WithGormTx(ctx, tx), meta, "kitesim.upstream.settings.update",
			"kitesim_upstream", "1", "updated Kitesim upstream account and card configuration",
		)
	})
}

func (s *Service) QueueUpstreamRefresh(ctx context.Context, meta MutationMeta) (*SyncTaskView, error) {
	queue, ok := s.queue.(UpstreamRefreshQueue)
	if !ok || queue == nil {
		return nil, errors.New("kitesim: task queue unavailable")
	}
	var settings upstreamSettingsModel
	shouldEnqueue := false
	now := s.now().UTC().Truncate(time.Millisecond)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ensureUpstreamSettings(tx); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&settings, upstreamSettingsID).Error; err != nil {
			return fmt.Errorf("load Kitesim upstream settings: %w", err)
		}
		if settings.AccountID == nil || *settings.AccountID == 0 {
			return ErrUpstreamNotConfigured
		}
		switch SyncTaskStatus(settings.RefreshStatus) {
		case SyncTaskRunning:
			return nil
		case SyncTaskQueued:
			shouldEnqueue = true
			return nil
		}
		if err := tx.Model(&upstreamSettingsModel{}).Where("id = ?", upstreamSettingsID).Updates(map[string]any{
			"refresh_status": SyncTaskQueued, "refresh_queued_at": now,
			"refresh_started_at": nil, "refresh_finished_at": nil,
			"refresh_attempts": 0, "last_safe_error": "",
		}).Error; err != nil {
			return fmt.Errorf("queue Kitesim upstream refresh: %w", err)
		}
		settings.RefreshStatus, settings.RefreshQueued = string(SyncTaskQueued), &now
		settings.RefreshStarted, settings.RefreshFinished = nil, nil
		settings.RefreshAttempts = 0
		shouldEnqueue = true
		return s.createAudit(platform.WithGormTx(ctx, tx), meta, "kitesim.upstream.refresh", "kitesim_upstream", "1", "queued Kitesim balance and product refresh")
	})
	if err != nil {
		return nil, err
	}
	if shouldEnqueue {
		_, _ = queue.EnqueueUpstreamRefresh(ctx)
	}
	return &SyncTaskView{
		AccountID: *settings.AccountID, Status: SyncTaskStatus(settings.RefreshStatus),
		QueuedAt: settings.RefreshQueued, StartedAt: settings.RefreshStarted,
		FinishedAt: settings.RefreshFinished, Attempts: settings.RefreshAttempts,
	}, nil
}

func (s *Service) processUpstreamRefresh(ctx context.Context, claim upstreamRefreshClaim) error {
	if claim.AccountID == 0 || claim.StartedAt.IsZero() {
		return ErrUpstreamNotConfigured
	}
	var account accountModel
	if err := s.db.WithContext(ctx).Where("id = ? AND deleted_at IS NULL", claim.AccountID).First(&account).Error; err != nil {
		return fmt.Errorf("load Kitesim upstream account: %w", err)
	}
	var (
		token          string
		balance        string
		packages       []NumberPackage
		tokenRefreshed bool
	)
	err := s.withAuthClient(ctx, account.Account, func(client *Client) error {
		var err error
		token, balance, tokenRefreshed, err = s.authenticatedBalance(ctx, client, account)
		if err != nil {
			return err
		}
		countries, err := client.PhoneCountries(ctx, token)
		if err != nil {
			return err
		}
		for _, country := range countries {
			items, err := client.NumberPackages(ctx, token, country, "")
			if err != nil {
				return err
			}
			for i := range items {
				if strings.TrimSpace(string(items[i].CountryCode)) == "" {
					items[i].CountryCode = stringValue(country)
				}
			}
			packages = append(packages, items...)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(packages) == 0 {
		return errors.New("kitesim: upstream product catalog is empty")
	}
	validPackages := 0
	for _, item := range packages {
		if strings.TrimSpace(string(item.CountryCode)) != "" && strings.TrimSpace(string(item.ID)) != "" {
			validPackages++
		}
	}
	if validPackages == 0 {
		return errors.New("kitesim: upstream product catalog has no valid products")
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&productModel{}).Where("active = ?", true).Update("active", false).Error; err != nil {
			return fmt.Errorf("retire Kitesim products: %w", err)
		}
		for _, item := range packages {
			countryCode := strings.ToUpper(strings.TrimSpace(string(item.CountryCode)))
			packageID := strings.TrimSpace(string(item.ID))
			if countryCode == "" || packageID == "" {
				continue
			}
			buyPrice, err := upstreamDecimal(item.BuyPrice)
			if err != nil {
				return fmt.Errorf("parse Kitesim buy price: %w", err)
			}
			originalPrice, err := upstreamDecimal(item.OriginalPrice)
			if err != nil {
				return fmt.Errorf("parse Kitesim original price: %w", err)
			}
			autoRenewPrice, err := upstreamDecimal(item.AutoRenewPrice)
			if err != nil {
				return fmt.Errorf("parse Kitesim auto-renew price: %w", err)
			}
			raw := jsonText(item.RawPayload)
			if len(raw) == 0 {
				raw = "{}"
			}
			model := productModel{
				CountryCode: countryCode, PackageID: packageID,
				DurationType: item.DurationType, DurationValue: item.DurationValue,
				Currency: "USD", BuyPrice: buyPrice,
				OriginalPrice:  originalPrice,
				AutoRenewPrice: autoRenewPrice,
				Active:         true, RawPayload: raw, LastSeenAt: now,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "country_code"}, {Name: "package_id"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"duration_type", "duration_value", "currency", "buy_price", "original_price",
					"auto_renew_price", "active", "raw_payload", "last_seen_at", "updated_at",
				}),
			}).Create(&model).Error; err != nil {
				return fmt.Errorf("upsert Kitesim product: %w", err)
			}
		}
		updates := map[string]any{
			"balance": normalizedDecimal(balance), "balance_updated_at": now,
			"refresh_status": SyncTaskSucceeded, "refresh_finished_at": now,
			"last_safe_error": "",
		}
		result := tx.Model(&upstreamSettingsModel{}).
			Where(
				"id = ? AND account_id = ? AND refresh_status = ? AND refresh_started_at = ?",
				upstreamSettingsID,
				claim.AccountID,
				SyncTaskRunning,
				claim.StartedAt,
			).
			Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("finish Kitesim upstream refresh: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return errRefreshSuperseded
		}
		if tokenRefreshed {
			if err := saveRefreshedAccountToken(tx, account, token, now); err != nil {
				return fmt.Errorf("save Kitesim upstream token: %w", err)
			}
		}
		return nil
	})
}

func (s *Service) authenticatedBalance(ctx context.Context, client *Client, account accountModel) (string, string, bool, error) {
	token := account.Token
	if strings.TrimSpace(token) != "" {
		balance, err := client.Balance(ctx, token)
		if err == nil {
			return token, balance, false, nil
		}
		if isProxyFailure(err) || ctx.Err() != nil {
			return "", "", false, err
		}
	}
	token, err := client.Login(ctx, account.Account, account.Password)
	if err != nil {
		return "", "", false, err
	}
	balance, err := client.Balance(ctx, token)
	return token, balance, true, err
}

func (s *Service) authenticateOperationClient(ctx context.Context, client *Client, account accountModel) (string, error) {
	token, balance, refreshed, err := s.authenticatedBalance(ctx, client, account)
	if err != nil {
		return "", err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	return token, s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if refreshed {
			if err := saveRefreshedAccountToken(tx, account, token, now); err != nil {
				return fmt.Errorf("save Kitesim upstream token: %w", err)
			}
		}
		if err := tx.Model(&upstreamSettingsModel{}).Where("id = ? AND account_id = ?", upstreamSettingsID, account.ID).Updates(map[string]any{
			"balance": normalizedDecimal(balance), "balance_updated_at": now,
		}).Error; err != nil {
			return fmt.Errorf("save Kitesim upstream balance: %w", err)
		}
		return nil
	})
}

func saveRefreshedAccountToken(tx *gorm.DB, account accountModel, token string, now time.Time) error {
	return tx.Model(&accountModel{}).
		Where(
			"id = ? AND deleted_at IS NULL AND password = ? AND COALESCE(token, '') = ?",
			account.ID,
			account.Password,
			account.Token,
		).
		Updates(map[string]any{"token": token, "token_updated_at": now}).Error
}

func (s *Service) loadUpstreamSettings(ctx context.Context) (*upstreamSettingsModel, error) {
	if err := ensureUpstreamSettings(s.db.WithContext(ctx)); err != nil {
		return nil, err
	}
	var settings upstreamSettingsModel
	if err := s.db.WithContext(ctx).First(&settings, upstreamSettingsID).Error; err != nil {
		return nil, fmt.Errorf("load Kitesim upstream settings: %w", err)
	}
	return &settings, nil
}

func ensureUpstreamSettings(db *gorm.DB) error {
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&upstreamSettingsModel{
		ID: upstreamSettingsID, Balance: "0", RefreshStatus: string(SyncTaskIdle),
	}).Error; err != nil {
		return fmt.Errorf("initialize Kitesim upstream settings: %w", err)
	}
	return nil
}

func normalizeCard(card *CardProfile, now time.Time) (string, error) {
	if card == nil {
		return "", ErrInvalidInput
	}
	card.Number = digitsOnly(card.Number)
	card.Holder = strings.TrimSpace(card.Holder)
	card.BillingEmail = strings.TrimSpace(card.BillingEmail)
	card.FirstName = strings.TrimSpace(card.FirstName)
	card.LastName = strings.TrimSpace(card.LastName)
	card.Phone = strings.TrimSpace(card.Phone)
	card.Country = strings.ToUpper(strings.TrimSpace(card.Country))
	card.City = strings.TrimSpace(card.City)
	card.Address = strings.TrimSpace(card.Address)
	if len(card.Number) < 13 || len(card.Number) > 19 || !validLuhn(card.Number) ||
		card.ExpiryMonth < 1 || card.ExpiryMonth > 12 || card.ExpiryYear < 2000 || card.ExpiryYear > 2200 ||
		card.ExpiryYear < now.Year() || card.ExpiryYear == now.Year() && card.ExpiryMonth < int(now.Month()) ||
		!safeField(card.Holder, 120) || !safeField(card.FirstName, 80) || !safeField(card.LastName, 80) ||
		!safeField(card.Phone, 40) || !safeField(card.City, 120) || !safeField(card.Address, 300) ||
		len(card.Country) != 2 || !asciiLetters(card.Country) {
		return "", ErrInvalidInput
	}
	parsed, err := mail.ParseAddress(card.BillingEmail)
	if err != nil || parsed.Address != card.BillingEmail || len(card.BillingEmail) > 320 {
		return "", ErrInvalidInput
	}
	return cardBrand(card.Number), nil
}

func safeField(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	return !strings.ContainsFunc(value, unicode.IsControl)
}

func asciiLetters(value string) bool {
	for _, char := range value {
		if char < 'A' || char > 'Z' {
			return false
		}
	}
	return true
}

func digitsOnly(value string) string {
	var builder strings.Builder
	for _, char := range value {
		if char >= '0' && char <= '9' {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

func validLuhn(value string) bool {
	sum, double := 0, false
	for i := len(value) - 1; i >= 0; i-- {
		digit := int(value[i] - '0')
		if double {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
		double = !double
	}
	return sum > 0 && sum%10 == 0
}

func cardBrand(number string) string {
	switch {
	case strings.HasPrefix(number, "4"):
		return "Visa"
	case strings.HasPrefix(number, "34"), strings.HasPrefix(number, "37"):
		return "American Express"
	case len(number) >= 2:
		prefix, _ := strconv.Atoi(number[:2])
		if prefix >= 51 && prefix <= 55 || prefix >= 22 && prefix <= 27 {
			return "Mastercard"
		}
	}
	return "Card"
}

func normalizedDecimal(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "0"
	}
	parsed, err := decimal.NewFromString(value)
	if err != nil {
		return value
	}
	return parsed.String()
}

func upstreamDecimal(value stringValue) (string, error) {
	raw := strings.TrimSpace(string(value))
	if raw == "" {
		return "0", nil
	}
	parsed, err := decimal.NewFromString(raw)
	if err != nil {
		return "", errors.New("kitesim: invalid decimal value")
	}
	return parsed.String(), nil
}
