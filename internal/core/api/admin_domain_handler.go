package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	"github.com/gin-gonic/gin"
)

type adminDomainItemResponse struct {
	ID              uint       `json:"id"`
	Version         uint64     `json:"version"`
	OwnerID         uint       `json:"ownerId"`
	OwnerEmail      string     `json:"ownerEmail"`
	OwnerNickname   string     `json:"ownerNickname"`
	OwnerRole       string     `json:"ownerRole"`
	Domain          string     `json:"domain"`
	DomainTLD       string     `json:"domainTld"`
	MailServerID    uint       `json:"mailServerId"`
	Purpose         string     `json:"purpose"`
	Status          string     `json:"status"`
	MailboxCount    int64      `json:"mailboxCount"`
	LastSafeError   *string    `json:"lastSafeError,omitempty"`
	LastAllocatedAt *time.Time `json:"lastAllocatedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type adminDomainStatusFacetsResponse struct {
	All        int64 `json:"all"`
	Pending    int64 `json:"pending"`
	Validating int64 `json:"validating"`
	Normal     int64 `json:"normal"`
	Abnormal   int64 `json:"abnormal"`
	Disabled   int64 `json:"disabled"`
	Deleted    int64 `json:"deleted"`
}

type adminDomainPurposeFacetsResponse struct {
	All     int64 `json:"all"`
	NotSale int64 `json:"not_sale"`
	Sale    int64 `json:"sale"`
	Binding int64 `json:"binding"`
}

type adminDomainFacetItemResponse struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

type adminDomainFacetsResponse struct {
	Status  adminDomainStatusFacetsResponse  `json:"status"`
	Purpose adminDomainPurposeFacetsResponse `json:"purpose"`
	TLDs    []adminDomainFacetItemResponse   `json:"tlds"`
}

type adminDomainListResponse struct {
	Items       []adminDomainItemResponse `json:"items"`
	Total       int64                     `json:"total"`
	Offset      int                       `json:"offset"`
	Limit       int                       `json:"limit"`
	NextAfterID *uint                     `json:"nextAfterId,omitempty"`
	Facets      adminDomainFacetsResponse `json:"facets"`
}

type createAdminDomainRequest struct {
	Domain       string `json:"domain" binding:"required"`
	OwnerID      uint   `json:"ownerId" binding:"required,gt=0"`
	Purpose      string `json:"purpose"`
	MailServerID uint   `json:"mailServerId"`
}

type patchAdminDomainRequest struct {
	OwnerID       *uint   `json:"ownerId"`
	Purpose       *string `json:"purpose"`
	MailServerID  *uint   `json:"mailServerId"`
	StatusCommand string  `json:"statusCommand"`
}

type adminDomainDNSStatusRequest struct {
	Normal *bool `json:"normal" binding:"required"`
}

type adminDomainBulkFilterRequest struct {
	Search       string     `json:"search"`
	Status       string     `json:"status"`
	Purpose      string     `json:"purpose"`
	TLD          string     `json:"tld"`
	OwnerID      uint       `json:"ownerId"`
	MailServerID uint       `json:"mailServerId"`
	CreatedFrom  *time.Time `json:"createdFrom"`
	CreatedTo    *time.Time `json:"createdTo"`
}

type adminDomainBulkSelectionRequest struct {
	Mode        string                        `json:"mode" binding:"required"`
	ResourceIDs []uint                        `json:"resourceIds" binding:"omitempty,max=1000,dive,gt=0"`
	Filter      *adminDomainBulkFilterRequest `json:"filter"`
}

type adminDomainBulkCommandRequest struct {
	Selection adminDomainBulkSelectionRequest `json:"selection" binding:"required"`
}

type adminDomainBulkResponse struct {
	Requested int `json:"requested"`
	Affected  int `json:"affected"`
	Skipped   int `json:"skipped"`
}

type adminDomainValidationResponse struct {
	Queued int `json:"queued"`
}

type adminMailServerItemResponse struct {
	ID            uint      `json:"id"`
	OwnerID       uint      `json:"ownerId"`
	Name          string    `json:"name"`
	ServerAddress string    `json:"serverAddress"`
	MXRecord      string    `json:"mxRecord"`
	SPFRecord     string    `json:"spfRecord"`
	DKIMRecord    string    `json:"dkimRecord"`
	DMARCRecord   string    `json:"dmarcRecord"`
	PTRRecord     string    `json:"ptrRecord"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type adminMailServerListResponse struct {
	Items  []adminMailServerItemResponse `json:"items"`
	Total  int64                         `json:"total"`
	Offset int                           `json:"offset"`
	Limit  int                           `json:"limit"`
}

// GET /v1/admin/domains
func (h *CoreHandler) GetAdminDomains(c *gin.Context) {
	if h.module == nil || h.module.AdminDomainQuery == nil {
		writeAdminResourceError(c, domain.ErrResourceDependency)
		return
	}
	if !validateAdminLimitQuery(c, coreapp.AdminResourceMaxLimit) {
		return
	}
	offset, limit, ok := middleware.ParsePagination(c, middleware.PaginationOptions{DefaultLimit: coreapp.AdminResourceDefaultLimit, MaxLimit: coreapp.AdminResourceMaxLimit})
	if !ok {
		return
	}
	afterID, ok := parseOptionalUintQuery(c, "afterId")
	if !ok {
		return
	}
	filter, ok := adminDomainFilterFromQuery(c)
	if !ok {
		return
	}
	result, err := h.module.AdminDomainQuery.List(c.Request.Context(), filter, offset, limit, afterID)
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAdminDomainListResponse(result))
}

