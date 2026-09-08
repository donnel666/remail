package infra

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/proto/domain"
	"gorm.io/gorm"
)

type ResourceFilter struct {
	Search         string     `json:"search,omitempty"`
	Suffix         string     `json:"suffix,omitempty"`
	Status         string     `json:"status,omitempty"`
	ForSale        *bool      `json:"forSale,omitempty"`
	LongLived      *bool      `json:"longLived,omitempty"`
	OwnerID        *uint      `json:"ownerId,omitempty"`
	CreatedFrom    *time.Time `json:"createdFrom,omitempty"`
	CreatedTo      *time.Time `json:"createdTo,omitempty"`
	Offset         int        `json:"-"`
	Limit          int        `json:"-"`
	AfterID        uint       `json:"-"`
	SkipTotal      bool       `json:"-"`
	SkipFacets     bool       `json:"-"`
	ExcludeDeleted bool       `json:"-"`
	AdminSearch    bool       `json:"-"`
	SearchOwnerIDs []uint     `json:"-"`
}
type ResourcePage struct {
	Items       []Resource
	Total       int64
	Facets      ResourceFacets
	NextAfterID *uint
	HasMore     bool
}
type StatusFacets struct {
	All         int64 `json:"all"`
	Pending     int64 `json:"pending"`
	Validating  int64 `json:"validating"`
	Identifying int64 `json:"identifying"`
	Normal      int64 `json:"normal"`
	Abnormal    int64 `json:"abnormal"`
	Disabled    int64 `json:"disabled"`
	Deleted     int64 `json:"deleted"`
}
type BooleanFacets struct {
	All int64 `json:"all"`
	Yes int64 `json:"yes"`
	No  int64 `json:"no"`
}
type SuffixFacet struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}
type ResourceFacets struct {
	StatusFacets
	Status    StatusFacets  `json:"status"`
	ForSale   BooleanFacets `json:"forSale"`
	LongLived BooleanFacets `json:"longLived"`
	Suffixes  []SuffixFacet `json:"suffixes"`
}

func normalizeFilter(f ResourceFilter) (ResourceFilter, error) {
	f.Search = strings.TrimSpace(f.Search)
	f.Status = strings.ToLower(strings.TrimSpace(f.Status))
	f.Suffix = strings.ToLower(strings.TrimSpace(f.Suffix))
	if f.Limit == 0 {
		f.Limit = 20
	}
	if f.Offset < 0 || f.Limit < 1 || f.Limit > 200 || len(f.Search) > 320 || len(f.Suffix) > 255 {
		return f, domain.ErrInvalidResource
	}
	if f.Suffix != "" && strings.ContainsAny(f.Suffix, "@ /\\\r\n\x00") {
		return f, domain.ErrInvalidResource
	}
	if f.Status != "" {
		switch f.Status {
		case domain.StatusPending, domain.StatusValidating, domain.StatusIdentifying, domain.StatusNormal, domain.StatusAbnormal, domain.StatusDisabled, domain.StatusDeleted:
		default:
			return f, domain.ErrInvalidResource
		}
	}
	if f.CreatedFrom != nil && f.CreatedTo != nil && f.CreatedFrom.After(*f.CreatedTo) {
		return f, domain.ErrInvalidResource
	}
	return f, nil
}
func protoResourceQuery(db *gorm.DB) *gorm.DB {
	return db.Model(&Resource{}).Where(`EXISTS (SELECT 1 FROM email_resources AS root WHERE root.id = proto_resources.id AND root.type = 'proto' AND root.owner_user_id = proto_resources.owner_user_id)`)
}
func resourceFilterQuery(db *gorm.DB, f ResourceFilter, ignore string) *gorm.DB {
	q := protoResourceQuery(db)
	if f.OwnerID != nil {
		q = q.Where("owner_user_id = ?", *f.OwnerID)
	}
	if f.Search != "" {
		if f.AdminSearch {
			q = adminResourceSearch(q, f)
		} else {
			value := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(strings.ToLower(f.Search))
			q = q.Where("LOWER(email_address) LIKE ?", "%"+value+"%")
		}
	}
	if f.ExcludeDeleted {
		q = q.Where("status <> ?", domain.StatusDeleted)
	}
	if ignore != "status" {
		if f.Status != "" {
			q = q.Where("status = ?", f.Status)
		} else if !f.ExcludeDeleted {
			q = q.Where("status <> ?", domain.StatusDeleted)
		}
	}
	if f.Suffix != "" && ignore != "suffix" {
		q = q.Where("email_domain = ?", f.Suffix)
	}
	if f.ForSale != nil && ignore != "forSale" {
		q = q.Where("for_sale = ?", *f.ForSale)
	}
	if f.LongLived != nil && ignore != "longLived" {
		q = q.Where("long_lived = ?", *f.LongLived)
	}
	if f.CreatedFrom != nil {
		q = q.Where("created_at >= ?", *f.CreatedFrom)
	}
	if f.CreatedTo != nil {
		q = q.Where("created_at <= ?", *f.CreatedTo)
	}
	return q
}

