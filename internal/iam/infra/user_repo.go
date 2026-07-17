package infra

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UserModel is the GORM model for the users table.
// This is the infra-layer representation, not exposed outside the package.
type UserModel struct {
	ID           uint           `gorm:"primaryKey;autoIncrement"`
	Email        string         `gorm:"type:varchar(255);uniqueIndex;not null"`
	PasswordHash string         `gorm:"type:varchar(255);not null;column:password_hash"`
	Nickname     string         `gorm:"type:varchar(100);not null;default:''"`
	Enabled      bool           `gorm:"not null;default:true"`
	Role         string         `gorm:"type:varchar(32);not null;default:'user'"`
	UserGroupID  uint           `gorm:"not null;default:1;column:user_group_id"`
	UserGroup    UserGroupModel `gorm:"foreignKey:UserGroupID"`
	TokenVersion int            `gorm:"not null;default:0;column:token_version"`
	LastLoginAt  *time.Time     `gorm:"column:last_login_at"`
	CreatedAt    time.Time      `gorm:"not null;autoCreateTime"`
	UpdatedAt    time.Time      `gorm:"not null;autoUpdateTime"`
}

type UserGroupModel struct {
	ID          uint      `gorm:"primaryKey;autoIncrement"`
	Code        string    `gorm:"type:varchar(64);uniqueIndex;not null"`
	Name        string    `gorm:"type:varchar(100);not null"`
	Description string    `gorm:"type:varchar(500);not null;default:''"`
	Enabled     bool      `gorm:"not null;default:true"`
	CreatedAt   time.Time `gorm:"not null;autoCreateTime"`
	UpdatedAt   time.Time `gorm:"not null;autoUpdateTime"`
}