// GET /v1/admin/domains/:domainId
func (h *CoreHandler) GetAdminDomain(c *gin.Context) {
	if h.module == nil || h.module.AdminDomainQuery == nil {
		writeAdminResourceError(c, domain.ErrResourceDependency)
		return
	}
	domainID, ok := parseUintParam(c, "domainId", "Invalid domain ID.")
	if !ok {
		return
	}
	detail, err := h.module.AdminDomainQuery.Get(c.Request.Context(), domainID)
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	c.JSON(http.StatusOK, adminDomainFromApp(detail))
}

// POST /v1/admin/domains
func (h *CoreHandler) PostAdminDomain(c *gin.Context) {
	if h.module == nil || h.module.AdminDomainCommands == nil || h.module.AdminDomainQuery == nil {
		writeAdminResourceError(c, domain.ErrResourceDependency)
		return
	}
	if !requireAdminIdempotencyKey(c) {
		return
	}
	var req createAdminDomainRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAdminInvalidBody(c, err)
		return
	}
	result, err := h.module.AdminDomainCommands.Create(c.Request.Context(), coreapp.AdminDomainCreateCommand{
		Domain: req.Domain, OwnerUserID: req.OwnerID, Purpose: domain.ResourcePurpose(strings.TrimSpace(req.Purpose)), MailServerID: req.MailServerID,
		OperatorUserID: mustCurrentAdminUserID(c), IdempotencyKey: c.GetHeader("Idempotency-Key"), RequestID: middleware.GetRequestID(c), Path: c.FullPath(),
	})
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	detail, err := h.module.AdminDomainQuery.Get(c.Request.Context(), result.ResourceID)
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, adminDomainFromApp(detail))
}

// PATCH /v1/admin/domains/:domainId
func (h *CoreHandler) PatchAdminDomain(c *gin.Context) {
	if h.module == nil || h.module.AdminDomainCommands == nil {
		writeAdminResourceError(c, domain.ErrResourceDependency)
		return
	}
	if !requireAdminIdempotencyKey(c) {
		return
	}
	domainID, version, ok := parseAdminDomainVersionCommand(c)
	if !ok {
		return
	}
	var req patchAdminDomainRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAdminInvalidBody(c, err)
		return
	}
	var purpose *domain.ResourcePurpose
	if req.Purpose != nil {
		value := domain.ResourcePurpose(strings.TrimSpace(*req.Purpose))
		purpose = &value
	}
	_, err := h.module.AdminDomainCommands.Edit(c.Request.Context(), coreapp.AdminDomainEditCommand{
		ResourceID: domainID, Version: version, OwnerUserID: req.OwnerID, Purpose: purpose, MailServerID: req.MailServerID,
		StatusCommand: coreapp.AdminDomainStatusCommand(strings.TrimSpace(req.StatusCommand)), OperatorUserID: mustCurrentAdminUserID(c),
		IdempotencyKey: c.GetHeader("Idempotency-Key"), RequestID: middleware.GetRequestID(c), Path: c.FullPath(),
	})
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	h.writeAdminDomainMutation(c, domainID, http.StatusOK)
}

