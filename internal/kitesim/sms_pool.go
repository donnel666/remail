package kitesim

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrSMSPhoneUnavailable       = errors.New("kitesim: SMS phone temporarily unavailable")
	ErrSMSPhoneBindingConflict   = errors.New("kitesim: SMS phone binding conflict")
	ErrSMSPhoneBoundUnavailable  = errors.New("kitesim: permanently bound SMS phone is unavailable")
	ErrSMSPhoneNumberAmbiguous   = errors.New("kitesim: SMS phone number matches multiple active phones")
	ErrSMSPhoneSuffixAmbiguous   = errors.New("kitesim: SMS phone suffix matches multiple active phones")
	ErrSMSPhoneExclusive         = errors.New("kitesim: SMS phone is exclusively assigned")
	ErrSMSPhoneBlacklisted       = errors.New("kitesim: SMS phone is in the blacklist")
	ErrSMSReservationNotFound    = errors.New("kitesim: SMS reservation not found")
	ErrSMSChallengeOwnerConflict = errors.New("kitesim: SMS challenge owner key conflict")
	ErrSMSChallengeInactive      = errors.New("kitesim: SMS challenge is no longer active")
	ErrAppleSMSMessageNotFound   = errors.New("kitesim: Apple SMS message not found")
)

const smsConsumerICloud = "icloud"

type phoneBindingModel struct {
	ID           uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	PhoneID      uint      `gorm:"column:phone_id"`
	ConsumerType string    `gorm:"column:consumer_type"`
	ConsumerKey  string    `gorm:"column:consumer_key"`
	Source       string    `gorm:"column:source"`
	CreatedAt    time.Time `gorm:"column:created_at"`
}

func (phoneBindingModel) TableName() string { return "kitesim_phone_bindings" }

type phoneUsageEventModel struct {
	ID         uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	PhoneID    uint       `gorm:"column:phone_id"`
	Purpose    string     `gorm:"column:purpose"`
	Result     string     `gorm:"column:result"`
	CreatedAt  time.Time  `gorm:"column:created_at"`
	ResolvedAt *time.Time `gorm:"column:resolved_at"`
}

func (phoneUsageEventModel) TableName() string { return "kitesim_phone_usage_events" }

type SMSPhoneBinding struct {
	PhoneID     uint
	PhoneCode   string
	PhoneNumber string
	CountryCode string
	Source      string
}

type SMSPhoneUnavailableError struct {
	RetryAt     time.Time
	Reason      string
	Blacklisted bool
}

func (e *SMSPhoneUnavailableError) Error() string {
	if e == nil || strings.TrimSpace(e.Reason) == "" {
		return ErrSMSPhoneUnavailable.Error()
	}
	return fmt.Sprintf("%s: %s", ErrSMSPhoneUnavailable, e.Reason)
}

func (e *SMSPhoneUnavailableError) Unwrap() error { return ErrSMSPhoneUnavailable }

func (e *SMSPhoneUnavailableError) Is(target error) bool {
	return e != nil && e.Blacklisted && target == ErrSMSPhoneBlacklisted
}

func SMSRetryAt(err error) (time.Time, bool) {
	var unavailable *SMSPhoneUnavailableError
	if errors.As(err, &unavailable) && !unavailable.RetryAt.IsZero() {
		return unavailable.RetryAt, true
	}
	return time.Time{}, false
}

