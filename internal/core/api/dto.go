package api

import "time"

// --- Request types ---

// CreateMailServerRequest represents a mail server creation request.
type CreateMailServerRequest struct {
	Name          string `json:"name"`
	ServerAddress string `json:"serverAddress" binding:"required"`
	MXRecord      string `json:"mxRecord"`
	SPFRecord     string `json:"spfRecord"`
	DKIMRecord    string `json:"dkimRecord"`
	DMARCRecord   string `json:"dmarcRecord"`
	PTRRecord     string `json:"ptrRecord"`
}

// CreateDomainRequest represents a domain resource creation request.
type CreateDomainRequest struct {
	Domain       string `json:"domain" binding:"required"`
	MailServerID uint   `json:"mailServerId"`
	Purpose      string `json:"purpose"`
}

// ResourceBulkFilterRequest describes the filter used by an "all matching" bulk command.
type ResourceBulkFilterRequest struct {
	ResourceType   string     `json:"resourceType"`
	Search         string     `json:"search,omitempty"`
	Suffix         string     `json:"suffix,omitempty"`
	TLD            string     `json:"tld,omitempty"`
	Status         string     `json:"status,omitempty"`
	Purpose        string     `json:"purpose,omitempty"`
	ForSale        *bool      `json:"forSale,omitempty"`
	LongLived      *bool      `json:"longLived,omitempty"`
	GraphAvailable *bool      `json:"graphAvailable,omitempty"`
	CreatedFrom    *time.Time `json:"createdFrom,omitempty"`
	CreatedTo      *time.Time `json:"createdTo,omitempty"`
}

// ResourceBulkSelectionRequest describes how a bulk command selects resources.
type ResourceBulkSelectionRequest struct {
	Mode        string                    `json:"mode" binding:"required,oneof=ids filter"`
	ResourceIDs []uint                    `json:"resourceIds,omitempty"`
	Filter      ResourceBulkFilterRequest `json:"filter,omitempty"`
}

// PublishResourcesRequest is the request body for batch resource publish.
type PublishResourcesRequest struct {
	Selection ResourceBulkSelectionRequest `json:"selection" binding:"required"`
}

// DeleteResourcesRequest is the request body for batch resource delete.
type DeleteResourcesRequest struct {
	Selection ResourceBulkSelectionRequest `json:"selection" binding:"required"`
}

// ValidateResourcesRequest is the request body for batch resource validation.
type ValidateResourcesRequest struct {
	Selection ResourceBulkSelectionRequest `json:"selection" binding:"required"`
}

// CreateProjectApplicationRequest creates a user project application.
type CreateProjectApplicationRequest struct {
	Name           string                   `json:"name" binding:"required"`
	TargetPlatform string                   `json:"targetPlatform" binding:"required"`
	LogoURL        string                   `json:"logoUrl,omitempty"`
	Description    string                   `json:"description,omitempty"`
	AccessType     string                   `json:"accessType,omitempty"`
	LooseMatch     *bool                    `json:"looseMatch,omitempty"`
	MailRules      []ProjectMailRuleRequest `json:"mailRules,omitempty"`
}

// AdminCreateProjectRequest creates a complete listed project.
type AdminCreateProjectRequest struct {
	Name           string                   `json:"name" binding:"required"`
	TargetPlatform string                   `json:"targetPlatform" binding:"required"`
	LogoURL        string                   `json:"logoUrl,omitempty"`
	Description    string                   `json:"description,omitempty"`
	AccessType     string                   `json:"accessType,omitempty"`
	AccessUserIDs  []uint                   `json:"accessUserIds,omitempty"`
	LooseMatch     *bool                    `json:"looseMatch,omitempty"`
	Products       []ProjectProductRequest  `json:"products" binding:"required"`
	MailRules      []ProjectMailRuleRequest `json:"mailRules" binding:"required"`
}

// AdminRejectProjectRequest rejects a reviewing project application.
type AdminRejectProjectRequest struct {
	ReviewReason string `json:"reviewReason" binding:"required"`
}

// ProjectBulkFilterRequest describes an admin project bulk command filter.
type ProjectBulkFilterRequest struct {
	Status         string     `json:"status,omitempty"`
	AccessType     string     `json:"accessType,omitempty"`
	LooseMatch     *bool      `json:"looseMatch,omitempty"`
	ProductType    string     `json:"productType,omitempty"`
	Search         string     `json:"search,omitempty"`
	TargetPlatform string     `json:"targetPlatform,omitempty"`
	CreatedFrom    *time.Time `json:"createdFrom,omitempty"`
	CreatedTo      *time.Time `json:"createdTo,omitempty"`
}

