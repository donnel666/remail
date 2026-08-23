package icloud

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"gorm.io/gorm"
)

const (
	adminICloudResourceDefaultLimit = 20
	adminICloudResourceMaxLimit     = 100
)

var (
	ErrICloudResourceQuery          = errors.New("icloud: invalid resource query")
	ErrICloudResourceQueryTemporary = errors.New("icloud: resource query temporarily unavailable")
)

type AdminICloudResourceListFilter struct {
	Search        string     `json:"search,omitempty"`
	Status        string     `json:"status,omitempty"`
	ForSale       *bool      `json:"forSale,omitempty"`
	CreatedFrom   *time.Time `json:"createdFrom,omitempty"`
	CreatedTo     *time.Time `json:"createdTo,omitempty"`
	Offset        int        `json:"-"`
	Limit         int        `json:"-"`
	IncludeFacets *bool      `json:"-"`
	IncludeTotal  *bool      `json:"-"`
}

type AdminICloudOwnerView struct {
	ID        uint   `json:"id"`
	Email     string `json:"email"`
	Nickname  string `json:"nickname"`
	GroupName string `json:"groupName"`
	Role      string `json:"role"`
	Enabled   bool   `json:"enabled"`
}

type AdminICloudSessionView struct {
	Status          string     `json:"status"`
	Failures        uint8      `json:"failures"`
	CooldownUntil   *time.Time `json:"cooldownUntil"`
	NextKeepaliveAt *time.Time `json:"nextKeepaliveAt"`
	LastCheckedAt   *time.Time `json:"lastCheckedAt"`
	LastValidAt     *time.Time `json:"lastValidAt"`
}

type AdminICloudResourceView struct {
	ID                      uint                    `json:"id"`
	Version                 uint64                  `json:"version"`
	PrimaryEmail            string                  `json:"primaryEmail"`
	AccountRole             string                  `json:"accountRole"`
	FamilyPrimaryResourceID *uint                   `json:"familyPrimaryResourceId"`
	FamilyPrimaryEmail      string                  `json:"familyPrimaryEmail,omitempty"`
	FamilyChildCount        uint                    `json:"familyChildCount"`
	FamilyChildLimit        uint                    `json:"familyChildLimit"`
	FamilySyncStatus        string                  `json:"familySyncStatus"`
	FamilySyncedAt          *time.Time              `json:"familySyncedAt"`
	FamilySyncErrorCategory string                  `json:"familySyncErrorCategory,omitempty"`
	Region                  string                  `json:"region"`
	CountryCode             string                  `json:"countryCode"`
	ICloudOpened            bool                    `json:"icloudOpened"`
	BoundPhoneNumber        string                  `json:"boundPhoneNumber,omitempty"`
	BoundPhoneCountryCode   string                  `json:"boundPhoneCountryCode,omitempty"`
	BoundPhoneSource        string                  `json:"boundPhoneSource,omitempty"`
	KitesimPhoneID          *uint                   `json:"kitesimPhoneId"`
	FamilyInviteURL         string                  `json:"familyInviteUrl,omitempty"`
	SelectedForwardTo       string                  `json:"selectedForwardTo"`
	Owner                   AdminICloudOwnerView    `json:"owner"`
	Status                  string                  `json:"status"`
	ForSale                 bool                    `json:"forSale"`
	NewSession              *AdminICloudSessionView `json:"newSession"`
	OldSession              *AdminICloudSessionView `json:"oldSession"`
	AliasCount              uint                    `json:"aliasCount"`
	ExpireAt                time.Time               `json:"expireAt"`
	NextValidationAt        *time.Time              `json:"nextValidationAt"`
	NextProvisionAt         *time.Time              `json:"nextProvisionAt"`
	LastCheckedAt           *time.Time              `json:"lastCheckedAt"`
	LastValidAt             *time.Time              `json:"lastValidAt"`
	LastAliasSyncAt         *time.Time              `json:"lastAliasSyncAt"`
	LastAllocatedAt         *time.Time              `json:"lastAllocatedAt"`
	LastSafeError           *string                 `json:"lastSafeError"`
	CreatedAt               time.Time               `json:"createdAt"`
	UpdatedAt               time.Time               `json:"updatedAt"`
}

