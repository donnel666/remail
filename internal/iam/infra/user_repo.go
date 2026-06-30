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
)

// UserModel is the GORM model for the users table.
// This is the infra-layer representation, not exposed outside the package.
type UserModel struct {
	ID           uint       `gorm:"primaryKey;autoIncrement"`
	Email        string     `gorm:"type:varchar(255);uniqueIndex;not null"`
	PasswordHash string     `gorm:"type:varchar(255);not null;column:password_hash"`
	Nickname     string     `gorm:"type:varchar(100);not null;default:''"`
	Enabled      bool       `gorm:"not null;default:true"`
	RoleLevel    int        `gorm:"not null;default:10;column:role_level"`
	TokenVersion int        `gorm:"not null;default:0;column:token_version"`
	LastLoginAt  *time.Time `gorm:"column:last_login_at"`
	CreatedAt    time.Time  `gorm:"not null;autoCreateTime"`
	UpdatedAt    time.Time  `gorm:"not null;autoUpdateTime"`
}

type InviteModel struct {
	Code            string     `gorm:"primaryKey;type:varchar(64)"`
	Enabled         bool       `gorm:"not null;default:true"`
	MaxUse          int        `gorm:"not null;column:max_use"`
	Used            int        `gorm:"not null;default:0"`
	ExpireAt        *time.Time `gorm:"column:expire_at"`
	CreatedByUserID *uint      `gorm:"column:created_by_user_id"`
	CreatedAt       time.Time  `gorm:"not null;autoCreateTime"`
	UpdatedAt       time.Time  `gorm:"not null;autoUpdateTime"`
}

func (InviteModel) TableName() string {
	return "invites"
}

type InviteUseModel struct {
	ID         uint64    `gorm:"primaryKey;autoIncrement"`
	InviteCode string    `gorm:"type:varchar(64);not null;column:invite_code"`
	UserID     uint      `gorm:"not null;column:user_id"`
	UsedAt     time.Time `gorm:"not null;autoCreateTime;column:used_at"`
}

func (InviteUseModel) TableName() string {
	return "invite_uses"
}

type CasbinRuleModel struct {
	ID    uint64 `gorm:"primaryKey;autoIncrement"`
	Ptype string `gorm:"type:varchar(100);not null;column:ptype"`
	V0    string `gorm:"type:varchar(255);not null;column:v0"`
	V1    string `gorm:"type:varchar(255);not null;column:v1"`
	V2    string `gorm:"type:varchar(255);not null;column:v2"`
	V3    string `gorm:"type:varchar(255);not null;column:v3"`
	V4    string `gorm:"type:varchar(255);not null;column:v4"`
	V5    string `gorm:"type:varchar(255);not null;column:v5"`
}

func (CasbinRuleModel) TableName() string {
	return "casbin_rule"
}

// TableName overrides the default table name.
func (UserModel) TableName() string {
	return "users"
}

// toDomain converts the GORM model to a domain entity.
func (m *UserModel) toDomain() *domain.User {
	return &domain.User{
		ID:           m.ID,
		Email:        m.Email,
		PasswordHash: m.PasswordHash,
		Nickname:     m.Nickname,
		Enabled:      m.Enabled,
		RoleLevel:    domain.RoleLevel(m.RoleLevel),
		TokenVersion: m.TokenVersion,
		LastLoginAt:  m.LastLoginAt,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
	}
}

// fromDomain converts a domain entity to a GORM model.
func fromDomain(u *domain.User) *UserModel {
	return &UserModel{
		ID:           u.ID,
		Email:        u.Email,
		PasswordHash: u.PasswordHash,
		Nickname:     u.Nickname,
		Enabled:      u.Enabled,
		RoleLevel:    int(u.RoleLevel),
		TokenVersion: u.TokenVersion,
		LastLoginAt:  u.LastLoginAt,
		CreatedAt:    u.CreatedAt,
		UpdatedAt:    u.UpdatedAt,
	}
}