type InviteModel struct {
	Code            string     `gorm:"primaryKey;type:varchar(64)"`
	Kind            string     `gorm:"type:varchar(32);not null;default:'admin';column:invite_kind"`
	Enabled         bool       `gorm:"not null;default:true"`
	MaxUse          int        `gorm:"not null;column:max_use"`
	Used            int        `gorm:"not null;default:0"`
	ExpireAt        *time.Time `gorm:"column:expire_at"`
	CreatedByUserID *uint      `gorm:"column:created_by_user_id"`
	ReferralOwnerID *uint      `gorm:"column:referral_owner_user_id"`
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

func (UserGroupModel) TableName() string {
	return "user_groups"
}

func (r *UserRepo) dbFor(ctx context.Context) *gorm.DB {
	if tx, ok := platform.GormTxFromContext(ctx); ok {
		return tx.WithContext(ctx)
	}
	return r.db.WithContext(ctx)
}

// toDomain converts the GORM model to a domain entity.
func (m *UserModel) toDomain() *domain.User {
	return &domain.User{
		ID:           m.ID,
		Email:        m.Email,
		PasswordHash: m.PasswordHash,
		Nickname:     m.Nickname,
		Enabled:      m.Enabled,
		Role:         domain.Role(m.Role),
		UserGroupID:  m.UserGroupID,
		UserGroup: domain.UserGroup{
			ID:          m.UserGroup.ID,
			Code:        m.UserGroup.Code,
			Name:        m.UserGroup.Name,
			Description: m.UserGroup.Description,
			Enabled:     m.UserGroup.Enabled,
			CreatedAt:   m.UserGroup.CreatedAt,
			UpdatedAt:   m.UserGroup.UpdatedAt,
		},
		TokenVersion: m.TokenVersion,
		LastLoginAt:  m.LastLoginAt,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
	}
}

// fromDomain converts a domain entity to a GORM model.
func fromDomain(u *domain.User) *UserModel {
	role := u.Role
	if role == "" {
		role = domain.RoleUser
	}
	userGroupID := u.UserGroupID
	if userGroupID == 0 {
		userGroupID = 1
	}
	return &UserModel{
		ID:           u.ID,
		Email:        u.Email,
		PasswordHash: u.PasswordHash,
		Nickname:     u.Nickname,
		Enabled:      u.Enabled,
		Role:         role.String(),
		UserGroupID:  userGroupID,
		TokenVersion: u.TokenVersion,
		LastLoginAt:  u.LastLoginAt,
		CreatedAt:    u.CreatedAt,
		UpdatedAt:    u.UpdatedAt,
	}
}

func userGroupToDomain(m UserGroupModel) domain.UserGroup {
	return domain.UserGroup{
		ID:          m.ID,
		Code:        m.Code,
		Name:        m.Name,
		Description: m.Description,
		Enabled:     m.Enabled,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}
}

func (r *UserRepo) loadUserGroup(ctx context.Context, user *domain.User) error {
	if user == nil || user.UserGroupID == 0 {
		return nil
	}
	var model UserGroupModel
	if err := r.db.WithContext(ctx).First(&model, user.UserGroupID).Error; err != nil {
		return fmt.Errorf("load user group: %w", err)
	}
	user.UserGroup = userGroupToDomain(model)
	return nil
}

func inviteToDomain(m *InviteModel) *domain.Invite {
	return &domain.Invite{
		Code:            m.Code,
		Kind:            domain.InviteKind(m.Kind),
		Enabled:         m.Enabled,
		MaxUse:          m.MaxUse,
		Used:            m.Used,
		ExpireAt:        m.ExpireAt,
		CreatedByUserID: m.CreatedByUserID,
		CreatedAt:       m.CreatedAt,
		UpdatedAt:       m.UpdatedAt,
	}
}

func inviteFromDomain(invite *domain.Invite) *InviteModel {
	kind := invite.Kind
	if kind == "" {
		kind = domain.InviteKindAdmin
	}
	return &InviteModel{
		Code:            invite.Code,
		Kind:            string(kind),
		Enabled:         invite.Enabled,
		MaxUse:          invite.MaxUse,
		Used:            invite.Used,
		ExpireAt:        invite.ExpireAt,
		CreatedByUserID: invite.CreatedByUserID,
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
	user.UserGroupID = model.UserGroupID
	user.CreatedAt = model.CreatedAt
	user.UpdatedAt = model.UpdatedAt
	_ = r.loadUserGroup(ctx, user)
	return nil
}

func (r *UserRepo) CreateWithInvite(ctx context.Context, user *domain.User, inviteCode string) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		user.UserGroupID = model.UserGroupID
		user.CreatedAt = model.CreatedAt
		user.UpdatedAt = model.UpdatedAt
		return nil
	})
	if err != nil {
		return err
	}
	if err := r.loadUserGroup(ctx, user); err != nil {
		return err
	}
	return nil
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
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		user.UserGroupID = model.UserGroupID
		user.CreatedAt = model.CreatedAt
		user.UpdatedAt = model.UpdatedAt
		return nil
	})
	if err != nil {
		return err
	}
	if err := r.loadUserGroup(ctx, user); err != nil {
		return err
	}
	return nil
}