// ProjectBulkSelectionRequest selects projects by IDs or by a filter.
type ProjectBulkSelectionRequest struct {
	Mode       string                   `json:"mode" binding:"required,oneof=ids filter"`
	ProjectIDs []uint                   `json:"projectIds,omitempty"`
	Filter     ProjectBulkFilterRequest `json:"filter,omitempty"`
}

// ProjectBulkCommandRequest is the request body for admin project bulk commands.
type ProjectBulkCommandRequest struct {
	Selection ProjectBulkSelectionRequest `json:"selection" binding:"required"`
}

// GrantProjectAccessRequest grants a user access to a private project.
type GrantProjectAccessRequest struct {
	UserID uint `json:"userId" binding:"required"`
}

// ProjectProductRequest describes one project product under the Project aggregate.
type ProjectProductRequest struct {
	Type                    string `json:"type" binding:"required"`
	Status                  string `json:"status,omitempty"`
	CodeEnabled             bool   `json:"codeEnabled"`
	PurchaseEnabled         bool   `json:"purchaseEnabled"`
	CodePrice               string `json:"codePrice,omitempty"`
	PurchasePrice           string `json:"purchasePrice,omitempty"`
	CodeSupplierPrice       string `json:"codeSupplierPrice,omitempty"`
	PurchaseSupplierPrice   string `json:"purchaseSupplierPrice,omitempty"`
	CodeWindowMinutes       int    `json:"codeWindowMinutes,omitempty"`
	ActivationWindowMinutes int    `json:"activationWindowMinutes,omitempty"`
	WarrantyMinutes         int    `json:"warrantyMinutes,omitempty"`
	MainWeight              int    `json:"mainWeight,omitempty"`
	DotWeight               int    `json:"dotWeight,omitempty"`
	PlusWeight              int    `json:"plusWeight,omitempty"`
}

// ProjectMailRuleRequest describes one mail matching rule.
type ProjectMailRuleRequest struct {
	RuleType string `json:"ruleType" binding:"required"`
	Pattern  string `json:"pattern" binding:"required"`
	Enabled  bool   `json:"enabled"`
}

// --- Response types ---