type AdminICloudResourceDetail struct {
	AdminICloudResourceView
	AliasLimit           uint                `json:"aliasLimit"`
	AliasRemaining       uint                `json:"aliasRemaining"`
	AliasProvisioning    bool                `json:"aliasProvisioning"`
	CredentialRevision   uint64              `json:"credentialRevision"`
	CredentialUpdatedAt  time.Time           `json:"credentialUpdatedAt"`
	ValidationGeneration uint64              `json:"validationGeneration"`
	ValidationFailures   uint8               `json:"validationFailures"`
	OnboardingTask       *OnboardingTaskView `json:"onboardingTask"`
	RefreshTask          *OnboardingTaskView `json:"refreshTask"`
}

type adminICloudResourceRow struct {
	ID                      uint       `gorm:"column:id"`
	Version                 uint64     `gorm:"column:version"`
	PrimaryEmail            string     `gorm:"column:primary_email"`
	AccountRole             string     `gorm:"column:account_role"`
	FamilyPrimaryResourceID *uint      `gorm:"column:family_primary_resource_id"`
	FamilyPrimaryEmail      string     `gorm:"column:family_primary_email"`
	FamilyChildCount        uint       `gorm:"column:family_child_count"`
	FamilySyncStatus        string     `gorm:"column:family_sync_status"`
	FamilySyncedAt          *time.Time `gorm:"column:family_synced_at"`
	FamilySyncErrorCategory string     `gorm:"column:family_sync_error_category"`
	Region                  string     `gorm:"column:region"`
	CountryCode             string     `gorm:"column:country_code"`
	ICloudOpened            bool       `gorm:"column:icloud_opened"`
	BoundPhoneNumber        string     `gorm:"column:bound_phone_number"`
	BoundPhoneCountryCode   string     `gorm:"column:bound_phone_country_code"`
	BoundPhoneSource        string     `gorm:"column:bound_phone_source"`
	KitesimPhoneID          *uint      `gorm:"column:kitesim_phone_id"`
	KitesimPhoneCode        string     `gorm:"column:kitesim_phone_code"`
	KitesimPhoneNumber      string     `gorm:"column:kitesim_phone_number"`
	FamilyInviteURL         string     `gorm:"column:family_invite_url"`
	SelectedForwardTo       string     `gorm:"column:selected_forward_to"`
	OwnerID                 uint       `gorm:"column:owner_id"`
	OwnerEmail              string     `gorm:"column:owner_email"`
	OwnerNickname           string     `gorm:"column:owner_nickname"`
	OwnerGroupName          string     `gorm:"column:owner_group_name"`
	OwnerRole               string     `gorm:"column:owner_role"`
	OwnerStatus             string     `gorm:"column:owner_status"`
	Status                  string     `gorm:"column:status"`
	ForSale                 bool       `gorm:"column:for_sale"`
	NewChannelID            *uint      `gorm:"column:new_channel_id"`
	NewSessionStatus        string     `gorm:"column:new_session_status"`
	NewSessionFailures      uint8      `gorm:"column:new_session_failures"`
	NewCooldownUntil        *time.Time `gorm:"column:new_cooldown_until"`
	NewNextKeepaliveAt      *time.Time `gorm:"column:new_next_keepalive_at"`
	NewLastCheckedAt        *time.Time `gorm:"column:new_last_checked_at"`
	NewLastValidAt          *time.Time `gorm:"column:new_last_valid_at"`
	OldChannelID            *uint      `gorm:"column:old_channel_id"`
	OldSessionStatus        string     `gorm:"column:old_session_status"`
	OldSessionFailures      uint8      `gorm:"column:old_session_failures"`
	OldCooldownUntil        *time.Time `gorm:"column:old_cooldown_until"`
	OldNextKeepaliveAt      *time.Time `gorm:"column:old_next_keepalive_at"`
	OldLastCheckedAt        *time.Time `gorm:"column:old_last_checked_at"`
	OldLastValidAt          *time.Time `gorm:"column:old_last_valid_at"`
	CredentialRevision      uint64     `gorm:"column:credential_revision"`
	CredentialUpdatedAt     time.Time  `gorm:"column:credential_updated_at"`
	ValidationGeneration    uint64     `gorm:"column:validation_generation"`
	ValidationFailures      uint8      `gorm:"column:validation_failures"`
	AliasCount              uint       `gorm:"column:alias_count"`
	AliasProvisionCandidate string     `gorm:"column:alias_provision_candidate"`
	AliasProvisionReconcile bool       `gorm:"column:alias_provision_reconcile"`
	ExpireAt                time.Time  `gorm:"column:expire_at"`
	NextValidationAt        *time.Time `gorm:"column:next_validation_at"`
	NextProvisionAt         *time.Time `gorm:"column:next_provision_at"`
	LastCheckedAt           *time.Time `gorm:"column:last_checked_at"`
	LastValidAt             *time.Time `gorm:"column:last_valid_at"`
	LastAliasSyncAt         *time.Time `gorm:"column:last_alias_sync_at"`
	LastAllocatedAt         *time.Time `gorm:"column:last_allocated_at"`
	LastSafeError           string     `gorm:"column:last_safe_error"`
	CreatedAt               time.Time  `gorm:"column:created_at"`
	UpdatedAt               time.Time  `gorm:"column:updated_at"`
}

