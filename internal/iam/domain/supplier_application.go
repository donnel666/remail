package domain

import "time"

// SupplierApplicationStatus represents the supplier permission application state.
type SupplierApplicationStatus string

const (
	SupplierApplicationReviewing SupplierApplicationStatus = "reviewing"
	SupplierApplicationApproved  SupplierApplicationStatus = "approved"
	SupplierApplicationRejected  SupplierApplicationStatus = "rejected"
	SupplierApplicationCanceled  SupplierApplicationStatus = "canceled"
)

// SupplierApplication records a normal user's request to become a supplier.
type SupplierApplication struct {
	ID              uint
	ApplicantUserID uint
	Reason          string
	Status          SupplierApplicationStatus
	ReviewReason    string
	ReviewedBy      *uint
	ReviewedAt      *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// IsValidSupplierApplicationStatus returns true for a known application state.
func IsValidSupplierApplicationStatus(status SupplierApplicationStatus) bool {
	switch status {
	case SupplierApplicationReviewing, SupplierApplicationApproved, SupplierApplicationRejected, SupplierApplicationCanceled:
		return true
	default:
		return false
	}
}
