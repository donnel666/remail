package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/donnel666/remail/api/middleware"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	"github.com/donnel666/remail/internal/proxy/domain"
	"github.com/gin-gonic/gin"
)

type ProxyHandler struct {
	module *ProxyModule
}

func NewProxyHandler(module *ProxyModule) *ProxyHandler {
	return &ProxyHandler{module: module}
}

func (h *ProxyHandler) GetProxies(c *gin.Context) {
	offset, limit, ok := parsePagination(c)
	if !ok {
		return
	}
	filter, ok := parseProxyFilter(c)
	if !ok {
		return
	}

	result, err := h.module.ProxyUseCase.List(c.Request.Context(), filter, offset, limit)
	if err != nil {
		writeProxyError(c, err)
		return
	}

	items := make([]ProxyItemResponse, len(result.Items))
	for i := range result.Items {
		items[i] = toProxyItemResponse(result.Items[i], false)
	}
	c.JSON(http.StatusOK, ProxyListResponse{
		Items:  items,
		Total:  result.Total,
		Offset: result.Offset,
		Limit:  result.Limit,
	})
}

func (h *ProxyHandler) GetProxyStats(c *gin.Context) {
	filter, ok := parseProxyFilter(c)
	if !ok {
		return
	}
	stats, err := h.module.ProxyUseCase.Stats(c.Request.Context(), filter)
	if err != nil {
		writeProxyError(c, err)
		return
	}
	c.JSON(http.StatusOK, ProxyStatsResponse{
		Total:      stats.Total,
		Countries:  toProxyCountResponse(stats.Countries),
		Statuses:   toProxyCountResponse(stats.Statuses),
		Pools:      toProxyCountResponse(stats.Pools),
		IPVersions: toProxyCountResponse(stats.IPVersions),
	})
}

func (h *ProxyHandler) GetProxyBindings(c *gin.Context) {
	offset, limit, ok := parsePagination(c)
	if !ok {
		return
	}
	filter, ok := parseProxyBindingFilter(c)
	if !ok {
		return
	}

	result, err := h.module.ProxyUseCase.ListBindings(c.Request.Context(), filter, offset, limit)
	if err != nil {
		writeProxyError(c, err)
		return
	}

	items := make([]ProxyBindingItemResponse, len(result.Items))
	for i := range result.Items {
		items[i] = toProxyBindingItemResponse(result.Items[i])
	}
	c.JSON(http.StatusOK, ProxyBindingListResponse{
		Items:  items,
		Total:  result.Total,
		Offset: result.Offset,
		Limit:  result.Limit,
	})
}

func (h *ProxyHandler) PostResourceProxy(c *gin.Context) {
	h.postProxy(c, domain.ProxyPoolResource)
}

func (h *ProxyHandler) PostSystemProxy(c *gin.Context) {
	h.postProxy(c, domain.ProxyPoolSystem)
}

func (h *ProxyHandler) PostProxyImports(c *gin.Context) {
	operatorUserID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}

	var req ImportProxiesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	pool, poolOK := domain.NormalizeProxyPool(req.Pool)
	if !poolOK {
		writeProxyError(c, domain.ErrInvalidProxyPool)
		return
	}

	result, err := h.module.ProxyUseCase.Import(c.Request.Context(), operatorUserID, middleware.GetRequestID(c), c.FullPath(), proxyapp.ImportProxiesRequest{
		Pool:     pool,
		URLs:     req.URLs,
		ExpireAt: req.ExpireAt,
	})
	if err != nil {
		writeProxyError(c, err)
		return
	}
	items := make([]ProxyItemResponse, len(result.Items))
	for i := range result.Items {
		items[i] = toProxyItemResponse(result.Items[i], false)
	}
	c.JSON(http.StatusCreated, ImportProxiesResponse{
		Requested:  result.Requested,
		Created:    result.Created,
		Duplicated: result.Duplicated,
		Items:      items,
	})
}

func (h *ProxyHandler) postProxy(c *gin.Context, pool domain.ProxyPool) {
	operatorUserID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}

	var req CreateProxyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	proxy, err := h.module.ProxyUseCase.Create(c.Request.Context(), operatorUserID, middleware.GetRequestID(c), c.FullPath(), proxyapp.CreateProxyRequest{
		Pool:     pool,
		URL:      req.URL,
		ExpireAt: req.ExpireAt,
	})
	if err != nil {
		writeProxyError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toProxyItemResponse(*proxy, false))
}

