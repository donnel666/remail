package infra

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// --- GORM Models ---

// EmailResourceModel is the GORM model for the email_resources table.
type EmailResourceModel struct {
	ID          uint      `gorm:"primaryKey;autoIncrement"`
	Type        string    `gorm:"type:varchar(32);not null"`
	OwnerUserID uint      `gorm:"not null;column:owner_user_id"`
	CreatedAt   time.Time `gorm:"not null;autoCreateTime"`
	UpdatedAt   time.Time `gorm:"not null;autoUpdateTime"`
}

func (EmailResourceModel) TableName() string {
	return "email_resources"
}

func (m *EmailResourceModel) toDomain() *domain.EmailResource {
	return &domain.EmailResource{
		ID:          m.ID,
		Type:        domain.ResourceType(m.Type),
		OwnerUserID: m.OwnerUserID,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}
}

// MicrosoftResourceModel is the GORM model for the microsoft_resources table.
type MicrosoftResourceModel struct {
	ID              uint       `gorm:"primaryKey"`
	EmailAddress    string     `gorm:"type:varchar(255);not null;uniqueIndex;column:email_address"`
	EmailDomain     string     `gorm:"type:varchar(255);not null;default:'';column:email_domain"`
	Password        string     `gorm:"type:varchar(512);not null"`
	ClientID        string     `gorm:"type:varchar(255);not null;default:'';column:client_id"`
	RefreshToken    string     `gorm:"type:varchar(1024);not null;default:'';column:refresh_token"`
	LongLived       bool       `gorm:"not null;default:false;column:long_lived"`
	GraphAvailable  bool       `gorm:"not null;default:false;column:graph_available"`
	RTExpireAt      *time.Time `gorm:"column:rt_expire_at"`
	ForSale         bool       `gorm:"not null;default:false;column:for_sale"`
	Status          string     `gorm:"type:varchar(32);not null;default:'pending'"`
	QualityScore    int        `gorm:"not null;default:0;column:quality_score"`
	PlusDailyLimit  int        `gorm:"not null;default:10000;column:plus_daily_limit"`
	AllocBucket     uint8      `gorm:"not null;default:0;column:alloc_bucket"`
	LastSafeError   string     `gorm:"type:varchar(500);not null;default:'';column:last_safe_error"`
	LastAllocatedAt *time.Time `gorm:"column:last_allocated_at"`
	CreatedAt       time.Time  `gorm:"not null;autoCreateTime"`
	UpdatedAt       time.Time  `gorm:"not null;autoUpdateTime"`
}

func (MicrosoftResourceModel) TableName() string {
	return "microsoft_resources"
}

func (m *MicrosoftResourceModel) toDomain() *domain.MicrosoftResource {
	return &domain.MicrosoftResource{
		ID:              m.ID,
		EmailAddress:    m.EmailAddress,
		Password:        m.Password,
		ClientID:        m.ClientID,
		RefreshToken:    m.RefreshToken,
		LongLived:       m.LongLived,
		GraphAvailable:  m.GraphAvailable,
		RTExpireAt:      m.RTExpireAt,
		ForSale:         m.ForSale,
		Status:          domain.MicrosoftResourceStatus(m.Status),
		QualityScore:    m.QualityScore,
		PlusDailyLimit:  m.PlusDailyLimit,
		LastSafeError:   m.LastSafeError,
		LastAllocatedAt: m.LastAllocatedAt,
		CreatedAt:       m.CreatedAt,
		UpdatedAt:       m.UpdatedAt,
	}
}

func fromMicrosoftDomain(ms *domain.MicrosoftResource) *MicrosoftResourceModel {
	return &MicrosoftResourceModel{
		ID:              ms.ID,
		EmailAddress:    ms.EmailAddress,
		EmailDomain:     microsoftEmailDomain(ms.EmailAddress),
		Password:        ms.Password,
		ClientID:        ms.ClientID,
		RefreshToken:    ms.RefreshToken,
		LongLived:       ms.LongLived,
		GraphAvailable:  ms.GraphAvailable,
		RTExpireAt:      ms.RTExpireAt,
		ForSale:         ms.ForSale,
		Status:          string(ms.Status),
		QualityScore:    ms.QualityScore,
		PlusDailyLimit:  normalizeDailyLimit(ms.PlusDailyLimit, domain.DefaultPlusDailyLimit),
		AllocBucket:     uint8(ms.ID % 64),
		LastSafeError:   ms.LastSafeError,
		LastAllocatedAt: ms.LastAllocatedAt,
		CreatedAt:       ms.CreatedAt,
		UpdatedAt:       ms.UpdatedAt,
	}
}

// DomainResourceModel is the GORM model for the domain_resources table.
type DomainResourceModel struct {
	ID                uint       `gorm:"primaryKey"`
	Domain            string     `gorm:"type:varchar(255);not null;uniqueIndex"`
	DomainTLD         string     `gorm:"type:varchar(64);not null;default:'';column:domain_tld"`
	OwnerUserID       uint       `gorm:"not null;column:owner_user_id"`
	MailServerID      uint       `gorm:"not null;column:mail_server_id"`
	Purpose           string     `gorm:"type:varchar(32);not null;default:'not_sale'"`
	Status            string     `gorm:"type:varchar(32);not null;default:'abnormal'"`
	LastSafeError     string     `gorm:"type:varchar(500);not null;default:'';column:last_safe_error"`
	MailboxDailyLimit int        `gorm:"not null;default:10000;column:mailbox_daily_limit"`
	AllocBucket       uint8      `gorm:"not null;default:0;column:alloc_bucket"`
	LastAllocatedAt   *time.Time `gorm:"column:last_allocated_at"`
	CreatedAt         time.Time  `gorm:"not null;autoCreateTime"`
	UpdatedAt         time.Time  `gorm:"not null;autoUpdateTime"`
}

func (DomainResourceModel) TableName() string {
	return "domain_resources"
}

func (m *DomainResourceModel) toDomain() *domain.MailDomainResource {
	return &domain.MailDomainResource{
		ID:                m.ID,
		Domain:            m.Domain,
		MailServerID:      m.MailServerID,
		Purpose:           domain.ResourcePurpose(m.Purpose),
		Status:            domain.MailDomainStatus(m.Status),
		MailboxDailyLimit: m.MailboxDailyLimit,
		LastSafeError:     m.LastSafeError,
		LastAllocatedAt:   m.LastAllocatedAt,
		CreatedAt:         m.CreatedAt,
		UpdatedAt:         m.UpdatedAt,
	}
}

// --- ResourceRepo ---

// ResourceRepo implements app.EmailResourceRepository using GORM.
type ResourceRepo struct {
	db            *gorm.DB
	operationLogs *governanceinfra.OperationLogRepo
	facetsCache   *platform.TTLCache[string, coreapp.ResourceListFacets]
}

const (
	resourceBulkMutationChunkSize      = 1000
	resourceBulkOperationLogResourceID = "filter"
	resourceImportLookupChunkSize      = 10000
	resourceImportInsertBatchSize      = 1000
	microsoftImportRestoreTempTable    = "tmp_microsoft_import_restore"
	resourceFacetsCacheTTL             = 10 * time.Second
)

// NewResourceRepo creates a new GORM-backed resource repository.
func NewResourceRepo(db *gorm.DB) *ResourceRepo {
	return &ResourceRepo{
		db:            db,
		operationLogs: governanceinfra.NewOperationLogRepo(db),
		facetsCache:   platform.NewTTLCache[string, coreapp.ResourceListFacets](),
	}
}

func microsoftEmailKey(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func microsoftEmailDomain(email string) string {
	normalized := microsoftEmailKey(email)
	index := strings.LastIndex(normalized, "@")
	if index < 0 || index == len(normalized)-1 {
		return ""
	}
	return normalized[index+1:]
}

func normalizeDailyLimit(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func uniqueMicrosoftEmails(emails []string) []string {
	seen := make(map[string]struct{}, len(emails))
	result := make([]string, 0, len(emails))
	for _, email := range emails {
		trimmed := strings.TrimSpace(email)
		key := microsoftEmailKey(trimmed)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, trimmed)
	}
	sort.Slice(result, func(i, j int) bool {
		return microsoftEmailKey(result[i]) < microsoftEmailKey(result[j])
	})
	return result
}

func findMicrosoftResourceModelsByEmails(db *gorm.DB, emails []string, lockRows bool, includeDeleted bool) ([]MicrosoftResourceModel, error) {
	uniqueEmails := uniqueMicrosoftEmails(emails)
	if len(uniqueEmails) == 0 {
		return nil, nil
	}

	models := make([]MicrosoftResourceModel, 0)
	for start := 0; start < len(uniqueEmails); start += resourceImportLookupChunkSize {
		end := start + resourceImportLookupChunkSize
		if end > len(uniqueEmails) {
			end = len(uniqueEmails)
		}

		query := db.Model(&MicrosoftResourceModel{}).Where("email_address IN ?", uniqueEmails[start:end])
		if lockRows {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if !includeDeleted {
			query = query.Where("status <> ?", string(domain.MicrosoftStatusDeleted))
		}

		var chunk []MicrosoftResourceModel
		if err := query.Find(&chunk).Error; err != nil {
			return nil, err
		}
		models = append(models, chunk...)
	}
	return models, nil
}

func findMicrosoftEmailAddressesByEmails(db *gorm.DB, emails []string) ([]string, error) {
	uniqueEmails := uniqueMicrosoftEmails(emails)
	if len(uniqueEmails) == 0 {
		return nil, nil
	}

	found := make([]string, 0)
	for start := 0; start < len(uniqueEmails); start += resourceImportLookupChunkSize {
		end := start + resourceImportLookupChunkSize
		if end > len(uniqueEmails) {
			end = len(uniqueEmails)
		}

		var chunk []string
		if err := db.Model(&MicrosoftResourceModel{}).
			Where("email_address IN ? AND status <> ?", uniqueEmails[start:end], string(domain.MicrosoftStatusDeleted)).
			Pluck("email_address", &chunk).Error; err != nil {
			return nil, err
		}
		found = append(found, chunk...)
	}
	return found, nil
}

func (r *ResourceRepo) CreateMicrosoft(ctx context.Context, resource *domain.EmailResource, ms *domain.MicrosoftResource) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root := &EmailResourceModel{
			Type:        string(resource.Type),
			OwnerUserID: resource.OwnerUserID,
		}
		if err := tx.Create(root).Error; err != nil {
			return fmt.Errorf("create email resource: %w", err)
		}

		msModel := fromMicrosoftDomain(ms)
		msModel.ID = root.ID
		msModel.AllocBucket = uint8(root.ID % 64)
		if err := tx.Create(msModel).Error; err != nil {
			return fmt.Errorf("create microsoft resource: %w", err)
		}

		resource.ID = root.ID
		resource.CreatedAt = root.CreatedAt
		resource.UpdatedAt = root.UpdatedAt
		ms.ID = msModel.ID
		ms.CreatedAt = msModel.CreatedAt
		ms.UpdatedAt = msModel.UpdatedAt
		return nil
	})
}

