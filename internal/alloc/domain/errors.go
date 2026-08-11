package domain

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidAllocationRequest          = errors.New("invalid allocation request")
	ErrAllocationNotFound                = errors.New("allocation not found")
	ErrAllocationConflict                = errors.New("allocation conflict")
	ErrActiveAllocation                  = errors.New("resource has an active allocation")
	ErrAllocationTxRequired              = errors.New("allocation transaction is required")
	ErrHistoricalAllocationOwnerRequired = errors.New("historical allocation owner is required")
	ErrInsufficientInventory             = errors.New("insufficient inventory")
	ErrDefinitiveInventoryExhausted      = fmt.Errorf("definitive inventory exhausted: %w", ErrInsufficientInventory)
	ErrProjectNotAllocatable             = errors.New("project is not allocatable")
	ErrInventoryRefreshInProgress        = errors.New("inventory refresh is in progress")
)