// POST /v1/admin/domains/:domainId/validate
func (h *CoreHandler) PostAdminDomainValidate(c *gin.Context) {
	if h.module == nil || h.module.AdminDomainCommands == nil {
		writeAdminResourceError(c, domain.ErrResourceDependency)
		return
	}
	if !requireAdminIdempotencyKey(c) {
		return
	}
	domainID, ok := parseUintParam(c, "domainId", "Invalid domain ID.")
	if !ok {
		return
	}
	if _, err := h.module.AdminDomainCommands.Validate(
		c.Request.Context(), domainID, mustCurrentAdminUserID(c), c.GetHeader("Idempotency-Key"), middleware.GetRequestID(c), c.FullPath(),
	); err != nil {
		writeAdminResourceError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, adminDomainValidationResponse{Queued: 1})
}

// GET /v1/admin/domains/:domainId/mailboxes
func (h *CoreHandler) GetAdminDomainMailboxes(c *gin.Context) {
	if h.module == nil || h.module.MailboxUseCase == nil {
		writeAdminResourceError(c, domain.ErrResourceDependency)
		return
	}
	h.GetDomainMailboxes(c)
}

// POST /v1/admin/domains/:domainId/dns-status
func (h *CoreHandler) PostAdminDomainDNSStatus(c *gin.Context) {
	if h.module == nil || h.module.AdminDomainCommands == nil {
		writeAdminResourceError(c, domain.ErrResourceDependency)
		return
	}
	if !requireAdminIdempotencyKey(c) {
		return
	}
	domainID, version, ok := parseAdminDomainVersionCommand(c)
	if !ok {
		return
	}
	var req adminDomainDNSStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAdminInvalidBody(c, err)
		return
	}
	statusCommand := coreapp.AdminDomainMarkAbnormal
	if *req.Normal {
		statusCommand = coreapp.AdminDomainMarkNormal
	}
	h.applyAdminDomainMutation(c, coreapp.AdminDomainEditCommand{ResourceID: domainID, Version: version, StatusCommand: statusCommand}, false)
}

func (h *CoreHandler) PostAdminDomainEnable(c *gin.Context) {
	h.applyAdminDomainStatus(c, coreapp.AdminDomainEnable, true)
}

func (h *CoreHandler) PostAdminDomainDisable(c *gin.Context) {
	h.applyAdminDomainStatus(c, coreapp.AdminDomainDisable, false)
}

func (h *CoreHandler) PostAdminDomainPublish(c *gin.Context) {
	h.applyAdminDomainAction(c, coreapp.AdminDomainPublish, false)
}

func (h *CoreHandler) PostAdminDomainUnpublish(c *gin.Context) {
	h.applyAdminDomainAction(c, coreapp.AdminDomainUnpublish, false)
}

func (h *CoreHandler) DeleteAdminDomain(c *gin.Context) {
	h.applyAdminDomainAction(c, coreapp.AdminDomainDelete, false)
}

func (h *CoreHandler) PostAdminDomainRecover(c *gin.Context) {
	h.applyAdminDomainAction(c, coreapp.AdminDomainRecover, true)
}

func (h *CoreHandler) PostAdminDomainValidations(c *gin.Context) {
	if h.module == nil || h.module.AdminDomainCommands == nil || !requireAdminIdempotencyKey(c) {
		return
	}
	var req adminDomainBulkCommandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAdminInvalidBody(c, err)
		return
	}
	selection, ok := toAdminDomainBulkSelection(c, req.Selection)
	if !ok {
		return
	}
	result, err := h.module.AdminDomainCommands.SubmitValidationBatch(
		c.Request.Context(), selection, mustCurrentAdminUserID(c), c.GetHeader("Idempotency-Key"), middleware.GetRequestID(c), c.FullPath(),
	)
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, adminDomainValidationResponse{Queued: result.Queued})
}

func (h *CoreHandler) PostAdminDomainsDisable(c *gin.Context) {
	h.applyAdminDomainBulk(c, "disable")
}

func (h *CoreHandler) PostAdminDomainsPublish(c *gin.Context) {
	h.applyAdminDomainBulk(c, "publish")
}

func (h *CoreHandler) PostAdminDomainsUnpublish(c *gin.Context) {
	h.applyAdminDomainBulk(c, "unpublish")
}

