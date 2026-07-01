package infra

import (
	"context"
	"errors"
	"fmt"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"

	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	"gorm.io/gorm"
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
	Password        string     `gorm:"type:varchar(512);not null"`
	ClientID        string     `gorm:"type:varchar(255);not null;default:'';column:client_id"`
	RefreshToken    string     `gorm:"type:varchar(1024);not null;default:'';column:refresh_token"`
	RTExpireAt      *time.Time `gorm:"column:rt_expire_at"`
	ForSale         bool       `gorm:"not null;default:true;column:for_sale"`
	Status          string     `gorm:"type:varchar(32);not null;default:'pending'"`
	QualityScore    int        `gorm:"not null;default:0;column:quality_score"`
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
		RTExpireAt:      m.RTExpireAt,
		ForSale:         m.ForSale,
		Status:          domain.MicrosoftResourceStatus(m.Status),
		QualityScore:    m.QualityScore,
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
		Password:        ms.Password,
		ClientID:        ms.ClientID,
		RefreshToken:    ms.RefreshToken,
		RTExpireAt:      ms.RTExpireAt,
		ForSale:         ms.ForSale,
		Status:          string(ms.Status),
		QualityScore:    ms.QualityScore,
		LastSafeError:   ms.LastSafeError,
		LastAllocatedAt: ms.LastAllocatedAt,
		CreatedAt:       ms.CreatedAt,
		UpdatedAt:       ms.UpdatedAt,
	}
}

// DomainResourceModel is the GORM model for the domain_resources table.
type DomainResourceModel struct {
	ID              uint       `gorm:"primaryKey"`
	Domain          string     `gorm:"type:varchar(255);not null;uniqueIndex"`
	OwnerUserID     uint       `gorm:"not null;column:owner_user_id"`
	MailServerID    uint       `gorm:"not null;column:mail_server_id"`
	Purpose         string     `gorm:"type:varchar(32);not null;default:'sale'"`
	Status          string     `gorm:"type:varchar(32);not null;default:'dns_abnormal'"`
	LastAllocatedAt *time.Time `gorm:"column:last_allocated_at"`
	CreatedAt       time.Time  `gorm:"not null;autoCreateTime"`
	UpdatedAt       time.Time  `gorm:"not null;autoUpdateTime"`
}

func (DomainResourceModel) TableName() string {
	return "domain_resources"
}

func (m *DomainResourceModel) toDomain() *domain.DomainResource {
	return &domain.DomainResource{
		ID:              m.ID,
		Domain:          m.Domain,
		MailServerID:    m.MailServerID,
		Purpose:         domain.ResourcePurpose(m.Purpose),
		Status:          domain.DomainResourceStatus(m.Status),
		LastAllocatedAt: m.LastAllocatedAt,
		CreatedAt:       m.CreatedAt,
		UpdatedAt:       m.UpdatedAt,
	}
}

func fromDomainDomain(dr *domain.DomainResource) *DomainResourceModel {
	return &DomainResourceModel{
		ID:              dr.ID,
		Domain:          dr.Domain,
		MailServerID:    dr.MailServerID,
		Purpose:         string(dr.Purpose),
		Status:          string(dr.Status),
		LastAllocatedAt: dr.LastAllocatedAt,
		CreatedAt:       dr.CreatedAt,
		UpdatedAt:       dr.UpdatedAt,
	}
}

// --- ResourceRepo ---

// ResourceRepo implements app.EmailResourceRepository using GORM.
type ResourceRepo struct {
	db            *gorm.DB
	operationLogs *governanceinfra.OperationLogRepo
}