func (r *UserRepo) FindByEmail(ctx context.Context, email string) (*domain.User, error) {
	var model UserModel
	err := r.db.WithContext(ctx).Preload("UserGroup").Where("email = ?", email).First(&model).Error
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
	err := r.dbFor(ctx).Preload("UserGroup").First(&model, id).Error
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

func (r *UserRepo) UpdateNonSuperAdminAccessWithOperationLog(ctx context.Context, userID uint, enabled *bool, role *domain.Role, userGroupID *uint, incrementTokenVersion bool, log *governancedomain.OperationLog) (*domain.User, error) {
	updates := make(map[string]any, 4)
	if enabled != nil {
		updates["enabled"] = *enabled
	}
	if role != nil {
		updates["role"] = role.String()
	}
	if userGroupID != nil {
		updates["user_group_id"] = *userGroupID
	}
	return r.updateNonSuperAdmin(ctx, userID, updates, incrementTokenVersion, log)
}

// UpdateNonSuperAdminProfileWithOperationLog updates profile and access fields
// (email, nickname, password, enabled, role, group) atomically, refuses a
// super_admin row, and writes the operation log in the same transaction.
func (r *UserRepo) UpdateNonSuperAdminProfileWithOperationLog(ctx context.Context, userID uint, email, nickname, passwordHash *string, enabled *bool, role *domain.Role, userGroupID *uint, incrementTokenVersion bool, log *governancedomain.OperationLog) (*domain.User, error) {
	updates := make(map[string]any, 6)
	if email != nil {
		updates["email"] = *email
	}
	if nickname != nil {
		updates["nickname"] = *nickname
	}
	if passwordHash != nil {
		updates["password_hash"] = *passwordHash
	}
	if enabled != nil {
		updates["enabled"] = *enabled
	}
	if role != nil {
		updates["role"] = role.String()
	}
	if userGroupID != nil {
		updates["user_group_id"] = *userGroupID
	}
	updated, err := r.updateNonSuperAdmin(ctx, userID, updates, incrementTokenVersion, log)
	if err != nil && isIAMDuplicateKeyError(err) {
		return nil, domain.ErrEmailAlreadyExists
	}
	return updated, err
}

func (r *UserRepo) updateNonSuperAdmin(ctx context.Context, userID uint, updates map[string]any, incrementTokenVersion bool, log *governancedomain.OperationLog) (*domain.User, error) {
	var updated *domain.User
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if incrementTokenVersion {
			updates["token_version"] = gorm.Expr("token_version + 1")
		}
		if len(updates) == 0 {
			return errors.New("user access update has no changes")
		}

		result := tx.Model(&UserModel{}).
			Where("id = ? AND role <> ?", userID, domain.RoleSuperAdmin.String()).
			Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("update non-super-admin with log: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			var current UserModel
			if err := tx.Select("id", "role").First(&current, userID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return domain.ErrUserNotFound
				}
				return fmt.Errorf("inspect unchanged user access: %w", err)
			}
			if domain.Role(current.Role) == domain.RoleSuperAdmin {
				return domain.ErrPermissionDenied
			}
		}

		if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
			return fmt.Errorf("create operation log: %w", err)
		}
		var model UserModel
		if err := tx.Preload("UserGroup").First(&model, userID).Error; err != nil {
			return fmt.Errorf("reload updated user access: %w", err)
		}
		updated = model.toDomain()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// DeleteNonSuperAdminWithOperationLog hard-deletes a user, refusing a
// super_admin row, and writes the operation log in the same transaction.
// ponytail: leaves other BCs' historical rows (orders/wallet/resources) keyed
// by owner id; add cascade cleanup if orphan rows ever need pruning.
func (r *UserRepo) DeleteNonSuperAdminWithOperationLog(ctx context.Context, userID uint, log *governancedomain.OperationLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current UserModel
		if err := tx.Select("id", "role").First(&current, userID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrUserNotFound
			}
			return fmt.Errorf("inspect user before delete: %w", err)
		}
		if domain.Role(current.Role) == domain.RoleSuperAdmin {
			return domain.ErrPermissionDenied
		}
		if err := tx.Delete(&UserModel{}, userID).Error; err != nil {
			return fmt.Errorf("delete user: %w", err)
		}
		if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
			return fmt.Errorf("create operation log: %w", err)
		}
		return nil
	})
}

// ResolveBulkUserIDs returns the non-super-admin user IDs targeted by a bulk
// selection, newest first and capped at 1000. When ids is non-empty it selects
// exactly those rows; otherwise it applies the admin list filter. super_admin
// rows are always excluded.
// bulkUserChunkSize bounds the id count per batch statement so an uncapped
// filter selection can't exceed MySQL's prepared-statement placeholder limit.
const bulkUserChunkSize = 5000