// ResourceItemResponse is the API-safe resource list item.
type ResourceItemResponse struct {
	ID             uint      `json:"id"`
	Type           string    `json:"type"`
	OwnerID        uint      `json:"ownerId"`
	Status         string    `json:"status"`
	ForSale        *bool     `json:"forSale,omitempty"`
	LongLived      *bool     `json:"longLived,omitempty"`
	GraphAvailable *bool     `json:"graphAvailable,omitempty"`
	LastSafeError  string    `json:"lastSafeError,omitempty"`
	Email          string    `json:"email,omitempty"`
	Domain         string    `json:"domain,omitempty"`
	DomainTLD      string    `json:"domainTld,omitempty"`
	MailServerID   uint      `json:"mailServerId,omitempty"`
	Purpose        string    `json:"purpose,omitempty"`
	MailboxCount   int       `json:"mailboxCount,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}

// ResourceListResponse is the paginated resource list response.
type ResourceListResponse struct {
	Items  []ResourceItemResponse `json:"items"`
	Total  int64                  `json:"total"`
	Offset int                    `json:"offset"`
	Limit  int                    `json:"limit"`
}

// MicrosoftResourceDetailResponse is the API-safe Microsoft resource detail (no credentials).
type MicrosoftResourceDetailResponse struct {
	ID              uint       `json:"id"`
	EmailAddress    string     `json:"emailAddress"`
	ForSale         bool       `json:"forSale"`
	LongLived       bool       `json:"longLived"`
	GraphAvailable  bool       `json:"graphAvailable"`
	Status          string     `json:"status"`
	QualityScore    int        `json:"qualityScore"`
	LastSafeError   string     `json:"lastSafeError"`
	LastAllocatedAt *time.Time `json:"lastAllocatedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}

// DomainResourceDetailResponse is the API-safe domain resource detail.
type DomainResourceDetailResponse struct {
	ID              uint       `json:"id"`
	Domain          string     `json:"domain"`
	MailServerID    uint       `json:"mailServerId"`
	Purpose         string     `json:"purpose"`
	Status          string     `json:"status"`
	LastSafeError   string     `json:"lastSafeError,omitempty"`
	LastAllocatedAt *time.Time `json:"lastAllocatedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}

// ImportResponse returns the import result.
type ImportResponse struct {
	ImportID uint `json:"importId"`
	Imported int  `json:"imported"`
}

// ImportStatusResponse returns the safe asynchronous import status.
type ImportStatusResponse struct {
	ImportID      uint      `json:"importId"`
	Status        string    `json:"status"`
	Imported      int       `json:"imported"`
	LastSafeError string    `json:"lastSafeError,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// PublishResourcesResponse returns the batch publish result.
type PublishResourcesResponse struct {
	Requested            int    `json:"requested"`
	Published            int    `json:"published"`
	PublishedResourceIDs []uint `json:"publishedResourceIds,omitempty"`
}

// DeleteResourcesResponse returns the batch delete result.
type DeleteResourcesResponse struct {
	Requested          int    `json:"requested"`
	Deleted            int    `json:"deleted"`
	DeletedResourceIDs []uint `json:"deletedResourceIds,omitempty"`
}

// ResourceValidationResponse returns an asynchronous validation job view.
type ResourceValidationResponse struct {
	ValidationID  uint      `json:"validationId"`
	ResourceID    uint      `json:"resourceId"`
	ResourceType  string    `json:"resourceType"`
	Status        string    `json:"status"`
	LastSafeError string    `json:"lastSafeError,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// ResourceValidationsResponse returns a bulk asynchronous validation submission result.
type ResourceValidationsResponse struct {
	Requested int `json:"requested"`
	Queued    int `json:"queued"`
}

// ServerItemResponse is the mail server list item.
type ServerItemResponse struct {
	ID            uint      `json:"id"`
	Name          string    `json:"name"`
	ServerAddress string    `json:"serverAddress"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"createdAt"`
}

// ServerListResponse is the paginated mail server list.
type ServerListResponse struct {
	Items  []ServerItemResponse `json:"items"`
	Total  int64                `json:"total"`
	Offset int                  `json:"offset"`
	Limit  int                  `json:"limit"`
}

// ServerCreateResponse is the response after creating a mail server.
type ServerCreateResponse struct {
	ID            uint      `json:"id"`
	Name          string    `json:"name"`
	ServerAddress string    `json:"serverAddress"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"createdAt"`
}

// MailboxItemResponse is the generated mailbox list item.
type MailboxItemResponse struct {
	ID              uint       `json:"id"`
	Email           string     `json:"email"`
	Status          string     `json:"status"`
	LastAllocatedAt *time.Time `json:"lastAllocatedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}

// MailboxListResponse is the paginated mailbox list.
type MailboxListResponse struct {
	Items  []MailboxItemResponse `json:"items"`
	Total  int64                 `json:"total"`
	Offset int                   `json:"offset"`
	Limit  int                   `json:"limit"`
}

// ProjectItemResponse is a project list item safe for user/admin consoles.
type ProjectItemResponse struct {
	ID              uint                            `json:"id"`
	Name            string                          `json:"name"`
	TargetPlatform  string                          `json:"targetPlatform"`
	LogoURL         string                          `json:"logoUrl,omitempty"`
	Description     string                          `json:"description,omitempty"`
	Status          string                          `json:"status"`
	AccessType      string                          `json:"accessType"`
	ApplicantUserID *uint                           `json:"applicantUserId,omitempty"`
	ReviewReason    string                          `json:"reviewReason,omitempty"`
	LooseMatch      bool                            `json:"looseMatch"`
	ProductCount    int                             `json:"productCount"`
	MailRuleCount   int                             `json:"mailRuleCount"`
	Products        []ProjectProductSummaryResponse `json:"products,omitempty"`
	CreatedAt       time.Time                       `json:"createdAt"`
	UpdatedAt       time.Time                       `json:"updatedAt"`
}

// ProjectListResponse is the paginated project list response.
type ProjectListResponse struct {
	Items  []ProjectItemResponse      `json:"items"`
	Total  int64                      `json:"total"`
	Offset int                        `json:"offset"`
	Limit  int                        `json:"limit"`
	Facets *ProjectListFacetsResponse `json:"facets,omitempty"`
}

type ProjectListFacetsResponse struct {
	Status      ProjectStatusFacetsResponse      `json:"status"`
	Access      ProjectAccessFacetsResponse      `json:"access"`
	Match       ProjectMatchFacetsResponse       `json:"match"`
	ProductType ProjectProductTypeFacetsResponse `json:"productType"`
}

type ProjectStatusFacetsResponse struct {
	All       int64 `json:"all"`
	Listed    int64 `json:"listed"`
	Reviewing int64 `json:"reviewing"`
	Rejected  int64 `json:"rejected"`
}

type ProjectAccessFacetsResponse struct {
	All     int64 `json:"all"`
	Public  int64 `json:"public"`
	Private int64 `json:"private"`
}

type ProjectMatchFacetsResponse struct {
	All    int64 `json:"all"`
	Loose  int64 `json:"loose"`
	Strict int64 `json:"strict"`
}

type ProjectProductTypeFacetsResponse struct {
	All       int64 `json:"all"`
	Microsoft int64 `json:"microsoft"`
	Domain    int64 `json:"domain"`
}

// ProjectProductResponse is a product view under a project.
type ProjectProductResponse struct {
	ID                      uint      `json:"id"`
	ProjectID               uint      `json:"projectId"`
	Type                    string    `json:"type"`
	Status                  string    `json:"status"`
	CodeEnabled             bool      `json:"codeEnabled"`
	PurchaseEnabled         bool      `json:"purchaseEnabled"`
	CodePrice               string    `json:"codePrice"`
	PurchasePrice           string    `json:"purchasePrice"`
	CodeSupplierPrice       string    `json:"codeSupplierPrice,omitempty"`
	PurchaseSupplierPrice   string    `json:"purchaseSupplierPrice,omitempty"`
	CodeWindowMinutes       int       `json:"codeWindowMinutes"`
	ActivationWindowMinutes int       `json:"activationWindowMinutes"`
	WarrantyMinutes         int       `json:"warrantyMinutes"`
	MainWeight              *int      `json:"mainWeight,omitempty"`
	DotWeight               *int      `json:"dotWeight,omitempty"`
	PlusWeight              *int      `json:"plusWeight,omitempty"`
	CreatedAt               time.Time `json:"createdAt"`
	UpdatedAt               time.Time `json:"updatedAt"`
}

// ProjectProductSummaryResponse is a safe product summary embedded in project lists.
type ProjectProductSummaryResponse struct {
	ID                      uint   `json:"id"`
	Type                    string `json:"type"`
	Status                  string `json:"status"`
	CodeEnabled             bool   `json:"codeEnabled"`
	PurchaseEnabled         bool   `json:"purchaseEnabled"`
	CodePrice               string `json:"codePrice"`
	PurchasePrice           string `json:"purchasePrice"`
	CodeWindowMinutes       int    `json:"codeWindowMinutes"`
	ActivationWindowMinutes int    `json:"activationWindowMinutes"`
	WarrantyMinutes         int    `json:"warrantyMinutes"`
	TotalAvailable          int64  `json:"totalAvailable"`
}

// ProjectMailRuleResponse is a mail matching rule view.
type ProjectMailRuleResponse struct {
	ID        uint      `json:"id"`
	ProjectID uint      `json:"projectId"`
	RuleType  string    `json:"ruleType"`
	Pattern   string    `json:"pattern"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ProjectAccessResponse is a private-project authorization view for admins.
type ProjectAccessResponse struct {
	ID        uint      `json:"id"`
	ProjectID uint      `json:"projectId"`
	UserID    uint      `json:"userId"`
	GrantedBy uint      `json:"grantedBy"`
	CreatedAt time.Time `json:"createdAt"`
}

// ProjectAccessListResponse returns private-project authorization rows.
type ProjectAccessListResponse struct {
	Items []ProjectAccessResponse `json:"items"`
	Total int                     `json:"total"`
}

// ProjectDetailResponse returns the Project aggregate detail.
type ProjectDetailResponse struct {
	Project   ProjectItemResponse       `json:"project"`
	Products  []ProjectProductResponse  `json:"products"`
	MailRules []ProjectMailRuleResponse `json:"mailRules,omitempty"`
	Accesses  []ProjectAccessResponse   `json:"accesses,omitempty"`
}

// ProjectBulkCommandResponse returns the number of projects affected by a bulk command.
type ProjectBulkCommandResponse struct {
	Affected int `json:"affected"`
}

// ProjectLogoUploadResponse returns the stable URL saved in project.logoUrl.
type ProjectLogoUploadResponse struct {
	LogoURL string `json:"logoUrl"`
}