const adminICloudResourceSelect = `
	ir.id, er.version, ir.primary_email, ir.account_role, ir.family_primary_resource_id,
	COALESCE(family_primary.primary_email, '') AS family_primary_email,
	ir.family_remote_member_count AS family_child_count,
	ir.family_sync_status, ir.family_synced_at, ir.family_sync_error_category,
	ir.region, ir.country_code, ir.icloud_opened, ir.bound_phone_number, ir.bound_phone_country_code,
		ir.bound_phone_source, ir.kitesim_phone_id,
		COALESCE(kp.phone_code, '') AS kitesim_phone_code,
		COALESCE(kp.phone_number, '') AS kitesim_phone_number,
		ir.family_invite_url,
	ir.selected_forward_to, er.owner_user_id AS owner_id,
	u.email AS owner_email, u.nickname AS owner_nickname,
	COALESCE(ug.name, '') AS owner_group_name, u.role AS owner_role,
	u.status AS owner_status, ir.status, ir.for_sale,
	new_ch.id AS new_channel_id, COALESCE(new_ch.session_status, '') AS new_session_status,
	COALESCE(new_ch.session_failures, 0) AS new_session_failures,
	new_ch.cooldown_until AS new_cooldown_until, new_ch.next_keepalive_at AS new_next_keepalive_at,
	new_ch.last_checked_at AS new_last_checked_at, new_ch.last_valid_at AS new_last_valid_at,
	old_ch.id AS old_channel_id, COALESCE(old_ch.session_status, '') AS old_session_status,
	COALESCE(old_ch.session_failures, 0) AS old_session_failures,
	old_ch.cooldown_until AS old_cooldown_until, old_ch.next_keepalive_at AS old_next_keepalive_at,
	old_ch.last_checked_at AS old_last_checked_at, old_ch.last_valid_at AS old_last_valid_at,
	ir.credential_revision, ir.credential_updated_at,
	ir.validation_generation, ir.validation_failures, ir.alias_count,
	ir.alias_provision_candidate, ir.alias_provision_reconcile, ir.expire_at,
	ir.next_validation_at, ir.next_provision_at, ir.last_checked_at,
	ir.last_valid_at,
	ir.last_alias_sync_at, ir.last_allocated_at, ir.last_safe_error,
	ir.created_at, ir.updated_at`

type AdminICloudStatusFacets struct {
	All        int64 `json:"all"`
	Pending    int64 `json:"pending"`
	Validating int64 `json:"validating"`
	Normal     int64 `json:"normal"`
	Abnormal   int64 `json:"abnormal"`
	Disabled   int64 `json:"disabled"`
	Deleted    int64 `json:"deleted"`
}

type AdminICloudBooleanFacets struct {
	All int64 `json:"all"`
	Yes int64 `json:"yes"`
	No  int64 `json:"no"`
}

type AdminICloudResourceFacets struct {
	Status  AdminICloudStatusFacets  `json:"status"`
	ForSale AdminICloudBooleanFacets `json:"forSale"`
}

type AdminICloudResourceList struct {
	Items              []AdminICloudResourceView `json:"items"`
	Total              int64                     `json:"total"`
	Offset             int                       `json:"offset"`
	Limit              int                       `json:"limit"`
	AliasLimit         int                       `json:"aliasLimit"`
	ForwardingSuffixes []string                  `json:"forwardingSuffixes"`
	Facets             AdminICloudResourceFacets `json:"facets"`
}

