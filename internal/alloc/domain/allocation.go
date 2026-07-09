package domain

import "time"

type AllocationType string

const (
	AllocationTypeMicrosoft AllocationType = "microsoft"
	AllocationTypeDomain    AllocationType = "domain"
)

type AllocationStatus string

const (
	AllocationStatusAllocated AllocationStatus = "allocated"
	AllocationStatusReleased  AllocationStatus = "released"
)

type MicrosoftMailbox string

const (
	MicrosoftMailboxMain  MicrosoftMailbox = "main"
	MicrosoftMailboxAlias MicrosoftMailbox = "alias"
	MicrosoftMailboxDot   MicrosoftMailbox = "dot"
	MicrosoftMailboxPlus  MicrosoftMailbox = "plus"
)

type SupplyScope string

const (
	SupplyScopePublic SupplyScope = "public"
	SupplyScopeOwned  SupplyScope = "owned"
)

type DailyUsageKind string

const (
	DailyUsageKindPlus          DailyUsageKind = "plus"
	DailyUsageKindDomainMailbox DailyUsageKind = "domain_mailbox"
)

type OrderGuard struct {
	OrderNo   string
	Type      AllocationType
	CreatedAt time.Time
}

type MicrosoftAllocation struct {
	ID              uint
	OrderNo         string
	ProjectID       uint
	ProductID       uint
	ResourceID      uint
	SupplyScope     SupplyScope
	Mailbox         MicrosoftMailbox
	ExplicitAliasID *uint
	DotAliasID      *uint
	PlusAliasID     *uint
	Email           string
	Status          AllocationStatus
	CreatedAt       time.Time
	ReleasedAt      *time.Time
}

type GeneratedMailboxAllocation struct {
	ID          uint
	OrderNo     string
	ProjectID   uint
	ProductID   uint
	ResourceID  uint
	SupplyScope SupplyScope
	MailboxID   uint
	Email       string
	Status      AllocationStatus
	CreatedAt   time.Time
	ReleasedAt  *time.Time
}

type UnifiedAllocation struct {
	Type        AllocationType
	ID          uint
	OrderNo     string
	ProjectID   uint
	ProductID   uint
	ResourceID  uint
	SupplyScope SupplyScope
	Mailbox     string
	Email       string
	Status      AllocationStatus
	CreatedAt   time.Time
	ReleasedAt  *time.Time
}

func IsValidAllocationType(value AllocationType) bool {
	return value == AllocationTypeMicrosoft || value == AllocationTypeDomain
}

func IsValidAllocationStatus(value AllocationStatus) bool {
	return value == AllocationStatusAllocated || value == AllocationStatusReleased
}

func IsValidMicrosoftMailbox(value MicrosoftMailbox) bool {
	switch value {
	case MicrosoftMailboxMain, MicrosoftMailboxAlias, MicrosoftMailboxDot, MicrosoftMailboxPlus:
		return true
	default:
		return false
	}
}

func NormalizeSupplyScope(value SupplyScope) SupplyScope {
	if value == SupplyScopeOwned {
		return SupplyScopeOwned
	}
	return SupplyScopePublic
}

func IsValidSupplyScope(value SupplyScope) bool {
	return value == SupplyScopePublic || value == SupplyScopeOwned
}
