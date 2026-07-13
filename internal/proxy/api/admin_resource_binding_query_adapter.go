package api

import (
	"context"

	coreapp "github.com/donnel666/remail/internal/core/app"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
)

// AdminResourceProxyBindingQueryAdapter translates the Proxy-owned safe binding
// view to Core's consumer-defined administrator read port.
type AdminResourceProxyBindingQueryAdapter struct {
	query *proxyapp.AdminResourceProxyBindingQuery
}

var _ coreapp.AdminProxyBindingQueryPort = (*AdminResourceProxyBindingQueryAdapter)(nil)

func NewAdminResourceProxyBindingQueryAdapter(query *proxyapp.AdminResourceProxyBindingQuery) *AdminResourceProxyBindingQueryAdapter {
	return &AdminResourceProxyBindingQueryAdapter{query: query}
}

func (a *AdminResourceProxyBindingQueryAdapter) GetByEmailAddresses(ctx context.Context, addresses []string) (map[string][]coreapp.AdminProxyBindingSummary, error) {
	result := make(map[string][]coreapp.AdminProxyBindingSummary)
	if a == nil || a.query == nil {
		return result, nil
	}
	bindings, err := a.query.GetByKeys(ctx, addresses)
	if err != nil {
		return nil, err
	}
	for key, items := range bindings {
		mapped := make([]coreapp.AdminProxyBindingSummary, len(items))
		for i := range items {
			mapped[i] = coreapp.AdminProxyBindingSummary{
				ProxyID:    items[i].ProxyID,
				Host:       items[i].Host,
				OutboundIP: items[i].OutboundIP,
				Country:    items[i].Country,
				IPVersion:  items[i].IPVersion,
				Status:     items[i].Status,
				ExpireAt:   items[i].ExpireAt,
			}
		}
		result[key] = mapped
	}
	return result, nil
}