func inviteToDomain(m *InviteModel) *domain.Invite {
	return &domain.Invite{
		Code:      m.Code,
		Enabled:   m.Enabled,
		MaxUse:    m.MaxUse,
		Used:      m.Used,
		ExpireAt:  m.ExpireAt,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

func inviteFromDomain(invite *domain.Invite) *InviteModel {
	return &InviteModel{
		Code:     invite.Code,
		Enabled:  invite.Enabled,
		MaxUse:   invite.MaxUse,
		Used:     invite.Used,
		ExpireAt: invite.ExpireAt,
	}
}

// UserRepo implements app.UserRepository using GORM.
type UserRepo struct {
	db            *gorm.DB
	operationLogs *governanceinfra.OperationLogRepo
}

// NewUserRepo creates a new GORM-backed user repository.
func NewUserRepo(db *gorm.DB) *UserRepo {
	return &UserRepo{db: db, operationLogs: governanceinfra.NewOperationLogRepo(db)}
}

func (r *UserRepo) Create(ctx context.Context, user *domain.User) error {
	model := fromDomain(user)
	err := r.db.WithContext(ctx).Create(model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return domain.ErrEmailAlreadyExists
		}
		return fmt.Errorf("create user: %w", err)
	}
	user.ID = model.ID
	user.CreatedAt = model.CreatedAt
	user.UpdatedAt = model.UpdatedAt
	return nil
}

func (r *UserRepo) CreateWithInvite(ctx context.Context, user *domain.User, inviteCode string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var invite InviteModel
		if err := tx.Raw("SELECT * FROM invites WHERE code = ? FOR UPDATE", inviteCode).Scan(&invite).Error; err != nil {
			return fmt.Errorf("lock invite: %w", err)
		}
		if invite.Code == "" || !invite.Enabled || invite.Used >= invite.MaxUse || (invite.ExpireAt != nil && invite.ExpireAt.Before(time.Now())) {
			return domain.ErrInviteInvalid
		}

		model := fromDomain(user)
		if err := tx.Create(model).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return domain.ErrEmailAlreadyExists
			}
			return fmt.Errorf("create invited user: %w", err)
		}
		if err := tx.Create(&InviteUseModel{InviteCode: inviteCode, UserID: model.ID}).Error; err != nil {
			return fmt.Errorf("create invite use: %w", err)
		}
		if err := tx.Model(&InviteModel{}).Where("code = ?", inviteCode).UpdateColumn("used", gorm.Expr("used + 1")).Error; err != nil {
			return fmt.Errorf("increment invite use: %w", err)
		}

		user.ID = model.ID
		user.CreatedAt = model.CreatedAt
		user.UpdatedAt = model.UpdatedAt
		return nil
	})
}

// CreateFirstUser creates the first user in a serialized transaction.
// It acquires a row-level lock on system_guard (a singleton table with one row)
// to prevent concurrent activation from creating multiple super_admin users
// (docs/8-iam.md:88, INV-I8). On a fresh DB the guard row is pre-inserted by
// migration 00002, so SELECT ... FOR UPDATE locks a real row.
//
// Returns ErrActivationAlreadyDone if a user already exists.
// Returns ErrEmailAlreadyExists on email conflict.
func (r *UserRepo) CreateFirstUser(ctx context.Context, user *domain.User) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock the guard row — this is a proven row in InnoDB, so FOR UPDATE
		// actually serializes concurrent transactions (unlike locking an empty
		// users table where there are no rows to lock).
		var guard struct {
			ID int
		}
		if err := tx.Raw("SELECT id FROM system_guard WHERE id = 1 FOR UPDATE").Scan(&guard).Error; err != nil {
			return fmt.Errorf("first user acquire guard lock: %w", err)
		}
		if guard.ID != 1 {
			return fmt.Errorf("first user: system_guard row missing")
		}

		// Now safely check if any user exists (serialized by guard row lock)
		var count int64
		if err := tx.Model(&UserModel{}).Count(&count).Error; err != nil {
			return fmt.Errorf("first user count: %w", err)
		}
		if count > 0 {
			return domain.ErrActivationAlreadyDone
		}

		// Check email uniqueness within the same transaction
		var existing int64
		if err := tx.Model(&UserModel{}).Where("email = ?", user.Email).Count(&existing).Error; err != nil {
			return fmt.Errorf("first user check email: %w", err)
		}
		if existing > 0 {
			return domain.ErrEmailAlreadyExists
		}

		model := fromDomain(user)
		if err := tx.Create(model).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return domain.ErrEmailAlreadyExists
			}
			return fmt.Errorf("first user create: %w", err)
		}

		user.ID = model.ID
		user.CreatedAt = model.CreatedAt
		user.UpdatedAt = model.UpdatedAt
		return nil
	})
}

