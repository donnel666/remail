package api

import (
	"bytes"
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/daum/domain"
	"github.com/donnel666/remail/internal/daum/infra"
	"github.com/gin-gonic/gin"
)

// The console contract deliberately does not embed the administrator resource.
type ownedDaumResourceResponse struct {
	ID            uint      `json:"id"`
	Version       uint64    `json:"version"`
	Type          string    `json:"type"`
	Email         string    `json:"email"`
	Suffix        string    `json:"suffix"`
	Status        string    `json:"status"`
	ForSale       bool      `json:"forSale"`
	LongLived     bool      `json:"longLived"`
	QualityScore  int       `json:"qualityScore"`
	LastSafeError string    `json:"lastSafeError,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type ownedDaumResourceListResponse struct {
	Items       []ownedDaumResourceResponse `json:"items"`
	Total       *int64                      `json:"total,omitempty"`
	Offset      int                         `json:"offset"`
	Limit       int                         `json:"limit"`
	NextAfterID *uint                       `json:"nextAfterId"`
	HasMore     bool                        `json:"hasMore"`
	Facets      infra.ResourceFacets        `json:"facets"`
}

func validOwnedFilter(c *gin.Context, filter infra.ResourceFilter) bool {
	if filter.OwnerID != nil || (filter.Suffix != "" && strings.ToLower(strings.TrimSpace(filter.Suffix)) != "daum.net") {
		writeDaumError(c, domain.ErrInvalidResource)
		return false
	}
	return true
}

func (h *handler) listOwned(c *gin.Context) {
	owner := currentOwner(c)
	if owner == nil {
		return
	}
	filter, ok := parseFilter(c)
	if !ok || !validOwnedFilter(c, filter) {
		return
	}
	if filter.Limit == 0 {
		filter.Limit = defaultLimit
	}
	page, err := h.module.Service.ListOwnedResources(c.Request.Context(), *owner, filter)
	if err != nil {
		writeDaumError(c, err)
		return
	}
	result := ownedDaumResourceListResponse{Items: make([]ownedDaumResourceResponse, 0, len(page.Items)), Offset: filter.Offset, Limit: filter.Limit, NextAfterID: page.NextAfterID, HasMore: page.HasMore, Facets: page.Facets}
	for _, row := range page.Items {
		result.Items = append(result.Items, ownedDaumResourceResponse{ID: row.ID, Version: row.Version, Type: domain.ResourceType, Email: row.EmailAddress, Suffix: row.EmailDomain, Status: row.Status, ForSale: row.ForSale, LongLived: row.LongLived, QualityScore: row.QualityScore, LastSafeError: ownedResourceError(row), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt})
	}
	if page.Total >= 0 {
		result.Total = &page.Total
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

func ownedResourceError(row infra.Resource) string {
	if row.LastSafeError == "" {
		return ""
	}
	switch row.FailureCategory {
	case "invalid_credentials":
		return "Daum mailbox authentication failed. Check the account credentials."
	case "identity_mismatch":
		return "Daum mailbox identity could not be verified."
	case "account_disabled":
		return "Daum mailbox is unavailable. Check the account status."
	}
	switch {
	case strings.Contains(row.LastSafeError, "stage=credentials;"):
		return "Daum mailbox credentials need to be checked."
	case strings.Contains(row.LastSafeError, "stage=connect_tls;"), strings.Contains(row.LastSafeError, "stage=greeting;"):
		return "Daum mailbox connection failed. Please retry validation."
	case strings.Contains(row.LastSafeError, "stage=authenticate;"):
		return "Daum mailbox authentication could not be completed."
	case strings.Contains(row.LastSafeError, "stage=mailbox_access;"):
		return "Daum mailbox could not be read. Please retry validation."
	default:
		return "Daum mailbox verification could not be completed. Please retry validation."
	}
}

func (h *handler) importOwned(c *gin.Context) {
	owner := currentOwner(c)
	if owner == nil {
		return
	}
	limitImportBody(c)
	if c.PostForm("ownerId") != "" {
		writeDaumError(c, domain.ErrInvalidResource)
		return
	}
	h.importFile(c, *owner, *owner, true)
}

func (h *handler) getImportOwned(c *gin.Context) {
	if owner := currentOwner(c); owner != nil {
		h.getImport(c, owner, false)
	}
}
func (h *handler) getImportItemsOwned(c *gin.Context) {
	if owner := currentOwner(c); owner != nil {
		h.getImport(c, owner, true)
	}
}
func (h *handler) getImportFailuresOwned(c *gin.Context) {
	if owner := currentOwner(c); owner != nil {
		h.getImportFailures(c, owner)
	}
}

func importResponse(status *infra.ImportStatus, reused, owned bool) daumImportResponse {
	result := toImportResponse(status, reused)
	if owned && result.LastSafeError != "" {
		result.LastSafeError = "Daum import could not be completed."
	}
	return result
}

func ownedImportItem(item infra.ImportItemStatus) daumImportItemResponse {
	result := daumImportItemResponse{Line: item.LineNumber, Outcome: item.Outcome}
	switch item.Category {
	case "":
	case "duplicate", "duplicate_email", "owner_conflict":
		result.Category, result.LastSafeError = "duplicate_email", "Email address already exists."
	case "invalid_format", "invalid_length":
		result.Category, result.LastSafeError = item.Category, "Invalid Daum import entry."
	default:
		result.Category, result.LastSafeError = "import_failed", "Daum import entry could not be imported."
	}
	return result
}

func (h *handler) writeOwnedImportFailures(c *gin.Context, importID uint64) {
	var content bytes.Buffer
	writer := csv.NewWriter(&content)
	_ = writer.Write([]string{"line", "category", "message"})
	for offset := 0; ; offset += maxLimit {
		page, err := h.module.Service.ListImportItems(c.Request.Context(), importID, offset, maxLimit)
		if err != nil {
			writeDaumError(c, err)
			return
		}
		for _, item := range page.Items {
			if item.Outcome == "failed" || item.Outcome == "skipped" {
				safe := ownedImportItem(item)
				_ = writer.Write([]string{strconv.Itoa(safe.Line), safe.Category, safe.LastSafeError})
			}
		}
		if int64(offset+len(page.Items)) >= page.Total || len(page.Items) == 0 {
			break
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		writeDaumError(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="daum-import-failures.csv"`)
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/csv; charset=utf-8", content.Bytes())
}

func allowOwnedAction(c *gin.Context, action string) bool {
	switch action {
	case "validate", "delete":
		return true
	case "publish":
		role, ok := middleware.GetCurrentRole(c)
		if ok && role.HasSupplierAccess() {
			return true
		}
		c.JSON(http.StatusForbidden, gin.H{"message": "Permission denied.", "requestId": middleware.GetRequestID(c)})
	default:
		writeDaumError(c, domain.ErrInvalidResource)
	}
	return false
}

func (h *handler) commandOwned(action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if owner := currentOwner(c); owner != nil && allowOwnedAction(c, action) {
			h.execute(c, owner, action)
		}
	}
}
func (h *handler) bulkOwned(c *gin.Context) {
	if owner := currentOwner(c); owner != nil && allowOwnedAction(c, c.Param("command")) {
		h.submitBulk(c, owner, c.Param("command"))
	}
}
func (h *handler) getBulkOwned(c *gin.Context) { h.getBulk(c, false) }