// batchInChunks runs fn over ids in bounded chunks and sums the rows affected.
func batchInChunks(ids []uint, fn func(chunk []uint) (int64, error)) (int64, error) {
	var total int64
	for start := 0; start < len(ids); start += bulkUserChunkSize {
		end := start + bulkUserChunkSize
		if end > len(ids) {
			end = len(ids)
		}
		affected, err := fn(ids[start:end])
		if err != nil {
			return total, err
		}
		total += affected
	}
	return total, nil
}

// ResolveBulkUserIDs returns the non-super-admin user ids targeted by a bulk
// selection. Uncapped: filter selections operate on every matching user; the
// follow-up mutation is chunked by batchInChunks.
func (r *UserRepo) ResolveBulkUserIDs(ctx context.Context, ids []uint, filter domain.UserListFilter) ([]uint, error) {
	q := r.dbFor(ctx).Model(&UserModel{}).Where("role <> ?", domain.RoleSuperAdmin.String())
	if len(ids) > 0 {
		q = q.Where("id IN ?", ids)
	} else {
		q = applyUserListFilter(q, filter)
	}
	var out []uint
	if err := q.Pluck("id", &out).Error; err != nil {
		return nil, fmt.Errorf("resolve bulk user ids: %w", err)
	}
	return out, nil
}

// BatchSetEnabledNonSuperAdmin flips enabled for the given non-super-admin rows
// whose current enabled differs, bumping token_version when disabling so live
// sessions are rejected (INV-I3). Returns the number of rows changed.
func (r *UserRepo) BatchSetEnabledNonSuperAdmin(ctx context.Context, ids []uint, enabled bool) (int64, error) {
	return batchInChunks(ids, func(chunk []uint) (int64, error) {
		updates := map[string]any{"enabled": enabled}
		if !enabled {
			updates["token_version"] = gorm.Expr("token_version + 1")
		}
		result := r.dbFor(ctx).Model(&UserModel{}).
			Where("id IN ? AND role <> ? AND enabled <> ?", chunk, domain.RoleSuperAdmin.String(), enabled).
			Updates(updates)
		if result.Error != nil {
			return 0, fmt.Errorf("batch set enabled non-super-admin: %w", result.Error)
		}
		return result.RowsAffected, nil
	})
}

// BatchBumpTokenVersionNonSuperAdmin increments token_version for the given
// non-super-admin rows, invalidating their live sessions. Returns rows changed.
func (r *UserRepo) BatchBumpTokenVersionNonSuperAdmin(ctx context.Context, ids []uint) (int64, error) {
	return batchInChunks(ids, func(chunk []uint) (int64, error) {
		result := r.dbFor(ctx).Model(&UserModel{}).
			Where("id IN ? AND role <> ?", chunk, domain.RoleSuperAdmin.String()).
			Update("token_version", gorm.Expr("token_version + 1"))
		if result.Error != nil {
			return 0, fmt.Errorf("batch bump token version non-super-admin: %w", result.Error)
		}
		return result.RowsAffected, nil
	})
}

// BatchDeleteNonSuperAdmin hard-deletes the given non-super-admin rows and
// returns the number deleted.
func (r *UserRepo) BatchDeleteNonSuperAdmin(ctx context.Context, ids []uint) (int64, error) {
	return batchInChunks(ids, func(chunk []uint) (int64, error) {
		result := r.dbFor(ctx).
			Where("id IN ? AND role <> ?", chunk, domain.RoleSuperAdmin.String()).
			Delete(&UserModel{})
		if result.Error != nil {
			return 0, fmt.Errorf("batch delete non-super-admin: %w", result.Error)
		}
		return result.RowsAffected, nil
	})
}

