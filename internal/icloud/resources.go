package icloud

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
	Suffix        string     `json:"suffix,omitempty"`
	Status        string     `json:"status,omitempty"`
	ForSale       *bool      `json:"forSale,omitempty"`
	SessionStatus string     `json:"sessionStatus,omitempty"`
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

type AdminICloudResourceView struct {
	ID                      uint                 `json:"id"`
	Version                 uint64               `json:"version"`
	PrimaryEmail            string               `json:"primaryEmail"`
	Suffix                  string               `json:"suffix"`
	GmailEmail              string               `json:"gmailEmail"`
	SelectedForwardTo       string               `json:"selectedForwardTo"`
	Owner                   AdminICloudOwnerView `json:"owner"`
	Status                  string               `json:"status"`
	ForSale                 bool                 `json:"forSale"`
	SessionStatus           string               `json:"sessionStatus"`
	AliasCount              uint                 `json:"aliasCount"`
	ExpireAt                time.Time            `json:"expireAt"`
	NextValidationAt        *time.Time           `json:"nextValidationAt"`
	NextKeepaliveAt         *time.Time           `json:"nextKeepaliveAt"`
	LastCheckedAt           *time.Time           `json:"lastCheckedAt"`
	LastValidAt             *time.Time           `json:"lastValidAt"`
	LastAliasSyncAt         *time.Time           `json:"lastAliasSyncAt"`
	DeliveryProbeVerifiedAt *time.Time           `json:"deliveryProbeVerifiedAt"`
	LastAllocatedAt         *time.Time           `json:"lastAllocatedAt"`
	LastSafeError           *string              `json:"lastSafeError"`
	CreatedAt               time.Time            `json:"createdAt"`
	UpdatedAt               time.Time            `json:"updatedAt"`
}

type AdminICloudResourceDetail struct {
	AdminICloudResourceView
	AliasLimit             uint       `json:"aliasLimit"`
	AliasRemaining         uint       `json:"aliasRemaining"`
	AliasProvisioning      bool       `json:"aliasProvisioning"`
	CredentialRevision     uint64     `json:"credentialRevision"`
	CredentialUpdatedAt    time.Time  `json:"credentialUpdatedAt"`
	ValidationGeneration   uint64     `json:"validationGeneration"`
	ValidationFailures     uint8      `json:"validationFailures"`
	SessionFailures        uint8      `json:"sessionFailures"`
	DeliveryProbeStartedAt *time.Time `json:"deliveryProbeStartedAt"`
}

type adminICloudResourceRow struct {
	ID                      uint       `gorm:"column:id"`
	Version                 uint64     `gorm:"column:version"`
	PrimaryEmail            string     `gorm:"column:primary_email"`
	GmailEmail              string     `gorm:"column:gmail_email"`
	SelectedForwardTo       string     `gorm:"column:selected_forward_to"`
	OwnerID                 uint       `gorm:"column:owner_id"`
	OwnerEmail              string     `gorm:"column:owner_email"`
	OwnerNickname           string     `gorm:"column:owner_nickname"`
	OwnerGroupName          string     `gorm:"column:owner_group_name"`
	OwnerRole               string     `gorm:"column:owner_role"`
	OwnerStatus             string     `gorm:"column:owner_status"`
	Status                  string     `gorm:"column:status"`
	ForSale                 bool       `gorm:"column:for_sale"`
	SessionStatus           string     `gorm:"column:session_status"`
	SessionFailures         uint8      `gorm:"column:session_failures"`
	CredentialRevision      uint64     `gorm:"column:credential_revision"`
	CredentialUpdatedAt     time.Time  `gorm:"column:credential_updated_at"`
	ValidationGeneration    uint64     `gorm:"column:validation_generation"`
	ValidationFailures      uint8      `gorm:"column:validation_failures"`
	AliasCount              uint       `gorm:"column:alias_count"`
	AliasProvisionCandidate string     `gorm:"column:alias_provision_candidate"`
	AliasProvisionReconcile bool       `gorm:"column:alias_provision_reconcile"`
	ExpireAt                time.Time  `gorm:"column:expire_at"`
	NextValidationAt        *time.Time `gorm:"column:next_validation_at"`
	NextKeepaliveAt         *time.Time `gorm:"column:next_keepalive_at"`
	LastCheckedAt           *time.Time `gorm:"column:last_checked_at"`
	LastValidAt             *time.Time `gorm:"column:last_valid_at"`
	LastAliasSyncAt         *time.Time `gorm:"column:last_alias_sync_at"`
	DeliveryProbeStartedAt  *time.Time `gorm:"column:delivery_probe_started_at"`
	DeliveryProbeVerifiedAt *time.Time `gorm:"column:delivery_probe_verified_at"`
	LastAllocatedAt         *time.Time `gorm:"column:last_allocated_at"`
	LastSafeError           string     `gorm:"column:last_safe_error"`
	CreatedAt               time.Time  `gorm:"column:created_at"`
	UpdatedAt               time.Time  `gorm:"column:updated_at"`
}

