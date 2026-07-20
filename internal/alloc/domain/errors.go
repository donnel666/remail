package domain

import "errors"

var (
	ErrInvalidAllocationRequest       = errors.New("invalid allocation request")
	ErrAllocationNotFound             = errors.New("allocation not found")
	ErrAllocationConflict             = errors.New("allocation conflict")
	ErrActiveAllocation               = errors.New("resource has an active allocation")
	ErrAllocationTxRequired           = errors.New("allocation transaction is required")
	ErrInsufficientInventory          = errors.New("insufficient inventory")
	ErrProjectNotAllocatable          = errors.New("project is not allocatable")
	ErrInventoryRefreshInProgress     = errors.New("inventory refresh is in progress")
	ErrCandidateRefreshInfrastructure = errors.New("candidate refresh infrastructure failure")
)