func (r *ResourceRepo) CreateDomain(ctx context.Context, resource *domain.EmailResource, dr *domain.MailDomainResource) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing DomainResourceModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("domain = ?", dr.Domain).
			First(&existing).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("find existing domain resource: %w", err)
		}
		if err == nil {
			if domain.MailDomainStatus(existing.Status) != domain.DomainStatusDeleted {
				return domain.ErrDuplicateDomain
			}

			now := time.Now().UTC()
			if err := tx.Where("resource_id = ?", existing.ID).
				Delete(&GeneratedMailboxModel{}).Error; err != nil {
				return fmt.Errorf("clear restored domain mailboxes: %w", err)
			}

			deleteResult := tx.Where("id = ? AND status = ?", existing.ID, string(domain.DomainStatusDeleted)).
				Delete(&DomainResourceModel{})
			if deleteResult.Error != nil {
				return fmt.Errorf("remove deleted domain row before restore: %w", deleteResult.Error)
			}
			if deleteResult.RowsAffected == 0 {
				return domain.ErrDuplicateDomain
			}

			if err := tx.Model(&EmailResourceModel{}).
				Where("id = ? AND type = ?", existing.ID, string(domain.ResourceTypeDomain)).
				Updates(map[string]interface{}{
					"owner_user_id": resource.OwnerUserID,
					"updated_at":    now,
				}).Error; err != nil {
				return fmt.Errorf("restore deleted domain root: %w", err)
			}

			restored := &DomainResourceModel{
				ID:                existing.ID,
				OwnerUserID:       resource.OwnerUserID,
				Domain:            dr.Domain,
				DomainTLD:         domain.TLD(dr.Domain),
				MailServerID:      dr.MailServerID,
				Purpose:           string(dr.Purpose),
				Status:            string(dr.Status),
				LastSafeError:     dr.LastSafeError,
				MailboxDailyLimit: normalizeDailyLimit(dr.MailboxDailyLimit, domain.DefaultMailboxDailyLimit),
				AllocBucket:       uint8(existing.ID % 64),
				CreatedAt:         existing.CreatedAt,
				UpdatedAt:         now,
			}
			if err := tx.Create(restored).Error; err != nil {
				return fmt.Errorf("restore deleted domain resource: %w", err)
			}

			resource.ID = existing.ID
			resource.CreatedAt = existing.CreatedAt
			resource.UpdatedAt = now
			dr.ID = existing.ID
			dr.CreatedAt = existing.CreatedAt
			dr.UpdatedAt = now
			return nil
		}

		root := &EmailResourceModel{
			Type:        string(resource.Type),
			OwnerUserID: resource.OwnerUserID,
		}
		if err := tx.Create(root).Error; err != nil {
			return fmt.Errorf("create email resource: %w", err)
		}

		domainModel := &DomainResourceModel{
			ID:                root.ID,
			OwnerUserID:       root.OwnerUserID,
			Domain:            dr.Domain,
			DomainTLD:         domain.TLD(dr.Domain),
			MailServerID:      dr.MailServerID,
			Purpose:           string(dr.Purpose),
			Status:            string(dr.Status),
			LastSafeError:     dr.LastSafeError,
			MailboxDailyLimit: normalizeDailyLimit(dr.MailboxDailyLimit, domain.DefaultMailboxDailyLimit),
			AllocBucket:       uint8(root.ID % 64),
		}
		if err := tx.Create(domainModel).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return domain.ErrDuplicateDomain
			}
			return fmt.Errorf("create domain resource: %w", err)
		}
		resource.ID = root.ID
		resource.CreatedAt = root.CreatedAt
		resource.UpdatedAt = root.UpdatedAt
		dr.ID = domainModel.ID
		dr.CreatedAt = domainModel.CreatedAt
		dr.UpdatedAt = domainModel.UpdatedAt
		return nil
	})
}

func createMicrosoftBatchTx(tx *gorm.DB, resources []domain.EmailResource, ms []domain.MicrosoftResource) error {
	if len(resources) != len(ms) {
		return fmt.Errorf("create microsoft batch: resource count mismatch")
	}
	if len(resources) == 0 {
		return nil
	}

	seenEmails := make(map[string]struct{}, len(ms))
	emails := make([]string, 0, len(ms))
	for i := range ms {
		key := microsoftEmailKey(ms[i].EmailAddress)
		if _, ok := seenEmails[key]; ok {
			return domain.ErrDuplicateEmail
		}
		seenEmails[key] = struct{}{}
		emails = append(emails, strings.TrimSpace(ms[i].EmailAddress))
	}

	existingModels, err := findMicrosoftResourceModelsByEmails(tx, emails, true, true)
	if err != nil {
		return fmt.Errorf("find existing microsoft resources: %w", err)
	}
	existingByEmail := make(map[string]MicrosoftResourceModel, len(existingModels))
	for _, model := range existingModels {
		if domain.MicrosoftResourceStatus(model.Status) != domain.MicrosoftStatusDeleted {
			return domain.ErrDuplicateEmail
		}
		existingByEmail[microsoftEmailKey(model.EmailAddress)] = model
	}

	newResources := make([]domain.EmailResource, 0, len(resources))
	newMicrosoftResources := make([]domain.MicrosoftResource, 0, len(ms))
	restoreRows := make([]microsoftImportRestoreRow, 0, len(existingByEmail))
	now := time.Now().UTC()
	for i := range ms {
		existing, ok := existingByEmail[microsoftEmailKey(ms[i].EmailAddress)]
		if !ok {
			newResources = append(newResources, resources[i])
			newMicrosoftResources = append(newMicrosoftResources, ms[i])
			continue
		}

		restoreRows = append(restoreRows, microsoftImportRestoreRow{
			ID:             existing.ID,
			OwnerUserID:    resources[i].OwnerUserID,
			EmailAddress:   strings.TrimSpace(ms[i].EmailAddress),
			EmailDomain:    microsoftEmailDomain(ms[i].EmailAddress),
			Password:       ms[i].Password,
			ClientID:       ms[i].ClientID,
			RefreshToken:   ms[i].RefreshToken,
			LongLived:      ms[i].LongLived,
			GraphAvailable: ms[i].GraphAvailable,
			RTExpireAt:     ms[i].RTExpireAt,
			Status:         string(ms[i].Status),
			QualityScore:   ms[i].QualityScore,
			LastSafeError:  ms[i].LastSafeError,
			UpdatedAt:      now,
		})

		resources[i].ID = existing.ID
		resources[i].CreatedAt = existing.CreatedAt
		resources[i].UpdatedAt = now
		ms[i].ID = existing.ID
		ms[i].CreatedAt = existing.CreatedAt
		ms[i].UpdatedAt = now
	}
	if err := restoreDeletedMicrosoftBatchTx(tx, restoreRows); err != nil {
		return err
	}
	resources = newResources
	ms = newMicrosoftResources
	if len(resources) == 0 {
		return nil
	}

	rootModels := make([]EmailResourceModel, len(resources))
	for i := range resources {
		rootModels[i] = EmailResourceModel{
			Type:        string(resources[i].Type),
			OwnerUserID: resources[i].OwnerUserID,
		}
	}
	if err := tx.CreateInBatches(&rootModels, resourceImportInsertBatchSize).Error; err != nil {
		return fmt.Errorf("create email resource batch: %w", err)
	}

	msModels := make([]MicrosoftResourceModel, len(ms))
	for i := range ms {
		msModels[i] = *fromMicrosoftDomain(&ms[i])
		msModels[i].ID = rootModels[i].ID
		msModels[i].AllocBucket = uint8(rootModels[i].ID % 64)
	}
	if err := tx.CreateInBatches(&msModels, resourceImportInsertBatchSize).Error; err != nil {
		if isDuplicateKeyError(err) {
			return domain.ErrDuplicateEmail
		}
		return fmt.Errorf("create microsoft resource batch: %w", err)
	}

	for i := range resources {
		resources[i].ID = rootModels[i].ID
		resources[i].CreatedAt = rootModels[i].CreatedAt
		resources[i].UpdatedAt = rootModels[i].UpdatedAt
		ms[i].ID = msModels[i].ID
		ms[i].CreatedAt = msModels[i].CreatedAt
		ms[i].UpdatedAt = msModels[i].UpdatedAt
	}
	return nil
}

