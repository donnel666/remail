package domain

import "time"

// Role is the user's RBAC role assignment.
// Entitlements such as quota, discounts, and limits are modeled separately by UserGroup.
type Role string

const (
	RoleUser       Role = "user"
	RoleSupplier   Role = "supplier"
	RoleAdmin      Role = "admin"
	RoleSuperAdmin Role = "super_admin"
)

func (r Role) String() string {
	if r == "" {
		return string(RoleUser)
	}
	return string(r)
}

func (r Role) IsValid() bool {
	switch r {
	case RoleUser, RoleSupplier, RoleAdmin, RoleSuperAdmin:
		return true
	default:
		return false
	}
}

func (r Role) HasAdminAccess() bool {
	return r == RoleAdmin || r == RoleSuperAdmin
}

func (r Role) HasSupplierAccess() bool {
	return r == RoleSupplier || r == RoleAdmin || r == RoleSuperAdmin
}

type UserGroup struct {
	ID          uint
	Code        string
	Name        string
	Description string
	Enabled     bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// User represents a platform user (admin, supplier, or regular user).
// All roles share one user table (ADR-IAM-1).
type User struct {
	ID           uint
	Email        string
	PasswordHash string
	Nickname     string
	Enabled      bool
	Role         Role
	UserGroupID  uint
	UserGroup    UserGroup
	TokenVersion int
	LastLoginAt  *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type UserListFilter struct {
	IDs         []uint
	Search      string
	Role        *Role
	Enabled     *bool
	UserGroupID *uint
	CreatedFrom *time.Time
	CreatedTo   *time.Time
}

// UserFacets holds admin-list aggregate counts. Each dimension is counted with
// its own filter omitted, so selecting a role tab still shows the other tabs'
// counts.
type UserFacets struct {
	Role   map[string]int64
	Status StatusFacet
	Group  []GroupFacet
}

type StatusFacet struct {
	All      int64
	Enabled  int64
	Disabled int64
}

type GroupFacet struct {
	ID    uint
	Code  string
	Name  string
	Count int64
}

// UserSummary is a compact, safe read model of a user joined to its group,
// used to enrich cross-cutting admin views (invite owners, wallet balances).
// Never carries password/session/token facts.
type UserSummary struct {
	ID        uint
	Email     string
	Nickname  string
	Role      string
	GroupID   uint
	GroupName string
}

// IsActivationNeeded returns true when no users exist.
// This is the gate for the first-activation flow (INV-I8).
func IsActivationNeeded(userCount int64) bool {
	return userCount == 0
}