func (s *Service) BindICloudSMSPhone(ctx context.Context, email, requestedNumber string) (SMSPhoneBinding, error) {
	if s == nil || s.db == nil {
		return SMSPhoneBinding{}, ErrSMSPhoneUnavailable
	}
	consumerKey := strings.ToLower(strings.TrimSpace(email))
	if consumerKey == "" {
		return SMSPhoneBinding{}, ErrInvalidInput
	}
	requestedDigits := phoneDigits(requestedNumber)
	now := s.now().UTC().Truncate(time.Millisecond)
	var binding SMSPhoneBinding
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, found, err := loadSMSBinding(tx, consumerKey, now)
		if err != nil {
			return err
		}
		if found {
			if requestedDigits != "" && !samePhoneDigits(current, requestedDigits) {
				return ErrSMSPhoneBindingConflict
			}
			binding = current
			return nil
		}

		var phone phoneModel
		source := "automatic"
		if requestedDigits != "" {
			source = "matched"
			var candidates []phoneModel
			err = tx.Select("id", "phone_code", "phone_number").
				Where("deleted_at IS NULL AND disabled_at IS NULL AND status = ?", int(PhoneActive)).
				Order("id ASC").Find(&candidates).Error
			if err != nil {
				return err
			}
			for _, candidate := range candidates {
				if samePhoneDigits(smsPhoneBinding(candidate, source), requestedDigits) {
					phone = candidate
					break
				}
			}
			if phone.ID == 0 {
				return ErrPhoneMissing
			}
			err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND deleted_at IS NULL AND disabled_at IS NULL AND status = ?", phone.ID, int(PhoneActive)).
				Take(&phone).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPhoneMissing
			}
		} else {
			phone, err = s.pickSMSPhone(tx, s.now().UTC().Truncate(time.Millisecond), consumerKey)
		}
		if err != nil {
			return err
		}
		if smsPhoneBlacklisted(phone, now) {
			return ErrSMSPhoneBlacklisted
		}
		if requestedDigits != "" {
			usage, loadErr := loadICloudPhoneUsage(tx, consumerKey)
			if loadErr != nil {
				return loadErr
			}
			if _, exclusive := usage.exclusive[phone.ID]; exclusive {
				return ErrSMSPhoneExclusive
			}
		}
		row := phoneBindingModel{PhoneID: phone.ID, ConsumerType: smsConsumerICloud, ConsumerKey: consumerKey, Source: source}
		if err = tx.Create(&row).Error; err != nil {
			if existing, found, loadErr := loadSMSBinding(tx, consumerKey, now); loadErr == nil && found {
				if requestedDigits != "" && !samePhoneDigits(existing, requestedDigits) {
					return ErrSMSPhoneBindingConflict
				}
				binding = existing
				return nil
			}
			return err
		}
		binding = smsPhoneBinding(phone, source)
		return nil
	})
	return binding, err
}

// RebindICloudSMSPhone updates the permanent iCloud phone assignment. The
// transaction-aware form is used by iCloud resource edits so the binding and
// resource metadata commit together.
func (s *Service) RebindICloudSMSPhone(ctx context.Context, email, requestedNumber string) (SMSPhoneBinding, error) {
	if s == nil || s.db == nil {
		return SMSPhoneBinding{}, ErrSMSPhoneUnavailable
	}
	var binding SMSPhoneBinding
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		binding, err = s.RebindICloudSMSPhoneTx(ctx, tx, email, requestedNumber)
		return err
	})
	return binding, err
}

// RebindICloudSMSPhoneByID updates the permanent iCloud phone assignment using
// the pool row selected by an administrator.  The number is still checked so
// a stale or tampered UI value cannot bind a different phone.
func (s *Service) RebindICloudSMSPhoneByID(ctx context.Context, email string, phoneID uint, requestedNumber string) (SMSPhoneBinding, error) {
	if s == nil || s.db == nil {
		return SMSPhoneBinding{}, ErrSMSPhoneUnavailable
	}
	var binding SMSPhoneBinding
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		binding, err = s.RebindICloudSMSPhoneByIDTx(ctx, tx, email, phoneID, requestedNumber)
		return err
	})
	return binding, err
}

// RebindICloudSMSPhoneTx is the transaction-aware rebinding primitive used by
// the iCloud resource edit command.
func (s *Service) RebindICloudSMSPhoneTx(ctx context.Context, tx *gorm.DB, email, requestedNumber string) (SMSPhoneBinding, error) {
	return s.rebindICloudSMSPhoneTx(ctx, tx, email, 0, requestedNumber, false)
}