type microsoftImportRestoreRow struct {
	ID             uint       `gorm:"column:id"`
	OwnerUserID    uint       `gorm:"column:owner_user_id"`
	EmailAddress   string     `gorm:"column:email_address"`
	EmailDomain    string     `gorm:"column:email_domain"`
	Password       string     `gorm:"column:password"`
	ClientID       string     `gorm:"column:client_id"`
	RefreshToken   string     `gorm:"column:refresh_token"`
	LongLived      bool       `gorm:"column:long_lived"`
	GraphAvailable bool       `gorm:"column:graph_available"`
	RTExpireAt     *time.Time `gorm:"column:rt_expire_at"`
	Status         string     `gorm:"column:status"`
	QualityScore   int        `gorm:"column:quality_score"`
	LastSafeError  string     `gorm:"column:last_safe_error"`
	UpdatedAt      time.Time  `gorm:"column:updated_at"`
}

func restoreDeletedMicrosoftBatchTx(tx *gorm.DB, rows []microsoftImportRestoreRow) error {
	if len(rows) == 0 {
		return nil
	}

	if err := tx.Exec("DROP TEMPORARY TABLE IF EXISTS " + microsoftImportRestoreTempTable).Error; err != nil {
		return fmt.Errorf("drop microsoft import restore temp table: %w", err)
	}
	defer tx.Exec("DROP TEMPORARY TABLE IF EXISTS " + microsoftImportRestoreTempTable)

	if err := tx.Exec(`
CREATE TEMPORARY TABLE ` + microsoftImportRestoreTempTable + ` (
	id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
	owner_user_id BIGINT UNSIGNED NOT NULL,
	email_address VARCHAR(255) NOT NULL,
	email_domain VARCHAR(255) NOT NULL,
	password VARCHAR(512) NOT NULL,
	client_id VARCHAR(255) NOT NULL,
	refresh_token VARCHAR(1024) NOT NULL,
	long_lived BOOLEAN NOT NULL,
	graph_available BOOLEAN NOT NULL,
	rt_expire_at DATETIME(3) NULL,
	status VARCHAR(32) NOT NULL,
	quality_score BIGINT NOT NULL,
	last_safe_error VARCHAR(500) NOT NULL,
	updated_at DATETIME(3) NOT NULL
) ENGINE=InnoDB`).Error; err != nil {
		return fmt.Errorf("create microsoft import restore temp table: %w", err)
	}

	if err := tx.Table(microsoftImportRestoreTempTable).CreateInBatches(&rows, resourceImportInsertBatchSize).Error; err != nil {
		return fmt.Errorf("stage deleted microsoft restore batch: %w", err)
	}

	rootResult := tx.Exec(`
UPDATE email_resources er
JOIN `+microsoftImportRestoreTempTable+` ir ON ir.id = er.id
SET er.owner_user_id = ir.owner_user_id,
	er.updated_at = ir.updated_at
WHERE er.type = ?`, string(domain.ResourceTypeMicrosoft))
	if rootResult.Error != nil {
		return fmt.Errorf("restore deleted email resources: %w", rootResult.Error)
	}

	microsoftResult := tx.Exec(`
UPDATE microsoft_resources mr
JOIN `+microsoftImportRestoreTempTable+` ir ON ir.id = mr.id
SET mr.email_address = ir.email_address,
	mr.email_domain = ir.email_domain,
	mr.password = ir.password,
	mr.client_id = ir.client_id,
	mr.refresh_token = ir.refresh_token,
	mr.long_lived = ir.long_lived,
	mr.graph_available = ir.graph_available,
	mr.rt_expire_at = ir.rt_expire_at,
	mr.for_sale = FALSE,
	mr.status = ir.status,
	mr.quality_score = ir.quality_score,
	mr.last_safe_error = ir.last_safe_error,
	mr.last_allocated_at = NULL,
	mr.updated_at = ir.updated_at
WHERE mr.status = ?`, string(domain.MicrosoftStatusDeleted))
	if microsoftResult.Error != nil {
		return fmt.Errorf("restore deleted microsoft resources: %w", microsoftResult.Error)
	}
	if microsoftResult.RowsAffected != int64(len(rows)) {
		return domain.ErrDuplicateEmail
	}

	return nil
}

func (r *ResourceRepo) FindByID(ctx context.Context, id uint) (*domain.EmailResource, error) {
	var model EmailResourceModel
	err := r.db.WithContext(ctx).First(&model, id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find email resource: %w", err)
	}
	return model.toDomain(), nil
}

func (r *ResourceRepo) FindMicrosoftByID(ctx context.Context, resourceID uint) (*domain.MicrosoftResource, error) {
	var model MicrosoftResourceModel
	err := r.db.WithContext(ctx).First(&model, resourceID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find microsoft resource: %w", err)
	}
	return model.toDomain(), nil
}

func (r *ResourceRepo) FindDomainByID(ctx context.Context, resourceID uint) (*domain.MailDomainResource, error) {
	var model DomainResourceModel
	err := r.db.WithContext(ctx).First(&model, resourceID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find domain resource: %w", err)
	}
	return model.toDomain(), nil
}

func (r *ResourceRepo) FindMicrosoftByEmail(ctx context.Context, email string) (*domain.MicrosoftResource, error) {
	var model MicrosoftResourceModel
	err := r.db.WithContext(ctx).Where("email_address = ?", email).First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find microsoft by email: %w", err)
	}
	return model.toDomain(), nil
}

func (r *ResourceRepo) FindExistingMicrosoftEmails(ctx context.Context, emails []string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	found, err := findMicrosoftEmailAddressesByEmails(r.db.WithContext(ctx), emails)
	if err != nil {
		return nil, fmt.Errorf("find existing microsoft emails: %w", err)
	}
	for _, email := range found {
		result[microsoftEmailKey(email)] = struct{}{}
	}
	return result, nil
}

func (r *ResourceRepo) listQuery(ctx context.Context, ownerUserID uint, filter coreapp.ResourceListFilter) *gorm.DB {
	q := r.db.WithContext(ctx).Model(&EmailResourceModel{})
	if ownerUserID > 0 {
		q = q.Where("email_resources.owner_user_id = ?", ownerUserID)
	}
	switch filter.ResourceType {
	case "":
		q = q.Joins("LEFT JOIN microsoft_resources ms_filter ON ms_filter.id = email_resources.id").
			Joins("LEFT JOIN domain_resources dr_filter ON dr_filter.id = email_resources.id").
			Where("email_resources.type <> ? OR ms_filter.status <> ?", string(domain.ResourceTypeMicrosoft), string(domain.MicrosoftStatusDeleted)).
			Where("email_resources.type <> ? OR dr_filter.status <> ?", string(domain.ResourceTypeDomain), string(domain.DomainStatusDeleted))
		if filter.Status != "" {
			q = q.Where(
				"(email_resources.type = ? AND ms_filter.status = ?) OR (email_resources.type = ? AND dr_filter.status = ?)",
				string(domain.ResourceTypeMicrosoft),
				filter.Status,
				string(domain.ResourceTypeDomain),
				filter.Status,
			)
		}
		if filter.Search != "" {
			like := "%" + filter.Search + "%"
			normalized := "%" + normalizeEmailTypeSearch(filter.Search) + "%"
			q = q.Where(
				"(LOWER(ms_filter.email_address) LIKE ? OR LOWER(SUBSTRING_INDEX(ms_filter.email_address, '@', -1)) LIKE ? OR LOWER(REPLACE(REPLACE(SUBSTRING_INDEX(ms_filter.email_address, '@', -1), '.', '_'), '-', '_')) LIKE ? OR LOWER(dr_filter.domain) LIKE ? OR LOWER(dr_filter.domain_tld) LIKE ?)",
				like,
				like,
				normalized,
				like,
				like,
			)
		}
	case domain.ResourceTypeMicrosoft:
		q = q.Joins("JOIN microsoft_resources ms_filter ON ms_filter.id = email_resources.id AND ms_filter.status <> ?", string(domain.MicrosoftStatusDeleted)).
			Where("email_resources.type = ?", string(domain.ResourceTypeMicrosoft))
		if filter.Status != "" {
			q = q.Where("ms_filter.status = ?", filter.Status)
		}
		if filter.ForSale != nil {
			q = q.Where("ms_filter.for_sale = ?", *filter.ForSale)
		}
		if filter.LongLived != nil {
			q = q.Where("ms_filter.long_lived = ?", *filter.LongLived)
		}
		if filter.GraphAvailable != nil {
			q = q.Where("ms_filter.graph_available = ?", *filter.GraphAvailable)
		}
		if filter.Suffix != "" {
			q = q.Where("ms_filter.email_domain = ?", filter.Suffix)
		}
		if filter.Search != "" {
			like := "%" + filter.Search + "%"
			normalized := "%" + normalizeEmailTypeSearch(filter.Search) + "%"
			q = q.Where(
				"(LOWER(ms_filter.email_address) LIKE ? OR LOWER(SUBSTRING_INDEX(ms_filter.email_address, '@', -1)) LIKE ? OR LOWER(REPLACE(REPLACE(SUBSTRING_INDEX(ms_filter.email_address, '@', -1), '.', '_'), '-', '_')) LIKE ?)",
				like,
				like,
				normalized,
			)
		}
	case domain.ResourceTypeDomain:
		q = q.Joins("JOIN domain_resources dr_filter ON dr_filter.id = email_resources.id AND dr_filter.status <> ?", string(domain.DomainStatusDeleted)).
			Where("email_resources.type = ?", string(domain.ResourceTypeDomain))
		if filter.Status != "" {
			q = q.Where("dr_filter.status = ?", filter.Status)
		}
		if filter.Purpose != "" {
			q = q.Where("dr_filter.purpose = ?", filter.Purpose)
		}
		if filter.TLD != "" {
			q = q.Where("dr_filter.domain_tld = ?", filter.TLD)
		}
		if filter.Search != "" {
			like := "%" + filter.Search + "%"
			q = q.Where("(LOWER(dr_filter.domain) LIKE ? OR LOWER(dr_filter.domain_tld) LIKE ?)", like, like)
		}
	default:
		q = q.Where("1 = 0")
	}
	if filter.CreatedFrom != nil {
		q = q.Where("email_resources.created_at >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		q = q.Where("email_resources.created_at <= ?", *filter.CreatedTo)
	}
	return q
}

func (r *ResourceRepo) List(ctx context.Context, ownerUserID uint, filter coreapp.ResourceListFilter, offset, limit int, afterID uint) ([]domain.EmailResource, error) {
	var models []EmailResourceModel
	q := r.listQuery(ctx, ownerUserID, filter).
		Order("email_resources.id DESC").
		Limit(limit)
	if afterID > 0 {
		q = q.Where("email_resources.id < ?", afterID)
	} else {
		q = q.Offset(offset)
	}
	err := q.Find(&models).Error
	if err != nil {
		return nil, fmt.Errorf("list resources: %w", err)
	}
	result := make([]domain.EmailResource, len(models))
	for i, m := range models {
		result[i] = *m.toDomain()
	}
	return result, nil
}

func (r *ResourceRepo) ListAll(ctx context.Context, filter coreapp.ResourceListFilter, offset, limit int, afterID uint) ([]domain.EmailResource, error) {
	return r.List(ctx, 0, filter, offset, limit, afterID)
}

func (r *ResourceRepo) Count(ctx context.Context, ownerUserID uint, filter coreapp.ResourceListFilter) (int64, error) {
	var count int64
	err := r.listQuery(ctx, ownerUserID, filter).Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("count resources: %w", err)
	}
	return count, nil
}