func (h *ProxyHandler) GetProxy(c *gin.Context) {
	proxyID, ok := parseProxyID(c)
	if !ok {
		return
	}
	proxy, err := h.module.ProxyUseCase.Get(c.Request.Context(), proxyID)
	if err != nil {
		writeProxyError(c, err)
		return
	}
	c.JSON(http.StatusOK, toProxyItemResponse(*proxy, true))
}

func (h *ProxyHandler) PatchProxy(c *gin.Context) {
	operatorUserID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	proxyID, ok := parseProxyID(c)
	if !ok {
		return
	}

	var req UpdateProxyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}
	var status *domain.ProxyStatus
	if req.Status != nil {
		normalized := domain.ProxyStatus(strings.ToLower(strings.TrimSpace(*req.Status)))
		status = &normalized
	}

	proxy, err := h.module.ProxyUseCase.Update(c.Request.Context(), proxyID, operatorUserID, middleware.GetRequestID(c), c.FullPath(), proxyapp.UpdateProxyRequest{
		Status:   status,
		ExpireAt: req.ExpireAt,
	})
	if err != nil {
		writeProxyError(c, err)
		return
	}
	c.JSON(http.StatusOK, toProxyItemResponse(*proxy, false))
}

func (h *ProxyHandler) PostProxyDeleteBatch(c *gin.Context) {
	operatorUserID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}

	var req DeleteProxiesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	var result *proxyapp.DeleteProxiesResult
	var err error
	if len(req.ProxyIDs) == 0 && req.All {
		filter, filterOK := proxyFilterFromBulkRequest(req.Filter)
		if !filterOK {
			writeProxyError(c, domain.ErrInvalidProxyFilter)
			return
		}
		result, err = h.module.ProxyUseCase.DeleteByFilter(c.Request.Context(), filter, operatorUserID, middleware.GetRequestID(c), c.FullPath())
	} else if len(req.ProxyIDs) == 0 {
		writeProxyError(c, domain.ErrInvalidProxyFilter)
		return
	} else {
		result, err = h.module.ProxyUseCase.DeleteBatch(c.Request.Context(), req.ProxyIDs, operatorUserID, middleware.GetRequestID(c), c.FullPath())
	}
	if err != nil {
		writeProxyError(c, err)
		return
	}
	c.JSON(http.StatusOK, DeleteProxiesResponse{
		Requested:       result.Requested,
		Deleted:         result.Deleted,
		DeletedProxyIDs: result.DeletedProxyIDs,
		DeletedByFilter: result.DeletedByFilter,
	})
}

func (h *ProxyHandler) PostProxyCheckBatch(c *gin.Context) {
	operatorUserID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}

	var req CheckProxiesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid request body.",
			"fields":    validationErrors(err),
			"requestId": middleware.GetRequestID(c),
		})
		return
	}

	var result *proxyapp.CheckProxiesResult
	var err error
	if len(req.ProxyIDs) == 0 && req.All {
		filter, filterOK := proxyFilterFromBulkRequest(req.Filter)
		if !filterOK {
			writeProxyError(c, domain.ErrInvalidProxyFilter)
			return
		}
		result, err = h.module.ProxyUseCase.CheckByFilter(c.Request.Context(), filter, operatorUserID, middleware.GetRequestID(c), c.FullPath())
	} else if len(req.ProxyIDs) == 0 {
		writeProxyError(c, domain.ErrInvalidProxyFilter)
		return
	} else {
		result, err = h.module.ProxyUseCase.CheckBatch(c.Request.Context(), req.ProxyIDs, operatorUserID, middleware.GetRequestID(c), c.FullPath())
	}
	if err != nil {
		writeProxyError(c, err)
		return
	}
	items := make([]ProxyItemResponse, len(result.Items))
	for i := range result.Items {
		items[i] = toProxyItemResponse(result.Items[i], false)
	}
	c.JSON(http.StatusOK, CheckProxiesResponse{
		Requested: result.Requested,
		Checked:   result.Checked,
		Failed:    result.Failed,
		Items:     items,
	})
}

func (h *ProxyHandler) PostProxyCheck(c *gin.Context) {
	operatorUserID, ok := requireCurrentUserID(c)
	if !ok {
		return
	}
	proxyID, ok := parseProxyID(c)
	if !ok {
		return
	}

	proxy, err := h.module.ProxyUseCase.Check(c.Request.Context(), proxyID, operatorUserID, middleware.GetRequestID(c), c.FullPath())
	if proxy != nil && (errors.Is(err, domain.ErrProxyCheckFailed) || errors.Is(err, domain.ErrProxyUnavailable)) {
		c.JSON(http.StatusOK, toProxyItemResponse(*proxy, false))
		return
	}
	if err != nil {
		writeProxyError(c, err)
		return
	}
	c.JSON(http.StatusOK, toProxyItemResponse(*proxy, false))
}