// RebindICloudSMSPhoneByIDTx is the transaction-aware, exact-ID variant used
// by the administrator editor.
func (s *Service) RebindICloudSMSPhoneByIDTx(ctx context.Context, tx *gorm.DB, email string, phoneID uint, requestedNumber string) (SMSPhoneBinding, error) {
	return s.rebindICloudSMSPhoneTx(ctx, tx, email, phoneID, requestedNumber, true)
}

func (s *Service) rebindICloudSMSPhoneTx(ctx context.Context, tx *gorm.DB, email string, phoneID uint, requestedNumber string, usePhoneID bool) (SMSPhoneBinding, error) {
	if s == nil || tx == nil {
		return SMSPhoneBinding{}, ErrSMSPhoneUnavailable
	}
	consumerKey := strings.ToLower(strings.TrimSpace(email))
	requestedDigits := phoneDigits(requestedNumber)
	if consumerKey == "" || requestedDigits == "" || (usePhoneID && phoneID == 0) {
		return SMSPhoneBinding{}, ErrInvalidInput
	}
	now := s.now().UTC().Truncate(time.Millisecond)

	var row phoneBindingModel
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("consumer_type = ? AND consumer_key = ?", smsConsumerICloud, consumerKey).
		Take(&row).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return SMSPhoneBinding{}, err
	}
	found := err == nil

	var phone phoneModel
	if usePhoneID {
		err := tx.WithContext(ctx).Select("id, phone_code, phone_number, country_code, sms_blacklisted_until").
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND deleted_at IS NULL AND disabled_at IS NULL AND status = ?", phoneID, int(PhoneActive)).
			Take(&phone).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return SMSPhoneBinding{}, ErrPhoneMissing
		}
		if err != nil {
			return SMSPhoneBinding{}, err
		}
		if smsPhoneBlacklisted(phone, now) {
			return SMSPhoneBinding{}, ErrSMSPhoneBlacklisted
		}
		if !samePhoneDigits(smsPhoneBinding(phone, "matched"), requestedDigits) {
			return SMSPhoneBinding{}, ErrInvalidInput
		}
	} else {
		// The compatibility path deliberately reads only matching fields without
		// locking the whole active pool, then locks the one selected row below.
		var candidates []phoneModel
		if err := tx.WithContext(ctx).Select("id, phone_code, phone_number, country_code").
			Where("deleted_at IS NULL AND disabled_at IS NULL AND status = ?", int(PhoneActive)).
			Order("id ASC").Find(&candidates).Error; err != nil {
			return SMSPhoneBinding{}, err
		}
		matches := make([]phoneModel, 0, 1)
		for _, candidate := range candidates {
			if samePhoneDigits(smsPhoneBinding(candidate, "matched"), requestedDigits) {
				matches = append(matches, candidate)
			}
		}
		switch len(matches) {
		case 0:
			return SMSPhoneBinding{}, ErrPhoneMissing
		case 1:
			phone = matches[0]
		default:
			return SMSPhoneBinding{}, ErrSMSPhoneNumberAmbiguous
		}
		err := tx.WithContext(ctx).Select("id, phone_code, phone_number, country_code, sms_blacklisted_until").
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND deleted_at IS NULL AND disabled_at IS NULL AND status = ?", phone.ID, int(PhoneActive)).
			Take(&phone).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return SMSPhoneBinding{}, ErrPhoneMissing
		}
		if err != nil {
			return SMSPhoneBinding{}, err
		}
	}
	if smsPhoneBlacklisted(phone, now) {
		return SMSPhoneBinding{}, ErrSMSPhoneBlacklisted
	}

	if !found {
		createdAt := time.Now().UTC()
		if s.now != nil {
			createdAt = s.now().UTC()
		}
		row = phoneBindingModel{
			PhoneID: phone.ID, ConsumerType: smsConsumerICloud, ConsumerKey: consumerKey,
			Source: "matched", CreatedAt: createdAt,
		}
		if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
			return SMSPhoneBinding{}, err
		}
	} else if row.PhoneID != phone.ID || row.Source != "matched" {
		if err := tx.WithContext(ctx).Model(&phoneBindingModel{}).Where("id = ?", row.ID).
			Updates(map[string]any{"phone_id": phone.ID, "source": "matched"}).Error; err != nil {
			return SMSPhoneBinding{}, err
		}
	}
	return smsPhoneBinding(phone, "matched"), nil
}

