package domain

import "time"

// RoleLevel defines the user's role hierarchy.
// Privileged users inherit lower-level capabilities.
// See ADR-IAM-1 and ADR-IAM-4.
type RoleLevel int

const (
	RoleUser       RoleLevel = 10  // Normal user
	RoleSupplier   RoleLevel = 20  // Inherits user, adds supplier pages
	RoleAdmin      RoleLevel = 80  // Inherits user, adds admin pages
	RoleSuperAdmin RoleLevel = 100 // Inherits admin, adds system-sensitive pages
)

// IsAtLeast returns true if the role level is at least the given minimum.
func (r RoleLevel) IsAtLeast(min RoleLevel) bool {
	return r >= min
}

// Name returns the stable role name used by API responses and permission policy.
func (r RoleLevel) Name() string {
	switch r {
	case RoleSuperAdmin:
		return "super_admin"
	case RoleAdmin:
		return "admin"
	case RoleSupplier:
		return "supplier"
	case RoleUser:
		return "user"
	default:
		return "unknown"
	}
}

// User represents a platform user (admin, supplier, or regular user).
// All roles share one user table (ADR-IAM-1).
type User struct {
	ID           uint
	Email        string
	PasswordHash string
	Nickname     string
	Enabled      bool
	RoleLevel    RoleLevel
	TokenVersion int
	LastLoginAt  *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// IsActivationNeeded returns true when no users exist.
// This is the gate for the first-activation flow (INV-I8).
func IsActivationNeeded(userCount int64) bool {
	return userCount == 0
}
