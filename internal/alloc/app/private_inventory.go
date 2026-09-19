package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/donnel666/remail/internal/alloc/domain"
	"github.com/donnel666/remail/internal/platform"
)

// PrivateInventoryTotals is stored separately from shared inventory so a
// viewer's resources can never enter the public project snapshot.
type PrivateInventoryTotals struct {
	Microsoft []PrivateProductInventoryTotal
	Domains   []PrivateProductInventoryTotal
	Gmail     []PrivateSingletonInventoryTotal
	ICloud    []PrivateSingletonInventoryTotal
	Proto     []PrivateProductInventoryTotal
}

func (uc *UseCase) SetPrivateInventoryCache(cache *platform.HourlySnapshotCache[PrivateInventoryTotals]) {
	uc.privateInventoryCache = cache
}

func (uc *UseCase) privateInventoryTotals(ctx context.Context, projectID, viewerUserID uint) (*PrivateInventoryTotals, error) {
	if uc.privateInventoryCache == nil {
		totals, err := uc.loadPrivateInventoryTotals(ctx, projectID, viewerUserID)
		return &totals, err
	}
	key := privateInventoryKey(projectID, viewerUserID)
	cached, fresh, err := uc.privateInventoryCache.Read(ctx, key)
	if err != nil {
		slog.WarnContext(ctx, "read private inventory failed", "key", key, "error", err)
		return nil, nil
	}
	// A cold read still serves shared inventory. The durable queue deduplicates
	// pending refreshes and retries failures without another HTTP request.
	if !fresh && uc.queue != nil {
		if err := uc.queue.EnqueuePrivateInventoryRefresh(ctx, projectID, viewerUserID); err != nil {
			slog.WarnContext(ctx, "enqueue private inventory refresh failed", "key", key, "error", err)
		}
	}
	return cached, nil
}

func (uc *UseCase) RefreshPrivateInventory(ctx context.Context, projectID, viewerUserID uint) error {
	if projectID == 0 || viewerUserID == 0 {
		return domain.ErrInvalidAllocationRequest
	}
	if uc.privateInventoryCache == nil {
		return fmt.Errorf("private inventory cache is unavailable")
	}
	return uc.privateInventoryCache.Refresh(ctx, privateInventoryKey(projectID, viewerUserID), func(ctx context.Context) (PrivateInventoryTotals, error) {
		return uc.loadPrivateInventoryTotals(ctx, projectID, viewerUserID)
	})
}

func privateInventoryKey(projectID, viewerUserID uint) string {
	return fmt.Sprintf("alloc:private-inventory:v1:%d:%d", projectID, viewerUserID)
}

func (uc *UseCase) loadPrivateInventoryTotals(ctx context.Context, projectID, viewerUserID uint) (totals PrivateInventoryTotals, err error) {
	totals.Microsoft, err = uc.repo.ListPrivateMicrosoftInventoryTotals(ctx, projectID, viewerUserID)
	if err != nil {
		return totals, err
	}
	totals.Domains, err = uc.repo.ListPrivateDomainInventoryTotals(ctx, projectID, viewerUserID)
	if err != nil {
		return totals, err
	}
	totals.Gmail, err = uc.repo.ListPrivateGmailInventoryTotals(ctx, projectID, viewerUserID)
	if err != nil {
		return totals, err
	}
	totals.ICloud, err = uc.repo.ListPrivateICloudInventoryTotals(ctx, projectID, viewerUserID)
	if err != nil {
		return totals, err
	}
	if protoRepo, ok := uc.repo.(ProtoInventoryRepository); ok {
		totals.Proto, err = protoRepo.ListPrivateProtoInventoryTotals(ctx, projectID, viewerUserID)
	}
	return totals, err
}
