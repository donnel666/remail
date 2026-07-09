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

	// UpdateWithOperationLog updates a user and writes an OperationLog in the
	// same database transaction.
	UpdateWithOperationLog(ctx context.Context, user *domain.User, log *governancedomain.OperationLog) error
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
	ReplaceUserPermissionPolicies(ctx context.Context, userID uint, policies []domain.PermissionPolicy) error
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