func adminResourceSearch(q *gorm.DB, f ResourceFilter) *gorm.DB {
	search := strings.ToLower(strings.TrimSpace(f.Search))
	condition, args := "email_address = ?", []any{search}
	if id, err := strconv.ParseUint(search, 10, 64); err == nil && id > 0 {
		condition, args = "id = ?", []any{id}
	} else if strings.HasPrefix(search, "@") {
		domain := strings.TrimSpace(strings.TrimPrefix(search, "@"))
		condition, args = "email_domain = ?", []any{domain}
		if domain == "" {
			condition, args = "FALSE", nil
		}
	} else if !strings.Contains(search, "@") {
		condition = "email_address LIKE ? ESCAPE ?"
		args = []any{strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(search) + "@%", "\\"}
	}
	if len(f.SearchOwnerIDs) > 0 {
		condition = "(" + condition + " OR owner_user_id IN ?)"
		args = append(args, f.SearchOwnerIDs)
	}
	return q.Where(condition, args...)
}

const safeResourceColumns = `id, resource_type, owner_user_id, email_address, email_domain, (password <> '') AS password_configured, for_sale, long_lived, quality_score, alloc_bucket, status, (SELECT root.version FROM email_resources AS root WHERE root.id = proto_resources.id) AS version, validation_generation, credential_revision, credential_updated_at, validation_request_id, validation_failures, last_safe_error, last_checked_at, last_allocated_at, created_at, updated_at`