// BindICloudSMSPhoneBySuffix reuses an existing permanent binding or creates
// one only when the supplied suffix identifies exactly one active pool phone.
// An empty suffix never allocates a new phone.
func (s *Service) BindICloudSMSPhoneBySuffix(ctx context.Context, email, lastDigits string) (SMSPhoneBinding, error) {
	if s == nil || s.db == nil {
		return SMSPhoneBinding{}, ErrSMSPhoneUnavailable
	}
	consumerKey := strings.ToLower(strings.TrimSpace(email))
	suffix := phoneDigits(lastDigits)
	if consumerKey == "" {
		return SMSPhoneBinding{}, ErrInvalidInput
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	var binding SMSPhoneBinding
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, found, err := loadSMSBinding(tx, consumerKey, now)
		if err != nil {
			return err
		}
		if found {
			if suffix != "" && !strings.HasSuffix(phoneDigits(current.PhoneNumber), suffix) {
				return ErrSMSPhoneBindingConflict
			}
			binding = current
			return nil
		}
		if suffix == "" {
			return ErrPhoneMissing
		}

		var phones []phoneModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("deleted_at IS NULL AND disabled_at IS NULL AND status = ? AND phone_number LIKE ?", int(PhoneActive), "%"+suffix).
			Order("id ASC").Find(&phones).Error; err != nil {
			return err
		}
		matches := phones[:0]
		for _, phone := range phones {
			if strings.HasSuffix(phoneDigits(phone.PhoneNumber), suffix) {
				matches = append(matches, phone)
			}
		}
		switch len(matches) {
		case 0:
			return ErrPhoneMissing
		case 1:
		default:
			return ErrSMSPhoneSuffixAmbiguous
		}
		phone := matches[0]
		if smsPhoneBlacklisted(phone, now) {
			return ErrSMSPhoneBlacklisted
		}
		usage, err := loadICloudPhoneUsage(tx, consumerKey)
		if err != nil {
			return err
		}
		if _, exclusive := usage.exclusive[phone.ID]; exclusive {
			return ErrSMSPhoneExclusive
		}
		row := phoneBindingModel{PhoneID: phone.ID, ConsumerType: smsConsumerICloud, ConsumerKey: consumerKey, Source: "matched"}
		if err := tx.Create(&row).Error; err != nil {
			if existing, found, loadErr := loadSMSBinding(tx, consumerKey, now); loadErr == nil && found {
				if !strings.HasSuffix(phoneDigits(existing.PhoneNumber), suffix) {
					return ErrSMSPhoneBindingConflict
				}
				binding = existing
				return nil
			}
			return err
		}
		binding = smsPhoneBinding(phone, "matched")
		return nil
	})
	return binding, err
}

