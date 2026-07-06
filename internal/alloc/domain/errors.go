package domain

import "errors"

var (
	ErrInvalidAllocationRequest = errors.New("invalid allocation request")
	ErrAllocationNotFound       = errors.New("allocation not found")
	ErrAllocationConflict       = errors.New("allocation conflict")
	ErrInsufficientInventory    = errors.New("insufficient inventory")
	ErrProjectNotAllocatable    = errors.New("project is not allocatable")
)
