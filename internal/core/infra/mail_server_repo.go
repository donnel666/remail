package infra

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/donnel666/remail/internal/core/domain"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// MailServerModel is the GORM model for the mail_servers table.
type MailServerModel struct {
	ID            uint      `gorm:"primaryKey;autoIncrement"`
	OwnerUserID   uint      `gorm:"not null;column:owner_user_id"`
	Name          string    `gorm:"type:varchar(255);not null;default:''"`
	ServerAddress string    `gorm:"type:varchar(255);not null;column:server_address"`
	MXRecord      string    `gorm:"type:varchar(255);not null;default:'';column:mx_record"`
	SPFRecord     string    `gorm:"type:varchar(512);not null;default:'';column:spf_record"`
	DKIMRecord    string    `gorm:"type:varchar(512);not null;default:'';column:dkim_record"`
	DMARCRecord   string    `gorm:"type:varchar(512);not null;default:'';column:dmarc_record"`
	PTRRecord     string    `gorm:"type:varchar(255);not null;default:'';column:ptr_record"`
	Status        string    `gorm:"type:varchar(32);not null;default:'online'"`
	CreatedAt     time.Time `gorm:"not null;autoCreateTime"`
	UpdatedAt     time.Time `gorm:"not null;autoUpdateTime"`
}

func (MailServerModel) TableName() string {
	return "mail_servers"
}

func (m *MailServerModel) toDomain() *domain.MailServer {
	return &domain.MailServer{
		ID:            m.ID,
		OwnerUserID:   m.OwnerUserID,
		Name:          m.Name,
		ServerAddress: m.ServerAddress,
		MXRecord:      m.MXRecord,
		SPFRecord:     m.SPFRecord,
		DKIMRecord:    m.DKIMRecord,
		DMARCRecord:   m.DMARCRecord,
		PTRRecord:     m.PTRRecord,
		Status:        domain.MailServerStatus(m.Status),
		CreatedAt:     m.CreatedAt,
		UpdatedAt:     m.UpdatedAt,
	}
}

func fromMailServerDomain(s *domain.MailServer) *MailServerModel {
	return &MailServerModel{
		ID:            s.ID,
		OwnerUserID:   s.OwnerUserID,
		Name:          s.Name,
		ServerAddress: s.ServerAddress,
		MXRecord:      s.MXRecord,
		SPFRecord:     s.SPFRecord,
		DKIMRecord:    s.DKIMRecord,
		DMARCRecord:   s.DMARCRecord,
		PTRRecord:     s.PTRRecord,
		Status:        string(s.Status),
		CreatedAt:     s.CreatedAt,
		UpdatedAt:     s.UpdatedAt,
	}
}

// MailServerRepo implements app.MailServerRepository.
type MailServerRepo struct {
	db *gorm.DB
}

// NewMailServerRepo creates a new GORM-backed mail server repository.
func NewMailServerRepo(db *gorm.DB) *MailServerRepo {
	return &MailServerRepo{db: db}
}

func (r *MailServerRepo) Create(ctx context.Context, server *domain.MailServer) error {
	model := fromMailServerDomain(server)
	if err := r.db.WithContext(ctx).Create(model).Error; err != nil {
		return fmt.Errorf("create mail server: %w", err)
	}
	server.ID = model.ID
	server.CreatedAt = model.CreatedAt
	server.UpdatedAt = model.UpdatedAt
	return nil
}

func (r *MailServerRepo) FindByID(ctx context.Context, id uint) (*domain.MailServer, error) {
	var model MailServerModel
	err := r.db.WithContext(ctx).First(&model, id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find mail server: %w", err)
	}
	return model.toDomain(), nil
}

// GetOrCreateDefaultInbound returns the owner's built-in local inbound server.
// The unique DB index on owner/address/MX is the concurrency guard; duplicate
// create races re-read the existing row.
func (r *MailServerRepo) GetOrCreateDefaultInbound(ctx context.Context, ownerUserID uint, name, serverAddress, mxRecord string) (*domain.MailServer, error) {
	var result MailServerModel
	err := r.db.WithContext(ctx).
		Where("owner_user_id = ? AND server_address = ? AND mx_record = ?", ownerUserID, serverAddress, mxRecord).
		First(&result).Error
	if err == nil {
		return result.toDomain(), nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("find default inbound mail server: %w", err)
	}

	var createErr error
	for attempt := 0; attempt < 5; attempt++ {
		result = MailServerModel{
			OwnerUserID:   ownerUserID,
			Name:          name,
			ServerAddress: serverAddress,
			MXRecord:      mxRecord,
			Status:        string(domain.MailServerOnline),
		}

		createErr = r.db.WithContext(ctx).Create(&result).Error
		if createErr == nil {
			return result.toDomain(), nil
		}
		if isRetryableMailServerConflict(createErr) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 10 * time.Millisecond):
				continue
			}
		}
		if isDuplicateMailServerKey(createErr) {
			err := r.db.WithContext(ctx).
				Where("owner_user_id = ? AND server_address = ? AND mx_record = ?", ownerUserID, serverAddress, mxRecord).
				First(&result).Error
			if err != nil {
				return nil, fmt.Errorf("find duplicate default inbound mail server: %w", err)
			}
			return result.toDomain(), nil
		}
		return nil, fmt.Errorf("create default inbound mail server: %w", createErr)
	}

	if createErr != nil {
		return nil, fmt.Errorf("create default inbound mail server: %w", createErr)
	}

	return nil, fmt.Errorf("create default inbound mail server: exhausted retries")
}

func isDuplicateMailServerKey(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func isRetryableMailServerConflict(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && (mysqlErr.Number == 1213 || mysqlErr.Number == 1205)
}

func (r *MailServerRepo) listQuery(ctx context.Context, ownerUserID uint) *gorm.DB {
	q := r.db.WithContext(ctx).Model(&MailServerModel{})
	if ownerUserID > 0 {
		q = q.Where("owner_user_id = ?", ownerUserID)
	}
	return q
}

func (r *MailServerRepo) List(ctx context.Context, ownerUserID uint, offset, limit int) ([]domain.MailServer, error) {
	var models []MailServerModel
	err := r.listQuery(ctx, ownerUserID).
		Order("created_at DESC").
		Offset(offset).Limit(limit).
		Find(&models).Error
	if err != nil {
		return nil, fmt.Errorf("list mail servers: %w", err)
	}
	result := make([]domain.MailServer, len(models))
	for i, m := range models {
		result[i] = *m.toDomain()
	}
	return result, nil
}

func (r *MailServerRepo) ListAll(ctx context.Context, offset, limit int) ([]domain.MailServer, error) {
	return r.List(ctx, 0, offset, limit)
}

func (r *MailServerRepo) Count(ctx context.Context, ownerUserID uint) (int64, error) {
	var count int64
	err := r.listQuery(ctx, ownerUserID).Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("count mail servers: %w", err)
	}
	return count, nil
}

func (r *MailServerRepo) CountAll(ctx context.Context) (int64, error) {
	return r.Count(ctx, 0)
}