// FacetsByFilter returns admin-list aggregate counts. Each dimension is counted
// with its own filter omitted so the tab/filter counts stay visible when that
// dimension is active.
func (r *UserRepo) FacetsByFilter(ctx context.Context, filter domain.UserListFilter, groups []domain.UserGroup) (*domain.UserFacets, error) {
	roleFilter := filter
	roleFilter.Role = nil
	roleRows := []struct {
		Role  string
		Count int64
	}{}
	if err := applyUserListFilter(r.db.WithContext(ctx).Model(&UserModel{}), roleFilter).
		Select("role, COUNT(*) AS count").Group("role").Scan(&roleRows).Error; err != nil {
		return nil, fmt.Errorf("facet roles: %w", err)
	}
	role := map[string]int64{}
	var roleAll int64
	for _, row := range roleRows {
		role[row.Role] = row.Count
		roleAll += row.Count
	}
	role["all"] = roleAll

	statusFilter := filter
	statusFilter.Enabled = nil
	statusRows := []struct {
		Enabled bool
		Count   int64
	}{}
	if err := applyUserListFilter(r.db.WithContext(ctx).Model(&UserModel{}), statusFilter).
		Select("enabled, COUNT(*) AS count").Group("enabled").Scan(&statusRows).Error; err != nil {
		return nil, fmt.Errorf("facet status: %w", err)
	}
	status := domain.StatusFacet{}
	for _, row := range statusRows {
		if row.Enabled {
			status.Enabled = row.Count
		} else {
			status.Disabled = row.Count
		}
	}
	status.All = status.Enabled + status.Disabled

	groupFilter := filter
	groupFilter.UserGroupID = nil
	groupRows := []struct {
		UserGroupID uint
		Count       int64
	}{}
	if err := applyUserListFilter(r.db.WithContext(ctx).Model(&UserModel{}), groupFilter).
		Select("user_group_id, COUNT(*) AS count").Group("user_group_id").Scan(&groupRows).Error; err != nil {
		return nil, fmt.Errorf("facet groups: %w", err)
	}
	groupCounts := make(map[uint]int64, len(groupRows))
	for _, row := range groupRows {
		groupCounts[row.UserGroupID] = row.Count
	}
	groupFacets := make([]domain.GroupFacet, len(groups))
	for i, g := range groups {
		groupFacets[i] = domain.GroupFacet{ID: g.ID, Code: g.Code, Name: g.Name, Count: groupCounts[g.ID]}
	}

	return &domain.UserFacets{Role: role, Status: status, Group: groupFacets}, nil
}