func (r *ResourceRepo) CountAll(ctx context.Context, filter coreapp.ResourceListFilter) (int64, error) {
	return r.Count(ctx, 0, filter)
}

func (r *ResourceRepo) Facets(ctx context.Context, ownerUserID uint, filter coreapp.ResourceListFilter) (*coreapp.ResourceListFacets, error) {
	cacheKey := resourceFacetsCacheKey(ownerUserID, filter)
	if cached, ok := r.facetsCache.Get(cacheKey); ok {
		return cloneResourceListFacets(&cached), nil
	}

	facets := &coreapp.ResourceListFacets{}

	statusBase := filter
	statusBase.Status = ""
	statusFacets, err := r.resourceStatusFacets(ctx, ownerUserID, statusBase)
	if err != nil {
		return nil, err
	}
	facets.Status = statusFacets

	if filter.ResourceType == domain.ResourceTypeMicrosoft {
		privateBase := filter
		privateBase.ForSale = nil
		facets.Private, err = r.resourceBooleanFacets(ctx, ownerUserID, privateBase, "ms_filter.for_sale", false)
		if err != nil {
			return nil, err
		}

		longLivedBase := filter
		longLivedBase.LongLived = nil
		facets.LongLived, err = r.resourceBooleanFacets(ctx, ownerUserID, longLivedBase, "ms_filter.long_lived", true)
		if err != nil {
			return nil, err
		}

		graphBase := filter
		graphBase.GraphAvailable = nil
		facets.GraphAvailable, err = r.resourceBooleanFacets(ctx, ownerUserID, graphBase, "ms_filter.graph_available", true)
		if err != nil {
			return nil, err
		}

		suffixBase := filter
		suffixBase.Suffix = ""
		facets.Suffixes, err = r.resourceFacetGroups(ctx, ownerUserID, suffixBase, "ms_filter.email_domain")
		if err != nil {
			return nil, err
		}
	}

	if filter.ResourceType == domain.ResourceTypeDomain {
		privateBase := filter
		privateBase.Purpose = ""
		facets.Private, err = r.resourceDomainPrivateFacets(ctx, ownerUserID, privateBase)
		if err != nil {
			return nil, err
		}

		tldBase := filter
		tldBase.TLD = ""
		facets.TLDs, err = r.resourceFacetGroups(ctx, ownerUserID, tldBase, "dr_filter.domain_tld")
		if err != nil {
			return nil, err
		}
	}

	r.facetsCache.Set(cacheKey, *cloneResourceListFacets(facets), resourceFacetsCacheTTL)
	return facets, nil
}

type resourceStatusFacetRow struct {
	All      int64 `gorm:"column:all_count"`
	Normal   int64 `gorm:"column:normal_count"`
	Pending  int64 `gorm:"column:pending_count"`
	Abnormal int64 `gorm:"column:abnormal_count"`
	Disabled int64 `gorm:"column:disabled_count"`
}

func (r *ResourceRepo) resourceStatusFacets(ctx context.Context, ownerUserID uint, filter coreapp.ResourceListFilter) (coreapp.ResourceFacetCounts, error) {
	statusExpr := resourceStatusExpression(filter.ResourceType)
	var row resourceStatusFacetRow
	err := r.listQuery(ctx, ownerUserID, filter).
		Select(
			`COUNT(*) AS all_count,
			COALESCE(SUM(CASE WHEN `+statusExpr+` = ? THEN 1 ELSE 0 END), 0) AS normal_count,
			COALESCE(SUM(CASE WHEN `+statusExpr+` = ? THEN 1 ELSE 0 END), 0) AS pending_count,
			COALESCE(SUM(CASE WHEN `+statusExpr+` = ? THEN 1 ELSE 0 END), 0) AS abnormal_count,
			COALESCE(SUM(CASE WHEN `+statusExpr+` = ? THEN 1 ELSE 0 END), 0) AS disabled_count`,
			string(domain.MicrosoftStatusNormal),
			string(domain.MicrosoftStatusPending),
			string(domain.MicrosoftStatusAbnormal),
			string(domain.MicrosoftStatusDisabled),
		).
		Scan(&row).Error
	if err != nil {
		return coreapp.ResourceFacetCounts{}, fmt.Errorf("resource status facets: %w", err)
	}
	return coreapp.ResourceFacetCounts{
		All:      row.All,
		Normal:   row.Normal,
		Pending:  row.Pending,
		Abnormal: row.Abnormal,
		Disabled: row.Disabled,
	}, nil
}

func resourceStatusExpression(resourceType domain.ResourceType) string {
	switch resourceType {
	case domain.ResourceTypeMicrosoft:
		return "ms_filter.status"
	case domain.ResourceTypeDomain:
		return "dr_filter.status"
	default:
		return "CASE WHEN email_resources.type = 'microsoft' THEN ms_filter.status WHEN email_resources.type = 'domain' THEN dr_filter.status ELSE '' END"
	}
}

type resourceBooleanFacetRow struct {
	All int64 `gorm:"column:all_count"`
	Yes int64 `gorm:"column:yes_count"`
	No  int64 `gorm:"column:no_count"`
}

func (r *ResourceRepo) resourceBooleanFacets(ctx context.Context, ownerUserID uint, filter coreapp.ResourceListFilter, column string, yesValue bool) (coreapp.ResourceBooleanFacets, error) {
	yesSQL := resourceBoolSQL(yesValue)
	noSQL := resourceBoolSQL(!yesValue)
	var row resourceBooleanFacetRow
	err := r.listQuery(ctx, ownerUserID, filter).
		Select(
			`COUNT(*) AS all_count,
			COALESCE(SUM(CASE WHEN ` + column + ` = ` + yesSQL + ` THEN 1 ELSE 0 END), 0) AS yes_count,
			COALESCE(SUM(CASE WHEN ` + column + ` = ` + noSQL + ` THEN 1 ELSE 0 END), 0) AS no_count`,
		).
		Scan(&row).Error
	if err != nil {
		return coreapp.ResourceBooleanFacets{}, fmt.Errorf("resource boolean facets: %w", err)
	}
	return coreapp.ResourceBooleanFacets{All: row.All, Yes: row.Yes, No: row.No}, nil
}

func resourceBoolSQL(value bool) string {
	if value {
		return "TRUE"
	}
	return "FALSE"
}

func (r *ResourceRepo) resourceDomainPrivateFacets(ctx context.Context, ownerUserID uint, filter coreapp.ResourceListFilter) (coreapp.ResourceBooleanFacets, error) {
	var row resourceBooleanFacetRow
	err := r.listQuery(ctx, ownerUserID, filter).
		Select(
			`COUNT(*) AS all_count,
			COALESCE(SUM(CASE WHEN dr_filter.purpose = ? THEN 1 ELSE 0 END), 0) AS yes_count,
			COALESCE(SUM(CASE WHEN dr_filter.purpose = ? THEN 1 ELSE 0 END), 0) AS no_count`,
			string(domain.PurposeNotSale),
			string(domain.PurposeSale),
		).
		Scan(&row).Error
	if err != nil {
		return coreapp.ResourceBooleanFacets{}, fmt.Errorf("resource domain private facets: %w", err)
	}
	return coreapp.ResourceBooleanFacets{All: row.All, Yes: row.Yes, No: row.No}, nil
}

