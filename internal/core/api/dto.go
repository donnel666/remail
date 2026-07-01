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
	MailServerID uint   `json:"mailServerId" binding:"required"`
	Purpose      string `json:"purpose" binding:"required"`
}

// PublishResourcesRequest is the request body for batch resource publish.
type PublishResourcesRequest struct {
	ResourceIDs []uint `json:"resourceIds" binding:"required,min=1,dive,required"`
}

// --- Response types ---

// ResourceItemResponse is the API-safe resource list item.
type ResourceItemResponse struct {
	ID            uint      `json:"id"`
	Type          string    `json:"type"`
	OwnerID       uint      `json:"ownerId"`
	Status        string    `json:"status"`
	ForSale       *bool     `json:"forSale,omitempty"`
	LongLived     *bool     `json:"longLived,omitempty"`
	LastSafeError string    `json:"lastSafeError,omitempty"`
	Email         string    `json:"email,omitempty"`
	Domain        string    `json:"domain,omitempty"`
	Purpose       string    `json:"purpose,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
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
	LastAllocatedAt *time.Time `json:"lastAllocatedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}

// ImportResponse returns the import result.
type ImportResponse struct {
	ImportID uint `json:"importId"`
	Imported int  `json:"imported"`
}

// PublishResourcesResponse returns the batch publish result.
type PublishResourcesResponse struct {
	Requested int `json:"requested"`
	Published int `json:"published"`
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
