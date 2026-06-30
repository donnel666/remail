package domain

// PermissionPolicy is a Casbin user-level permission override.
type PermissionPolicy struct {
	Resource string
	Action   string
	Effect   string
}

// PermissionCatalogItem describes an admin permission resource.
type PermissionCatalogItem struct {
	Resource string
	Actions  []string
}

// CasbinRule is the persisted Casbin policy representation.
type CasbinRule struct {
	ID    uint64
	Ptype string
	V0    string
	V1    string
	V2    string
	V3    string
	V4    string
	V5    string
}