func applyUserListFilter(q *gorm.DB, filter domain.UserListFilter) *gorm.DB {
	if len(filter.IDs) > 0 {
		q = q.Where("id IN ?", filter.IDs)
	}
	if filter.Role != nil {
		q = q.Where("role = ?", filter.Role.String())
	}
	if filter.Enabled != nil {
		q = q.Where("enabled = ?", *filter.Enabled)
	}
	if filter.UserGroupID != nil {
		q = q.Where("user_group_id = ?", *filter.UserGroupID)
	}
	if filter.CreatedFrom != nil {
		q = q.Where("created_at >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		q = q.Where("created_at <= ?", *filter.CreatedTo)
	}
	search := strings.TrimSpace(filter.Search)
	if search == "" {
		return q
	}
	searchQuery := userSearchBooleanQuery(search)
	numericID, numericErr := strconv.ParseUint(search, 10, 64)
	if email, ok := exactEmailSearch(search); ok {
		return q.Where("email = ?", email)
	}
	switch {
	case numericErr == nil:
		return q.Where("id = ?", numericID)
	case searchQuery != "":
		return q.Where("MATCH(email, nickname) AGAINST (? IN BOOLEAN MODE)", searchQuery)
	default:
		return q.Where("1 = 0")
	}
}

func exactEmailSearch(search string) (string, bool) {
	addr, err := mail.ParseAddress(search)
	if err != nil || addr.Address == "" || !strings.EqualFold(addr.Address, search) {
		return "", false
	}
	return strings.ToLower(addr.Address), true
}

func userSearchBooleanQuery(search string) string {
	parts := strings.FieldsFunc(strings.ToLower(search), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	terms := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if len([]rune(part)) < 3 {
			continue
		}
		terms = append(terms, "+"+part+"*")
	}
	return strings.Join(terms, " ")
}

func (r *UserRepo) List(ctx context.Context, offset, limit int) ([]domain.User, error) {
	return r.ListByFilter(ctx, domain.UserListFilter{}, offset, limit)
}

func (r *UserRepo) ListByFilter(ctx context.Context, filter domain.UserListFilter, offset, limit int) ([]domain.User, error) {
	var models []UserModel
	err := applyUserListFilter(r.dbFor(ctx).Preload("UserGroup").Model(&UserModel{}), filter).
		Order("created_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&models).Error
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
	return r.CountByFilter(ctx, domain.UserListFilter{})
}

func (r *UserRepo) CountByFilter(ctx context.Context, filter domain.UserListFilter) (int64, error) {
	var count int64
	err := applyUserListFilter(r.db.WithContext(ctx).Model(&UserModel{}), filter).Count(&count).Error
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
	err := r.dbFor(ctx).Preload("UserGroup").Where("id IN ?", ids).Find(&models).Error
	if err != nil {
		return nil, fmt.Errorf("find users by ids: %w", err)
	}
	users := make([]domain.User, len(models))
	for i, m := range models {
		users[i] = *m.toDomain()
	}
	return users, nil
}

// FindInviterID returns the referral owner of the invite the user registered
// with, or nil when the user was not referred (e.g. admin invite / activation).
func (r *UserRepo) FindInviterID(ctx context.Context, userID uint) (*uint, error) {
	var row struct {
		ReferralOwnerUserID *uint
	}
	err := r.db.WithContext(ctx).
		Table("invite_uses AS iu").
		Select("i.referral_owner_user_id AS referral_owner_user_id").
		Joins("JOIN invites i ON i.code = iu.invite_code").
		Where("iu.user_id = ?", userID).
		Order("iu.used_at ASC").
		Limit(1).
		Scan(&row).Error
	if err != nil {
		return nil, fmt.Errorf("find inviter id: %w", err)
	}
	return row.ReferralOwnerUserID, nil
}

// ListInviteeIDs returns the ids of users who registered through the given
// user's referral invite, newest first.
func (r *UserRepo) ListInviteeIDs(ctx context.Context, ownerUserID uint) ([]uint, error) {
	var ids []uint
	err := r.db.WithContext(ctx).
		Table("invite_uses AS iu").
		Select("iu.user_id").
		Joins("JOIN invites i ON i.code = iu.invite_code").
		Where("i.referral_owner_user_id = ?", ownerUserID).
		Order("iu.used_at DESC").
		Scan(&ids).Error
	if err != nil {
		return nil, fmt.Errorf("list invitee ids: %w", err)
	}
	return ids, nil
}

func (r *UserRepo) ListUserGroups(ctx context.Context) ([]domain.UserGroup, error) {
	var models []UserGroupModel
	if err := r.db.WithContext(ctx).Order("id ASC").Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list user groups: %w", err)
	}
	groups := make([]domain.UserGroup, len(models))
	for i := range models {
		groups[i] = userGroupToDomain(models[i])
	}
	return groups, nil
}

func (r *UserRepo) FindUserGroupByID(ctx context.Context, id uint) (*domain.UserGroup, error) {
	var model UserGroupModel
	if err := r.db.WithContext(ctx).First(&model, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find user group: %w", err)
	}
	group := userGroupToDomain(model)
	return &group, nil
}

func (r *UserRepo) CreateUserGroup(ctx context.Context, group *domain.UserGroup) error {
	model := UserGroupModel{
		Code:        strings.TrimSpace(group.Code),
		Name:        strings.TrimSpace(group.Name),
		Description: strings.TrimSpace(group.Description),
		Enabled:     group.Enabled,
	}
	if err := r.db.WithContext(ctx).Create(&model).Error; err != nil {
		if isIAMDuplicateKeyError(err) {
			return domain.ErrInvalidUserGroup
		}
		return fmt.Errorf("create user group: %w", err)
	}
	*group = userGroupToDomain(model)
	return nil
}

func (r *UserRepo) UpdateUserGroup(ctx context.Context, group *domain.UserGroup) error {
	model := UserGroupModel{
		ID:          group.ID,
		Code:        group.Code,
		Name:        strings.TrimSpace(group.Name),
		Description: strings.TrimSpace(group.Description),
		Enabled:     group.Enabled,
	}
	if err := r.db.WithContext(ctx).
		Model(&UserGroupModel{}).
		Where("id = ?", group.ID).
		Select("name", "description", "enabled").
		Updates(model).Error; err != nil {
		return fmt.Errorf("update user group: %w", err)
	}
	return r.loadUserGroupModel(ctx, group)
}

func (r *UserRepo) loadUserGroupModel(ctx context.Context, group *domain.UserGroup) error {
	var model UserGroupModel
	if err := r.db.WithContext(ctx).First(&model, group.ID).Error; err != nil {
		return fmt.Errorf("reload user group: %w", err)
	}
	*group = userGroupToDomain(model)
	return nil
}

func (r *UserRepo) ListInvites(ctx context.Context, offset, limit int) ([]domain.Invite, error) {
	var models []InviteModel
	if err := r.db.WithContext(ctx).
		Where("invite_kind = ?", domain.InviteKindAdmin).
		Order("created_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&models).Error; err != nil {
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
	if err := r.db.WithContext(ctx).
		Model(&InviteModel{}).
		Where("invite_kind = ?", domain.InviteKindAdmin).
		Count(&count).Error; err != nil {
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

func (r *UserRepo) FindReferralInviteByOwner(ctx context.Context, userID uint) (*domain.Invite, error) {
	var model InviteModel
	err := r.db.WithContext(ctx).
		Where("invite_kind = ? AND referral_owner_user_id = ?", domain.InviteKindReferral, userID).
		First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find referral invite: %w", err)
	}
	return inviteToDomain(&model), nil
}

func (r *UserRepo) CreateInviteWithOperationLog(ctx context.Context, invite *domain.Invite, createdByUserID uint, log *governancedomain.OperationLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model := inviteFromDomain(invite)
		model.Kind = string(domain.InviteKindAdmin)
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
		if err := tx.Model(&InviteModel{}).
			Where("code = ? AND invite_kind = ?", invite.Code, domain.InviteKindAdmin).
			Select("enabled", "max_use", "expire_at").
			Updates(model).Error; err != nil {
			return fmt.Errorf("update invite: %w", err)
		}
		if err := r.operationLogs.CreateInTx(ctx, tx, log); err != nil {
			return fmt.Errorf("create invite operation log: %w", err)
		}
		return nil
	})
}

func (r *UserRepo) GetOrCreateReferralInvite(ctx context.Context, userID uint, code string, maxUse int) (*domain.Invite, error) {
	var invite domain.Invite
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user UserModel
		if err := tx.WithContext(ctx).
			Select("id").
			Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&user, userID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrUserNotFound
			}
			return fmt.Errorf("lock referral invite owner: %w", err)
		}

		var existing InviteModel
		err := tx.WithContext(ctx).
			Where("invite_kind = ? AND referral_owner_user_id = ?", domain.InviteKindReferral, userID).
			First(&existing).Error
		if err == nil {
			invite = *inviteToDomain(&existing)
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("find referral invite: %w", err)
		}

		model := InviteModel{
			Code:            strings.TrimSpace(code),
			Kind:            string(domain.InviteKindReferral),
			Enabled:         true,
			MaxUse:          maxUse,
			CreatedByUserID: &userID,
			ReferralOwnerID: &userID,
		}
		if err := tx.WithContext(ctx).Create(&model).Error; err != nil {
			if isIAMDuplicateKeyError(err) {
				return domain.ErrInviteAlreadyExists
			}
			return fmt.Errorf("create referral invite: %w", err)
		}
		invite = *inviteToDomain(&model)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &invite, nil
}

func isIAMDuplicateKeyError(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