type AdminICloudAliasView struct {
	ID                uint       `json:"id"`
	AnonymousID       string     `json:"anonymousId"`
	Email             string     `json:"email"`
	Status            string     `json:"status"`
	Origin            string     `json:"origin"`
	ProviderCreatedAt *time.Time `json:"providerCreatedAt"`
	LastSeenAt        *time.Time `json:"lastSeenAt"`
	LastAllocatedAt   *time.Time `json:"lastAllocatedAt"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

type AdminICloudAliasList struct {
	Items  []AdminICloudAliasView `json:"items"`
	Total  int64                  `json:"total"`
	Offset int                    `json:"offset"`
	Limit  int                    `json:"limit"`
}

type adminICloudFilterIgnore struct {
	status  bool
	forSale bool
}

func (s *Service) ListAdminICloudResources(ctx context.Context, filter AdminICloudResourceListFilter) (*AdminICloudResourceList, error) {
	if s == nil || s.db == nil {
		return nil, ErrICloudResourceQueryTemporary
	}
	filter, err := normalizeAdminICloudResourceFilter(filter)
	if err != nil {
		return nil, err
	}

	var total int64
	if includeAdminICloudListSection(filter.IncludeTotal) {
		if err := s.applyAdminICloudResourceFilter(s.adminICloudResourceQuery(ctx), filter, adminICloudFilterIgnore{}).Count(&total).Error; err != nil {
			return nil, fmt.Errorf("count administrator iCloud resources: %w", ErrICloudResourceQueryTemporary)
		}
	}
	var rows []adminICloudResourceRow
	query := s.applyAdminICloudResourceFilter(s.adminICloudResourceViewQuery(ctx), filter, adminICloudFilterIgnore{})
	if err := query.Select(adminICloudResourceSelect).Order("ir.id DESC").Offset(filter.Offset).Limit(filter.Limit).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list administrator iCloud resources: %w", ErrICloudResourceQueryTemporary)
	}
	items := make([]AdminICloudResourceView, len(rows))
	for index, row := range rows {
		items[index] = adminICloudResourceView(row)
	}
	var facets AdminICloudResourceFacets
	if includeAdminICloudListSection(filter.IncludeFacets) {
		facets, err = s.adminICloudResourceFacets(ctx, filter)
		if err != nil {
			return nil, err
		}
	}
	forwardingSuffixes := append([]string{}, runtimeconfig.ICloudForwardingSuffixes(
		runtimeconfig.String(runtimeconfig.ICloudForwardingSuffixesKey, ""),
	)...)
	return &AdminICloudResourceList{
		Items: items, Total: total, Offset: filter.Offset, Limit: filter.Limit,
		AliasLimit: iCloudMaxAliases, ForwardingSuffixes: forwardingSuffixes, Facets: facets,
	}, nil
}

func (s *Service) GetAdminICloudResource(ctx context.Context, resourceID uint) (*AdminICloudResourceDetail, error) {
	if s == nil || s.db == nil {
		return nil, ErrICloudResourceQueryTemporary
	}
	if resourceID == 0 {
		return nil, ErrICloudResourceQuery
	}
	var row adminICloudResourceRow
	err := s.adminICloudResourceViewQuery(ctx).Where("ir.id = ?", resourceID).Select(adminICloudResourceSelect).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrICloudResourceNotFound
	}
	if err != nil {
		return nil, ErrICloudResourceQueryTemporary
	}
	remaining := uint(0)
	if row.AliasCount < iCloudMaxAliases {
		remaining = iCloudMaxAliases - row.AliasCount
	}
	detail := &AdminICloudResourceDetail{
		AdminICloudResourceView: adminICloudResourceView(row), AliasLimit: iCloudMaxAliases,
		AliasRemaining:     remaining,
		AliasProvisioning:  row.NextProvisionAt != nil || strings.TrimSpace(row.AliasProvisionCandidate) != "" || row.AliasProvisionReconcile,
		CredentialRevision: row.CredentialRevision, CredentialUpdatedAt: row.CredentialUpdatedAt,
		ValidationGeneration: row.ValidationGeneration, ValidationFailures: row.ValidationFailures,
	}
	var refresh iCloudOnboardingTaskModel
	var onboarding iCloudOnboardingTaskModel
	if err := s.db.WithContext(ctx).Where("task_kind = ? AND resource_id = ?", "onboarding", resourceID).Order("id DESC").Take(&onboarding).Error; err == nil {
		view := iCloudOnboardingTaskView(onboarding)
		views := []OnboardingTaskView{view}
		if err := s.populateICloudOnboardingFamilyEmails(ctx, views); err != nil {
			return nil, err
		}
		detail.OnboardingTask = &views[0]
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrICloudResourceQueryTemporary
	}
	if err := s.db.WithContext(ctx).Where("task_kind IN ? AND resource_id = ?", []string{"refresh", iCloudCookieRecoveryTaskKind}, resourceID).Order("id DESC").Take(&refresh).Error; err == nil {
		view := iCloudOnboardingTaskView(refresh)
		detail.RefreshTask = &view
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrICloudResourceQueryTemporary
	}
	return detail, nil
}

func adminICloudResourceView(row adminICloudResourceRow) AdminICloudResourceView {
	var safeError *string
	if value := strings.TrimSpace(row.LastSafeError); value != "" {
		safeError = &value
	}
	return AdminICloudResourceView{
		ID: row.ID, Version: row.Version, PrimaryEmail: row.PrimaryEmail,
		AccountRole: firstNonEmpty(row.AccountRole, "unknown"), FamilyPrimaryResourceID: row.FamilyPrimaryResourceID,
		FamilyPrimaryEmail: row.FamilyPrimaryEmail, FamilyChildCount: row.FamilyChildCount, FamilyChildLimit: iCloudFamilyChildLimit,
		FamilySyncStatus: row.FamilySyncStatus, FamilySyncedAt: row.FamilySyncedAt, FamilySyncErrorCategory: row.FamilySyncErrorCategory,
		Region: row.Region, CountryCode: row.CountryCode, ICloudOpened: row.ICloudOpened,
		BoundPhoneNumber: adminICloudBoundPhoneNumber(row), BoundPhoneCountryCode: row.BoundPhoneCountryCode,
		BoundPhoneSource: row.BoundPhoneSource, KitesimPhoneID: row.KitesimPhoneID, FamilyInviteURL: row.FamilyInviteURL,
		SelectedForwardTo: row.SelectedForwardTo,
		Owner:             AdminICloudOwnerView{ID: row.OwnerID, Email: row.OwnerEmail, Nickname: row.OwnerNickname, GroupName: row.OwnerGroupName, Role: row.OwnerRole, Enabled: row.OwnerStatus == "active"},
		Status:            row.Status, ForSale: row.ForSale,
		NewSession: adminICloudSessionView(row.NewChannelID, row.NewSessionStatus, row.NewSessionFailures, row.NewCooldownUntil, row.NewNextKeepaliveAt, row.NewLastCheckedAt, row.NewLastValidAt),
		OldSession: adminICloudSessionView(row.OldChannelID, row.OldSessionStatus, row.OldSessionFailures, row.OldCooldownUntil, row.OldNextKeepaliveAt, row.OldLastCheckedAt, row.OldLastValidAt),
		AliasCount: row.AliasCount, ExpireAt: row.ExpireAt, NextValidationAt: row.NextValidationAt,
		NextProvisionAt: row.NextProvisionAt, LastCheckedAt: row.LastCheckedAt, LastValidAt: row.LastValidAt,
		LastAliasSyncAt: row.LastAliasSyncAt,
		LastAllocatedAt: row.LastAllocatedAt, LastSafeError: safeError,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func adminICloudBoundPhoneNumber(row adminICloudResourceRow) string {
	number := onboardingPhoneDigits(row.KitesimPhoneNumber)
	if number == "" {
		return strings.TrimSpace(row.BoundPhoneNumber)
	}
	if code := onboardingPhoneDigits(row.KitesimPhoneCode); code != "" {
		return "+" + code + " " + strings.TrimPrefix(number, code)
	}
	return number
}

func adminICloudSessionView(id *uint, status string, failures uint8, cooldown, keepalive, checked, valid *time.Time) *AdminICloudSessionView {
	if id == nil || *id == 0 {
		return nil
	}
	return &AdminICloudSessionView{Status: normalizeICloudChannelStatus(status), Failures: failures, CooldownUntil: cooldown, NextKeepaliveAt: keepalive, LastCheckedAt: checked, LastValidAt: valid}
}

func normalizeICloudChannelStatus(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value != iCloudSessionValid && value != iCloudSessionInvalid {
		return iCloudSessionUnchecked
	}
	return value
}

func includeAdminICloudListSection(value *bool) bool { return value == nil || *value }

func normalizeAdminICloudResourceFilter(filter AdminICloudResourceListFilter) (AdminICloudResourceListFilter, error) {
	filter.Search = strings.ToLower(strings.TrimSpace(filter.Search))
	filter.Status = strings.ToLower(strings.TrimSpace(filter.Status))
	if len(filter.Search) > 320 || filter.Offset < 0 || filter.CreatedFrom != nil && filter.CreatedTo != nil && !filter.CreatedTo.After(*filter.CreatedFrom) {
		return filter, ErrICloudResourceQuery
	}
	if filter.Status != "" && !validAdminICloudResourceStatus(filter.Status) {
		return filter, ErrICloudResourceQuery
	}
	if filter.Limit == 0 {
		filter.Limit = adminICloudResourceDefaultLimit
	}
	if filter.Limit < 1 || filter.Limit > adminICloudResourceMaxLimit {
		return filter, ErrICloudResourceQuery
	}
	return filter, nil
}

func validAdminICloudResourceStatus(status string) bool {
	switch status {
	case iCloudResourcePending, iCloudResourceValidating, iCloudResourceNormal, iCloudResourceAbnormal, iCloudResourceDisabled, iCloudResourceDeleted:
		return true
	default:
		return false
	}
}

func (s *Service) adminICloudResourceQuery(ctx context.Context) *gorm.DB {
	return adminICloudResourceQueryDB(ctx, s.db)
}

func (s *Service) adminICloudResourceViewQuery(ctx context.Context) *gorm.DB {
	return s.adminICloudResourceQuery(ctx).
		Joins("LEFT JOIN kitesim_phones AS kp ON kp.id = ir.kitesim_phone_id")
}

func adminICloudResourceQueryDB(ctx context.Context, db *gorm.DB) *gorm.DB {
	return db.WithContext(ctx).Table("icloud_resources AS ir").
		Joins("JOIN email_resources AS er ON er.id = ir.id AND er.type = ?", "icloud").
		Joins("JOIN users AS u ON u.id = er.owner_user_id").
		Joins("LEFT JOIN user_groups AS ug ON ug.id = u.user_group_id").
		Joins("LEFT JOIN icloud_resources AS family_primary ON family_primary.id = ir.family_primary_resource_id").
		Joins("LEFT JOIN icloud_resource_channels AS new_ch ON new_ch.resource_id = ir.id AND new_ch.kind = ?", iCloudChannelAppleAccount).
		Joins("LEFT JOIN icloud_resource_channels AS old_ch ON old_ch.resource_id = ir.id AND old_ch.kind = ?", iCloudChannelWeb)
}

func (s *Service) ListAdminICloudAliases(ctx context.Context, resourceID uint, offset, limit int) (*AdminICloudAliasList, error) {
	if s == nil || s.db == nil {
		return nil, ErrICloudResourceQueryTemporary
	}
	if resourceID == 0 || offset < 0 {
		return nil, ErrICloudResourceQuery
	}
	if limit == 0 {
		limit = adminICloudResourceDefaultLimit
	}
	if limit < 1 || limit > adminICloudResourceMaxLimit {
		return nil, ErrICloudResourceQuery
	}
	var root struct{ ID uint }
	if err := s.db.WithContext(ctx).Table("email_resources").Select("id").Where("id = ? AND type = ?", resourceID, "icloud").Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrICloudResourceNotFound
		}
		return nil, ErrICloudResourceQueryTemporary
	}
	query := s.db.WithContext(ctx).Table("icloud_aliases").Where("resource_id = ?", resourceID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, ErrICloudResourceQueryTemporary
	}
	items := make([]AdminICloudAliasView, 0, limit)
	if err := query.Select("id, anonymous_id, email, status, origin, provider_created_at, last_seen_at, last_allocated_at, created_at, updated_at").
		Order("id DESC").Offset(offset).Limit(limit).Scan(&items).Error; err != nil {
		return nil, ErrICloudResourceQueryTemporary
	}
	return &AdminICloudAliasList{Items: items, Total: total, Offset: offset, Limit: limit}, nil
}

func (s *Service) applyAdminICloudResourceFilter(query *gorm.DB, filter AdminICloudResourceListFilter, ignore adminICloudFilterIgnore) *gorm.DB {
	return applyAdminICloudResourceFilterDB(query, filter, ignore)
}

func applyAdminICloudResourceFilterDB(query *gorm.DB, filter AdminICloudResourceListFilter, ignore adminICloudFilterIgnore) *gorm.DB {
	if filter.Search != "" {
		like := "%" + filter.Search + "%"
		query = query.Where(`(
			LOWER(ir.primary_email) LIKE ? OR LOWER(u.email) LIKE ? OR LOWER(u.nickname) LIKE ? OR
			LOWER(ir.region) LIKE ? OR LOWER(ir.country_code) LIKE ? OR LOWER(ir.account_role) LIKE ? OR
			LOWER(ir.bound_phone_number) LIKE ? OR LOWER(COALESCE(family_primary.primary_email, '')) LIKE ? OR
			CAST(ir.id AS CHAR) LIKE ? OR CAST(er.owner_user_id AS CHAR) LIKE ? OR EXISTS (
				SELECT 1 FROM icloud_aliases AS ia
				WHERE ia.resource_id = ir.id AND (LOWER(ia.email) LIKE ? OR LOWER(ia.anonymous_id) LIKE ?)
			)
		)`, like, like, like, like, like, like, like, like, like, like, like, like)
	}
	if !ignore.status {
		if filter.Status == "" {
			query = query.Where("ir.status <> ?", iCloudResourceDeleted)
		} else {
			query = query.Where("ir.status = ?", filter.Status)
		}
	}
	if !ignore.forSale && filter.ForSale != nil {
		query = query.Where("ir.for_sale = ?", *filter.ForSale)
	}
	if filter.CreatedFrom != nil {
		query = query.Where("ir.created_at >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		query = query.Where("ir.created_at < ?", *filter.CreatedTo)
	}
	return query
}

func (s *Service) adminICloudResourceFacets(ctx context.Context, filter AdminICloudResourceListFilter) (AdminICloudResourceFacets, error) {
	var facets AdminICloudResourceFacets
	type countRow struct {
		Key   string `gorm:"column:key"`
		Count int64  `gorm:"column:count"`
	}
	var statusRows []countRow
	statusQuery := s.applyAdminICloudResourceFilter(s.adminICloudResourceQuery(ctx), filter, adminICloudFilterIgnore{status: true})
	if err := statusQuery.Select("ir.status AS `key`, COUNT(*) AS count").Group("ir.status").Scan(&statusRows).Error; err != nil {
		return facets, fmt.Errorf("count administrator iCloud status facets: %w", ErrICloudResourceQueryTemporary)
	}
	for _, row := range statusRows {
		if row.Key != iCloudResourceDeleted {
			facets.Status.All += row.Count
		}
		switch row.Key {
		case iCloudResourcePending:
			facets.Status.Pending = row.Count
		case iCloudResourceValidating:
			facets.Status.Validating = row.Count
		case iCloudResourceNormal:
			facets.Status.Normal = row.Count
		case iCloudResourceAbnormal:
			facets.Status.Abnormal = row.Count
		case iCloudResourceDisabled:
			facets.Status.Disabled = row.Count
		case iCloudResourceDeleted:
			facets.Status.Deleted = row.Count
		}
	}
	var saleRows []struct {
		ForSale bool  `gorm:"column:for_sale"`
		Count   int64 `gorm:"column:count"`
	}
	saleQuery := s.applyAdminICloudResourceFilter(s.adminICloudResourceQuery(ctx), filter, adminICloudFilterIgnore{forSale: true})
	if err := saleQuery.Select("ir.for_sale, COUNT(*) AS count").Group("ir.for_sale").Scan(&saleRows).Error; err != nil {
		return facets, fmt.Errorf("count administrator iCloud sale facets: %w", ErrICloudResourceQueryTemporary)
	}
	for _, row := range saleRows {
		facets.ForSale.All += row.Count
		if row.ForSale {
			facets.ForSale.Yes += row.Count
		} else {
			facets.ForSale.No += row.Count
		}
	}
	return facets, nil
}