func loadSMSBinding(tx *gorm.DB, consumerKey string, now time.Time) (SMSPhoneBinding, bool, error) {
	var row struct {
		PhoneID          uint       `gorm:"column:phone_id"`
		PhoneCode        string     `gorm:"column:phone_code"`
		PhoneNumber      string     `gorm:"column:phone_number"`
		CountryCode      string     `gorm:"column:country_code"`
		Source           string     `gorm:"column:source"`
		Status           int        `gorm:"column:status"`
		DisabledAt       *time.Time `gorm:"column:disabled_at"`
		DeletedAt        *time.Time `gorm:"column:deleted_at"`
		BlacklistedUntil *time.Time `gorm:"column:sms_blacklisted_until"`
	}
	err := tx.Table("kitesim_phone_bindings AS b").
		Select("b.phone_id, p.phone_code, p.phone_number, p.country_code, b.source, p.status, p.disabled_at, p.deleted_at, p.sms_blacklisted_until").
		Joins("JOIN kitesim_phones AS p ON p.id = b.phone_id").
		Where("b.consumer_type = ? AND b.consumer_key = ?", smsConsumerICloud, consumerKey).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return SMSPhoneBinding{}, false, nil
	}
	if err != nil {
		return SMSPhoneBinding{}, false, err
	}
	binding := SMSPhoneBinding{PhoneID: row.PhoneID, PhoneCode: row.PhoneCode, PhoneNumber: row.PhoneNumber, CountryCode: row.CountryCode, Source: row.Source}
	if row.DeletedAt != nil || row.DisabledAt != nil || PhoneStatus(row.Status) != PhoneActive {
		return binding, true, ErrSMSPhoneBoundUnavailable
	}
	if row.BlacklistedUntil != nil && row.BlacklistedUntil.After(now) {
		return binding, true, &SMSPhoneUnavailableError{RetryAt: *row.BlacklistedUntil, Reason: "phone number is blacklisted", Blacklisted: true}
	}
	return binding, true, nil
}

func smsPhoneBlacklisted(phone phoneModel, now time.Time) bool {
	return phone.SMSBlacklistedUntil != nil && phone.SMSBlacklistedUntil.After(now)
}

type iCloudPhoneUsage struct {
	linked    map[uint]int64
	exclusive map[uint]struct{}
}

func loadICloudPhoneUsage(tx *gorm.DB, exemptEmail string) (iCloudPhoneUsage, error) {
	usage := iCloudPhoneUsage{linked: make(map[uint]int64), exclusive: make(map[uint]struct{})}
	if tx == nil || !tx.Migrator().HasTable("icloud_resources") ||
		!tx.Migrator().HasColumn("icloud_resources", "status") {
		return usage, nil
	}
	hasPhoneID := tx.Migrator().HasColumn("icloud_resources", "kitesim_phone_id")
	hasBoundPhone := tx.Migrator().HasColumn("icloud_resources", "bound_phone_number") &&
		tx.Migrator().HasTable("kitesim_phones")
	if !hasPhoneID && !hasBoundPhone {
		return usage, nil
	}
	hasAliasCount := tx.Migrator().HasColumn("icloud_resources", "alias_count")
	hasPrimaryEmail := tx.Migrator().HasColumn("icloud_resources", "primary_email")
	type claim struct {
		KitesimPhoneID   *uint  `gorm:"column:kitesim_phone_id"`
		BoundPhoneNumber string `gorm:"column:bound_phone_number"`
		PrimaryEmail     string `gorm:"column:primary_email"`
		AliasCount       uint   `gorm:"column:alias_count"`
	}
	columns := make([]string, 0, 4)
	if hasPhoneID {
		columns = append(columns, "kitesim_phone_id")
	}
	if hasBoundPhone {
		columns = append(columns, "bound_phone_number")
	}
	if hasPrimaryEmail {
		columns = append(columns, "primary_email")
	}
	if hasAliasCount {
		columns = append(columns, "COALESCE(alias_count, 0) AS alias_count")
	}
	var claims []claim
	if err := tx.Table("icloud_resources").Select(strings.Join(columns, ", ")).
		Where("status <> ?", "deleted").Find(&claims).Error; err != nil {
		return usage, err
	}

	var phones []phoneModel
	needsPhoneMatch := false
	for _, claim := range claims {
		if claim.KitesimPhoneID == nil && claim.BoundPhoneNumber != "" {
			needsPhoneMatch = true
			break
		}
	}
	if needsPhoneMatch {
		query := tx.Select("id", "phone_code", "phone_number")
		if tx.Migrator().HasColumn("kitesim_phones", "deleted_at") {
			query = query.Where("deleted_at IS NULL")
		}
		if err := query.Find(&phones).Error; err != nil {
			return usage, err
		}
	}
	exemptEmail = strings.TrimSpace(exemptEmail)
	for _, claim := range claims {
		phoneIDs := make(map[uint]struct{}, 1)
		if claim.KitesimPhoneID != nil {
			phoneIDs[*claim.KitesimPhoneID] = struct{}{}
		} else if claim.BoundPhoneNumber != "" {
			// ponytail: placeholders are transient; backfill phone IDs if this scan becomes hot.
			for _, phone := range phones {
				if sameICloudPhoneNumber(phone.PhoneCode, phone.PhoneNumber, claim.BoundPhoneNumber) {
					phoneIDs[phone.ID] = struct{}{}
				}
			}
		}
		for phoneID := range phoneIDs {
			usage.linked[phoneID]++
			if hasAliasCount && claim.AliasCount < platform.ICloudMaxAliases &&
				(exemptEmail == "" || !strings.EqualFold(strings.TrimSpace(claim.PrimaryEmail), exemptEmail)) {
				usage.exclusive[phoneID] = struct{}{}
			}
		}
	}
	return usage, nil
}

