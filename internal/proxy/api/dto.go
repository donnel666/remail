package api

import "time"

type ProxyItemResponse struct {
	ID            uint       `json:"id"`
	Pool          string     `json:"pool"`
	URL           string     `json:"url"`
	ExpireAt      time.Time  `json:"expireAt"`
	IPVersion     string     `json:"ipVersion"`
	OutboundIP    string     `json:"outboundIp"`
	Country       string     `json:"country"`
	LatencyMs     int        `json:"latencyMs"`
	Status        string     `json:"status"`
	Errors        int        `json:"errors"`
	LastSafeError string     `json:"lastSafeError,omitempty"`
	LastCheckedAt *time.Time `json:"lastCheckedAt,omitempty"`
	LastUsedAt    *time.Time `json:"lastUsedAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

type ProxyListResponse struct {
	Items  []ProxyItemResponse `json:"items"`
	Total  int64               `json:"total"`
	Offset int                 `json:"offset"`
	Limit  int                 `json:"limit"`
}

type ProxyCountResponse struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

type ProxyStatsResponse struct {
	Total      int64                `json:"total"`
	Countries  []ProxyCountResponse `json:"countries"`
	Statuses   []ProxyCountResponse `json:"statuses"`
	Pools      []ProxyCountResponse `json:"pools"`
	IPVersions []ProxyCountResponse `json:"ipVersions"`
}

type ProxyBindingItemResponse struct {
	ID         uint       `json:"id"`
	Key        string     `json:"key"`
	ProxyID    uint       `json:"proxyId"`
	IPVersion  string     `json:"ipVersion"`
	ExpireAt   time.Time  `json:"expireAt"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

type ProxyBindingListResponse struct {
	Items  []ProxyBindingItemResponse `json:"items"`
	Total  int64                      `json:"total"`
	Offset int                        `json:"offset"`
	Limit  int                        `json:"limit"`
}

type CreateProxyRequest struct {
	URL      string    `json:"url" binding:"required"`
	ExpireAt time.Time `json:"expireAt" binding:"required"`
}

type ImportProxiesRequest struct {
	Pool     string    `json:"pool" binding:"required"`
	URLs     []string  `json:"urls" binding:"required,min=1,dive,required"`
	ExpireAt time.Time `json:"expireAt" binding:"required"`
}

type ImportProxiesResponse struct {
	Requested  int                 `json:"requested"`
	Created    int                 `json:"created"`
	Duplicated int                 `json:"duplicated"`
	Items      []ProxyItemResponse `json:"items"`
}

type UpdateProxyRequest struct {
	Status   *string    `json:"status,omitempty"`
	ExpireAt *time.Time `json:"expireAt,omitempty"`
}

type ProxyBulkFilterRequest struct {
	Pool        string     `json:"pool,omitempty"`
	IPVersion   string     `json:"ip,omitempty"`
	IPv6        *bool      `json:"ipv6,omitempty"`
	Status      string     `json:"status,omitempty"`
	Country     string     `json:"country,omitempty"`
	Search      string     `json:"search,omitempty"`
	CreatedFrom *time.Time `json:"createdFrom,omitempty"`
	CreatedTo   *time.Time `json:"createdTo,omitempty"`
}

type DeleteProxiesRequest struct {
	All      bool                    `json:"all,omitempty"`
	Filter   *ProxyBulkFilterRequest `json:"filter,omitempty"`
	ProxyIDs []uint                  `json:"proxyIds,omitempty" binding:"omitempty,dive,gt=0"`
}

type DeleteProxiesResponse struct {
	Requested       int    `json:"requested"`
	Deleted         int    `json:"deleted"`
	DeletedProxyIDs []uint `json:"deletedProxyIds,omitempty"`
	DeletedByFilter bool   `json:"deletedByFilter,omitempty"`
}

type CheckProxiesRequest struct {
	All      bool                    `json:"all,omitempty"`
	Filter   *ProxyBulkFilterRequest `json:"filter,omitempty"`
	ProxyIDs []uint                  `json:"proxyIds,omitempty" binding:"omitempty,dive,gt=0"`
}

type CheckProxiesResponse struct {
	Requested int                 `json:"requested"`
	Checked   int                 `json:"checked"`
	Failed    int                 `json:"failed"`
	Items     []ProxyItemResponse `json:"items"`
}
