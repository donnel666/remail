package app

import (
	"context"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/iam/domain"
)

// UserRepository defines the persistence contract for users.
// Implemented by the infra layer (GORM).
type UserRepository interface {
	// Create persists a new user. Returns ErrEmailAlreadyExists on duplicate.
	Create(ctx context.Context, user *domain.User) error

	// CreateWithInvite persists a new user and atomically consumes an invite.
	CreateWithInvite(ctx context.Context, user *domain.User, inviteCode string) error

	// FindByEmail looks up a user by email. Returns nil, nil if not found.
	FindByEmail(ctx context.Context, email string) (*domain.User, error)

	// FindByID looks up a user by primary key. Returns nil, nil if not found.
	FindByID(ctx context.Context, id uint) (*domain.User, error)

	// Update applies partial updates to an existing user.
	Update(ctx context.Context, user *domain.User) error

	// List returns a paginated slice of users ordered by created_at desc.
	List(ctx context.Context, offset, limit int) ([]domain.User, error)

	// Count returns the total number of users.
	Count(ctx context.Context) (int64, error)

	// ListByFilter returns a paginated slice of users matching admin filters.
	ListByFilter(ctx context.Context, filter domain.UserListFilter, offset, limit int) ([]domain.User, error)

	// CountByFilter returns the total number of users matching admin filters.
	CountByFilter(ctx context.Context, filter domain.UserListFilter) (int64, error)

	// FindByIDs returns users matching the given IDs.
	FindByIDs(ctx context.Context, ids []uint) ([]domain.User, error)

	ListUserGroups(ctx context.Context) ([]domain.UserGroup, error)
	FindUserGroupByID(ctx context.Context, id uint) (*domain.UserGroup, error)
	CreateUserGroup(ctx context.Context, group *domain.UserGroup) error
	UpdateUserGroup(ctx context.Context, group *domain.UserGroup) error

	// CreateFirstUser creates the first user in a serialized transaction.
	// Uses SELECT ... FOR UPDATE to prevent concurrent activation from creating
	// multiple super_admin users (docs/8-iam.md:88, INV-I8).
	// Returns ErrActivationAlreadyDone if a user already exists.
	// Returns ErrEmailAlreadyExists on email conflict.
	CreateFirstUser(ctx context.Context, user *domain.User) error

	// UpdateNonSuperAdminAccessWithOperationLog applies only the requested access
	// fields, atomically refuses a row whose current role is super_admin, and
	// writes the operation log in the same transaction.
	UpdateNonSuperAdminAccessWithOperationLog(ctx context.Context, userID uint, enabled *bool, role *domain.Role, userGroupID *uint, incrementTokenVersion bool, log *governancedomain.OperationLog) (*domain.User, error)

	// UpdateNonSuperAdminProfileWithOperationLog updates profile and access
	// fields (email, nickname, password, enabled, role, group) atomically,
	// refuses a super_admin row, and writes the operation log in one transaction.
	UpdateNonSuperAdminProfileWithOperationLog(ctx context.Context, userID uint, email, nickname, passwordHash *string, enabled *bool, role *domain.Role, userGroupID *uint, incrementTokenVersion bool, log *governancedomain.OperationLog) (*domain.User, error)

	// DeleteNonSuperAdminWithOperationLog hard-deletes a user, refusing a
	// super_admin row, and writes the operation log in the same transaction.
	DeleteNonSuperAdminWithOperationLog(ctx context.Context, userID uint, log *governancedomain.OperationLog) error

	// ResolveBulkUserIDs returns non-super-admin user IDs for a bulk selection,
	// capped at 1000. When ids is non-empty it selects those rows; otherwise it
	// applies the list filter.
	ResolveBulkUserIDs(ctx context.Context, ids []uint, filter domain.UserListFilter) ([]uint, error)

	// BatchSetEnabledNonSuperAdmin flips enabled for the given non-super-admin
	// rows whose value differs (bumping token_version on disable) and returns the
	// number of rows changed.
	BatchSetEnabledNonSuperAdmin(ctx context.Context, ids []uint, enabled bool) (int64, error)

	// BatchBumpTokenVersionNonSuperAdmin increments token_version for the given
	// non-super-admin rows and returns the number of rows changed.
	BatchBumpTokenVersionNonSuperAdmin(ctx context.Context, ids []uint) (int64, error)

	// BatchDeleteNonSuperAdmin hard-deletes the given non-super-admin rows and
	// returns the number of rows deleted.
	BatchDeleteNonSuperAdmin(ctx context.Context, ids []uint) (int64, error)

	// FacetsByFilter returns admin-list aggregate counts per role/status/group.
	FacetsByFilter(ctx context.Context, filter domain.UserListFilter, groups []domain.UserGroup) (*domain.UserFacets, error)

	// FindInviterID returns the referral owner of the invite the user registered
	// with, or nil when the user was not referred.
	FindInviterID(ctx context.Context, userID uint) (*uint, error)

	// ListInviteeIDs returns ids of users registered through the user's referral
	// invite, newest first.
	ListInviteeIDs(ctx context.Context, ownerUserID uint) ([]uint, error)
}