func (h *CoreHandler) PostAdminDomainsDelete(c *gin.Context) {
	h.applyAdminDomainBulk(c, "delete")
}

func (h *CoreHandler) applyAdminDomainStatus(c *gin.Context, command coreapp.AdminDomainStatusCommand, returnDomain bool) {
	if h.module == nil || h.module.AdminDomainCommands == nil {
		writeAdminResourceError(c, domain.ErrResourceDependency)
		return
	}
	if !requireAdminIdempotencyKey(c) {
		return
	}
	domainID, version, ok := parseAdminDomainVersionCommand(c)
	if !ok {
		return
	}
	h.applyAdminDomainMutation(c, coreapp.AdminDomainEditCommand{ResourceID: domainID, Version: version, StatusCommand: command}, returnDomain)
}

func (h *CoreHandler) applyAdminDomainAction(c *gin.Context, action coreapp.AdminDomainAction, returnDomain bool) {
	if h.module == nil || h.module.AdminDomainCommands == nil {
		writeAdminResourceError(c, domain.ErrResourceDependency)
		return
	}
	if !requireAdminIdempotencyKey(c) {
		return
	}
	domainID, version, ok := parseAdminDomainVersionCommand(c)
	if !ok {
		return
	}
	h.applyAdminDomainMutation(c, coreapp.AdminDomainEditCommand{ResourceID: domainID, Version: version, Action: action}, returnDomain)
}

func (h *CoreHandler) applyAdminDomainMutation(c *gin.Context, command coreapp.AdminDomainEditCommand, returnDomain bool) {
	command.OperatorUserID = mustCurrentAdminUserID(c)
	command.IdempotencyKey = c.GetHeader("Idempotency-Key")
	command.RequestID = middleware.GetRequestID(c)
	command.Path = c.FullPath()
	if _, err := h.module.AdminDomainCommands.ApplyAction(c.Request.Context(), command); err != nil {
		writeAdminResourceError(c, err)
		return
	}
	if returnDomain {
		h.writeAdminDomainMutation(c, command.ResourceID, http.StatusOK)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *CoreHandler) applyAdminDomainBulk(c *gin.Context, action string) {
	if h.module == nil || h.module.AdminDomainCommands == nil {
		writeAdminResourceError(c, domain.ErrResourceDependency)
		return
	}
	if !requireAdminIdempotencyKey(c) {
		return
	}
	var req adminDomainBulkCommandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAdminInvalidBody(c, err)
		return
	}
	selection, ok := toAdminDomainBulkSelection(c, req.Selection)
	if !ok {
		return
	}
	// Publish/unpublish/delete may span a large domain table, so they run through
	// the durable Redis-leased batch worker page by page. Disable stays a bounded
	// synchronous command.
	if action != "disable" {
		result, err := h.module.AdminDomainCommands.SubmitBulkState(
			c.Request.Context(), action, selection, mustCurrentAdminUserID(c), c.GetHeader("Idempotency-Key"), middleware.GetRequestID(c), c.FullPath(),
		)
		if err != nil {
			writeAdminResourceError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, adminDomainValidationResponse{Queued: result.Requested})
		return
	}
	result, err := h.module.AdminDomainCommands.ApplyBulk(
		c.Request.Context(), action, selection, mustCurrentAdminUserID(c), c.GetHeader("Idempotency-Key"), middleware.GetRequestID(c), c.FullPath(),
	)
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	c.JSON(http.StatusOK, adminDomainBulkResponse{Requested: result.Requested, Affected: result.Affected, Skipped: result.Skipped})
}

func (h *CoreHandler) writeAdminDomainMutation(c *gin.Context, resourceID uint, status int) {
	detail, err := h.module.AdminDomainQuery.Get(c.Request.Context(), resourceID)
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	c.JSON(status, adminDomainFromApp(detail))
}