func parseProxyFilter(c *gin.Context) (proxyapp.ProxyListFilter, bool) {
	filter := proxyapp.ProxyListFilter{
		Country: c.Query("country"),
		Search:  c.Query("search"),
	}
	if rawPool := strings.TrimSpace(c.Query("pool")); rawPool != "" {
		pool, ok := domain.NormalizeProxyPool(rawPool)
		if !ok {
			writeProxyError(c, domain.ErrInvalidProxyFilter)
			return filter, false
		}
		filter.Pool = pool
	}
	rawIP := strings.TrimSpace(c.Query("ip"))
	if rawIP == "" {
		rawIP = strings.TrimSpace(c.Query("ipVersion"))
	}
	if rawIP != "" {
		ipVersion, ok := domain.NormalizeProxyIPVersion(rawIP)
		if !ok {
			writeProxyError(c, domain.ErrInvalidProxyFilter)
			return filter, false
		}
		filter.IPVersion = ipVersion
	}
	if rawIPv6 := strings.TrimSpace(c.Query("ipv6")); rawIPv6 != "" {
		ipv6, err := strconv.ParseBool(rawIPv6)
		if err != nil {
			writeProxyError(c, domain.ErrInvalidProxyFilter)
			return filter, false
		}
		filter.IPv6 = &ipv6
	}
	if rawStatus := strings.TrimSpace(c.Query("status")); rawStatus != "" {
		status := domain.ProxyStatus(strings.ToLower(rawStatus))
		if !domain.IsValidProxyStatus(string(status)) {
			writeProxyError(c, domain.ErrInvalidProxyFilter)
			return filter, false
		}
		filter.Status = status
	}
	createdFrom, ok := parseOptionalTimeQuery(c, "createdFrom")
	if !ok {
		return filter, false
	}
	createdTo, ok := parseOptionalTimeQuery(c, "createdTo")
	if !ok {
		return filter, false
	}
	filter.CreatedFrom = createdFrom
	filter.CreatedTo = createdTo
	return filter, true
}

func parseProxyBindingFilter(c *gin.Context) (proxyapp.ProxyBindingListFilter, bool) {
	filter := proxyapp.ProxyBindingListFilter{
		Key: c.Query("key"),
	}
	if rawProxyID := strings.TrimSpace(c.Query("proxyId")); rawProxyID != "" {
		parsed, err := strconv.ParseUint(rawProxyID, 10, 64)
		if err != nil || parsed == 0 {
			writeProxyError(c, domain.ErrInvalidProxyFilter)
			return filter, false
		}
		filter.ProxyID = uint(parsed)
	}
	rawIP := strings.TrimSpace(c.Query("ip"))
	if rawIP == "" {
		rawIP = strings.TrimSpace(c.Query("ipVersion"))
	}
	if rawIP != "" {
		ipVersion, ok := domain.NormalizeProxyIPVersion(rawIP)
		if !ok {
			writeProxyError(c, domain.ErrInvalidProxyFilter)
			return filter, false
		}
		filter.IPVersion = ipVersion
	}
	return filter, true
}

func proxyFilterFromBulkRequest(req *ProxyBulkFilterRequest) (proxyapp.ProxyListFilter, bool) {
	if req == nil {
		return proxyapp.ProxyListFilter{}, true
	}
	filter := proxyapp.ProxyListFilter{
		Country:     req.Country,
		Search:      req.Search,
		IPv6:        req.IPv6,
		CreatedFrom: req.CreatedFrom,
		CreatedTo:   req.CreatedTo,
	}
	if strings.TrimSpace(req.Pool) != "" {
		pool, ok := domain.NormalizeProxyPool(req.Pool)
		if !ok {
			return filter, false
		}
		filter.Pool = pool
	}
	if strings.TrimSpace(req.IPVersion) != "" {
		ipVersion, ok := domain.NormalizeProxyIPVersion(req.IPVersion)
		if !ok {
			return filter, false
		}
		filter.IPVersion = ipVersion
	}
	if strings.TrimSpace(req.Status) != "" {
		status := domain.ProxyStatus(strings.ToLower(strings.TrimSpace(req.Status)))
		if !domain.IsValidProxyStatus(string(status)) {
			return filter, false
		}
		filter.Status = status
	}
	return filter, true
}

