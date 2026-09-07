package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/proto/app"
	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/donnel666/remail/internal/proto/infra"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/gin-gonic/gin"
)

const (
	defaultLimit   = 20
	maxLimit       = 200
	maxImportBytes = 512 << 20
)

type handler struct {
	module  *Module
	checker middleware.PermissionChecker
}
type OwnerSummary struct {
	ID        uint   `json:"id"`
	Email     string `json:"email"`
	Nickname  string `json:"nickname"`
	GroupName string `json:"groupName"`
	Role      string `json:"role"`
	Enabled   bool   `json:"enabled"`
}
type protoResourceResponse struct {
	ID                   uint          `json:"id"`
	Version              uint64        `json:"version"`
	OwnerUserID          uint          `json:"ownerUserId"`
	Owner                *OwnerSummary `json:"owner,omitempty"`
	Email                string        `json:"email"`
	Suffix               string        `json:"suffix"`
	Status               string        `json:"status"`
	ForSale              bool          `json:"forSale"`
	LongLived            bool          `json:"longLived"`
	QualityScore         int           `json:"qualityScore"`
	PasswordConfigured   bool          `json:"passwordConfigured"`
	CredentialRevision   uint64        `json:"credentialRevision"`
	CredentialUpdatedAt  time.Time     `json:"credentialUpdatedAt"`
	ValidationGeneration uint64        `json:"validationGeneration"`
	ValidationFailures   int           `json:"validationFailures"`
	LastSafeError        string        `json:"lastSafeError,omitempty"`
	LastCheckedAt        *time.Time    `json:"lastCheckedAt,omitempty"`
	LastAllocatedAt      *time.Time    `json:"lastAllocatedAt,omitempty"`
	CreatedAt            time.Time     `json:"createdAt"`
	UpdatedAt            time.Time     `json:"updatedAt"`
}
type protoResourceListResponse struct {
	Items       []protoResourceResponse `json:"items"`
	Total       *int64                  `json:"total,omitempty"`
	Offset      int                     `json:"offset"`
	Limit       int                     `json:"limit"`
	NextAfterID *uint                   `json:"nextAfterId"`
	HasMore     bool                    `json:"hasMore"`
	Facets      infra.ResourceFacets    `json:"facets"`
}
type protoImportResponse struct {
	ImportID         uint64    `json:"importId"`
	Status           string    `json:"status"`
	Accepted         int       `json:"accepted"`
	Imported         int       `json:"imported"`
	Skipped          int       `json:"skipped"`
	Failed           int       `json:"failed"`
	DispatchStatus   string    `json:"dispatchStatus"`
	DispatchAttempts int       `json:"dispatchAttempts"`
	FailureAvailable bool      `json:"failureAvailable"`
	LastSafeError    string    `json:"lastSafeError,omitempty"`
	Reused           bool      `json:"reused"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}
type protoImportItemResponse struct {
	Line          int    `json:"line"`
	ResourceID    *uint  `json:"resourceId,omitempty"`
	Outcome       string `json:"outcome"`
	Category      string `json:"category"`
	LastSafeError string `json:"lastSafeError,omitempty"`
}
type protoMaintenanceRunResponse struct {
	ID                   uint64     `json:"id"`
	ResourceID           uint       `json:"resourceId"`
	ValidationGeneration uint64     `json:"validationGeneration"`
	Kind                 string     `json:"kind"`
	Status               string     `json:"status"`
	Attempts             int        `json:"attempts"`
	MaxAttempts          int        `json:"maxAttempts"`
	CredentialRevision   uint64     `json:"credentialRevision"`
	RequestID            string     `json:"requestId,omitempty"`
	LastSafeError        string     `json:"lastSafeError,omitempty"`
	QueuedAt             time.Time  `json:"queuedAt"`
	StartedAt            *time.Time `json:"startedAt,omitempty"`
	FinishedAt           *time.Time `json:"finishedAt,omitempty"`
	CreatedAt            time.Time  `json:"createdAt"`
	UpdatedAt            time.Time  `json:"updatedAt"`
}
type protoCommandRequest struct {
	Version      uint64  `json:"version"`
	Email        *string `json:"email,omitempty"`
	OwnerID      *uint   `json:"ownerId,omitempty"`
	Password     *string `json:"password,omitempty"`
	QualityScore *int    `json:"qualityScore,omitempty"`
	LongLived    *bool   `json:"longLived,omitempty"`
}
type protoBatchRequest struct {
	ResourceIDs []uint               `json:"resourceIds,omitempty"`
	Selection   *infra.BulkSelection `json:"selection,omitempty"`
}

type protoReasonCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}
type protoBulkResponse struct {
	TaskID         string             `json:"taskId"`
	Kind           string             `json:"kind"`
	ResourceType   string             `json:"resourceType"`
	Status         string             `json:"status"`
	Action         string             `json:"action"`
	OperatorUserID uint               `json:"operatorUserId"`
	Requested      int                `json:"requested"`
	Processed      int                `json:"processed"`
	Affected       int                `json:"affected"`
	Skipped        int                `json:"skipped"`
	ReasonCounts   []protoReasonCount `json:"reasonCounts"`
	Attempts       int                `json:"attempts"`
	MaxAttempts    int                `json:"maxAttempts"`
	RequestID      string             `json:"requestId"`
	CreatedAt      time.Time          `json:"createdAt"`
	StartedAt      *time.Time         `json:"startedAt,omitempty"`
	UpdatedAt      time.Time          `json:"updatedAt"`
	FinishedAt     *time.Time         `json:"finishedAt,omitempty"`
}

func toBulkResponse(status *infra.BulkStatus) protoBulkResponse {
	result := protoBulkResponse{
		TaskID: status.TaskID, Kind: status.Kind, ResourceType: status.ResourceType, Status: status.Status, Action: status.Action,
		OperatorUserID: status.OperatorUserID, Requested: status.Requested, Processed: status.Processed, Affected: status.Affected, Skipped: status.Skipped,
		ReasonCounts: []protoReasonCount{}, Attempts: status.Attempts, MaxAttempts: status.MaxAttempts, RequestID: status.RequestID,
		CreatedAt: status.CreatedAt, StartedAt: status.StartedAt, UpdatedAt: status.UpdatedAt, FinishedAt: status.FinishedAt,
	}
	for reason, count := range status.ReasonCounts {
		result.ReasonCounts = append(result.ReasonCounts, protoReasonCount{reason, count})
	}
	sort.Slice(result.ReasonCounts, func(i, j int) bool { return result.ReasonCounts[i].Reason < result.ReasonCounts[j].Reason })
	return result
}

func currentOwner(c *gin.Context) *uint {
	id, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.Status(http.StatusUnauthorized)
		return nil
	}
	return &id
}
func (h *handler) listOwned(c *gin.Context) {
	if owner := currentOwner(c); owner != nil {
		h.list(c, owner)
	}
}
func (h *handler) listAdmin(c *gin.Context) { h.list(c, nil) }
func (h *handler) list(c *gin.Context, owner *uint) {
	f, ok := parseFilter(c)
	if !ok {
		return
	}
	if owner != nil {
		f.OwnerID = owner
		f.ExcludeDeleted = true
	} else if !h.resolveAdminSearch(c, &f) {
		return
	}
	page, err := h.module.Service.ListResources(c.Request.Context(), f)
	if err != nil {
		writeProtoError(c, err)
		return
	}
	items := make([]protoResourceResponse, len(page.Items))
	ids := make([]uint, 0, len(items))
	for i, row := range page.Items {
		items[i] = toResourceResponse(row)
		ids = append(ids, row.OwnerUserID)
	}
	if h.module.OwnerSummaries != nil {
		owners, err := h.module.OwnerSummaries(c.Request.Context(), ids)
		if err != nil {
			writeProtoError(c, err)
			return
		}
		for i := range items {
			if value, ok := owners[items[i].OwnerUserID]; ok {
				items[i].Owner = &value
			}
		}
	}
	c.Header("Cache-Control", "no-store")
	response := protoResourceListResponse{Items: items, Offset: f.Offset, Limit: f.Limit, NextAfterID: page.NextAfterID, HasMore: page.HasMore, Facets: page.Facets}
	if page.Total >= 0 {
		response.Total = &page.Total
	}
	c.JSON(http.StatusOK, response)
}

func (h *handler) resolveAdminSearch(c *gin.Context, filter *infra.ResourceFilter) bool {
	filter.AdminSearch = true
	filter.Search = strings.TrimSpace(filter.Search)
	if filter.Search == "" {
		return true
	}
	if len(filter.Search) > 320 {
		writeProtoError(c, domain.ErrInvalidResource)
		return false
	}
	if h.module.SearchOwners == nil {
		writeProtoError(c, domain.ErrDependency)
		return false
	}
	ids, err := h.module.SearchOwners(c.Request.Context(), filter.Search, 1000)
	if err != nil {
		writeProtoError(c, err)
		return false
	}
	filter.SearchOwnerIDs = ids
	return true
}
func parseFilter(c *gin.Context) (infra.ResourceFilter, bool) {
	f := infra.ResourceFilter{Search: c.Query("search"), Suffix: c.Query("suffix"), Status: c.Query("status"), Limit: defaultLimit}
	for _, field := range []struct {
		name   string
		target *int
	}{{"offset", &f.Offset}, {"limit", &f.Limit}} {
		if value := c.Query(field.name); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 0 {
				writeProtoError(c, domain.ErrInvalidResource)
				return f, false
			}
			*field.target = parsed
		}
	}
	for _, field := range []struct {
		name   string
		target **bool
	}{{"forSale", &f.ForSale}, {"longLived", &f.LongLived}} {
		if value := c.Query(field.name); value != "" {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				writeProtoError(c, domain.ErrInvalidResource)
				return f, false
			}
			*field.target = &parsed
		}
	}
	for _, field := range []struct {
		name   string
		target **time.Time
	}{{"createdFrom", &f.CreatedFrom}, {"createdTo", &f.CreatedTo}} {
		if value := c.Query(field.name); value != "" {
			parsed, err := time.Parse(time.RFC3339, value)
			if err != nil {
				writeProtoError(c, domain.ErrInvalidResource)
				return f, false
			}
			*field.target = &parsed
		}
	}
	if value := c.Query("afterId"); value != "" {
		id, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			writeProtoError(c, domain.ErrInvalidResource)
			return f, false
		}
		f.AfterID = uint(id)
	}
	if value := c.Query("ownerId"); value != "" {
		id, err := strconv.ParseUint(value, 10, 64)
		if err != nil || id == 0 {
			writeProtoError(c, domain.ErrInvalidResource)
			return f, false
		}
		owner := uint(id)
		f.OwnerID = &owner
	}
	f.SkipTotal = c.Query("includeTotal") == "false"
	f.SkipFacets = c.Query("includeFacets") == "false"
	return f, true
}
func (h *handler) getOwned(c *gin.Context) {
	if owner := currentOwner(c); owner != nil {
		h.getResource(c, owner)
	}
}
func (h *handler) getAdmin(c *gin.Context) { h.getResource(c, nil) }
func (h *handler) getResource(c *gin.Context, owner *uint) {
	id, ok := parseID(c, "resourceId")
	if !ok {
		return
	}
	row, err := h.module.Service.GetResource(c.Request.Context(), uint(id), owner)
	if err != nil {
		writeProtoError(c, err)
		return
	}
	result := toResourceResponse(*row)
	if h.module.OwnerSummaries != nil {
		owners, err := h.module.OwnerSummaries(c.Request.Context(), []uint{row.OwnerUserID})
		if err != nil {
			writeProtoError(c, err)
			return
		}
		if item, ok := owners[row.OwnerUserID]; ok {
			result.Owner = &item
		}
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}
func (h *handler) importOwned(c *gin.Context) {
	if owner := currentOwner(c); owner != nil {
		h.importFile(c, *owner, *owner)
	}
}
func (h *handler) importAdmin(c *gin.Context) {
	operator := currentOwner(c)
	if operator == nil {
		return
	}
	limitImportBody(c)
	raw := c.PostForm("ownerId")
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		writeProtoError(c, domain.ErrInvalidResource)
		return
	}
	if h.module.ValidateOwner == nil {
		writeProtoError(c, domain.ErrDependency)
		return
	}
	valid, err := h.module.ValidateOwner(c.Request.Context(), uint(id))
	if err != nil || !valid {
		writeProtoError(c, domain.ErrInvalidResource)
		return
	}
	h.importFile(c, *operator, uint(id))
}
func limitImportBody(c *gin.Context) {
	maximum := min(runtimeconfig.Int("resource_import_max_bytes", maxImportBytes, 1), maxImportBytes)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, int64(maximum)+(1<<20))
}
func (h *handler) importFile(c *gin.Context, operator, owner uint) {
	if !requireProtoIdempotency(c) {
		return
	}
	limitImportBody(c)
	strategy := c.PostForm("errorStrategy")
	if strategy == "" {
		strategy = domain.ErrorStrategySkip
	}
	if strategy != domain.ErrorStrategySkip && strategy != domain.ErrorStrategyAbort {
		writeProtoError(c, domain.ErrInvalidImportFormat)
		return
	}
	longLived := false
	if value := c.PostForm("longLived"); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			writeProtoError(c, domain.ErrInvalidResource)
			return
		}
		longLived = parsed
	}
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		writeProtoError(c, domain.ErrInvalidImportFormat)
		return
	}
	defer file.Close()
	maximum := min(runtimeconfig.Int("resource_import_max_bytes", maxImportBytes, 1), maxImportBytes)
	content, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil || len(content) > maximum || len(content) == 0 {
		writeProtoError(c, domain.ErrInvalidImportFormat)
		return
	}
	if h.module.Files == nil {
		writeProtoError(c, domain.ErrDependency)
		return
	}
	name := filepath.Base(strings.TrimSpace(header.Filename))
	key := fmt.Sprintf("private/proto/imports/%d/%s.txt", owner, platform.NewUUIDV7String())
	stored, err := h.module.Files.SavePrivate(c.Request.Context(), governancedomain.PrivateFile{ObjectKey: key, FileName: name, ContentType: "text/plain; charset=utf-8", ContentBytes: content})
	if err != nil {
		writeProtoError(c, err)
		return
	}
	id, reused, err := h.module.Service.CreateImportWithOptions(c.Request.Context(), operator, owner, strategy, strings.TrimSpace(c.GetHeader("Idempotency-Key")), stored.ObjectKey, name, middleware.GetRequestID(c), content, longLived)
	if err != nil || reused {
		_ = h.module.Files.DeletePrivate(c.Request.Context(), stored.ObjectKey)
	}
	if err != nil {
		writeProtoError(c, err)
		return
	}
	if h.module.Queue != nil {
		_ = app.EnqueueImportDispatcher(c.Request.Context(), h.module.Queue)
	}
	status, err := h.module.Service.GetImport(c.Request.Context(), id)
	if err != nil {
		writeProtoError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, toImportResponse(status, reused))
}
func (h *handler) getImportOwned(c *gin.Context) {
	if owner := currentOwner(c); owner != nil {
		h.getImport(c, owner, false)
	}
}
func (h *handler) getImportAdmin(c *gin.Context) { h.getImport(c, nil, false) }
func (h *handler) getImportItemsOwned(c *gin.Context) {
	if owner := currentOwner(c); owner != nil {
		h.getImport(c, owner, true)
	}
}
func (h *handler) getImportItemsAdmin(c *gin.Context) { h.getImport(c, nil, true) }
func (h *handler) getImport(c *gin.Context, owner *uint, items bool) {
	id, ok := parseID(c, "importId")
	if !ok {
		return
	}
	var status *infra.ImportStatus
	var err error
	if owner != nil {
		status, err = h.module.Service.GetImportForUser(c.Request.Context(), id, *owner)
	} else {
		status, err = h.module.Service.GetImport(c.Request.Context(), id)
	}
	if err != nil {
		writeProtoError(c, err)
		return
	}
	if !items {
		c.JSON(http.StatusOK, toImportResponse(status, false))
		return
	}
	f, ok := parseFilter(c)
	if !ok {
		return
	}
	page, err := h.module.Service.ListImportItems(c.Request.Context(), id, f.Offset, f.Limit)
	if err != nil {
		writeProtoError(c, err)
		return
	}
	out := make([]protoImportItemResponse, len(page.Items))
	for i, item := range page.Items {
		out[i] = protoImportItemResponse{item.LineNumber, item.ResourceID, item.Outcome, item.Category, item.LastSafeError}
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "total": page.Total, "offset": f.Offset, "limit": f.Limit})
}
func (h *handler) listMaintenanceAdmin(c *gin.Context) {
	id, ok := parseID(c, "resourceId")
	if !ok {
		return
	}
	if _, err := h.module.Service.GetResource(c.Request.Context(), uint(id), nil); err != nil {
		writeProtoError(c, err)
		return
	}
	f, ok := parseFilter(c)
	if !ok {
		return
	}
	page, err := h.module.Service.ListMaintenanceRuns(c.Request.Context(), uint(id), f.Offset, min(f.Limit, 100))
	if err != nil {
		writeProtoError(c, err)
		return
	}
	items := make([]protoMaintenanceRunResponse, len(page.Items))
	for i, row := range page.Items {
		items[i] = toMaintenanceResponse(row)
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": page.Total, "offset": f.Offset, "limit": min(f.Limit, 100)})
}

func (h *handler) execute(c *gin.Context, owner *uint, action string) {
	if !requireProtoIdempotency(c) {
		return
	}
	id, ok := parseID(c, "resourceId")
	if !ok {
		return
	}
	var request protoCommandRequest
	if err := bindProtoJSON(c, &request); err != nil || request.Version == 0 {
		writeProtoError(c, domain.ErrInvalidResource)
		return
	}
	if action != "edit" && action != "credentials" && (request.Email != nil || request.OwnerID != nil || request.Password != nil || request.QualityScore != nil || request.LongLived != nil) {
		writeProtoError(c, domain.ErrInvalidResource)
		return
	}
	operator := currentOwner(c)
	if operator == nil {
		return
	}
	if action == "edit" && request.Password != nil {
		permission(h.checker, "operate")(c)
		if c.IsAborted() {
			return
		}
	}
	result, err := h.module.Service.ExecuteCommand(c.Request.Context(), infra.Command{ResourceID: uint(id), Version: request.Version, Action: action, Email: request.Email, OwnerID: request.OwnerID, Password: request.Password, QualityScore: request.QualityScore, LongLived: request.LongLived, OperatorUserID: *operator, ScopeOwnerID: owner, IdempotencyKey: c.GetHeader("Idempotency-Key"), RequestID: middleware.GetRequestID(c), Path: c.FullPath()})
	if err != nil {
		writeProtoError(c, err)
		return
	}
	if h.module.Queue != nil {
		if result.Status == domain.StatusPending {
			_ = app.EnqueueValidationDispatcher(c.Request.Context(), h.module.Queue)
		}
		if result.Status == domain.StatusIdentifying {
			_ = app.EnqueueHistoryDispatcher(c.Request.Context(), h.module.Queue)
		}
	}
	status := http.StatusOK
	if action == "validate" || action == "history" {
		status = http.StatusAccepted
	}
	c.JSON(status, result)
}
func (h *handler) validateOwned(c *gin.Context) {
	if owner := currentOwner(c); owner != nil {
		h.execute(c, owner, "validate")
	}
}
func (h *handler) validateAdmin(c *gin.Context) { h.execute(c, nil, "validate") }
func (h *handler) publishOwned(c *gin.Context) {
	if !requireProtoSupplier(c) {
		return
	}
	if owner := currentOwner(c); owner != nil {
		h.execute(c, owner, "publish")
	}
}
func (h *handler) publishAdmin(c *gin.Context)   { h.execute(c, nil, "publish") }
func (h *handler) unpublishAdmin(c *gin.Context) { h.execute(c, nil, "unpublish") }
func (h *handler) deleteOwned(c *gin.Context) {
	if owner := currentOwner(c); owner != nil {
		h.execute(c, owner, "delete")
	}
}
func (h *handler) deleteAdmin(c *gin.Context)             { h.execute(c, nil, "delete") }
func (h *handler) recoverAdmin(c *gin.Context)            { h.execute(c, nil, "recover") }
func (h *handler) enableAdmin(c *gin.Context)             { h.execute(c, nil, "enable") }
func (h *handler) disableAdmin(c *gin.Context)            { h.execute(c, nil, "disable") }
func (h *handler) patchAdmin(c *gin.Context)              { h.execute(c, nil, "edit") }
func (h *handler) replaceAdminCredentials(c *gin.Context) { h.execute(c, nil, "credentials") }
func (h *handler) historyAdmin(c *gin.Context)            { h.execute(c, nil, "history") }
func (h *handler) validateOwnedBatch(c *gin.Context) {
	if owner := currentOwner(c); owner != nil {
		h.submitBulk(c, owner, "validate")
	}
}
func (h *handler) validateAdminBatch(c *gin.Context) { h.submitBulk(c, nil, "validate") }
func (h *handler) batchCommand(action string) gin.HandlerFunc {
	return func(c *gin.Context) { h.submitBulk(c, nil, action) }
}
func (h *handler) bulkOwned(c *gin.Context) {
	if c.Param("command") == "publish" && !requireProtoSupplier(c) {
		return
	}
	if owner := currentOwner(c); owner != nil {
		h.submitBulk(c, owner, c.Param("command"))
	}
}
func (h *handler) bulkAdmin(c *gin.Context) { h.submitBulk(c, nil, c.Param("command")) }
func (h *handler) submitBulk(c *gin.Context, owner *uint, action string) {
	if !requireProtoIdempotency(c) {
		return
	}
	var request protoBatchRequest
	if err := bindProtoJSON(c, &request); err != nil {
		writeProtoError(c, domain.ErrInvalidResource)
		return
	}
	selection := infra.BulkSelection{Mode: "ids", ResourceIDs: request.ResourceIDs}
	if request.Selection != nil {
		if len(request.ResourceIDs) > 0 {
			writeProtoError(c, domain.ErrInvalidResource)
			return
		}
		selection = *request.Selection
	}
	if owner == nil && selection.Mode == "filter" && !h.resolveAdminSearch(c, &selection.Filter) {
		return
	}
	operator := currentOwner(c)
	if operator == nil {
		return
	}
	status, err := h.module.Service.SubmitBulk(c.Request.Context(), action, selection, *operator, owner, c.GetHeader("Idempotency-Key"), middleware.GetRequestID(c))
	if err != nil {
		writeProtoError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, toBulkResponse(status))
}
func (h *handler) getBulkOwned(c *gin.Context) { h.getBulk(c, false) }
func (h *handler) getBulkAdmin(c *gin.Context) { h.getBulk(c, true) }
func (h *handler) getBulk(c *gin.Context, admin bool) {
	operator := currentOwner(c)
	if operator == nil {
		return
	}
	status, err := h.module.Service.GetBulkStatus(c.Request.Context(), c.Param("taskId"), *operator, admin)
	if err != nil {
		writeProtoError(c, err)
		return
	}
	c.JSON(http.StatusOK, toBulkResponse(status))
}

func toResourceResponse(row infra.Resource) protoResourceResponse {
	return protoResourceResponse{ID: row.ID, Version: row.Version, OwnerUserID: row.OwnerUserID, Email: row.EmailAddress, Suffix: row.EmailDomain, Status: row.Status, ForSale: row.ForSale, LongLived: row.LongLived, QualityScore: row.QualityScore, PasswordConfigured: row.PasswordConfigured, CredentialRevision: row.CredentialRevision, CredentialUpdatedAt: row.CredentialUpdatedAt, ValidationGeneration: row.ValidationGeneration, ValidationFailures: row.ValidationFailures, LastSafeError: row.LastSafeError, LastCheckedAt: row.LastCheckedAt, LastAllocatedAt: row.LastAllocatedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}
func toImportResponse(row *infra.ImportStatus, reused bool) protoImportResponse {
	return protoImportResponse{ImportID: row.ID, Status: row.Status, Accepted: row.AcceptedCount, Imported: row.ImportedCount, Skipped: row.SkippedCount, Failed: row.FailedCount, DispatchStatus: row.DispatchStatus, DispatchAttempts: row.DispatchAttempts, FailureAvailable: row.FailureObjectKey != "", LastSafeError: row.LastSafeError, Reused: reused, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}
func toMaintenanceResponse(row infra.MaintenanceRun) protoMaintenanceRunResponse {
	return protoMaintenanceRunResponse{row.ID, row.ResourceID, row.ValidationGeneration, row.Kind, row.Status, row.Attempts, row.MaxAttempts, row.CredentialRevision, row.RequestID, row.LastSafeError, row.QueuedAt, row.StartedAt, row.FinishedAt, row.CreatedAt, row.UpdatedAt}
}
func parseID(c *gin.Context, name string) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil || id == 0 {
		writeProtoError(c, domain.ErrInvalidResource)
		return 0, false
	}
	return id, true
}
func requireProtoIdempotency(c *gin.Context) bool {
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if key == "" || len(key) > 128 {
		writeProtoError(c, domain.ErrInvalidResource)
		return false
	}
	return true
}
func bindProtoJSON(c *gin.Context, target any) error {
	if c.Request == nil || c.Request.Body == nil {
		return domain.ErrInvalidResource
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return domain.ErrInvalidResource
	}
	return nil
}
func requireProtoSupplier(c *gin.Context) bool {
	role, ok := middleware.GetCurrentRole(c)
	if !ok {
		c.Status(http.StatusUnauthorized)
		return false
	}
	if !role.HasSupplierAccess() {
		c.JSON(http.StatusForbidden, gin.H{"code": "forbidden", "message": "Permission denied."})
		return false
	}
	return true
}
func writeProtoError(c *gin.Context, err error) {
	status, code, message := http.StatusInternalServerError, "internal_error", "An unexpected error occurred."
	switch {
	case errors.Is(err, domain.ErrVersionConflict):
		status, code, message = http.StatusConflict, "resource_version_conflict", "Resource changed; refresh and retry."
	case errors.Is(err, domain.ErrResourceBusy):
		status, code, message = http.StatusConflict, "proto_resource_busy", "Proto resource has an active allocation."
	case errors.Is(err, domain.ErrResourceConflict):
		status, code, message = http.StatusConflict, "proto_resource_conflict", "Proto resource conflicts with an existing resource."
	case errors.Is(err, domain.ErrResourceMissing):
		status, code, message = http.StatusNotFound, "proto_resource_not_found", "Proto resource not found."
	case errors.Is(err, domain.ErrImportConflict):
		status, code, message = http.StatusConflict, "idempotency_conflict", "Idempotency key conflicts with an earlier request."
	case errors.Is(err, domain.ErrInvalidImportFormat):
		status, code, message = http.StatusBadRequest, "invalid_import_format", "Invalid Proto import format."
	case errors.Is(err, domain.ErrResourceNotPrivate):
		status, code, message = http.StatusConflict, "resource_not_private", "Only private resources can be deleted."
	case errors.Is(err, domain.ErrInvalidResource):
		status, code, message = http.StatusBadRequest, "invalid_request", "Invalid Proto resource request."
	case errors.Is(err, domain.ErrDependency):
		status, code, message = http.StatusServiceUnavailable, "temporarily_unavailable", "Proto service is temporarily unavailable."
	}
	c.JSON(status, gin.H{"code": code, "message": message, "requestId": middleware.GetRequestID(c)})
}

func (h *handler) getImportFailuresOwned(c *gin.Context) {
	if owner := currentOwner(c); owner != nil {
		h.getImportFailures(c, owner)
	}
}
func (h *handler) getImportFailuresAdmin(c *gin.Context) { h.getImportFailures(c, nil) }
func (h *handler) getImportFailures(c *gin.Context, owner *uint) {
	id, ok := parseID(c, "importId")
	if !ok {
		return
	}
	var row *infra.ImportStatus
	var err error
	if owner != nil {
		row, err = h.module.Service.GetImportForUser(c.Request.Context(), id, *owner)
	} else {
		row, err = h.module.Service.GetImport(c.Request.Context(), id)
	}
	if err != nil {
		writeProtoError(c, err)
		return
	}
	if row.FailureObjectKey == "" {
		writeProtoError(c, domain.ErrResourceMissing)
		return
	}
	if h.module.Files == nil {
		writeProtoError(c, domain.ErrDependency)
		return
	}
	file, err := h.module.Files.ReadPrivate(c.Request.Context(), row.FailureObjectKey)
	if err != nil {
		writeProtoError(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="proto-import-failures.csv"`)
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/csv; charset=utf-8", file.ContentBytes)
}
