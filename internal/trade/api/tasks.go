package api

import (
	"context"
	"log/slog"
	"time"
)

const orderLifecycleScannerInterval = 30 * time.Second

func StartLifecycleScanner(module *Module) func(context.Context) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(orderLifecycleScannerInterval)
		defer ticker.Stop()
		runOrderLifecycleScan(ctx, module)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runOrderLifecycleScan(ctx, module)
			}
		}
	}()
	return func(shutdownCtx context.Context) {
		cancel()
		select {
		case <-done:
		case <-shutdownCtx.Done():
		}
	}
}

func runOrderLifecycleScan(ctx context.Context, module *Module) {
	if module == nil || module.UseCase == nil {
		return
	}
	result, err := module.UseCase.ExpireDueOrders(ctx, 0)
	if err != nil {
		slog.Warn("trade lifecycle scanner failed", "error", err)
		return
	}
	if result == nil {
		return
	}
	if result.CodeTimedOut == 0 &&
		result.PurchaseActivationCompleted == 0 &&
		result.PurchaseWarrantyCompleted == 0 &&
		result.CodeCleaned == 0 &&
		result.Failed == 0 {
		return
	}
	slog.Info(
		"trade lifecycle scanner finished",
		"code_timed_out", result.CodeTimedOut,
		"purchase_activation_completed", result.PurchaseActivationCompleted,
		"purchase_warranty_completed", result.PurchaseWarrantyCompleted,
		"code_cleaned", result.CodeCleaned,
		"failed", result.Failed,
	)
}