func parseOptionalTimeQuery(c *gin.Context, name string) (*time.Time, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return nil, true
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		writeProxyError(c, domain.ErrInvalidProxyFilter)
		return nil, false
	}
	utc := parsed.UTC()
	return &utc, true
}

func parseProxyID(c *gin.Context) (uint, bool) {
	raw := c.Param("proxyId")
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || parsed == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid proxy ID.",
			"requestId": middleware.GetRequestID(c),
		})
		return 0, false
	}
	return uint(parsed), true
}

func parsePagination(c *gin.Context) (int, int, bool) {
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid query parameters.",
			"requestId": middleware.GetRequestID(c),
		})
		return 0, 0, false
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message":   "Invalid query parameters.",
			"requestId": middleware.GetRequestID(c),
		})
		return 0, 0, false
	}
	return offset, limit, true
}

func requireCurrentUserID(c *gin.Context) (uint, bool) {
	userID, ok := middleware.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"message":   "Authentication is required.",
			"requestId": middleware.GetRequestID(c),
		})
		return 0, false
	}
	return userID, true
}

func toProxyItemResponse(proxy domain.Proxy, includeRawURL bool) ProxyItemResponse {
	urlValue := domain.RedactProxyURL(proxy.URL)
	if includeRawURL {
		urlValue = proxy.URL
	}
	return ProxyItemResponse{
		ID:            proxy.ID,
		Pool:          string(proxy.Pool),
		URL:           urlValue,
		ExpireAt:      proxy.ExpireAt,
		IPVersion:     string(proxy.IPVersion),
		OutboundIP:    proxy.OutboundIP,
		Country:       proxy.Country,
		LatencyMs:     proxy.LatencyMs,
		Status:        string(proxy.Status),
		Errors:        proxy.Errors,
		LastSafeError: proxy.LastSafeError,
		LastCheckedAt: proxy.LastCheckedAt,
		LastUsedAt:    proxy.LastUsedAt,
		CreatedAt:     proxy.CreatedAt,
		UpdatedAt:     proxy.UpdatedAt,
	}
}

func toProxyBindingItemResponse(binding domain.Binding) ProxyBindingItemResponse {
	return ProxyBindingItemResponse{
		ID:         binding.ID,
		Key:        binding.Key,
		ProxyID:    binding.ProxyID,
		IPVersion:  string(binding.IPVersion),
		ExpireAt:   binding.ExpireAt,
		CreatedAt:  binding.CreatedAt,
		LastUsedAt: binding.LastUsedAt,
	}
}

func toProxyCountResponse(items []proxyapp.ProxyCount) []ProxyCountResponse {
	response := make([]ProxyCountResponse, len(items))
	for i := range items {
		response[i] = ProxyCountResponse{
			Key:   items[i].Key,
			Count: items[i].Count,
		}
	}
	return response
}

func writeProxyError(c *gin.Context, err error) {
	requestID := middleware.GetRequestID(c)
	switch {
	case errors.Is(err, domain.ErrProxyNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "Proxy not found.", "requestId": requestID})
	case errors.Is(err, domain.ErrDuplicateProxy):
		c.JSON(http.StatusConflict, gin.H{"message": "Proxy already exists.", "requestId": requestID})
	case errors.Is(err, domain.ErrInvalidProxyPool):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid proxy pool.", "requestId": requestID})
	case errors.Is(err, domain.ErrInvalidProxyURL):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid proxy URL.", "requestId": requestID})
	case errors.Is(err, domain.ErrInvalidProxyStatus):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid proxy status.", "requestId": requestID})
	case errors.Is(err, domain.ErrInvalidProxyExpireAt):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid proxy expiration time.", "requestId": requestID})
	case errors.Is(err, domain.ErrInvalidProxyFilter):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Invalid proxy filter.", "requestId": requestID})
	case errors.Is(err, domain.ErrProxyCheckFailed):
		c.JSON(http.StatusBadGateway, gin.H{"message": "Proxy check failed.", "requestId": requestID})
	case errors.Is(err, domain.ErrProxyUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Proxy unavailable.", "requestId": requestID})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "An unexpected error occurred.", "requestId": requestID})
	}
}

func validationErrors(err error) map[string]string {
	type validator interface {
		Field() string
		Tag() string
	}

	fields := make(map[string]string)
	if errs, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range errs.Unwrap() {
			if v, ok := e.(validator); ok {
				fields[v.Field()] = v.Tag() + " validation failed"
			}
		}
	} else {
		fields["body"] = err.Error()
	}
	return fields
}