func sameICloudPhoneNumber(phoneCode, phoneNumber, requested string) bool {
	code, number := phoneNumberParts(phoneCode, phoneNumber)
	left, right := code+number, phoneDigits(requested)
	if left == "" || right == "" {
		return false
	}
	if len(left) < len(right) {
		left, right = right, left
	}
	return left == right || number == right || len(right) >= 7 && strings.HasSuffix(left, right)
}

func iCloudExclusivePhoneIDs(exclusive map[uint]struct{}) []uint {
	ids := make([]uint, 0, len(exclusive))
	for id := range exclusive {
		ids = append(ids, id)
	}
	return ids
}

func (s *Service) pickSMSPhone(tx *gorm.DB, now time.Time, exemptEmail string) (phoneModel, error) {
	limit := runtimeconfig.Int(runtimeconfig.ICloudPhoneHourlySMSLimitKey, 10, 1)
	windowStart := now.Add(-time.Hour)
	usage, err := loadICloudPhoneUsage(tx, exemptEmail)
	if err != nil {
		return phoneModel{}, err
	}
	query := `SELECT p.*
		FROM kitesim_phones AS p
		WHERE p.deleted_at IS NULL AND p.disabled_at IS NULL AND p.status = ?
		  AND (p.sms_blacklisted_until IS NULL OR p.sms_blacklisted_until <= ?)
		  AND (p.sms_cooldown_until IS NULL OR p.sms_cooldown_until <= ?)
		  AND (SELECT COUNT(*) FROM kitesim_phone_usage_events AS e
		       WHERE e.phone_id = p.id AND e.created_at > ?) < ?`
	args := []any{int(PhoneActive), now, now, windowStart, limit}
	if excluded := iCloudExclusivePhoneIDs(usage.exclusive); len(excluded) > 0 {
		query += " AND p.id NOT IN ?"
		args = append(args, excluded)
	}
	query += `
		ORDER BY
		  (SELECT COUNT(*) FROM kitesim_phone_bindings AS b WHERE b.phone_id = p.id) ASC,
		  (SELECT COUNT(*) FROM kitesim_phone_usage_events AS e WHERE e.phone_id = p.id AND e.created_at > ?) ASC,
		  COALESCE(p.sms_last_used_at, p.created_at) ASC,
		  p.id ASC
		LIMIT 1`
	args = append(args, windowStart)
	if tx.Name() == "mysql" {
		query += " FOR UPDATE"
	}
	var phone phoneModel
	err = tx.Raw(query, args...).Scan(&phone).Error
	if err != nil {
		return phone, err
	}
	if phone.ID == 0 {
		retryAt := s.nextSMSAvailability(tx, now, limit, usage.exclusive)
		return phone, &SMSPhoneUnavailableError{RetryAt: retryAt, Reason: "all active phone numbers are cooling down, rate-limited, or blacklisted"}
	}
	return phone, nil
}