func resourceFacetsCacheKey(ownerUserID uint, filter coreapp.ResourceListFilter) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("owner=%d", ownerUserID))
	b.WriteString("|type=")
	b.WriteString(string(filter.ResourceType))
	b.WriteString("|search=")
	b.WriteString(strings.ToLower(strings.TrimSpace(filter.Search)))
	b.WriteString("|suffix=")
	b.WriteString(strings.ToLower(strings.TrimSpace(filter.Suffix)))
	b.WriteString("|tld=")
	b.WriteString(strings.ToLower(strings.TrimSpace(filter.TLD)))
	b.WriteString("|status=")
	b.WriteString(strings.TrimSpace(filter.Status))
	b.WriteString("|purpose=")
	b.WriteString(strings.TrimSpace(filter.Purpose))
	b.WriteString("|forSale=")
	b.WriteString(resourceBoolPtrKey(filter.ForSale))
	b.WriteString("|longLived=")
	b.WriteString(resourceBoolPtrKey(filter.LongLived))
	b.WriteString("|graph=")
	b.WriteString(resourceBoolPtrKey(filter.GraphAvailable))
	b.WriteString("|from=")
	b.WriteString(resourceTimePtrKey(filter.CreatedFrom))
	b.WriteString("|to=")
	b.WriteString(resourceTimePtrKey(filter.CreatedTo))
	return b.String()
}

func resourceBoolPtrKey(value *bool) string {
	if value == nil {
		return "nil"
	}
	if *value {
		return "true"
	}
	return "false"
}

func resourceTimePtrKey(value *time.Time) string {
	if value == nil {
		return "nil"
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func cloneResourceListFacets(facets *coreapp.ResourceListFacets) *coreapp.ResourceListFacets {
	if facets == nil {
		return nil
	}
	clone := *facets
	if facets.Suffixes != nil {
		clone.Suffixes = append([]coreapp.ResourceKeyFacet(nil), facets.Suffixes...)
	}
	if facets.TLDs != nil {
		clone.TLDs = append([]coreapp.ResourceKeyFacet(nil), facets.TLDs...)
	}
	return &clone
}

func (r *ResourceRepo) resourceFacetGroups(ctx context.Context, ownerUserID uint, filter coreapp.ResourceListFilter, column string) ([]coreapp.ResourceKeyFacet, error) {
	type row struct {
		Key   string `gorm:"column:facet_key"`
		Count int64  `gorm:"column:count"`
	}
	rows := make([]row, 0)
	err := r.listQuery(ctx, ownerUserID, filter).
		Select(column + " AS facet_key, COUNT(*) AS count").
		Where(column + " <> ''").
		Group(column).
		Order("count DESC, facet_key ASC").
		Limit(100).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("resource facet groups: %w", err)
	}
	result := make([]coreapp.ResourceKeyFacet, len(rows))
	for i := range rows {
		result[i] = coreapp.ResourceKeyFacet{Key: rows[i].Key, Count: rows[i].Count}
	}
	return result, nil
}

// ListMicrosoftStatus returns API-safe status for a batch of Microsoft resources.
func (r *ResourceRepo) ListMicrosoftStatus(ctx context.Context, ids []uint) ([]coreapp.MicrosoftStatusResult, error) {
	var models []MicrosoftResourceModel
	err := r.db.WithContext(ctx).
		Where("id IN ?", ids).
		Find(&models).Error
	if err != nil {
		return nil, fmt.Errorf("list microsoft status: %w", err)
	}
	result := make([]coreapp.MicrosoftStatusResult, len(models))
	for i, m := range models {
		result[i] = coreapp.MicrosoftStatusResult{
			ID:             m.ID,
			EmailAddress:   m.EmailAddress,
			ForSale:        m.ForSale,
			LongLived:      m.LongLived,
			GraphAvailable: m.GraphAvailable,
			Status:         m.Status,
			LastSafeError:  m.LastSafeError,
		}
	}
	return result, nil
}

// ListDomainStatus returns API-safe status for a batch of domain resources.
func (r *ResourceRepo) ListDomainStatus(ctx context.Context, ids []uint) ([]coreapp.DomainStatusResult, error) {
	type domainStatusRow struct {
		ID            uint
		Domain        string
		DomainTLD     string
		MailServerID  uint
		Purpose       string
		Status        string
		LastSafeError string
		MailboxCount  int
	}
	var rows []domainStatusRow
	err := r.db.WithContext(ctx).
		Table("domain_resources AS dr").
		Select("dr.id, dr.domain, dr.domain_tld, dr.mail_server_id, dr.purpose, dr.status, dr.last_safe_error, COUNT(gm.id) AS mailbox_count").
		Joins("LEFT JOIN generated_mailboxes gm ON gm.resource_id = dr.id AND gm.owner_user_id = dr.owner_user_id").
		Where("dr.id IN ? AND dr.status <> ?", ids, string(domain.DomainStatusDeleted)).
		Group("dr.id, dr.domain, dr.domain_tld, dr.mail_server_id, dr.purpose, dr.status, dr.last_safe_error").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list domain status: %w", err)
	}
	result := make([]coreapp.DomainStatusResult, len(rows))
	for i, row := range rows {
		result[i] = coreapp.DomainStatusResult{
			ID:            row.ID,
			Domain:        row.Domain,
			DomainTLD:     row.DomainTLD,
			MailServerID:  row.MailServerID,
			Purpose:       row.Purpose,
			Status:        row.Status,
			LastSafeError: row.LastSafeError,
			MailboxCount:  row.MailboxCount,
		}
	}
	return result, nil
}

// UpdateMicrosoftWithLog updates non-credential Microsoft resource fields and writes an OperationLog
// in the same transaction.
func (r *ResourceRepo) UpdateMicrosoftWithLog(ctx context.Context, resource *domain.MicrosoftResource, log *governancedomain.OperationLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updates := map[string]interface{}{
			"for_sale":          resource.ForSale,
			"status":            string(resource.Status),
			"quality_score":     resource.QualityScore,
			"graph_available":   resource.GraphAvailable,
			"plus_daily_limit":  normalizeDailyLimit(resource.PlusDailyLimit, domain.DefaultPlusDailyLimit),
			"last_safe_error":   resource.LastSafeError,
			"last_allocated_at": resource.LastAllocatedAt,
			"updated_at":        time.Now(),
		}
		if err := tx.Model(&MicrosoftResourceModel{}).Where("id = ?", resource.ID).Updates(updates).Error; err != nil {
			return fmt.Errorf("update microsoft resource: %w", err)
		}
		if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
			return fmt.Errorf("create operation log: %w", err)
		}
		return nil
	})
}

// PublishMicrosoftWithLog publishes one owned Microsoft resource and writes an
// OperationLog only when the row actually changes from private to public supply.
func (r *ResourceRepo) PublishMicrosoftWithLog(ctx context.Context, ownerUserID uint, resourceID uint, log governancedomain.OperationLog) (bool, error) {
	if resourceID == 0 {
		return false, domain.ErrResourceNotFound
	}

	published := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root EmailResourceModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND owner_user_id = ? AND type = ?", resourceID, ownerUserID, string(domain.ResourceTypeMicrosoft)).
			First(&root).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrForbiddenResource
			}
			return fmt.Errorf("lock owned microsoft resource: %w", err)
		}

		var ms MicrosoftResourceModel
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", resourceID).
			First(&ms).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrResourceNotFound
			}
			return fmt.Errorf("lock microsoft resource: %w", err)
		}
		if domain.MicrosoftResourceStatus(ms.Status) == domain.MicrosoftStatusDeleted {
			return domain.ErrResourceNotFound
		}
		if ms.ForSale {
			return nil
		}

		result := tx.Model(&MicrosoftResourceModel{}).
			Where("id = ? AND for_sale = ? AND status <> ?", resourceID, false, string(domain.MicrosoftStatusDeleted)).
			Updates(map[string]interface{}{
				"for_sale":   true,
				"updated_at": time.Now().UTC(),
			})
		if result.Error != nil {
			return fmt.Errorf("publish microsoft resource: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return nil
		}

		if log.ResourceID == "" {
			log.ResourceID = fmt.Sprintf("%d", resourceID)
		}
		if err := r.operationLogs.CreateInTx(ctx, tx, &log); err != nil {
			return fmt.Errorf("create operation log: %w", err)
		}
		published = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return published, nil
}

// PublishResourcesBatchWithLog validates all requested resources and publishes the
// eligible private rows in a single transaction. Already-public rows are
// idempotently skipped; deleted rows and binding domain resources are rejected.
func (r *ResourceRepo) PublishResourcesBatchWithLog(ctx context.Context, ownerUserID uint, resourceIDs []uint, microsoftLog governancedomain.OperationLog, domainLog governancedomain.OperationLog) ([]uint, error) {
	if len(resourceIDs) == 0 {
		return nil, domain.ErrResourceNotFound
	}

	publishedIDs := make([]uint, 0, len(resourceIDs))
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var roots []EmailResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id IN ? AND owner_user_id = ?", resourceIDs, ownerUserID).
			Order("id ASC").
			Find(&roots).Error; err != nil {
			return fmt.Errorf("lock owned resources: %w", err)
		}
		if len(roots) != len(resourceIDs) {
			return domain.ErrForbiddenResource
		}

		microsoftIDs := make([]uint, 0, len(roots))
		domainIDs := make([]uint, 0, len(roots))
		for _, root := range roots {
			switch domain.ResourceType(root.Type) {
			case domain.ResourceTypeMicrosoft:
				microsoftIDs = append(microsoftIDs, root.ID)
			case domain.ResourceTypeDomain:
				domainIDs = append(domainIDs, root.ID)
			default:
				return domain.ErrInvalidResourceType
			}
		}

		if len(microsoftIDs) > 0 {
			ids, err := publishLockedMicrosoftRows(ctx, tx, microsoftIDs, microsoftLog, r.operationLogs)
			if err != nil {
				return err
			}
			publishedIDs = append(publishedIDs, ids...)
		}
		if len(domainIDs) > 0 {
			ids, err := publishLockedDomainRows(ctx, tx, domainIDs, domainLog, r.operationLogs)
			if err != nil {
				return err
			}
			publishedIDs = append(publishedIDs, ids...)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}
	return publishedIDs, nil
}