func (r *UserRepo) FindByEmail(ctx context.Context, email string) (*domain.User, error) {
	var model UserModel
	err := r.db.WithContext(ctx).Where("email = ?", email).First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find user by email: %w", err)
	}
	return model.toDomain(), nil
}

func (r *UserRepo) FindByID(ctx context.Context, id uint) (*domain.User, error) {
	var model UserModel
	err := r.db.WithContext(ctx).First(&model, id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find user by id: %w", err)
	}
	return model.toDomain(), nil
}

func (r *UserRepo) Update(ctx context.Context, user *domain.User) error {
	model := fromDomain(user)
	// Uses Select("*") to ensure zero values (e.g. Enabled=false) are persisted.
	// GORM's Updates() with a struct skips zero values by default.
	err := r.db.WithContext(ctx).Model(&UserModel{}).Where("id = ?", user.ID).Select("*").Updates(model).Error
	if err != nil {
		return fmt.Errorf("update user: %w", err)
	}
	return nil
}

func (r *UserRepo) UpdateWithOperationLog(ctx context.Context, user *domain.User, log *governancedomain.OperationLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model := fromDomain(user)
		if err := tx.Model(&UserModel{}).Where("id = ?", user.ID).Select("*").Updates(model).Error; err != nil {
			return fmt.Errorf("update user with log: %w", err)
		}
		if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
			return fmt.Errorf("create operation log: %w", err)
		}
		return nil
	})
}

func (r *UserRepo) List(ctx context.Context, offset, limit int) ([]domain.User, error) {
	var models []UserModel
	err := r.db.WithContext(ctx).Order("created_at DESC").Offset(offset).Limit(limit).Find(&models).Error
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	users := make([]domain.User, len(models))
	for i, m := range models {
		users[i] = *m.toDomain()
	}
	return users, nil
}

func (r *UserRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&UserModel{}).Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return count, nil
}

func (r *UserRepo) FindByIDs(ctx context.Context, ids []uint) ([]domain.User, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var models []UserModel
	err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&models).Error
	if err != nil {
		return nil, fmt.Errorf("find users by ids: %w", err)
	}
	users := make([]domain.User, len(models))
	for i, m := range models {
		users[i] = *m.toDomain()
	}
	return users, nil
}

func (r *UserRepo) ListInvites(ctx context.Context, offset, limit int) ([]domain.Invite, error) {
	var models []InviteModel
	if err := r.db.WithContext(ctx).Order("created_at DESC").Offset(offset).Limit(limit).Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list invites: %w", err)
	}
	invites := make([]domain.Invite, len(models))
	for i := range models {
		invites[i] = *inviteToDomain(&models[i])
	}
	return invites, nil
}

func (r *UserRepo) CountInvites(ctx context.Context) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&InviteModel{}).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count invites: %w", err)
	}
	return count, nil
}

func (r *UserRepo) FindInviteByCode(ctx context.Context, code string) (*domain.Invite, error) {
	var model InviteModel
	err := r.db.WithContext(ctx).Where("code = ?", code).First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find invite: %w", err)
	}
	return inviteToDomain(&model), nil
}

func (r *UserRepo) CreateInviteWithOperationLog(ctx context.Context, invite *domain.Invite, createdByUserID uint, log *governancedomain.OperationLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model := inviteFromDomain(invite)
		model.CreatedByUserID = &createdByUserID
		if err := tx.Create(model).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return domain.ErrInviteAlreadyExists
			}
			return fmt.Errorf("create invite: %w", err)
		}
		if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
			return fmt.Errorf("create invite operation log: %w", err)
		}
		invite.CreatedAt = model.CreatedAt
		invite.UpdatedAt = model.UpdatedAt
		return nil
	})
}

func (r *UserRepo) UpdateInviteWithOperationLog(ctx context.Context, invite *domain.Invite, log *governancedomain.OperationLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model := inviteFromDomain(invite)
		if err := tx.Model(&InviteModel{}).Where("code = ?", invite.Code).Select("enabled", "max_use", "expire_at").Updates(model).Error; err != nil {
			return fmt.Errorf("update invite: %w", err)
		}
		if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
			return fmt.Errorf("create invite operation log: %w", err)
		}
		return nil
	})
}