// InviteRepository defines administrator invite persistence.
type InviteRepository interface {
	ListInvites(ctx context.Context, offset, limit int) ([]domain.Invite, error)
	CountInvites(ctx context.Context) (int64, error)
	CreateInviteWithOperationLog(ctx context.Context, invite *domain.Invite, createdByUserID uint, log *governancedomain.OperationLog) error
	UpdateInviteWithOperationLog(ctx context.Context, invite *domain.Invite, log *governancedomain.OperationLog) error
	FindInviteByCode(ctx context.Context, code string) (*domain.Invite, error)
	FindReferralInviteByOwner(ctx context.Context, userID uint) (*domain.Invite, error)
	GetOrCreateReferralInvite(ctx context.Context, userID uint, code string, maxUse int) (*domain.Invite, error)
}

// SupplierApplicationRepository defines supplier permission application persistence.
type SupplierApplicationRepository interface {
	CreateSupplierApplicationReviewing(ctx context.Context, application *domain.SupplierApplication) error
	FindLatestSupplierApplicationByApplicantUserID(ctx context.Context, applicantUserID uint) (*domain.SupplierApplication, error)
	FindSupplierApplicationByID(ctx context.Context, id uint) (*domain.SupplierApplication, error)
	ListSupplierApplications(ctx context.Context, status string, offset, limit int) ([]domain.SupplierApplication, error)
	CountSupplierApplications(ctx context.Context, status string) (int64, error)
	ApproveSupplierApplicationWithUserAndLog(ctx context.Context, application *domain.SupplierApplication, user *domain.User, log *governancedomain.OperationLog) error
	RejectSupplierApplicationWithLog(ctx context.Context, application *domain.SupplierApplication, log *governancedomain.OperationLog) error
}

// PermissionRepository defines user-level Casbin policy management.
type PermissionRepository interface {
	ListUserPermissionPolicies(ctx context.Context, userID uint) ([]domain.PermissionPolicy, error)
	ReplaceUserPermissionPoliciesGuarded(ctx context.Context, userID uint, policies []domain.PermissionPolicy, allowSensitive bool) ([]domain.PermissionPolicy, error)
	Reload(ctx context.Context) error
}

// SessionStore defines the persistence contract for sessions.
// Implemented by the infra layer (Redis).
type SessionStore interface {
	// Create stores a new session with TTL.
	Create(ctx context.Context, session *domain.Session, ttlSeconds int) error

	// Get retrieves a session by ID. Returns nil, nil if not found.
	Get(ctx context.Context, sessionID string) (*domain.Session, error)

	// Delete removes a single session.
	Delete(ctx context.Context, sessionID string) error

	// DeleteByUserID removes all sessions for a given user.
	DeleteByUserID(ctx context.Context, userID uint) error
}

// CaptchaStore defines the persistence contract for captchas.
// Implemented by the infra layer (Redis).
type CaptchaStore interface {
	// Create stores a captcha with a TTL (typically 5 minutes).
	Create(ctx context.Context, captchaID, answer string, ttlSeconds int) error

	// Get retrieves a captcha answer. Returns "", nil if not found.
	Get(ctx context.Context, captchaID string) (string, error)

	// GetDel atomically retrieves and removes a captcha answer.
	GetDel(ctx context.Context, captchaID string) (string, error)

	// Delete removes a captcha (prevents replay).
	Delete(ctx context.Context, captchaID string) error
}

// EmailCodeStore defines storage for email verification codes.
type EmailCodeStore interface {
	// CreateIfAbsent stores a code with TTL and returns the existing code when
	// the same email is requested again before expiration.
	CreateIfAbsent(ctx context.Context, key, code string, ttlSeconds int) (storedCode string, reused bool, err error)

	// Get retrieves a code. Returns "", nil if not found.
	Get(ctx context.Context, key string) (string, error)

	// Delete removes a verification code.
	Delete(ctx context.Context, key string) error
}

// Hasher defines the password hashing contract.
// Implemented by the infra layer (bcrypt).
type Hasher interface {
	// Hash returns a bcrypt hash of the password.
	Hash(password string) (string, error)

	// Verify compares a password against a hash. Returns true on match.
	Verify(password, hash string) bool
}

// PermissionChecker checks fine-grained admin permissions.
type PermissionChecker interface {
	Check(ctx context.Context, userID uint, role domain.Role, resource, action string) (bool, error)
	Reload(ctx context.Context) error
}