func publishLockedMicrosoftRows(ctx context.Context, tx *gorm.DB, resourceIDs []uint, baseLog governancedomain.OperationLog, operationLogs *governanceinfra.OperationLogRepo) ([]uint, error) {
	var rows []MicrosoftResourceModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", resourceIDs).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("lock microsoft resources: %w", err)
	}
	if len(rows) != len(resourceIDs) {
		return nil, domain.ErrResourceNotFound
	}

	idsToPublish := make([]uint, 0, len(rows))
	for _, row := range rows {
		if domain.MicrosoftResourceStatus(row.Status) == domain.MicrosoftStatusDeleted {
			return nil, domain.ErrResourceNotFound
		}
		if !row.ForSale {
			idsToPublish = append(idsToPublish, row.ID)
		}
	}
	if len(idsToPublish) == 0 {
		return nil, nil
	}

	result := tx.Model(&MicrosoftResourceModel{}).
		Where("id IN ? AND for_sale = ? AND status <> ?", idsToPublish, false, string(domain.MicrosoftStatusDeleted)).
		Updates(map[string]interface{}{
			"for_sale":   true,
			"updated_at": time.Now().UTC(),
		})
	if result.Error != nil {
		return nil, fmt.Errorf("publish microsoft resources: %w", result.Error)
	}
	if int(result.RowsAffected) != len(idsToPublish) {
		return nil, domain.ErrResourceNotPrivate
	}

	for _, id := range idsToPublish {
		log := baseLog
		log.ResourceID = fmt.Sprintf("%d", id)
		if err := operationLogs.CreateInTx(ctx, tx, &log); err != nil {
			return nil, fmt.Errorf("create operation log: %w", err)
		}
	}

	return idsToPublish, nil
}

func publishLockedDomainRows(ctx context.Context, tx *gorm.DB, resourceIDs []uint, baseLog governancedomain.OperationLog, operationLogs *governanceinfra.OperationLogRepo) ([]uint, error) {
	var rows []DomainResourceModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", resourceIDs).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("lock domain resources: %w", err)
	}
	if len(rows) != len(resourceIDs) {
		return nil, domain.ErrResourceNotFound
	}

	idsToPublish := make([]uint, 0, len(rows))
	for _, row := range rows {
		switch domain.MailDomainStatus(row.Status) {
		case domain.DomainStatusDeleted:
			return nil, domain.ErrResourceNotFound
		}

		switch domain.ResourcePurpose(row.Purpose) {
		case domain.PurposeNotSale:
			idsToPublish = append(idsToPublish, row.ID)
		case domain.PurposeSale:
			continue
		case domain.PurposeBinding:
			return nil, domain.ErrResourceNotPrivate
		default:
			return nil, domain.ErrInvalidPurpose
		}
	}
	if len(idsToPublish) == 0 {
		return nil, nil
	}

	result := tx.Model(&DomainResourceModel{}).
		Where("id IN ? AND purpose = ? AND status <> ?", idsToPublish, string(domain.PurposeNotSale), string(domain.DomainStatusDeleted)).
		Updates(map[string]interface{}{
			"purpose":    string(domain.PurposeSale),
			"updated_at": time.Now().UTC(),
		})
	if result.Error != nil {
		return nil, fmt.Errorf("publish domain resources: %w", result.Error)
	}
	if int(result.RowsAffected) != len(idsToPublish) {
		return nil, domain.ErrResourceNotPrivate
	}

	for _, id := range idsToPublish {
		log := baseLog
		log.ResourceID = fmt.Sprintf("%d", id)
		if err := operationLogs.CreateInTx(ctx, tx, &log); err != nil {
			return nil, fmt.Errorf("create operation log: %w", err)
		}
	}

	return idsToPublish, nil
}

// PublishResourcesByFilterWithLog publishes owned private resources matching a
// server-side filter. It works in small transactions so "all matching" commands
// do not build huge HTTP payloads or single massive IN clauses.
func (r *ResourceRepo) PublishResourcesByFilterWithLog(ctx context.Context, ownerUserID uint, filter coreapp.ResourceBulkFilter, microsoftLog governancedomain.OperationLog, domainLog governancedomain.OperationLog) (int, error) {
	switch filter.ResourceType {
	case domain.ResourceTypeMicrosoft:
		return r.publishMicrosoftByFilterWithLog(ctx, ownerUserID, filter, microsoftLog)
	case domain.ResourceTypeDomain:
		return r.publishDomainByFilterWithLog(ctx, ownerUserID, filter, domainLog)
	default:
		return 0, domain.ErrInvalidResourceType
	}
}

func (r *ResourceRepo) publishMicrosoftByFilterWithLog(ctx context.Context, ownerUserID uint, filter coreapp.ResourceBulkFilter, log governancedomain.OperationLog) (int, error) {
	published := 0
	for {
		candidateCount := 0
		chunkPublished := 0
		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			ids, err := selectMicrosoftBulkIDs(ctx, tx, ownerUserID, filter)
			if err != nil {
				return err
			}
			candidateCount = len(ids)
			if len(ids) == 0 {
				return nil
			}

			publishedIDs, err := publishLockedMicrosoftRows(ctx, tx, ids, log, r.operationLogs)
			if err != nil {
				return err
			}
			chunkPublished = len(publishedIDs)
			return nil
		})
		if err != nil {
			return published, err
		}
		if candidateCount == 0 {
			return published, nil
		}
		published += chunkPublished
	}
}

func (r *ResourceRepo) publishDomainByFilterWithLog(ctx context.Context, ownerUserID uint, filter coreapp.ResourceBulkFilter, log governancedomain.OperationLog) (int, error) {
	published := 0
	for {
		candidateCount := 0
		chunkPublished := 0
		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			ids, err := selectDomainBulkIDs(ctx, tx, ownerUserID, filter)
			if err != nil {
				return err
			}
			candidateCount = len(ids)
			if len(ids) == 0 {
				return nil
			}

			publishedIDs, err := publishLockedDomainRows(ctx, tx, ids, log, r.operationLogs)
			if err != nil {
				return err
			}
			chunkPublished = len(publishedIDs)
			return nil
		})
		if err != nil {
			return published, err
		}
		if candidateCount == 0 {
			return published, nil
		}
		published += chunkPublished
	}
}

func selectMicrosoftBulkIDs(ctx context.Context, tx *gorm.DB, ownerUserID uint, filter coreapp.ResourceBulkFilter) ([]uint, error) {
	var ids []uint
	err := applyMicrosoftBulkFilter(tx.WithContext(ctx), ownerUserID, filter).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Order("er.id ASC").
		Limit(resourceBulkMutationChunkSize).
		Pluck("er.id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("select microsoft bulk resources: %w", err)
	}
	return ids, nil
}

func selectDomainBulkIDs(ctx context.Context, tx *gorm.DB, ownerUserID uint, filter coreapp.ResourceBulkFilter) ([]uint, error) {
	var ids []uint
	err := applyDomainBulkFilter(tx.WithContext(ctx), ownerUserID, filter).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Order("er.id ASC").
		Limit(resourceBulkMutationChunkSize).
		Pluck("er.id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("select domain bulk resources: %w", err)
	}
	return ids, nil
}

func applyMicrosoftBulkFilter(db *gorm.DB, ownerUserID uint, filter coreapp.ResourceBulkFilter) *gorm.DB {
	q := db.Table("email_resources AS er").
		Joins("JOIN microsoft_resources ms ON ms.id = er.id")
	if strings.TrimSpace(filter.Suffix) != "" {
		q = db.Table("microsoft_resources AS ms").
			Joins("STRAIGHT_JOIN email_resources AS er ON er.id = ms.id")
	}
	q = q.Where("er.owner_user_id = ? AND er.type = ?", ownerUserID, string(domain.ResourceTypeMicrosoft)).
		Where("ms.for_sale = ? AND ms.status <> ?", false, string(domain.MicrosoftStatusDeleted))

	if filter.Status != "" {
		q = q.Where("ms.status = ?", filter.Status)
	}
	if filter.LongLived != nil {
		q = q.Where("ms.long_lived = ?", *filter.LongLived)
	}
	if filter.GraphAvailable != nil {
		q = q.Where("ms.graph_available = ?", *filter.GraphAvailable)
	}
	if filter.CreatedFrom != nil {
		q = q.Where("er.created_at >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		q = q.Where("er.created_at <= ?", *filter.CreatedTo)
	}
	if filter.Suffix != "" {
		suffix := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(filter.Suffix)), "@")
		if suffix != "" {
			q = q.Where("ms.email_domain = ?", suffix)
		}
	}
	if filter.Search != "" {
		like := "%" + filter.Search + "%"
		normalized := "%" + normalizeEmailTypeSearch(filter.Search) + "%"
		q = q.Where(
			"(LOWER(ms.email_address) LIKE ? OR LOWER(SUBSTRING_INDEX(ms.email_address, '@', -1)) LIKE ? OR LOWER(REPLACE(REPLACE(SUBSTRING_INDEX(ms.email_address, '@', -1), '.', '_'), '-', '_')) LIKE ?)",
			like,
			like,
			normalized,
		)
	}
	return q
}