// POST /v1/admin/domain-mailboxes/:mailboxId/disable
func (h *CoreHandler) PostAdminDomainMailboxDisable(c *gin.Context) {
	if h.module == nil || h.module.MailboxUseCase == nil {
		writeAdminResourceError(c, domain.ErrResourceDependency)
		return
	}
	if !requireAdminIdempotencyKey(c) {
		return
	}
	mailboxID, ok := parseUintParam(c, "mailboxId", "Invalid mailbox ID.")
	if !ok {
		return
	}
	if err := h.module.MailboxUseCase.DisableAdmin(
		c.Request.Context(), mailboxID, mustCurrentAdminUserID(c), middleware.GetRequestID(c), c.FullPath(),
	); err != nil {
		writeAdminResourceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// GET /v1/admin/servers
func (h *CoreHandler) GetAdminServers(c *gin.Context) {
	if h.module == nil || h.module.ServerUseCase == nil {
		writeAdminResourceError(c, domain.ErrResourceDependency)
		return
	}
	offset, limit, ok := middleware.ParsePagination(c, middleware.PaginationOptions{DefaultLimit: 20, MaxLimit: 100})
	if !ok {
		return
	}
	result, err := h.module.ServerUseCase.List(c.Request.Context(), mustCurrentAdminUserID(c), "all", offset, limit)
	if err != nil {
		writeCoreError(c, err)
		return
	}
	items := make([]adminMailServerItemResponse, len(result.Items))
	for i := range result.Items {
		items[i] = adminMailServerFromDomain(result.Items[i])
	}
	c.JSON(http.StatusOK, adminMailServerListResponse{Items: items, Total: result.Total, Offset: result.Offset, Limit: result.Limit})
}

// POST /v1/admin/servers
func (h *CoreHandler) PostAdminServer(c *gin.Context) {
	if h.module == nil || h.module.ServerUseCase == nil {
		writeAdminResourceError(c, domain.ErrResourceDependency)
		return
	}
	var req CreateMailServerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAdminInvalidBody(c, err)
		return
	}
	server, err := h.module.ServerUseCase.Create(c.Request.Context(), mustCurrentAdminUserID(c), &coreapp.CreateServerRequest{
		Name: req.Name, ServerAddress: req.ServerAddress, MXRecord: req.MXRecord, SPFRecord: req.SPFRecord,
		DKIMRecord: req.DKIMRecord, DMARCRecord: req.DMARCRecord, PTRRecord: req.PTRRecord,
	})
	if err != nil {
		writeCoreError(c, err)
		return
	}
	c.JSON(http.StatusCreated, adminMailServerFromDomain(*server))
}

func adminDomainFilterFromQuery(c *gin.Context) (coreapp.AdminDomainListFilter, bool) {
	ownerID, ok := parseOptionalUintQuery(c, "ownerId")
	if !ok {
		return coreapp.AdminDomainListFilter{}, false
	}
	mailServerID, ok := parseOptionalUintQuery(c, "mailServerId")
	if !ok {
		return coreapp.AdminDomainListFilter{}, false
	}
	createdFrom, ok := parseOptionalCoreTimeQuery(c, "createdFrom")
	if !ok {
		return coreapp.AdminDomainListFilter{}, false
	}
	createdTo, ok := parseOptionalCoreTimeQuery(c, "createdTo")
	if !ok {
		return coreapp.AdminDomainListFilter{}, false
	}
	status := strings.TrimSpace(c.Query("status"))
	if status == "all" {
		status = ""
	}
	purpose := strings.TrimSpace(c.Query("purpose"))
	if purpose == "all" {
		purpose = ""
	}
	return coreapp.AdminDomainListFilter{
		Search: strings.TrimSpace(c.Query("search")), Status: domain.MailDomainStatus(status), Purpose: domain.ResourcePurpose(purpose),
		TLD: strings.TrimSpace(c.Query("tld")), OwnerID: ownerID, MailServerID: mailServerID, CreatedFrom: createdFrom, CreatedTo: createdTo,
	}, true
}

func toAdminDomainBulkSelection(c *gin.Context, req adminDomainBulkSelectionRequest) (coreapp.AdminDomainBulkSelection, bool) {
	switch req.Mode {
	case string(coreapp.AdminDomainBulkIDs):
		if len(req.ResourceIDs) == 0 || req.Filter != nil {
			writeAdminResourceError(c, domain.ErrInvalidResourceCommand)
			return coreapp.AdminDomainBulkSelection{}, false
		}
		return coreapp.AdminDomainBulkSelection{Mode: coreapp.AdminDomainBulkIDs, ResourceIDs: req.ResourceIDs}, true
	case string(coreapp.AdminDomainBulkFilter):
		if len(req.ResourceIDs) != 0 || req.Filter == nil {
			writeAdminResourceError(c, domain.ErrInvalidResourceCommand)
			return coreapp.AdminDomainBulkSelection{}, false
		}
		status := strings.TrimSpace(req.Filter.Status)
		if status == "all" {
			status = ""
		}
		purpose := strings.TrimSpace(req.Filter.Purpose)
		if purpose == "all" {
			purpose = ""
		}
		return coreapp.AdminDomainBulkSelection{Mode: coreapp.AdminDomainBulkFilter, Filter: coreapp.AdminDomainListFilter{
			Search: strings.TrimSpace(req.Filter.Search), Status: domain.MailDomainStatus(status), Purpose: domain.ResourcePurpose(purpose),
			TLD: strings.TrimSpace(req.Filter.TLD), OwnerID: req.Filter.OwnerID, MailServerID: req.Filter.MailServerID,
			CreatedFrom: req.Filter.CreatedFrom, CreatedTo: req.Filter.CreatedTo,
		}}, true
	default:
		writeAdminResourceError(c, domain.ErrInvalidResourceCommand)
		return coreapp.AdminDomainBulkSelection{}, false
	}
}

func parseAdminDomainVersionCommand(c *gin.Context) (uint, uint64, bool) {
	resourceID, ok := parseUintParam(c, "domainId", "Invalid domain ID.")
	if !ok {
		return 0, 0, false
	}
	value, err := strconv.ParseUint(strings.TrimSpace(c.Query("version")), 10, 64)
	if err != nil || value == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid version.", "requestId": middleware.GetRequestID(c)})
		return 0, 0, false
	}
	return resourceID, value, true
}