func (s *Service) ListResources(ctx context.Context, f ResourceFilter) (*ResourcePage, error) {
	if s == nil || s.DB == nil {
		return nil, domain.ErrDependency
	}
	var err error
	f, err = normalizeFilter(f)
	if err != nil {
		return nil, err
	}
	page := &ResourcePage{Items: []Resource{}, Total: -1, Facets: ResourceFacets{Suffixes: []SuffixFacet{}}}
	if !f.SkipTotal {
		if err := resourceFilterQuery(s.dbFor(ctx), f, "").Count(&page.Total).Error; err != nil {
			return nil, err
		}
	}
	q := resourceFilterQuery(s.dbFor(ctx), f, "")
	if f.AfterID > 0 {
		q = q.Where("id < ?", f.AfterID)
	} else {
		q = q.Offset(f.Offset)
	}
	if err := q.Select(safeResourceColumns).Order("id DESC").Limit(f.Limit + 1).Find(&page.Items).Error; err != nil {
		return nil, err
	}
	page.HasMore = len(page.Items) > f.Limit
	if page.HasMore {
		page.Items = page.Items[:f.Limit]
		id := page.Items[len(page.Items)-1].ID
		page.NextAfterID = &id
	}
	if !f.SkipFacets {
		page.Facets, err = s.resourceFacets(ctx, f)
		if err != nil {
			return nil, err
		}
	}
	return page, nil
}
func (s *Service) resourceFacets(ctx context.Context, f ResourceFilter) (ResourceFacets, error) {
	result := ResourceFacets{Suffixes: []SuffixFacet{}}
	var statuses []struct {
		Status string
		Count  int64
	}
	if err := resourceFilterQuery(s.dbFor(ctx), f, "status").Select("status, COUNT(*) AS count").Group("status").Scan(&statuses).Error; err != nil {
		return result, err
	}
	for _, row := range statuses {
		result.All += row.Count
		switch row.Status {
		case "pending":
			result.Pending = row.Count
		case "validating":
			result.Validating = row.Count
		case "identifying":
			result.Identifying = row.Count
		case "normal":
			result.Normal = row.Count
		case "abnormal":
			result.Abnormal = row.Count
		case "disabled":
			result.Disabled = row.Count
		case "deleted":
			result.Deleted = row.Count
		}
	}
	result.Status = result.StatusFacets
	for _, dimension := range []struct {
		name, column string
		target       *BooleanFacets
	}{{"forSale", "for_sale", &result.ForSale}, {"longLived", "long_lived", &result.LongLived}} {
		var rows []struct {
			Value bool
			Count int64
		}
		if err := resourceFilterQuery(s.dbFor(ctx), f, dimension.name).Select(dimension.column + " AS value, COUNT(*) AS count").Group(dimension.column).Scan(&rows).Error; err != nil {
			return result, err
		}
		for _, row := range rows {
			dimension.target.All += row.Count
			if row.Value {
				dimension.target.Yes = row.Count
			} else {
				dimension.target.No = row.Count
			}
		}
	}
	if err := resourceFilterQuery(s.dbFor(ctx), f, "suffix").Select("email_domain AS `key`, COUNT(*) AS count").Group("email_domain").Order("email_domain ASC").Scan(&result.Suffixes).Error; err != nil {
		return result, err
	}
	return result, nil
}
func (s *Service) GetResource(ctx context.Context, id uint, owner *uint) (*Resource, error) {
	if s == nil || s.DB == nil || id == 0 {
		return nil, domain.ErrInvalidResource
	}
	q := protoResourceQuery(s.dbFor(ctx)).Where("id = ?", id)
	if owner != nil {
		q = q.Where("owner_user_id = ?", *owner)
	}
	var row Resource
	if err := q.Select(safeResourceColumns).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrResourceMissing
		}
		return nil, err
	}
	return &row, nil
}
func (s *Service) SetForSale(ctx context.Context, id uint, owner *uint, sale bool) error {
	return s.mutateResource(ctx, id, owner, func(_ context.Context, _ *gorm.DB, row *Resource) error {
		if row.Status == domain.StatusDeleted {
			return domain.ErrResourceMissing
		}
		row.ForSale = sale
		return nil
	})
}
func (s *Service) UpdateResource(ctx context.Context, id uint, email *string, owner *uint) error {
	return s.mutateResource(ctx, id, nil, func(_ context.Context, tx *gorm.DB, row *Resource) error {
		if row.Status == domain.StatusDeleted {
			return domain.ErrResourceMissing
		}
		if err := assertNoAllocations(tx, id); err != nil {
			return err
		}
		changed := false
		if email != nil {
			value := strings.ToLower(strings.TrimSpace(*email))
			if !validEmail(value) {
				return domain.ErrInvalidResource
			}
			if value != row.EmailAddress {
				row.EmailAddress = value
				row.EmailDomain = emailDomain(value)
				row.Password = ""
				changed = true
			}
		}
		if owner != nil {
			if *owner == 0 {
				return domain.ErrInvalidResource
			}
			if row.OwnerUserID != *owner {
				row.OwnerUserID = *owner
				changed = true
			}
		}
		if changed {
			invalidateResource(row, s.Now().UTC(), true)
		}
		return nil
	})
}
func (s *Service) SetStatus(ctx context.Context, id uint, owner *uint, status string) error {
	return s.mutateResource(ctx, id, owner, func(_ context.Context, tx *gorm.DB, row *Resource) error {
		if row.Status == status {
			return nil
		}
		if !validStatusTransition(row.Status, status) {
			return domain.ErrInvalidResource
		}
		if status == domain.StatusDeleted {
			if owner != nil && row.ForSale {
				return domain.ErrResourceNotPrivate
			}
			if err := assertNoAllocations(tx, id); err != nil {
				return err
			}
			row.ForSale = false
		}
		if status == domain.StatusPending {
			wasDeleted := row.Status == domain.StatusDeleted
			invalidateResource(row, s.Now().UTC(), false)
			if wasDeleted {
				row.ForSale = false
			}
		} else {
			row.Status = status
			row.ValidationGeneration++
		}
		return nil
	})
}
func validStatusTransition(from, to string) bool {
	switch from {
	case domain.StatusPending, domain.StatusValidating, domain.StatusIdentifying, domain.StatusNormal, domain.StatusAbnormal:
		return to == domain.StatusDisabled || to == domain.StatusDeleted
	case domain.StatusDisabled:
		return to == domain.StatusPending || to == domain.StatusDeleted
	case domain.StatusDeleted:
		return to == domain.StatusPending
	}
	return false
}
func (s *Service) ReplaceCredentials(ctx context.Context, id uint, owner *uint, password string) error {
	if !validPassword(password) {
		return domain.ErrInvalidResource
	}
	return s.mutateResource(ctx, id, owner, func(_ context.Context, _ *gorm.DB, row *Resource) error {
		if row.Status == domain.StatusDeleted {
			return domain.ErrResourceMissing
		}
		row.Password = password
		invalidateResource(row, s.Now().UTC(), true)
		return nil
	})
}
func invalidateResource(row *Resource, now time.Time, credentials bool) {
	row.Status = domain.StatusPending
	row.ValidationGeneration++
	row.ValidationFailures = 0
	row.QualityScore = 0
	row.LastSafeError = ""
	row.LastCheckedAt = nil
	row.ValidationRequestID = ""
	if credentials {
		row.CredentialRevision++
		row.CredentialUpdatedAt = now
	}
}
func (s *Service) mutateResource(ctx context.Context, id uint, owner *uint, apply func(context.Context, *gorm.DB, *Resource) error) error {
	return s.transaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
		row, err := lockResource(tx, id, owner)
		if err != nil {
			return err
		}
		before := *row
		generation := row.ValidationGeneration
		if err := apply(ctx, tx, row); err != nil {
			return err
		}
		if before == *row {
			return nil
		}
		if row.CredentialRevision != before.CredentialRevision || row.Status == domain.StatusDeleted {
			if err := deleteSessionTx(tx, id); err != nil {
				return err
			}
		}
		now := s.Now().UTC()
		if row.ValidationGeneration != generation {
			if err := cancelMaintenanceRunsTx(tx, id, "Superseded by a resource command.", now); err != nil {
				return err
			}
		}
		row.Version++
		row.UpdatedAt = now
		if err := tx.Model(&Resource{}).Where("id = ?", id).Select("*").Omit("id", "password_configured").Updates(row).Error; err != nil {
			return err
		}
		return tx.Model(&resourceRoot{}).Where("id = ? AND type = ?", id, domain.ResourceType).Updates(map[string]any{"owner_user_id": row.OwnerUserID, "version": row.Version, "updated_at": now}).Error
	})
}
func (s *Service) ClaimForValidation(ctx context.Context, id uint, owner *uint) (uint64, error) {
	return s.ClaimForValidationWithRequest(ctx, id, owner, "")
}
func (s *Service) ClaimForValidationWithRequest(ctx context.Context, id uint, owner *uint, requestID string) (uint64, error) {
	var generation uint64
	err := s.mutateResource(ctx, id, owner, func(_ context.Context, _ *gorm.DB, row *Resource) error {
		if row.Status == domain.StatusDeleted || row.Status == domain.StatusDisabled {
			return domain.ErrInvalidResource
		}
		invalidateResource(row, s.Now().UTC(), false)
		row.ValidationRequestID = strings.TrimSpace(requestID)
		generation = row.ValidationGeneration
		return nil
	})
	return generation, err
}
func (s *Service) GetImport(ctx context.Context, id uint64) (*ImportStatus, error) {
	if s == nil || s.DB == nil || id == 0 {
		return nil, domain.ErrInvalidResource
	}
	var row ImportStatus
	if err := s.dbFor(ctx).Where("id = ?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrResourceMissing
		}
		return nil, err
	}
	return &row, nil
}
func (s *Service) GetImportForUser(ctx context.Context, id uint64, userID uint) (*ImportStatus, error) {
	row, err := s.GetImport(ctx, id)
	if err != nil {
		return nil, err
	}
	if row.OwnerUserID != userID {
		return nil, domain.ErrResourceMissing
	}
	return row, nil
}
