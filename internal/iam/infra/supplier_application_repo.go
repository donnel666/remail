package infra

import (
	"context"
	"errors"
	"fmt"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/iam/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SupplierApplicationModel struct {
	ID              uint       `gorm:"primaryKey;autoIncrement"`
	ApplicantUserID uint       `gorm:"not null;column:applicant_user_id"`
	Reason          string     `gorm:"type:varchar(1000);not null"`
	Status          string     `gorm:"type:varchar(32);not null;default:'reviewing'"`
	ReviewReason    string     `gorm:"type:varchar(500);not null;default:'';column:review_reason"`
	ReviewedBy      *uint      `gorm:"column:reviewed_by"`
	ReviewedAt      *time.Time `gorm:"column:reviewed_at"`
	CreatedAt       time.Time  `gorm:"not null;autoCreateTime"`
	UpdatedAt       time.Time  `gorm:"not null;autoUpdateTime"`
}

func (SupplierApplicationModel) TableName() string {
	return "supplier_applications"
}

func supplierApplicationToDomain(model *SupplierApplicationModel) *domain.SupplierApplication {
	return &domain.SupplierApplication{
		ID:              model.ID,
		ApplicantUserID: model.ApplicantUserID,
		Reason:          model.Reason,
		Status:          domain.SupplierApplicationStatus(model.Status),
		ReviewReason:    model.ReviewReason,
		ReviewedBy:      model.ReviewedBy,
		ReviewedAt:      model.ReviewedAt,
		CreatedAt:       model.CreatedAt,
		UpdatedAt:       model.UpdatedAt,
	}
}

func supplierApplicationFromDomain(application *domain.SupplierApplication) *SupplierApplicationModel {
	return &SupplierApplicationModel{
		ID:              application.ID,
		ApplicantUserID: application.ApplicantUserID,
		Reason:          application.Reason,
		Status:          string(application.Status),
		ReviewReason:    application.ReviewReason,
		ReviewedBy:      application.ReviewedBy,
		ReviewedAt:      application.ReviewedAt,
		CreatedAt:       application.CreatedAt,
		UpdatedAt:       application.UpdatedAt,
	}
}

// SupplierApplicationRepo implements supplier application persistence using GORM.
type SupplierApplicationRepo struct {
	db            *gorm.DB
	operationLogs *governanceinfra.OperationLogRepo
}

// NewSupplierApplicationRepo creates a new GORM-backed supplier application repository.
func NewSupplierApplicationRepo(db *gorm.DB) *SupplierApplicationRepo {
	return &SupplierApplicationRepo{db: db, operationLogs: governanceinfra.NewOperationLogRepo(db)}
}

func (r *SupplierApplicationRepo) CreateSupplierApplicationReviewing(ctx context.Context, application *domain.SupplierApplication) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing SupplierApplicationModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("applicant_user_id = ? AND status = ?", application.ApplicantUserID, string(domain.SupplierApplicationReviewing)).
			First(&existing).Error
		if err == nil {
			return domain.ErrSupplierApplicationAlreadyReviewing
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("find reviewing supplier application: %w", err)
		}

		model := supplierApplicationFromDomain(application)
		model.Status = string(domain.SupplierApplicationReviewing)
		if err := tx.Create(model).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return domain.ErrSupplierApplicationAlreadyReviewing
			}
			return fmt.Errorf("create supplier application: %w", err)
		}
		application.ID = model.ID
		application.CreatedAt = model.CreatedAt
		application.UpdatedAt = model.UpdatedAt
		return nil
	})
}

func (r *SupplierApplicationRepo) FindLatestSupplierApplicationByApplicantUserID(ctx context.Context, applicantUserID uint) (*domain.SupplierApplication, error) {
	var model SupplierApplicationModel
	err := r.db.WithContext(ctx).
		Where("applicant_user_id = ?", applicantUserID).
		Order("created_at DESC, id DESC").
		First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find latest supplier application: %w", err)
	}
	return supplierApplicationToDomain(&model), nil
}

func (r *SupplierApplicationRepo) FindSupplierApplicationByID(ctx context.Context, id uint) (*domain.SupplierApplication, error) {
	var model SupplierApplicationModel
	err := r.db.WithContext(ctx).First(&model, id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find supplier application: %w", err)
	}
	return supplierApplicationToDomain(&model), nil
}

func (r *SupplierApplicationRepo) ListSupplierApplications(ctx context.Context, status string, offset, limit int) ([]domain.SupplierApplication, error) {
	q := r.db.WithContext(ctx).Model(&SupplierApplicationModel{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var models []SupplierApplicationModel
	if err := q.Order("created_at DESC, id DESC").Offset(offset).Limit(limit).Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list supplier applications: %w", err)
	}
	result := make([]domain.SupplierApplication, len(models))
	for i := range models {
		result[i] = *supplierApplicationToDomain(&models[i])
	}
	return result, nil
}

func (r *SupplierApplicationRepo) CountSupplierApplications(ctx context.Context, status string) (int64, error) {
	q := r.db.WithContext(ctx).Model(&SupplierApplicationModel{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count supplier applications: %w", err)
	}
	return count, nil
}

func (r *SupplierApplicationRepo) ApproveSupplierApplicationWithUserAndLog(ctx context.Context, application *domain.SupplierApplication, user *domain.User, log *governancedomain.OperationLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := updateSupplierApplicationReviewInTx(ctx, tx, application); err != nil {
			return err
		}
		if err := tx.Model(&UserModel{}).
			Where("id = ?", user.ID).
			Updates(map[string]interface{}{
				"role":       user.Role.String(),
				"updated_at": time.Now().UTC(),
			}).Error; err != nil {
			return fmt.Errorf("promote supplier application user: %w", err)
		}
		if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
			return fmt.Errorf("create supplier application operation log: %w", err)
		}
		return nil
	})
}

func (r *SupplierApplicationRepo) RejectSupplierApplicationWithLog(ctx context.Context, application *domain.SupplierApplication, log *governancedomain.OperationLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := updateSupplierApplicationReviewInTx(ctx, tx, application); err != nil {
			return err
		}
		if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
			return fmt.Errorf("create supplier application operation log: %w", err)
		}
		return nil
	})
}

func updateSupplierApplicationReviewInTx(ctx context.Context, tx *gorm.DB, application *domain.SupplierApplication) error {
	updates := map[string]interface{}{
		"status":        string(application.Status),
		"review_reason": application.ReviewReason,
		"reviewed_by":   application.ReviewedBy,
		"reviewed_at":   application.ReviewedAt,
		"updated_at":    time.Now().UTC(),
	}
	result := tx.WithContext(ctx).Model(&SupplierApplicationModel{}).
		Where("id = ? AND status = ?", application.ID, string(domain.SupplierApplicationReviewing)).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("update supplier application review: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return domain.ErrInvalidSupplierApplicationStatus
	}
	return nil
}