func toAdminDomainListResponse(result *coreapp.AdminDomainListResult) adminDomainListResponse {
	items := make([]adminDomainItemResponse, len(result.Items))
	for i := range result.Items {
		items[i] = adminDomainFromApp(&result.Items[i])
	}
	tlds := make([]adminDomainFacetItemResponse, len(result.Facets.TLDs))
	for i := range result.Facets.TLDs {
		tlds[i] = adminDomainFacetItemResponse{Key: result.Facets.TLDs[i].Key, Count: result.Facets.TLDs[i].Count}
	}
	return adminDomainListResponse{
		Items: items, Total: result.Total, Offset: result.Offset, Limit: result.Limit, NextAfterID: result.NextAfterID,
		Facets: adminDomainFacetsResponse{
			Status: adminDomainStatusFacetsResponse{
				All: result.Facets.Status.All, Pending: result.Facets.Status.Pending, Validating: result.Facets.Status.Validating,
				Normal: result.Facets.Status.Normal, Abnormal: result.Facets.Status.Abnormal,
				Disabled: result.Facets.Status.Disabled, Deleted: result.Facets.Status.Deleted,
			},
			Purpose: adminDomainPurposeFacetsResponse{
				All: result.Facets.Purpose.All, NotSale: result.Facets.Purpose.NotSale, Sale: result.Facets.Purpose.Sale, Binding: result.Facets.Purpose.Binding,
			},
			TLDs: tlds,
		},
	}
}

func adminDomainFromApp(item *coreapp.AdminDomainItem) adminDomainItemResponse {
	return adminDomainItemResponse{
		ID: item.ID, Version: item.Version, OwnerID: item.Owner.ID, OwnerEmail: item.Owner.Email, OwnerNickname: item.Owner.Nickname, OwnerRole: item.Owner.Role,
		Domain: item.Domain, DomainTLD: item.DomainTLD, MailServerID: item.MailServerID, Purpose: item.Purpose, Status: item.Status,
		MailboxCount: item.MailboxCount, LastSafeError: item.LastSafeError, LastAllocatedAt: item.LastAllocatedAt, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func adminMailServerFromDomain(server domain.MailServer) adminMailServerItemResponse {
	return adminMailServerItemResponse{
		ID: server.ID, OwnerID: server.OwnerUserID, Name: server.Name, ServerAddress: server.ServerAddress, MXRecord: server.MXRecord,
		SPFRecord: server.SPFRecord, DKIMRecord: server.DKIMRecord, DMARCRecord: server.DMARCRecord, PTRRecord: server.PTRRecord,
		Status: string(server.Status), CreatedAt: server.CreatedAt, UpdatedAt: server.UpdatedAt,
	}
}