func applyDomainBulkFilter(db *gorm.DB, ownerUserID uint, filter coreapp.ResourceBulkFilter) *gorm.DB {
	q := db.Table("email_resources AS er").
		Joins("JOIN domain_resources dr ON dr.id = er.id")
	if strings.TrimSpace(filter.TLD) != "" {
		q = db.Table("domain_resources AS dr").
			Joins("STRAIGHT_JOIN email_resources AS er ON er.id = dr.id")
	}
	q = q.Where("er.owner_user_id = ? AND er.type = ?", ownerUserID, string(domain.ResourceTypeDomain)).
		Where("dr.owner_user_id = ?", ownerUserID).
		Where("dr.purpose = ? AND dr.status <> ?", string(domain.PurposeNotSale), string(domain.DomainStatusDeleted))

	if filter.Status != "" {
		q = q.Where("dr.status = ?", filter.Status)
	}
	if filter.CreatedFrom != nil {
		q = q.Where("er.created_at >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		q = q.Where("er.created_at <= ?", *filter.CreatedTo)
	}
	if filter.TLD != "" {
		tld, err := domain.NormalizeDomainSuffix(filter.TLD)
		if err != nil {
			q = q.Where("1 = 0")
		} else {
			q = q.Where("dr.domain_tld = ?", tld)
		}
	}
	if filter.Search != "" {
		q = q.Where("LOWER(dr.domain) LIKE ?", "%"+filter.Search+"%")
	}
	return q
}

func normalizeEmailTypeSearch(value string) string {
	return strings.NewReplacer(".", "_", "-", "_", "@", "").Replace(value)
}

// PublishDomainWithLog publishes one owned private domain resource and writes an
// OperationLog only when the row actually changes from private to public supply.
func (r *ResourceRepo) PublishDomainWithLog(ctx context.Context, ownerUserID uint, resourceID uint, log governancedomain.OperationLog) (bool, error) {
	if resourceID == 0 {
		return false, domain.ErrResourceNotFound
	}

	published := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root EmailResourceModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND owner_user_id = ? AND type = ?", resourceID, ownerUserID, string(domain.ResourceTypeDomain)).
			First(&root).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrForbiddenResource
			}
			return fmt.Errorf("lock owned domain resource: %w", err)
		}

		var dr DomainResourceModel
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", resourceID).
			First(&dr).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrResourceNotFound
			}
			return fmt.Errorf("lock domain resource: %w", err)
		}
		if domain.MailDomainStatus(dr.Status) == domain.DomainStatusDeleted {
			return domain.ErrResourceNotFound
		}
		if domain.ResourcePurpose(dr.Purpose) == domain.PurposeSale {
			return nil
		}
		if domain.ResourcePurpose(dr.Purpose) != domain.PurposeNotSale {
			return domain.ErrResourceNotPrivate
		}

		result := tx.Model(&DomainResourceModel{}).
			Where("id = ? AND purpose = ? AND status <> ?", resourceID, string(domain.PurposeNotSale), string(domain.DomainStatusDeleted)).
			Updates(map[string]interface{}{
				"purpose":    string(domain.PurposeSale),
				"updated_at": time.Now().UTC(),
			})
		if result.Error != nil {
			return fmt.Errorf("publish domain resource: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return nil
		}

		if log.ResourceID == "" {
			log.ResourceID = fmt.Sprintf("%d", resourceID)
		}
		if err := r.operationLogs.CreateInTx(ctx, tx, &log); err != nil {
			return fmt.Errorf("create operation log: %w", err)
		}
		published = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return published, nil
}

// DeletePrivateMicrosoftWithLog logically removes one owned Microsoft resource while it is still private.
func (r *ResourceRepo) DeletePrivateMicrosoftWithLog(ctx context.Context, ownerUserID uint, resourceID uint, log governancedomain.OperationLog) error {
	if resourceID == 0 {
		return domain.ErrResourceNotFound
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root EmailResourceModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND owner_user_id = ? AND type = ?", resourceID, ownerUserID, string(domain.ResourceTypeMicrosoft)).
			First(&root).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrForbiddenResource
			}
			return fmt.Errorf("lock owned microsoft resource: %w", err)
		}

		var ms MicrosoftResourceModel
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", resourceID).
			First(&ms).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrResourceNotFound
			}
			return fmt.Errorf("lock microsoft resource: %w", err)
		}
		if ms.ForSale {
			return domain.ErrResourceNotPrivate
		}
		if domain.MicrosoftResourceStatus(ms.Status) == domain.MicrosoftStatusDeleted {
			return domain.ErrResourceNotFound
		}
		msDomain := ms.toDomain()
		if err := msDomain.MarkDeleted(); err != nil {
			return err
		}

		result := tx.Model(&MicrosoftResourceModel{}).
			Where("id = ? AND for_sale = ? AND status <> ?", resourceID, false, string(domain.MicrosoftStatusDeleted)).
			Updates(map[string]interface{}{
				"status":            string(msDomain.Status),
				"last_safe_error":   msDomain.LastSafeError,
				"last_allocated_at": msDomain.LastAllocatedAt,
				"updated_at":        time.Now().UTC(),
			})
		if result.Error != nil {
			return fmt.Errorf("logical delete private microsoft resource: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return domain.ErrResourceNotPrivate
		}

		if log.ResourceID == "" {
			log.ResourceID = fmt.Sprintf("%d", resourceID)
		}
		if err := r.operationLogs.CreateInTx(ctx, tx, &log); err != nil {
			return fmt.Errorf("create operation log: %w", err)
		}
		return nil
	})
}

// DeletePrivateDomainWithLog logically removes one owned domain resource while it is still private.
func (r *ResourceRepo) DeletePrivateDomainWithLog(ctx context.Context, ownerUserID uint, resourceID uint, log governancedomain.OperationLog) error {
	if resourceID == 0 {
		return domain.ErrResourceNotFound
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root EmailResourceModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND owner_user_id = ? AND type = ?", resourceID, ownerUserID, string(domain.ResourceTypeDomain)).
			First(&root).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrForbiddenResource
			}
			return fmt.Errorf("lock owned domain resource: %w", err)
		}

		var dr DomainResourceModel
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", resourceID).
			First(&dr).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrResourceNotFound
			}
			return fmt.Errorf("lock domain resource: %w", err)
		}
		if domain.ResourcePurpose(dr.Purpose) != domain.PurposeNotSale {
			return domain.ErrResourceNotPrivate
		}
		if domain.MailDomainStatus(dr.Status) == domain.DomainStatusDeleted {
			return domain.ErrResourceNotFound
		}
		drDomain := dr.toDomain()
		if err := drDomain.MarkDeleted(); err != nil {
			return err
		}

		result := tx.Model(&DomainResourceModel{}).
			Where("id = ? AND purpose = ? AND status <> ?", resourceID, string(domain.PurposeNotSale), string(domain.DomainStatusDeleted)).
			Updates(map[string]interface{}{
				"status":            string(drDomain.Status),
				"last_allocated_at": drDomain.LastAllocatedAt,
				"updated_at":        time.Now().UTC(),
			})
		if result.Error != nil {
			return fmt.Errorf("logical delete private domain resource: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return domain.ErrResourceNotPrivate
		}

		if log.ResourceID == "" {
			log.ResourceID = fmt.Sprintf("%d", resourceID)
		}
		if err := r.operationLogs.CreateInTx(ctx, tx, &log); err != nil {
			return fmt.Errorf("create operation log: %w", err)
		}
		return nil
	})
}

// DeleteResourcesBatchWithLog logically deletes owned private resources in a single transaction.
// Non-private resources are skipped; missing or non-owned roots are rejected to prevent enumeration and partial cross-owner changes.
func (r *ResourceRepo) DeleteResourcesBatchWithLog(ctx context.Context, ownerUserID uint, resourceIDs []uint, microsoftLog governancedomain.OperationLog, domainLog governancedomain.OperationLog) ([]uint, error) {
	if len(resourceIDs) == 0 {
		return nil, domain.ErrResourceNotFound
	}

	deletedIDs := make([]uint, 0, len(resourceIDs))
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var roots []EmailResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id IN ? AND owner_user_id = ?", resourceIDs, ownerUserID).
			Order("id ASC").
			Find(&roots).Error; err != nil {
			return fmt.Errorf("lock owned resources for delete: %w", err)
		}
		if len(roots) != len(resourceIDs) {
			return domain.ErrForbiddenResource
		}

		microsoftIDs := make([]uint, 0, len(roots))
		domainIDs := make([]uint, 0, len(roots))
		for _, root := range roots {
			switch domain.ResourceType(root.Type) {
			case domain.ResourceTypeMicrosoft:
				microsoftIDs = append(microsoftIDs, root.ID)
			case domain.ResourceTypeDomain:
				domainIDs = append(domainIDs, root.ID)
			default:
				return domain.ErrInvalidResourceType
			}
		}

		if len(microsoftIDs) > 0 {
			ids, err := deleteLockedMicrosoftRows(ctx, tx, microsoftIDs, microsoftLog, r.operationLogs)
			if err != nil {
				return err
			}
			deletedIDs = append(deletedIDs, ids...)
		}
		if len(domainIDs) > 0 {
			ids, err := deleteLockedDomainRows(ctx, tx, domainIDs, domainLog, r.operationLogs)
			if err != nil {
				return err
			}
			deletedIDs = append(deletedIDs, ids...)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}
	return deletedIDs, nil
}

