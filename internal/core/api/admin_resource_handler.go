package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	coreapp "github.com/donnel666/remail/internal/core/app"
	"github.com/donnel666/remail/internal/core/domain"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

const maxAdminResourceJSONBytes int64 = 1 << 20

type adminMicrosoftOwnerResponse struct {
	ID        uint   `json:"id"`
	Email     string `json:"email"`
	Nickname  string `json:"nickname"`
	GroupName string `json:"groupName"`
	Role      string `json:"role"`
	Enabled   bool   `json:"enabled"`
}

type adminTaskSummaryResponse struct {
	TaskID             string    `json:"taskId"`
	Kind               string    `json:"kind"`
	Status             string    `json:"status"`
	CredentialRevision *uint64   `json:"credentialRevision"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

type adminTaskViewResponse struct {
	TaskID             string                     `json:"taskId"`
	Kind               string                     `json:"kind"`
	BizType            string                     `json:"bizType"`
	BizID              uint                       `json:"bizId"`
	Status             string                     `json:"status"`
	CredentialRevision *uint64                    `json:"credentialRevision"`
	Attempts           int                        `json:"attempts"`
	MaxAttempts        int                        `json:"maxAttempts"`
	RemainingAttempts  int                        `json:"remainingAttempts"`
	Progress           *adminTaskProgressResponse `json:"progress"`
	QueuedAt           time.Time                  `json:"queuedAt"`
	StartedAt          *time.Time                 `json:"startedAt"`
	FinishedAt         *time.Time                 `json:"finishedAt"`
	UpdatedAt          time.Time                  `json:"updatedAt"`
}

type adminTaskProgressResponse struct {
	Total        int64                     `json:"total"`
	Processed    int64                     `json:"processed"`
	Succeeded    int64                     `json:"succeeded"`
	Failed       int64                     `json:"failed"`
	Skipped      int64                     `json:"skipped"`
	ReasonCounts []adminTaskReasonResponse `json:"reasonCounts"`
}

type adminTaskReasonResponse struct {
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}

type adminMicrosoftResourceItemResponse struct {
	ID              uint                        `json:"id"`
	Type            string                      `json:"type"`
	Version         uint64                      `json:"version"`
	EmailAddress    string                      `json:"emailAddress"`
	Suffix          string                      `json:"suffix"`
	BindingAddress  *string                     `json:"bindingAddress"`
	Owner           adminMicrosoftOwnerResponse `json:"owner"`
	Status          string                      `json:"status"`
	ForSale         bool                        `json:"forSale"`
	LongLived       bool                        `json:"longLived"`
	GraphAvailable  bool                        `json:"graphAvailable"`
	MailProtocol    string                      `json:"mailProtocol"`
	QualityScore    int                         `json:"qualityScore"`
	TokenHealth     string                      `json:"tokenHealth"`
	RTExpireAt      *time.Time                  `json:"rtExpireAt"`
	LastAllocatedAt *time.Time                  `json:"lastAllocatedAt"`
	LastSafeError   *string                     `json:"lastSafeError"`
	ActiveTask      *adminTaskSummaryResponse   `json:"activeTask"`
	CreatedAt       time.Time                   `json:"createdAt"`
	UpdatedAt       time.Time                   `json:"updatedAt"`
}

type adminMicrosoftStatusFacetResponse struct {
	All         int64 `json:"all"`
	Pending     int64 `json:"pending"`
	Validating  int64 `json:"validating"`
	Identifying int64 `json:"identifying"`
	Normal      int64 `json:"normal"`
	Abnormal    int64 `json:"abnormal"`
	Disabled    int64 `json:"disabled"`
	Deleted     int64 `json:"deleted"`
}

type adminMicrosoftBooleanFacetResponse struct {
	All int64 `json:"all"`
	Yes int64 `json:"yes"`
	No  int64 `json:"no"`
}

type adminMicrosoftTokenFacetResponse struct {
	All      int64 `json:"all"`
	Valid    int64 `json:"valid"`
	Expiring int64 `json:"expiring"`
	Expired  int64 `json:"expired"`
	Missing  int64 `json:"missing"`
}

type adminMicrosoftSuffixFacetResponse struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

type adminMicrosoftFacetsResponse struct {
	Status         adminMicrosoftStatusFacetResponse   `json:"status"`
	ForSale        adminMicrosoftBooleanFacetResponse  `json:"forSale"`
	LongLived      adminMicrosoftBooleanFacetResponse  `json:"longLived"`
	GraphAvailable adminMicrosoftBooleanFacetResponse  `json:"graphAvailable"`
	TokenHealth    adminMicrosoftTokenFacetResponse    `json:"tokenHealth"`
	Suffixes       []adminMicrosoftSuffixFacetResponse `json:"suffixes"`
}

type adminMicrosoftListResponse struct {
	Items       []adminMicrosoftResourceItemResponse `json:"items"`
	Total       int64                                `json:"total"`
	Offset      int                                  `json:"offset"`
	Limit       int                                  `json:"limit"`
	NextAfterID *uint                                `json:"nextAfterId"`
	Facets      adminMicrosoftFacetsResponse         `json:"facets"`
}

type adminMicrosoftAliasCountsResponse struct {
	Explicit int64 `json:"explicit"`
	Dot      int64 `json:"dot"`
	Plus     int64 `json:"plus"`
}

type adminMicrosoftCredentialResponse struct {
	PasswordConfigured     bool       `json:"passwordConfigured"`
	ClientIDConfigured     bool       `json:"clientIdConfigured"`
	RefreshTokenConfigured bool       `json:"refreshTokenConfigured"`
	Revision               uint64     `json:"revision"`
	UpdatedAt              *time.Time `json:"updatedAt"`
}

type adminMicrosoftTokenResponse struct {
	Health               string     `json:"health"`
	RTExpireAt           *time.Time `json:"rtExpireAt"`
	RemainingSeconds     *int64     `json:"remainingSeconds"`
	LastRefreshedAt      *time.Time `json:"lastRefreshedAt"`
	Scopes               []string   `json:"scopes"`
	LastRefreshRequestID *string    `json:"lastRefreshRequestId"`
	LastSafeError        *string    `json:"lastSafeError"`
}

type adminMicrosoftProxyBindingResponse struct {
	ProxyID    uint      `json:"proxyId"`
	Host       string    `json:"host"`
	OutboundIP string    `json:"outboundIp"`
	Country    string    `json:"country"`
	IPVersion  string    `json:"ipVersion"`
	Status     string    `json:"status"`
	ExpireAt   time.Time `json:"expireAt"`
}

type adminMicrosoftDetailResponse struct {
	adminMicrosoftResourceItemResponse
	AliasCounts   adminMicrosoftAliasCountsResponse    `json:"aliasCounts"`
	RecentTasks   []adminTaskSummaryResponse           `json:"recentTasks"`
	Credentials   adminMicrosoftCredentialResponse     `json:"credentials"`
	Token         adminMicrosoftTokenResponse          `json:"token"`
	ProxyBindings []adminMicrosoftProxyBindingResponse `json:"proxyBindings"`
}

type adminMicrosoftCredentialsRequest struct {
	Password     string `json:"password"`
	ClientID     string `json:"clientId"`
	RefreshToken string `json:"refreshToken"`
}

type adminMicrosoftUpdateRequest struct {
	Version        uint64                            `json:"version" binding:"required,min=1"`
	EmailAddress   *string                           `json:"emailAddress"`
	BindingAddress json.RawMessage                   `json:"bindingAddress"`
	OwnerID        *uint                             `json:"ownerId"`
	QualityScore   *int                              `json:"qualityScore"`
	ForSale        *bool                             `json:"forSale"`
	LongLived      *bool                             `json:"longLived"`
	Credentials    *adminMicrosoftCredentialsRequest `json:"credentials"`
}

type adminMicrosoftReplaceCredentialsRequest struct {
	Version      uint64 `json:"version" binding:"required,min=1"`
	Password     string `json:"password" binding:"required"`
	ClientID     string `json:"clientId"`
	RefreshToken string `json:"refreshToken"`
}

type adminMicrosoftMutationResponse struct {
	Resource adminMicrosoftDetailResponse `json:"resource"`
}

type adminTaskAcceptedResponse struct {
	TaskID    string                `json:"taskId"`
	RequestID string                `json:"requestId"`
	Status    string                `json:"status"`
	Accepted  int64                 `json:"accepted"`
	Task      adminTaskViewResponse `json:"task"`
	Reused    bool                  `json:"reused"`
}

type adminMicrosoftAliasItemResponse struct {
	ID           uint64    `json:"id"`
	Kind         string    `json:"kind"`
	EmailAddress string    `json:"emailAddress"`
	CreatedAt    time.Time `json:"createdAt"`
}

type adminMicrosoftAliasScheduleResponse struct {
	WeekCreated int        `json:"weekCreated"`
	WeekLimit   int        `json:"weekLimit"`
	YearCreated int        `json:"yearCreated"`
	YearLimit   int        `json:"yearLimit"`
	NextRunAt   *time.Time `json:"nextRunAt"`
}

type adminMicrosoftAliasListResponse struct {
	Items    []adminMicrosoftAliasItemResponse    `json:"items"`
	Total    int64                                `json:"total"`
	Offset   int                                  `json:"offset"`
	Limit    int                                  `json:"limit"`
	Schedule *adminMicrosoftAliasScheduleResponse `json:"schedule"`
}

type adminMicrosoftImportResponse struct {
	ImportID      uint                  `json:"importId"`
	TaskID        string                `json:"taskId"`
	RequestID     string                `json:"requestId"`
	Status        string                `json:"status"`
	Accepted      int64                 `json:"accepted"`
	Imported      int64                 `json:"imported"`
	Skipped       int64                 `json:"skipped"`
	LastSafeError *string               `json:"lastSafeError"`
	Task          adminTaskViewResponse `json:"task"`
	Reused        bool                  `json:"reused"`
	CreatedAt     time.Time             `json:"createdAt"`
	UpdatedAt     time.Time             `json:"updatedAt"`
}

type adminMicrosoftIDsSelectionRequest struct {
	Mode        string          `json:"mode" binding:"required"`
	ResourceIDs []uint          `json:"resourceIds" binding:"required,min=1,max=1000,dive,gt=0"`
	Filter      json.RawMessage `json:"filter"`
}

type adminMicrosoftIDsCommandRequest struct {
	Selection adminMicrosoftIDsSelectionRequest `json:"selection" binding:"required"`
}

type adminMicrosoftBulkFilterRequest struct {
	Type           string     `json:"type" binding:"required"`
	Search         string     `json:"search"`
	Suffix         string     `json:"suffix"`
	Status         string     `json:"status"`
	ForSale        *bool      `json:"forSale"`
	LongLived      *bool      `json:"longLived"`
	GraphAvailable *bool      `json:"graphAvailable"`
	TokenHealth    string     `json:"tokenHealth"`
	CreatedFrom    *time.Time `json:"createdFrom"`
	CreatedTo      *time.Time `json:"createdTo"`
}

type adminMicrosoftBulkSelectionRequest struct {
	Mode        string                           `json:"mode" binding:"required"`
	ResourceIDs []uint                           `json:"resourceIds" binding:"omitempty,max=1000,dive,gt=0"`
	Filter      *adminMicrosoftBulkFilterRequest `json:"filter"`
}

type adminMicrosoftBulkCommandRequest struct {
	Selection adminMicrosoftBulkSelectionRequest `json:"selection" binding:"required"`
}

type adminMicrosoftMaintenanceRequest struct {
	Action    string                             `json:"action" binding:"required,oneof=validate alias history token"`
	Selection adminMicrosoftBulkSelectionRequest `json:"selection" binding:"required"`
}

type adminMicrosoftBulkResultResponse struct {
	Requested           int                       `json:"requested"`
	Affected            int                       `json:"affected"`
	Skipped             int                       `json:"skipped"`
	AffectedResourceIDs []uint                    `json:"affectedResourceIds,omitempty"`
	SkippedResourceIDs  []uint                    `json:"skippedResourceIds,omitempty"`
	ReasonCounts        []adminTaskReasonResponse `json:"reasonCounts"`
}

func (h *CoreHandler) GetAdminMicrosoftResources(c *gin.Context) {
	if h.module == nil || h.module.AdminResourceQuery == nil {
		writeAdminResourceError(c, domain.ErrResourceDependency)
		return
	}
	if strings.TrimSpace(c.Query("type")) != string(domain.ResourceTypeMicrosoft) {
		writeAdminResourceError(c, domain.ErrInvalidResourceFilter)
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
	filter, ok := adminMicrosoftFilterFromQuery(c)
	if !ok {
		return
	}
	result, err := h.module.AdminResourceQuery.List(c.Request.Context(), filter, offset, limit, afterID)
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAdminMicrosoftListResponse(result))
}

func (h *CoreHandler) PostAdminMicrosoftResourceImport(c *gin.Context) {
	if h.module == nil || h.module.ImportUseCase == nil || h.module.AdminCommands == nil {
		writeAdminResourceError(c, domain.ErrResourceDependency)
		return
	}
	if !validAdminIdempotencyKey(c.GetHeader("Idempotency-Key")) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid Idempotency-Key.", "requestId": middleware.GetRequestID(c)})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxImportBytes)
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid import file.", "requestId": middleware.GetRequestID(c)})
		return
	}
	defer file.Close()
	ownerID64, err := strconv.ParseUint(strings.TrimSpace(c.PostForm("ownerId")), 10, 64)
	if err != nil || ownerID64 == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid ownerId value.", "requestId": middleware.GetRequestID(c)})
		return
	}
	if _, err := h.module.AdminCommands.ValidateImportOwner(c.Request.Context(), uint(ownerID64)); err != nil {
		writeAdminResourceError(c, err)
		return
	}
	longLived, err := strconv.ParseBool(strings.TrimSpace(c.PostForm("longLived")))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid longLived value.", "requestId": middleware.GetRequestID(c)})
		return
	}
	strategy, ok := domain.NormalizeImportErrorStrategy(c.PostForm("errorStrategy"))
	if !ok {
		writeAdminResourceError(c, domain.ErrInvalidImportFormat)
		return
	}
	content, err := io.ReadAll(io.LimitReader(file, MaxImportBytes+1))
	if err != nil || len(content) > MaxImportBytes {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid import file.", "requestId": middleware.GetRequestID(c)})
		return
	}
	accepted, err := h.module.ImportUseCase.AcceptAdminMicrosoftTXTFile(
		c.Request.Context(),
		mustCurrentAdminUserID(c),
		uint(ownerID64),
		header.Filename,
		content,
		longLived,
		strategy,
		c.GetHeader("Idempotency-Key"),
		middleware.GetRequestID(c),
		c.FullPath(),
	)
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	status, err := h.module.ImportUseCase.GetAdminImportStatus(c.Request.Context(), accepted.ImportID)
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, toAdminImportResponse(status, accepted.Reused))
}

func (h *CoreHandler) GetAdminMicrosoftResourceImport(c *gin.Context) {
	importID, ok := parseUintParam(c, "importId", "Invalid import ID.")
	if !ok {
		return
	}
	status, err := h.module.ImportUseCase.GetAdminImportStatus(c.Request.Context(), importID)
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAdminImportResponse(status, false))
}

func (h *CoreHandler) GetAdminMicrosoftResource(c *gin.Context) {
	resourceID, ok := parseResourceID(c)
	if !ok {
		return
	}
	detail, err := h.module.AdminResourceQuery.Get(c.Request.Context(), resourceID)
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAdminMicrosoftDetailResponse(detail))
}

func (h *CoreHandler) GetAdminMicrosoftResourceAliases(c *gin.Context) {
	resourceID, ok := parseResourceID(c)
	if !ok {
		return
	}
	if !validateAdminLimitQuery(c, coreapp.AdminResourceMaxLimit) {
		return
	}
	offset, limit, ok := middleware.ParsePagination(c, middleware.PaginationOptions{DefaultLimit: 20, MaxLimit: coreapp.AdminResourceMaxLimit})
	if !ok {
		return
	}
	result, err := h.module.AdminResourceQuery.ListAliases(c.Request.Context(), resourceID, c.Query("kind"), offset, limit)
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	response := adminMicrosoftAliasListResponse{
		Items:  make([]adminMicrosoftAliasItemResponse, len(result.Items)),
		Total:  result.Total,
		Offset: result.Offset,
		Limit:  result.Limit,
	}
	for i := range result.Items {
		response.Items[i] = adminMicrosoftAliasItemResponse{
			ID: result.Items[i].ID, Kind: result.Items[i].Kind,
			EmailAddress: result.Items[i].EmailAddress, CreatedAt: result.Items[i].CreatedAt,
		}
	}
	if result.Schedule != nil {
		response.Schedule = &adminMicrosoftAliasScheduleResponse{
			WeekCreated: result.Schedule.WeekCreated, WeekLimit: result.Schedule.WeekLimit,
			YearCreated: result.Schedule.YearCreated, YearLimit: result.Schedule.YearLimit,
			NextRunAt: result.Schedule.NextRunAt,
		}
	}
	c.JSON(http.StatusOK, response)
}

func (h *CoreHandler) PatchAdminMicrosoftResource(c *gin.Context) {
	if !requireAdminIdempotencyKey(c) {
		return
	}
	resourceID, ok := parseResourceID(c)
	if !ok {
		return
	}
	var request adminMicrosoftUpdateRequest
	if err := bindAdminResourceJSON(c, &request); err != nil {
		writeAdminInvalidBody(c, err)
		return
	}
	if request.EmailAddress == nil && request.BindingAddress == nil && request.OwnerID == nil && request.QualityScore == nil && request.ForSale == nil && request.LongLived == nil && request.Credentials == nil {
		writeAdminResourceError(c, domain.ErrInvalidResourceCommand)
		return
	}
	if (request.ForSale != nil || request.Credentials != nil) && !h.requireAdminResourcePermission(c, "core:resource", "operate") {
		return
	}
	if request.BindingAddress != nil && !h.requireAdminResourcePermission(c, "mailtransport:binding", "write") {
		return
	}
	bindingAddress, bindingSet, ok := decodeNullableAdminEmail(c, request.BindingAddress)
	if !ok {
		return
	}
	command := coreapp.AdminMicrosoftEditCommand{
		ResourceID: resourceID, Version: request.Version, EmailAddress: request.EmailAddress,
		BindingAddressSet: bindingSet, BindingAddress: bindingAddress, OwnerUserID: request.OwnerID,
		QualityScore: request.QualityScore, ForSale: request.ForSale, LongLived: request.LongLived,
		OperatorUserID: mustCurrentAdminUserID(c), IdempotencyKey: c.GetHeader("Idempotency-Key"),
		RequestID: middleware.GetRequestID(c), Path: c.FullPath(),
	}
	if request.Credentials != nil {
		command.Credentials = &coreapp.AdminMicrosoftCredentials{
			Password: request.Credentials.Password, ClientID: request.Credentials.ClientID, RefreshToken: request.Credentials.RefreshToken,
		}
	}
	_, err := h.module.AdminCommands.Edit(c.Request.Context(), command)
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	h.writeAdminMutation(c, resourceID)
}

func (h *CoreHandler) PutAdminMicrosoftResourceCredentials(c *gin.Context) {
	if !requireAdminIdempotencyKey(c) {
		return
	}
	resourceID, ok := parseResourceID(c)
	if !ok {
		return
	}
	var request adminMicrosoftReplaceCredentialsRequest
	if err := bindAdminResourceJSON(c, &request); err != nil {
		writeAdminInvalidBody(c, err)
		return
	}
	_, err := h.module.AdminCommands.ReplaceCredentials(c.Request.Context(), coreapp.AdminMicrosoftEditCommand{
		ResourceID:     resourceID,
		Version:        request.Version,
		Credentials:    &coreapp.AdminMicrosoftCredentials{Password: request.Password, ClientID: request.ClientID, RefreshToken: request.RefreshToken},
		OperatorUserID: mustCurrentAdminUserID(c), IdempotencyKey: c.GetHeader("Idempotency-Key"),
		RequestID: middleware.GetRequestID(c), Path: c.FullPath(),
	})
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	h.writeAdminMutation(c, resourceID)
}

func (h *CoreHandler) PostAdminMicrosoftResourceValidate(c *gin.Context) {
	if !requireAdminIdempotencyKey(c) {
		return
	}
	resourceID, ok := parseResourceID(c)
	if !ok {
		return
	}
	result, err := h.module.AdminCommands.Validate(
		c.Request.Context(), resourceID, mustCurrentAdminUserID(c), c.GetHeader("Idempotency-Key"), middleware.GetRequestID(c), c.FullPath(),
	)
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, ResourceValidationsResponse{Requested: result.Accepted, Queued: result.Accepted})
}

func (h *CoreHandler) PostAdminMicrosoftResourceEnable(c *gin.Context) {
	if !requireAdminIdempotencyKey(c) {
		return
	}
	resourceID, version, ok := parseAdminResourceVersionCommand(c)
	if !ok {
		return
	}
	_, err := h.module.AdminCommands.Enable(
		c.Request.Context(), resourceID, version, mustCurrentAdminUserID(c), c.GetHeader("Idempotency-Key"), middleware.GetRequestID(c), c.FullPath(),
	)
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	h.writeAdminMutation(c, resourceID)
}

func (h *CoreHandler) PostAdminMicrosoftResourceRecover(c *gin.Context) {
	if !requireAdminIdempotencyKey(c) {
		return
	}
	resourceID, version, ok := parseAdminResourceVersionCommand(c)
	if !ok {
		return
	}
	_, err := h.module.AdminCommands.Recover(
		c.Request.Context(), resourceID, version, mustCurrentAdminUserID(c), c.GetHeader("Idempotency-Key"), middleware.GetRequestID(c), c.FullPath(),
	)
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	h.writeAdminMutation(c, resourceID)
}

func (h *CoreHandler) PostAdminMicrosoftResourceDisable(c *gin.Context) {
	h.applyAdminResourceState(c, coreapp.AdminMicrosoftDisable)
}

func (h *CoreHandler) PostAdminMicrosoftResourcePublish(c *gin.Context) {
	h.applyAdminResourceState(c, coreapp.AdminMicrosoftPublish)
}

func (h *CoreHandler) PostAdminMicrosoftResourceUnpublish(c *gin.Context) {
	h.applyAdminResourceState(c, coreapp.AdminMicrosoftUnpublish)
}

func (h *CoreHandler) DeleteAdminMicrosoftResource(c *gin.Context) {
	h.applyAdminResourceState(c, coreapp.AdminMicrosoftDelete)
}

func (h *CoreHandler) PostAdminMicrosoftResourcesDisable(c *gin.Context) {
	h.applyAdminResourceStateBatch(c, coreapp.AdminMicrosoftDisable)
}

func (h *CoreHandler) PostAdminMicrosoftResourcesPublish(c *gin.Context) {
	h.submitAdminResourceStateBulk(c, coreapp.AdminResourceBulkPublish)
}

func (h *CoreHandler) PostAdminMicrosoftResourcesUnpublish(c *gin.Context) {
	h.submitAdminResourceStateBulk(c, coreapp.AdminResourceBulkUnpublish)
}

func (h *CoreHandler) PostAdminMicrosoftResourcesDelete(c *gin.Context) {
	h.submitAdminResourceStateBulk(c, coreapp.AdminResourceBulkDelete)
}

func (h *CoreHandler) PostAdminMicrosoftResourceValidations(c *gin.Context) {
	if !requireAdminIdempotencyKey(c) {
		return
	}
	var request adminMicrosoftBulkCommandRequest
	if err := bindAdminResourceJSON(c, &request); err != nil {
		writeAdminInvalidBody(c, err)
		return
	}
	selection, ok := toAdminBulkSelection(c, request.Selection)
	if !ok {
		return
	}
	result, err := h.module.AdminCommands.SubmitValidationBatch(
		c.Request.Context(), selection, mustCurrentAdminUserID(c), c.GetHeader("Idempotency-Key"), middleware.GetRequestID(c), c.FullPath(),
	)
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, ResourceValidationsResponse{Requested: result.Requested, Queued: result.Queued})
}

func (h *CoreHandler) PostAdminMicrosoftResourcesMaintenance(c *gin.Context) {
	if !requireAdminIdempotencyKey(c) {
		return
	}
	var request adminMicrosoftMaintenanceRequest
	if err := bindAdminResourceJSON(c, &request); err != nil {
		writeAdminInvalidBody(c, err)
		return
	}
	selection, ok := toAdminBulkSelection(c, request.Selection)
	if !ok {
		return
	}
	h.submitAdminResourceBulkSelection(c, coreapp.AdminResourceBulkAction(request.Action), selection)
}

func (h *CoreHandler) applyAdminResourceState(c *gin.Context, command coreapp.AdminMicrosoftStateCommand) {
	if !requireAdminIdempotencyKey(c) {
		return
	}
	resourceID, version, ok := parseAdminResourceVersionCommand(c)
	if !ok {
		return
	}
	err := h.module.AdminCommands.ApplyState(
		c.Request.Context(), command, resourceID, version, mustCurrentAdminUserID(c), c.GetHeader("Idempotency-Key"), middleware.GetRequestID(c), c.FullPath(),
	)
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *CoreHandler) applyAdminResourceStateBatch(c *gin.Context, command coreapp.AdminMicrosoftStateCommand) {
	if !validAdminIdempotencyKey(c.GetHeader("Idempotency-Key")) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid Idempotency-Key.", "requestId": middleware.GetRequestID(c)})
		return
	}
	var request adminMicrosoftIDsCommandRequest
	if err := bindAdminResourceJSON(c, &request); err != nil {
		writeAdminInvalidBody(c, err)
		return
	}
	if request.Selection.Mode != "ids" {
		writeAdminResourceError(c, domain.ErrInvalidResourceCommand)
		return
	}
	if request.Selection.Filter != nil {
		writeAdminResourceError(c, domain.ErrInvalidResourceCommand)
		return
	}
	result, err := h.module.AdminCommands.ApplyStateBatch(
		c.Request.Context(), command, request.Selection.ResourceIDs, mustCurrentAdminUserID(c), c.GetHeader("Idempotency-Key"), middleware.GetRequestID(c), c.FullPath(),
	)
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	reasons := make([]adminTaskReasonResponse, len(result.ReasonCounts))
	for i := range result.ReasonCounts {
		reasons[i] = adminTaskReasonResponse{Reason: result.ReasonCounts[i].Reason, Count: result.ReasonCounts[i].Count}
	}
	c.JSON(http.StatusOK, adminMicrosoftBulkResultResponse{
		Requested: result.Requested, Affected: result.Affected, Skipped: result.Skipped,
		AffectedResourceIDs: result.AffectedResourceIDs, SkippedResourceIDs: result.SkippedResourceIDs, ReasonCounts: reasons,
	})
}

// submitAdminResourceStateBulk accepts a Microsoft publish/unpublish/delete
// batch (explicit IDs or filter) into the Redis-backed AdminResourceBulkService, the
// same asynchronous worker that already runs bulk maintenance. Large Microsoft
// tables no longer block the request thread; the client polls the returned task.
func (h *CoreHandler) submitAdminResourceStateBulk(c *gin.Context, bulkAction coreapp.AdminResourceBulkAction) {
	if !validAdminIdempotencyKey(c.GetHeader("Idempotency-Key")) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid Idempotency-Key.", "requestId": middleware.GetRequestID(c)})
		return
	}
	var request adminMicrosoftBulkCommandRequest
	if err := bindAdminResourceJSON(c, &request); err != nil {
		writeAdminInvalidBody(c, err)
		return
	}
	selection, ok := toAdminBulkSelection(c, request.Selection)
	if !ok {
		return
	}
	h.submitAdminResourceBulkSelection(c, bulkAction, selection)
}

func (h *CoreHandler) submitAdminResourceBulkSelection(c *gin.Context, action coreapp.AdminResourceBulkAction, selection coreapp.AdminResourceBulkSelection) {
	if h.module == nil || h.module.AdminBulk == nil {
		writeAdminResourceError(c, domain.ErrResourceDependency)
		return
	}
	command, reused, err := h.module.AdminBulk.Submit(
		c.Request.Context(), action, selection, mustCurrentAdminUserID(c), c.GetHeader("Idempotency-Key"), middleware.GetRequestID(c), c.FullPath(),
	)
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	task := toAdminBulkTask(command)
	c.JSON(http.StatusAccepted, adminTaskAcceptedResponse{
		TaskID: task.TaskID, RequestID: command.RequestID, Status: task.Status, Accepted: int64(command.MatchedCount),
		Task: task, Reused: reused,
	})
}

func (h *CoreHandler) writeAdminMutation(c *gin.Context, resourceID uint) {
	detail, err := h.module.AdminResourceQuery.Get(c.Request.Context(), resourceID)
	if err != nil {
		writeAdminResourceError(c, err)
		return
	}
	c.JSON(http.StatusOK, adminMicrosoftMutationResponse{Resource: toAdminMicrosoftDetailResponse(detail)})
}

func adminMicrosoftFilterFromQuery(c *gin.Context) (coreapp.AdminMicrosoftListFilter, bool) {
	filter := coreapp.AdminMicrosoftListFilter{
		Search: c.Query("search"), Suffix: c.Query("suffix"),
		Status:      domain.MicrosoftResourceStatus(strings.TrimSpace(c.Query("status"))),
		TokenHealth: strings.TrimSpace(c.Query("tokenHealth")),
	}
	var ok bool
	filter.ForSale, ok = parseOptionalBoolQuery(c, "forSale")
	if !ok {
		return filter, false
	}
	filter.LongLived, ok = parseOptionalBoolQuery(c, "longLived")
	if !ok {
		return filter, false
	}
	filter.GraphAvailable, ok = parseOptionalBoolQuery(c, "graphAvailable")
	if !ok {
		return filter, false
	}
	filter.CreatedFrom, ok = parseOptionalCoreTimeQuery(c, "createdFrom")
	if !ok {
		return filter, false
	}
	filter.CreatedTo, ok = parseOptionalCoreTimeQuery(c, "createdTo")
	return filter, ok
}

func toAdminMicrosoftListResponse(result *coreapp.AdminMicrosoftListResult) adminMicrosoftListResponse {
	items := make([]adminMicrosoftResourceItemResponse, len(result.Items))
	for i := range result.Items {
		items[i] = toAdminMicrosoftItemResponse(result.Items[i])
	}
	suffixes := make([]adminMicrosoftSuffixFacetResponse, len(result.Facets.Suffixes))
	for i := range result.Facets.Suffixes {
		suffixes[i] = adminMicrosoftSuffixFacetResponse{Key: result.Facets.Suffixes[i].Key, Count: result.Facets.Suffixes[i].Count}
	}
	return adminMicrosoftListResponse{
		Items: items, Total: result.Total, Offset: result.Offset, Limit: result.Limit, NextAfterID: result.NextAfterID,
		Facets: adminMicrosoftFacetsResponse{
			Status:         adminMicrosoftStatusFacetResponse(result.Facets.Status),
			ForSale:        adminMicrosoftBooleanFacetResponse(result.Facets.ForSale),
			LongLived:      adminMicrosoftBooleanFacetResponse(result.Facets.LongLived),
			GraphAvailable: adminMicrosoftBooleanFacetResponse(result.Facets.GraphAvailable),
			TokenHealth:    adminMicrosoftTokenFacetResponse(result.Facets.TokenHealth), Suffixes: suffixes,
		},
	}
}

func toAdminMicrosoftItemResponse(item coreapp.AdminMicrosoftResourceItem) adminMicrosoftResourceItemResponse {
	return adminMicrosoftResourceItemResponse{
		ID: item.ID, Type: "microsoft", Version: item.Version, EmailAddress: item.EmailAddress, Suffix: item.Suffix,
		BindingAddress: item.BindingAddress,
		Owner:          adminMicrosoftOwnerResponse{ID: item.Owner.ID, Email: item.Owner.Email, Nickname: item.Owner.Nickname, GroupName: item.Owner.GroupName, Role: item.Owner.Role, Enabled: item.Owner.Enabled},
		Status:         item.Status, ForSale: item.ForSale, LongLived: item.LongLived, GraphAvailable: item.GraphAvailable,
		MailProtocol: item.MailProtocol, QualityScore: item.QualityScore, TokenHealth: item.TokenHealth, RTExpireAt: item.RTExpireAt,
		LastAllocatedAt: item.LastAllocatedAt, LastSafeError: item.LastSafeError, ActiveTask: toAdminTaskSummary(item.ActiveTask),
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func toAdminMicrosoftDetailResponse(detail *coreapp.AdminMicrosoftResourceDetail) adminMicrosoftDetailResponse {
	recent := make([]adminTaskSummaryResponse, len(detail.RecentTasks))
	for i := range detail.RecentTasks {
		recent[i] = *toAdminTaskSummary(&detail.RecentTasks[i])
	}
	proxyBindings := make([]adminMicrosoftProxyBindingResponse, len(detail.ProxyBindings))
	for i := range detail.ProxyBindings {
		proxyBindings[i] = adminMicrosoftProxyBindingResponse{
			ProxyID: detail.ProxyBindings[i].ProxyID, Host: detail.ProxyBindings[i].Host,
			OutboundIP: detail.ProxyBindings[i].OutboundIP, Country: detail.ProxyBindings[i].Country,
			IPVersion: detail.ProxyBindings[i].IPVersion, Status: detail.ProxyBindings[i].Status,
			ExpireAt: detail.ProxyBindings[i].ExpireAt,
		}
	}
	return adminMicrosoftDetailResponse{
		adminMicrosoftResourceItemResponse: toAdminMicrosoftItemResponse(detail.AdminMicrosoftResourceItem),
		AliasCounts:                        adminMicrosoftAliasCountsResponse{Explicit: detail.AliasCounts.Explicit, Dot: detail.AliasCounts.Dot, Plus: detail.AliasCounts.Plus},
		RecentTasks:                        recent,
		Credentials: adminMicrosoftCredentialResponse{
			PasswordConfigured: detail.Credentials.PasswordConfigured, ClientIDConfigured: detail.Credentials.ClientIDConfigured,
			RefreshTokenConfigured: detail.Credentials.RefreshTokenConfigured, Revision: detail.Credentials.Revision, UpdatedAt: detail.Credentials.UpdatedAt,
		},
		Token: adminMicrosoftTokenResponse{
			Health: detail.Token.Health, RTExpireAt: detail.Token.RTExpireAt, RemainingSeconds: detail.Token.RemainingSeconds,
			LastRefreshedAt: detail.Token.LastRefreshedAt, Scopes: detail.Token.Scopes,
			LastRefreshRequestID: detail.Token.LastRefreshRequestID, LastSafeError: detail.Token.LastSafeError,
		},
		ProxyBindings: proxyBindings,
	}
}

func toAdminTaskSummary(task *coreapp.AdminTaskSummary) *adminTaskSummaryResponse {
	if task == nil {
		return nil
	}
	return &adminTaskSummaryResponse{TaskID: task.TaskID, Kind: task.Kind, Status: task.Status, CredentialRevision: task.CredentialRevision, UpdatedAt: task.UpdatedAt}
}

func toAdminImportResponse(item *coreapp.ResourceImportStatusView, reused bool) adminMicrosoftImportResponse {
	taskStatus := item.TaskStatus
	if taskStatus == "pending" {
		taskStatus = "queued"
	}
	if taskStatus == "" {
		taskStatus = "running"
		switch item.Status {
		case string(domain.ResourceImportImported):
			taskStatus = "succeeded"
		case string(domain.ResourceImportFailed):
			taskStatus = "failed"
		}
	}
	var safeError *string
	if value := strings.TrimSpace(item.LastSafeError); value != "" {
		safeError = &value
	}
	task := adminTaskViewResponse{
		TaskID: fmt.Sprintf("import:%d", item.ImportID), Kind: "import", BizType: "microsoft_resource", BizID: item.ImportID,
		Status: taskStatus, Attempts: item.Attempts, MaxAttempts: item.MaxAttempts,
		RemainingAttempts: max(0, item.MaxAttempts-item.Attempts),
		QueuedAt:          item.CreatedAt, StartedAt: item.StartedAt, FinishedAt: item.FinishedAt, UpdatedAt: item.UpdatedAt,
	}
	return adminMicrosoftImportResponse{
		ImportID: item.ImportID, Status: item.Status, Accepted: int64(item.Accepted), Imported: int64(item.Imported), Skipped: int64(item.Skipped),
		TaskID: task.TaskID, RequestID: item.RequestID, LastSafeError: safeError, Task: task, Reused: reused,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func toAdminBulkTask(command *coreapp.AdminResourceBulkCommand) adminTaskViewResponse {
	kind := "bulk_" + string(command.Action)
	if command.Action == coreapp.AdminResourceBulkValidate {
		kind = "bulk_validation"
	}
	remaining := command.MaxAttempts - command.Attempts
	if remaining < 0 {
		remaining = 0
	}
	reasons := make([]adminTaskReasonResponse, 0, len(command.ReasonCounts))
	keys := make([]string, 0, len(command.ReasonCounts))
	for key := range command.ReasonCounts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		reasons = append(reasons, adminTaskReasonResponse{Reason: key, Count: command.ReasonCounts[key]})
	}
	return adminTaskViewResponse{
		TaskID: fmt.Sprintf("bulk:%d", command.ID), Kind: kind, BizType: "microsoft_resource_bulk", BizID: uint(command.ID),
		Status: command.Status, Attempts: command.Attempts, MaxAttempts: command.MaxAttempts, RemainingAttempts: remaining,
		Progress: &adminTaskProgressResponse{
			Total: int64(command.MatchedCount), Processed: int64(command.ProcessedCount),
			Succeeded: int64(command.AffectedCount), Failed: 0, Skipped: int64(command.SkippedCount), ReasonCounts: reasons,
		},
		QueuedAt: command.CreatedAt, StartedAt: command.StartedAt, FinishedAt: command.FinishedAt, UpdatedAt: command.UpdatedAt,
	}
}

func toAdminBulkSelection(c *gin.Context, request adminMicrosoftBulkSelectionRequest) (coreapp.AdminResourceBulkSelection, bool) {
	switch request.Mode {
	case "ids":
		if request.Filter != nil {
			writeAdminResourceError(c, domain.ErrInvalidResourceCommand)
			return coreapp.AdminResourceBulkSelection{}, false
		}
		return coreapp.AdminResourceBulkSelection{Mode: coreapp.AdminResourceBulkIDs, ResourceIDs: request.ResourceIDs}, true
	case "filter":
		if request.Filter == nil || request.Filter.Type != "microsoft" || len(request.ResourceIDs) != 0 {
			writeAdminResourceError(c, domain.ErrInvalidResourceCommand)
			return coreapp.AdminResourceBulkSelection{}, false
		}
		return coreapp.AdminResourceBulkSelection{
			Mode: coreapp.AdminResourceBulkFilter,
			Filter: coreapp.AdminResourceBulkFilterValue{
				Search: request.Filter.Search, Suffix: request.Filter.Suffix,
				Status:  domain.MicrosoftResourceStatus(request.Filter.Status),
				ForSale: request.Filter.ForSale, LongLived: request.Filter.LongLived,
				GraphAvailable: request.Filter.GraphAvailable, TokenHealth: request.Filter.TokenHealth,
				CreatedFrom: request.Filter.CreatedFrom, CreatedTo: request.Filter.CreatedTo,
			},
		}, true
	default:
		writeAdminResourceError(c, domain.ErrInvalidResourceCommand)
		return coreapp.AdminResourceBulkSelection{}, false
	}
}

func bindAdminResourceJSON(c *gin.Context, destination any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAdminResourceJSONBytes)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return binding.Validator.ValidateStruct(destination)
}

func decodeNullableAdminEmail(c *gin.Context, raw json.RawMessage) (*string, bool, bool) {
	if raw == nil {
		return nil, false, true
	}
	if string(raw) == "null" {
		return nil, true, true
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		writeAdminInvalidBody(c, err)
		return nil, false, false
	}
	return &value, true, true
}

func parseAdminResourceVersionCommand(c *gin.Context) (uint, uint64, bool) {
	resourceID, ok := parseResourceID(c)
	if !ok {
		return 0, 0, false
	}
	raw := strings.TrimSpace(c.Query("version"))
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid version.", "requestId": middleware.GetRequestID(c)})
		return 0, 0, false
	}
	return resourceID, value, true
}

func mustCurrentAdminUserID(c *gin.Context) uint {
	userID, _ := middleware.GetCurrentUserID(c)
	return userID
}

func validAdminIdempotencyKey(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 128
}

func validateAdminLimitQuery(c *gin.Context, maximum int) bool {
	raw := strings.TrimSpace(c.Query("limit"))
	if raw == "" {
		return true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > maximum {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid pagination parameters.", "requestId": middleware.GetRequestID(c)})
		return false
	}
	return true
}

func requireAdminIdempotencyKey(c *gin.Context) bool {
	if validAdminIdempotencyKey(c.GetHeader("Idempotency-Key")) {
		return true
	}
	c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid Idempotency-Key.", "requestId": middleware.GetRequestID(c)})
	return false
}

func (h *CoreHandler) requireAdminResourcePermission(c *gin.Context, resource, action string) bool {
	userID, userOK := middleware.GetCurrentUserID(c)
	role, roleOK := middleware.GetCurrentRole(c)
	if !userOK || !roleOK {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "Authentication is required.", "requestId": middleware.GetRequestID(c)})
		return false
	}
	if h.permissionChecker == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": middleware.GetRequestID(c)})
		return false
	}
	allowed, err := h.permissionChecker.Check(c.Request.Context(), userID, role, resource, action)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": middleware.GetRequestID(c)})
		return false
	}
	if !allowed {
		c.JSON(http.StatusForbidden, gin.H{"message": "Permission denied.", "requestId": middleware.GetRequestID(c)})
		return false
	}
	return true
}

func writeAdminInvalidBody(c *gin.Context, err error) {
	c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request body.", "fields": validationErrors(err), "requestId": middleware.GetRequestID(c)})
}

func writeAdminResourceError(c *gin.Context, err error) {
	rid := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, domain.ErrResourceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "Resource not found.", "requestId": rid})
	case errors.Is(err, domain.ErrResourceVersionConflict):
		c.JSON(http.StatusConflict, gin.H{"message": "Resource changed; refresh and try again.", "requestId": rid})
	case errors.Is(err, domain.ErrResourceIdempotencyConflict):
		c.JSON(http.StatusConflict, gin.H{"message": "Idempotency key was already used for a different command.", "requestId": rid})
	case errors.Is(err, domain.ErrResourceHasAllocation):
		c.JSON(http.StatusConflict, gin.H{"message": "Resource has an active allocation.", "requestId": rid})
	case errors.Is(err, domain.ErrDuplicateEmail):
		c.JSON(http.StatusConflict, gin.H{"message": "Email address already exists.", "requestId": rid})
	case errors.Is(err, domain.ErrInvalidResourceStatus):
		c.JSON(http.StatusConflict, gin.H{"message": "Resource state does not allow this command.", "requestId": rid})
	case errors.Is(err, domain.ErrInvalidResourceFilter), errors.Is(err, domain.ErrInvalidResourceCommand), errors.Is(err, domain.ErrInvalidResourceOwner):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid resource command.", "requestId": rid})
	case errors.Is(err, domain.ErrResourceDependency):
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Resource service is temporarily unavailable.", "requestId": rid})
	default:
		writeCoreError(c, err)
	}
}
