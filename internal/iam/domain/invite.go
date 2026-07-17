package domain

import "time"

type InviteKind string

const (
	InviteKindAdmin    InviteKind = "admin"
	InviteKindReferral InviteKind = "referral"
)

// Invite is an administrator-managed registration invitation.
type Invite struct {
	Code            string
	Kind            InviteKind
	Enabled         bool
	MaxUse          int
	Used            int
	ExpireAt        *time.Time
	CreatedByUserID *uint
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// InviteUse is the immutable fact that an invitation was consumed by a user.
type InviteUse struct {
	ID         uint64
	InviteCode string
	UserID     uint
	UsedAt     time.Time
}

// InviteListFilter narrows the admin invite browse list. An empty Kind means
// "all kinds"; the other fields filter by the invite's owner (CreatedByUserID).
type InviteListFilter struct {
	Search       string
	Kind         InviteKind
	OwnerRole    *Role
	OwnerGroupID *uint
	Enabled      *bool
}

// InviteFacets holds admin invite browse-list aggregate counts, computed over
// all invites matching only the Kind filter so the filter chips stay stable.
type InviteFacets struct {
	Role    InviteRoleFacet
	Group   []GroupFacet
	Enabled InviteEnabledFacet
}

// InviteRoleFacet buckets invites by their owner's role. All counts every
// invite (including owner-less ones); the named buckets only the owned ones.
type InviteRoleFacet struct {
	All        int64
	User       int64
	Supplier   int64
	Admin      int64
	SuperAdmin int64
}

type InviteEnabledFacet struct {
	All      int64
	Enabled  int64
	Disabled int64
}