const adminICloudResourceSelect = `
	ir.id, er.version, ir.primary_email, gr.email AS gmail_email,
	ir.selected_forward_to, er.owner_user_id AS owner_id,
	u.email AS owner_email, u.nickname AS owner_nickname,
	COALESCE(ug.name, '') AS owner_group_name, u.role AS owner_role,
	u.status AS owner_status, ir.status, ir.for_sale, ir.session_status,
	ir.session_failures, ir.credential_revision, ir.credential_updated_at,
	ir.validation_generation, ir.validation_failures, ir.alias_count,
	ir.alias_provision_candidate, ir.alias_provision_reconcile, ir.expire_at,
	ir.next_validation_at, ir.next_keepalive_at, ir.last_checked_at,
	ir.last_valid_at, ir.last_alias_sync_at, ir.delivery_probe_started_at,
	ir.delivery_probe_verified_at, ir.last_allocated_at, ir.last_safe_error,
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

type AdminICloudSessionFacets struct {
	All       int64 `json:"all"`
	Unchecked int64 `json:"unchecked"`
	Valid     int64 `json:"valid"`
	Invalid   int64 `json:"invalid"`
}

type AdminICloudSuffixFacet struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

type AdminICloudResourceFacets struct {
	Status        AdminICloudStatusFacets  `json:"status"`
	ForSale       AdminICloudBooleanFacets `json:"forSale"`
	SessionStatus AdminICloudSessionFacets `json:"sessionStatus"`
	Suffixes      []AdminICloudSuffixFacet `json:"suffixes"`
}

type AdminICloudResourceList struct {
	Items      []AdminICloudResourceView `json:"items"`
	Total      int64                     `json:"total"`
	Offset     int                       `json:"offset"`
	Limit      int                       `json:"limit"`
	AliasLimit int                       `json:"aliasLimit"`
	Facets     AdminICloudResourceFacets `json:"facets"`
}

type AdminICloudAliasView struct {
	ID                uint       `json:"id"`
	Email             string     `json:"email"`
	Status            string     `json:"status"`
	ForwardToEmail    string     `json:"forwardToEmail"`
	Origin            string     `json:"origin"`
	ProviderDomain    string     `json:"providerDomain"`
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
	status        bool
	forSale       bool
	sessionStatus bool
	suffix        bool
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
		query := s.applyAdminICloudResourceFilter(s.adminICloudResourceQuery(ctx), filter, adminICloudFilterIgnore{})
		if err := query.Count(&total).Error; err != nil {
			return nil, fmt.Errorf("count administrator iCloud resources: %w", ErrICloudResourceQueryTemporary)
		}
	}

	var rows []adminICloudResourceRow
	query := s.applyAdminICloudResourceFilter(s.adminICloudResourceQuery(ctx), filter, adminICloudFilterIgnore{})
	if err := query.Select(adminICloudResourceSelect).
		Order("ir.id DESC").Offset(filter.Offset).Limit(filter.Limit).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list administrator iCloud resources: %w", ErrICloudResourceQueryTemporary)
	}

	items := make([]AdminICloudResourceView, len(rows))
	for index, row := range rows {
		items[index] = adminICloudResourceView(row)
	}

	facets := AdminICloudResourceFacets{Suffixes: []AdminICloudSuffixFacet{}}
	if includeAdminICloudListSection(filter.IncludeFacets) {
		facets, err = s.adminICloudResourceFacets(ctx, filter)
		if err != nil {
			return nil, err
		}
	}
	return &AdminICloudResourceList{
		Items: items, Total: total, Offset: filter.Offset, Limit: filter.Limit,
		AliasLimit: iCloudMaxAliases, Facets: facets,
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
	err := s.adminICloudResourceQuery(ctx).Where("ir.id = ?", resourceID).
		Select(adminICloudResourceSelect).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrICloudResourceNotFound
		}
		return nil, ErrICloudResourceQueryTemporary
	}
	remaining := uint(0)
	if row.AliasCount < iCloudMaxAliases {
		remaining = iCloudMaxAliases - row.AliasCount
	}
	return &AdminICloudResourceDetail{
		AdminICloudResourceView: adminICloudResourceView(row),
		AliasLimit:              iCloudMaxAliases,
		AliasRemaining:          remaining,
		AliasProvisioning:       strings.TrimSpace(row.AliasProvisionCandidate) != "" || row.AliasProvisionReconcile,
		CredentialRevision:      row.CredentialRevision,
		CredentialUpdatedAt:     row.CredentialUpdatedAt,
		ValidationGeneration:    row.ValidationGeneration,
		ValidationFailures:      row.ValidationFailures,
		SessionFailures:         row.SessionFailures,
		DeliveryProbeStartedAt:  row.DeliveryProbeStartedAt,
	}, nil
}

func adminICloudResourceView(row adminICloudResourceRow) AdminICloudResourceView {
	var safeError *string
	if value := strings.TrimSpace(row.LastSafeError); value != "" {
		safeError = &value
	}
	return AdminICloudResourceView{
		ID: row.ID, Version: row.Version, PrimaryEmail: row.PrimaryEmail,
		Suffix: iCloudEmailSuffix(row.PrimaryEmail), GmailEmail: row.GmailEmail,
		SelectedForwardTo: row.SelectedForwardTo,
		Owner: AdminICloudOwnerView{
			ID: row.OwnerID, Email: row.OwnerEmail, Nickname: row.OwnerNickname,
			GroupName: row.OwnerGroupName, Role: row.OwnerRole, Enabled: row.OwnerStatus == "active",
		},
		Status: row.Status, ForSale: row.ForSale, SessionStatus: row.SessionStatus,
		AliasCount: row.AliasCount, ExpireAt: row.ExpireAt,
		NextValidationAt: row.NextValidationAt, NextKeepaliveAt: row.NextKeepaliveAt,
		LastCheckedAt: row.LastCheckedAt, LastValidAt: row.LastValidAt,
		LastAliasSyncAt: row.LastAliasSyncAt, DeliveryProbeVerifiedAt: row.DeliveryProbeVerifiedAt,
		LastAllocatedAt: row.LastAllocatedAt, LastSafeError: safeError,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func includeAdminICloudListSection(value *bool) bool {
	return value == nil || *value
}

func normalizeAdminICloudResourceFilter(filter AdminICloudResourceListFilter) (AdminICloudResourceListFilter, error) {
	filter.Search = strings.ToLower(strings.TrimSpace(filter.Search))
	filter.Status = strings.ToLower(strings.TrimSpace(filter.Status))
	filter.SessionStatus = strings.ToLower(strings.TrimSpace(filter.SessionStatus))
	filter.Suffix = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(filter.Suffix), "@"))
	if len(filter.Search) > 320 || len(filter.Suffix) > 255 || strings.Contains(filter.Suffix, "@") ||
		filter.Offset < 0 || filter.CreatedFrom != nil && filter.CreatedTo != nil && !filter.CreatedTo.After(*filter.CreatedFrom) {
		return filter, ErrICloudResourceQuery
	}
	if filter.Status != "" && !validAdminICloudResourceStatus(filter.Status) {
		return filter, ErrICloudResourceQuery
	}
	if filter.SessionStatus != "" && !validAdminICloudSessionStatus(filter.SessionStatus) {
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
	case iCloudResourcePending, iCloudResourceValidating, iCloudResourceNormal,
		iCloudResourceAbnormal, iCloudResourceDisabled, iCloudResourceDeleted:
		return true
	default:
		return false
	}
}

func validAdminICloudSessionStatus(status string) bool {
	switch status {
	case iCloudSessionUnchecked, iCloudSessionValid, iCloudSessionInvalid:
		return true
	default:
		return false
	}
}

func (s *Service) adminICloudResourceQuery(ctx context.Context) *gorm.DB {
	return adminICloudResourceQueryDB(ctx, s.db)
}

func adminICloudResourceQueryDB(ctx context.Context, db *gorm.DB) *gorm.DB {
	return db.WithContext(ctx).Table("icloud_resources AS ir").
		Joins("JOIN email_resources AS er ON er.id = ir.id AND er.type = ?", "icloud").
		Joins("JOIN gmail_resources AS gr ON gr.id = ir.gmail_resource_id").
		Joins("JOIN users AS u ON u.id = er.owner_user_id").
		Joins("LEFT JOIN user_groups AS ug ON ug.id = u.user_group_id")
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

	var root struct {
		ID uint `gorm:"column:id"`
	}
	if err := s.db.WithContext(ctx).Table("email_resources").Select("id").
		Where("id = ? AND type = ?", resourceID, "icloud").Take(&root).Error; err != nil {
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
	if err := query.Select(`id, email, status, forward_to_email, origin, provider_domain,
		provider_created_at, last_seen_at, last_allocated_at, created_at, updated_at`).
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
			LOWER(ir.primary_email) LIKE ? OR LOWER(gr.email) LIKE ? OR
			LOWER(ir.selected_forward_to) LIKE ? OR LOWER(u.email) LIKE ? OR
			LOWER(u.nickname) LIKE ? OR CAST(ir.id AS CHAR) LIKE ? OR
			CAST(er.owner_user_id AS CHAR) LIKE ? OR EXISTS (
				SELECT 1 FROM icloud_aliases AS ia
				WHERE ia.resource_id = ir.id AND LOWER(ia.email) LIKE ?
			)
		)`, like, like, like, like, like, like, like, like)
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
	if !ignore.sessionStatus && filter.SessionStatus != "" {
		query = query.Where("ir.session_status = ?", filter.SessionStatus)
	}
	if !ignore.suffix && filter.Suffix != "" {
		query = query.Where("LOWER(SUBSTR(ir.primary_email, INSTR(ir.primary_email, '@') + 1)) = ?", filter.Suffix)
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

	var sessionRows []countRow
	sessionQuery := s.applyAdminICloudResourceFilter(s.adminICloudResourceQuery(ctx), filter, adminICloudFilterIgnore{sessionStatus: true})
	if err := sessionQuery.Select("ir.session_status AS `key`, COUNT(*) AS count").Group("ir.session_status").Scan(&sessionRows).Error; err != nil {
		return facets, fmt.Errorf("count administrator iCloud session facets: %w", ErrICloudResourceQueryTemporary)
	}
	for _, row := range sessionRows {
		facets.SessionStatus.All += row.Count
		switch row.Key {
		case iCloudSessionUnchecked:
			facets.SessionStatus.Unchecked = row.Count
		case iCloudSessionValid:
			facets.SessionStatus.Valid = row.Count
		case iCloudSessionInvalid:
			facets.SessionStatus.Invalid = row.Count
		}
	}

	var suffixRows []countRow
	suffixQuery := s.applyAdminICloudResourceFilter(s.adminICloudResourceQuery(ctx), filter, adminICloudFilterIgnore{suffix: true})
	suffixExpression := "LOWER(SUBSTR(ir.primary_email, INSTR(ir.primary_email, '@') + 1))"
	if err := suffixQuery.Select(suffixExpression + " AS `key`, COUNT(*) AS count").
		Where(suffixExpression + " <> ''").Group(suffixExpression).Order("count DESC, `key` ASC").Scan(&suffixRows).Error; err != nil {
		return facets, fmt.Errorf("count administrator iCloud suffix facets: %w", ErrICloudResourceQueryTemporary)
	}
	facets.Suffixes = make([]AdminICloudSuffixFacet, 0, len(suffixRows))
	for _, row := range suffixRows {
		facets.Suffixes = append(facets.Suffixes, AdminICloudSuffixFacet(row))
	}
	return facets, nil
}

func iCloudEmailSuffix(email string) string {
	_, suffix, found := strings.Cut(strings.ToLower(strings.TrimSpace(email)), "@")
	if !found {
		return ""
	}
	return suffix
}