// NewResourceRepo creates a new GORM-backed resource repository.
func NewResourceRepo(db *gorm.DB) *ResourceRepo {
	return &ResourceRepo{
		db:            db,
		operationLogs: governanceinfra.NewOperationLogRepo(db),
	}
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

func (r *ResourceRepo) CreateDomain(ctx context.Context, resource *domain.EmailResource, dr *domain.DomainResource) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root := &EmailResourceModel{
			Type:        string(resource.Type),
			OwnerUserID: resource.OwnerUserID,
		}
		if err := tx.Create(root).Error; err != nil {
			return fmt.Errorf("create email resource: %w", err)
		}

		domainModel := &DomainResourceModel{
			ID:           root.ID,
			OwnerUserID:  root.OwnerUserID,
			Domain:       dr.Domain,
			MailServerID: dr.MailServerID,
			Purpose:      string(dr.Purpose),
			Status:       string(dr.Status),
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

func (r *ResourceRepo) CreateMicrosoftBatch(ctx context.Context, resources []domain.EmailResource, ms []domain.MicrosoftResource) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i := range resources {
			root := &EmailResourceModel{
				Type:        string(resources[i].Type),
				OwnerUserID: resources[i].OwnerUserID,
			}
			if err := tx.Create(root).Error; err != nil {
				return fmt.Errorf("create email resource batch: %w", err)
			}

			msModel := fromMicrosoftDomain(&ms[i])
			msModel.ID = root.ID
			if err := tx.Create(msModel).Error; err != nil {
				if errors.Is(err, gorm.ErrDuplicatedKey) {
					return domain.ErrDuplicateEmail
				}
				return fmt.Errorf("create microsoft resource batch: %w", err)
			}

			resources[i].ID = root.ID
			resources[i].CreatedAt = root.CreatedAt
			resources[i].UpdatedAt = root.UpdatedAt
			ms[i].ID = msModel.ID
			ms[i].CreatedAt = msModel.CreatedAt
			ms[i].UpdatedAt = msModel.UpdatedAt
		}
		return nil
	})
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

func (r *ResourceRepo) FindDomainByID(ctx context.Context, resourceID uint) (*domain.DomainResource, error) {
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

func (r *ResourceRepo) listQuery(ctx context.Context, ownerUserID uint, resourceType string) *gorm.DB {
	q := r.db.WithContext(ctx).Model(&EmailResourceModel{})
	if ownerUserID > 0 {
		q = q.Where("owner_user_id = ?", ownerUserID)
	}
	if resourceType != "" && resourceType != "all" {
		q = q.Where("type = ?", resourceType)
	}
	return q
}

func (r *ResourceRepo) List(ctx context.Context, ownerUserID uint, resourceType string, offset, limit int) ([]domain.EmailResource, error) {
	var models []EmailResourceModel
	err := r.listQuery(ctx, ownerUserID, resourceType).
		Order("created_at DESC").
		Offset(offset).Limit(limit).
		Find(&models).Error
	if err != nil {
		return nil, fmt.Errorf("list resources: %w", err)
	}
	result := make([]domain.EmailResource, len(models))
	for i, m := range models {
		result[i] = *m.toDomain()
	}
	return result, nil
}

func (r *ResourceRepo) ListAll(ctx context.Context, resourceType string, offset, limit int) ([]domain.EmailResource, error) {
	return r.List(ctx, 0, resourceType, offset, limit)
}

func (r *ResourceRepo) Count(ctx context.Context, ownerUserID uint, resourceType string) (int64, error) {
	var count int64
	err := r.listQuery(ctx, ownerUserID, resourceType).Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("count resources: %w", err)
	}
	return count, nil
}

func (r *ResourceRepo) CountAll(ctx context.Context, resourceType string) (int64, error) {
	return r.Count(ctx, 0, resourceType)
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
			ID:           m.ID,
			EmailAddress: m.EmailAddress,
			ForSale:      m.ForSale,
			Status:       m.Status,
		}
	}
	return result, nil
}

// ListDomainStatus returns API-safe status for a batch of domain resources.
func (r *ResourceRepo) ListDomainStatus(ctx context.Context, ids []uint) ([]coreapp.DomainStatusResult, error) {
	var models []DomainResourceModel
	err := r.db.WithContext(ctx).
		Where("id IN ?", ids).
		Find(&models).Error
	if err != nil {
		return nil, fmt.Errorf("list domain status: %w", err)
	}
	result := make([]coreapp.DomainStatusResult, len(models))
	for i, m := range models {
		result[i] = coreapp.DomainStatusResult{
			ID:      m.ID,
			Domain:  m.Domain,
			Purpose: m.Purpose,
			Status:  m.Status,
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

// UpdateDomainWithLog updates a domain resource and writes an OperationLog
// in the same transaction.
func (r *ResourceRepo) UpdateDomainWithLog(ctx context.Context, resource *domain.DomainResource, log *governancedomain.OperationLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updates := map[string]interface{}{
			"domain":            resource.Domain,
			"mail_server_id":    resource.MailServerID,
			"purpose":           string(resource.Purpose),
			"status":            string(resource.Status),
			"last_allocated_at": resource.LastAllocatedAt,
			"updated_at":        time.Now(),
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