// DeleteResourcesByFilterWithLog logically deletes owned private resources
// matching a server-side filter with one set-based update.
func (r *ResourceRepo) DeleteResourcesByFilterWithLog(ctx context.Context, ownerUserID uint, filter coreapp.ResourceBulkFilter, microsoftLog governancedomain.OperationLog, domainLog governancedomain.OperationLog) (int, error) {
	switch filter.ResourceType {
	case domain.ResourceTypeMicrosoft:
		return r.deleteMicrosoftByFilterWithLog(ctx, ownerUserID, filter, microsoftLog)
	case domain.ResourceTypeDomain:
		return r.deleteDomainByFilterWithLog(ctx, ownerUserID, filter, domainLog)
	default:
		return 0, domain.ErrInvalidResourceType
	}
}

func (r *ResourceRepo) deleteMicrosoftByFilterWithLog(ctx context.Context, ownerUserID uint, filter coreapp.ResourceBulkFilter, log governancedomain.OperationLog) (int, error) {
	deleted := 0
	for {
		candidateCount := 0
		chunkDeleted := 0
		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			ids, err := selectMicrosoftBulkIDs(ctx, tx, ownerUserID, filter)
			if err != nil {
				return err
			}
			candidateCount = len(ids)
			if len(ids) == 0 {
				return nil
			}

			result := tx.Model(&MicrosoftResourceModel{}).
				Where("id IN ? AND for_sale = ? AND status <> ?", ids, false, string(domain.MicrosoftStatusDeleted)).
				Updates(map[string]interface{}{
					"status":            string(domain.MicrosoftStatusDeleted),
					"last_safe_error":   "",
					"last_allocated_at": nil,
					"updated_at":        time.Now().UTC(),
				})
			if result.Error != nil {
				return fmt.Errorf("delete microsoft resources by filter: %w", result.Error)
			}
			chunkDeleted = int(result.RowsAffected)
			if chunkDeleted == 0 {
				return nil
			}
			return createBulkMutationLog(ctx, tx, log, int64(chunkDeleted), r.operationLogs)
		})
		if err != nil {
			return deleted, err
		}
		if candidateCount == 0 {
			return deleted, nil
		}
		deleted += chunkDeleted
	}
}

func (r *ResourceRepo) deleteDomainByFilterWithLog(ctx context.Context, ownerUserID uint, filter coreapp.ResourceBulkFilter, log governancedomain.OperationLog) (int, error) {
	deleted := 0
	for {
		candidateCount := 0
		chunkDeleted := 0
		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			ids, err := selectDomainBulkIDs(ctx, tx, ownerUserID, filter)
			if err != nil {
				return err
			}
			candidateCount = len(ids)
			if len(ids) == 0 {
				return nil
			}

			result := tx.Model(&DomainResourceModel{}).
				Where("id IN ? AND purpose = ? AND status <> ?", ids, string(domain.PurposeNotSale), string(domain.DomainStatusDeleted)).
				Updates(map[string]interface{}{
					"status":            string(domain.DomainStatusDeleted),
					"last_safe_error":   "",
					"last_allocated_at": nil,
					"updated_at":        time.Now().UTC(),
				})
			if result.Error != nil {
				return fmt.Errorf("delete domain resources by filter: %w", result.Error)
			}
			chunkDeleted = int(result.RowsAffected)
			if chunkDeleted == 0 {
				return nil
			}
			return createBulkMutationLog(ctx, tx, log, int64(chunkDeleted), r.operationLogs)
		})
		if err != nil {
			return deleted, err
		}
		if candidateCount == 0 {
			return deleted, nil
		}
		deleted += chunkDeleted
	}
}

func createBulkMutationLog(ctx context.Context, tx *gorm.DB, log governancedomain.OperationLog, affected int64, operationLogs *governanceinfra.OperationLogRepo) error {
	log.ResourceID = resourceBulkOperationLogResourceID
	log.SafeSummary = bulkMutationSummary(log.SafeSummary, affected)
	if err := operationLogs.CreateInTx(ctx, tx, &log); err != nil {
		return fmt.Errorf("create operation log: %w", err)
	}
	return nil
}

func bulkMutationSummary(base string, affected int64) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "Bulk resource command completed."
	}
	base = strings.TrimSuffix(base, ".")
	return fmt.Sprintf("%s. Count: %d.", base, affected)
}

func deleteLockedMicrosoftRows(ctx context.Context, tx *gorm.DB, resourceIDs []uint, baseLog governancedomain.OperationLog, operationLogs *governanceinfra.OperationLogRepo) ([]uint, error) {
	var rows []MicrosoftResourceModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", resourceIDs).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("lock microsoft resources for delete: %w", err)
	}
	if len(rows) != len(resourceIDs) {
		return nil, domain.ErrResourceNotFound
	}

	idsToDelete := make([]uint, 0, len(rows))
	for _, row := range rows {
		if domain.MicrosoftResourceStatus(row.Status) == domain.MicrosoftStatusDeleted || row.ForSale {
			continue
		}
		msDomain := row.toDomain()
		if err := msDomain.MarkDeleted(); err != nil {
			return nil, err
		}
		idsToDelete = append(idsToDelete, row.ID)
	}
	if len(idsToDelete) == 0 {
		return nil, nil
	}

	result := tx.Model(&MicrosoftResourceModel{}).
		Where("id IN ? AND for_sale = ? AND status <> ?", idsToDelete, false, string(domain.MicrosoftStatusDeleted)).
		Updates(map[string]interface{}{
			"status":            string(domain.MicrosoftStatusDeleted),
			"last_safe_error":   "",
			"last_allocated_at": nil,
			"updated_at":        time.Now().UTC(),
		})
	if result.Error != nil {
		return nil, fmt.Errorf("delete microsoft resources: %w", result.Error)
	}
	if int(result.RowsAffected) != len(idsToDelete) {
		return nil, domain.ErrResourceNotPrivate
	}

	for _, id := range idsToDelete {
		log := baseLog
		log.ResourceID = fmt.Sprintf("%d", id)
		if err := operationLogs.CreateInTx(ctx, tx, &log); err != nil {
			return nil, fmt.Errorf("create operation log: %w", err)
		}
	}

	return idsToDelete, nil
}

func deleteLockedDomainRows(ctx context.Context, tx *gorm.DB, resourceIDs []uint, baseLog governancedomain.OperationLog, operationLogs *governanceinfra.OperationLogRepo) ([]uint, error) {
	var rows []DomainResourceModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", resourceIDs).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("lock domain resources for delete: %w", err)
	}
	if len(rows) != len(resourceIDs) {
		return nil, domain.ErrResourceNotFound
	}

	idsToDelete := make([]uint, 0, len(rows))
	for _, row := range rows {
		if domain.MailDomainStatus(row.Status) == domain.DomainStatusDeleted {
			continue
		}
		switch domain.ResourcePurpose(row.Purpose) {
		case domain.PurposeNotSale:
			drDomain := row.toDomain()
			if err := drDomain.MarkDeleted(); err != nil {
				return nil, err
			}
			idsToDelete = append(idsToDelete, row.ID)
		case domain.PurposeSale, domain.PurposeBinding:
			continue
		default:
			return nil, domain.ErrInvalidPurpose
		}
	}
	if len(idsToDelete) == 0 {
		return nil, nil
	}

	result := tx.Model(&DomainResourceModel{}).
		Where("id IN ? AND purpose = ? AND status <> ?", idsToDelete, string(domain.PurposeNotSale), string(domain.DomainStatusDeleted)).
		Updates(map[string]interface{}{
			"status":            string(domain.DomainStatusDeleted),
			"last_safe_error":   "",
			"last_allocated_at": nil,
			"updated_at":        time.Now().UTC(),
		})
	if result.Error != nil {
		return nil, fmt.Errorf("delete domain resources: %w", result.Error)
	}
	if int(result.RowsAffected) != len(idsToDelete) {
		return nil, domain.ErrResourceNotPrivate
	}

	for _, id := range idsToDelete {
		log := baseLog
		log.ResourceID = fmt.Sprintf("%d", id)
		if err := operationLogs.CreateInTx(ctx, tx, &log); err != nil {
			return nil, fmt.Errorf("create operation log: %w", err)
		}
	}

	return idsToDelete, nil
}

// UpdateDomainWithLog updates a domain resource and writes an OperationLog
// in the same transaction.
func (r *ResourceRepo) UpdateDomainWithLog(ctx context.Context, resource *domain.MailDomainResource, log *governancedomain.OperationLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		domainName, err := domain.NormalizeDomainName(resource.Domain)
		if err != nil {
			return err
		}
		resource.Domain = domainName
		updates := map[string]interface{}{
			"domain":              domainName,
			"domain_tld":          domain.TLD(domainName),
			"mail_server_id":      resource.MailServerID,
			"purpose":             string(resource.Purpose),
			"status":              string(resource.Status),
			"mailbox_daily_limit": normalizeDailyLimit(resource.MailboxDailyLimit, domain.DefaultMailboxDailyLimit),
			"last_safe_error":     resource.LastSafeError,
			"last_allocated_at":   resource.LastAllocatedAt,
			"updated_at":          time.Now(),
		}
		if err := tx.Model(&DomainResourceModel{}).Where("id = ?", resource.ID).Updates(updates).Error; err != nil {
			return fmt.Errorf("update domain resource: %w", err)
		}
		if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
			return fmt.Errorf("create operation log: %w", err)
		}
		return nil
	})
}