func (s *Service) nextSMSAvailability(tx *gorm.DB, now time.Time, limit int, exclusive map[uint]struct{}) time.Time {
	var phones []phoneModel
	if err := tx.Where("deleted_at IS NULL AND disabled_at IS NULL AND status = ?", int(PhoneActive)).Find(&phones).Error; err != nil || len(phones) == 0 {
		return now.Add(time.Minute)
	}
	windowStart := now.Add(-time.Hour)
	type usageRow struct {
		PhoneID uint      `gorm:"column:phone_id"`
		Count   int       `gorm:"column:usage_count"`
		Oldest  time.Time `gorm:"column:oldest_at"`
	}
	var usage []usageRow
	_ = tx.Table("kitesim_phone_usage_events").
		Select("phone_id, COUNT(*) AS usage_count, MIN(created_at) AS oldest_at").
		Where("created_at > ?", windowStart).Group("phone_id").Scan(&usage).Error
	byPhone := make(map[uint]usageRow, len(usage))
	for _, row := range usage {
		byPhone[row.PhoneID] = row
	}
	earliest := now.Add(24 * time.Hour)
	eligible := false
	for _, phone := range phones {
		if _, excluded := exclusive[phone.ID]; excluded {
			continue
		}
		eligible = true
		available := now
		if phone.SMSCooldownUntil != nil && phone.SMSCooldownUntil.After(available) {
			available = *phone.SMSCooldownUntil
		}
		if phone.SMSBlacklistedUntil != nil && phone.SMSBlacklistedUntil.After(available) {
			available = *phone.SMSBlacklistedUntil
		}
		if row := byPhone[phone.ID]; row.Count >= limit && !row.Oldest.IsZero() && row.Oldest.Add(time.Hour).After(available) {
			available = row.Oldest.Add(time.Hour)
		}
		if available.Before(earliest) {
			earliest = available
		}
	}
	if !eligible {
		return now.Add(time.Minute)
	}
	if !earliest.After(now) {
		return now.Add(30 * time.Second)
	}
	return earliest
}

func smsPhoneBinding(phone phoneModel, source string) SMSPhoneBinding {
	return SMSPhoneBinding{PhoneID: phone.ID, PhoneCode: strings.TrimSpace(phone.PhoneCode), PhoneNumber: strings.TrimSpace(phone.PhoneNumber), CountryCode: strings.ToUpper(strings.TrimSpace(phone.CountryCode)), Source: source}
}

func samePhoneDigits(binding SMSPhoneBinding, requested string) bool {
	code, number := phoneNumberParts(binding.PhoneCode, binding.PhoneNumber)
	return number == requested || code+number == requested
}

func formatPhoneNumber(phoneCode, phoneNumber string) string {
	code, number := phoneNumberParts(phoneCode, phoneNumber)
	if code == "" {
		return number
	}
	return "+" + code + " " + number
}

func phoneNumberParts(phoneCode, phoneNumber string) (string, string) {
	code := phoneDigits(phoneCode)
	number := phoneDigits(phoneNumber)
	// Kitesim mixes full numbers and local numbers; a repeated phoneCode belongs to the upstream full number.
	return code, strings.TrimPrefix(number, code)
}

func phoneDigits(value string) string {
	var result strings.Builder
	for _, char := range strings.TrimSpace(value) {
		if unicode.IsDigit(char) {
			result.WriteRune(char)
		}
	}
	return result.String()
}
