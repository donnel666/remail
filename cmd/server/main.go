package main

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/donnel666/remail/api"
	"github.com/donnel666/remail/internal/platform"
)

//go:embed webdist
var frontendFS embed.FS

func main() {
	// Load configuration
	cfg, err := platform.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Set up logging
	platform.SetupLogger(cfg.Log)
	slog.Info("starting remail server", "addr", cfg.Server.Addr)

	// Initialize platform clients
	ctx := context.Background()
	p, cleanup, err := platform.New(ctx, cfg)
	if err != nil {
		slog.Error("failed to initialize platform", "error", err)
		os.Exit(1)
	}
	defer cleanup()

	runtimeDiagnosticsCtx, stopRuntimeDiagnostics := context.WithCancel(context.Background())
	defer stopRuntimeDiagnostics()
	pprofSrv := startPprofServer(p.Diagnostics)
	startPprofCPUMonitor(runtimeDiagnosticsCtx, p.Diagnostics)

	// Resolve migrations directory:
	//   1. cfg.Migrations.Dir (MIGRATIONS_DIR, Docker sets this)
	//   2. <exe-dir>/migrations (flat deploy layout)
	//   3. ./migrations (local dev CWD)
	migrationsDir := cfg.Migrations.Dir
	if migrationsDir == "" {
		migrationsDir = filepath.Join(execDir(), "migrations")
		if _, err := os.Stat(migrationsDir); err != nil {
			migrationsDir = "migrations"
		}
	}
	if err := platform.RunMigrations(p.SQLDB, migrationsDir); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}
	resetCtx, resetCancel := context.WithTimeout(ctx, 5*time.Second)
	if _, err := p.SQLDB.ExecContext(resetCtx, "UPDATE api_keys SET active_requests = 0 WHERE active_requests > 0"); err != nil {
		resetCancel()
		slog.Error("failed to reset api key active requests", "error", err)
		os.Exit(1)
	}
	resetCancel()

	// Get embedded frontend filesystem (subdirectory)
	var feFS fs.FS
	if sub, err := fs.Sub(frontendFS, "webdist"); err == nil {
		// Check if the embedded FS has actual content (not just placeholder)
		if _, err := fs.Stat(sub, "index.html"); err == nil {
			feFS = sub
			slog.Info("serving embedded frontend")
		} else {
			slog.Info("no embedded frontend found (use Rsbuild dev server)")
		}
	}

	// Set up Gin router
	router, routerCleanup, err := api.SetupRouter(p, feFS)
	if err != nil {
		slog.Error("router setup failed", "error", err)
		os.Exit(1)
	}
	defer routerCleanup(context.Background())

	// Create HTTP server
	srv := &http.Server{
		Addr:         cfg.Server.Addr,
		Handler:      router,
		ReadTimeout:  cfg.Server.Timeout,
		WriteTimeout: cfg.Server.Timeout,
		IdleTimeout:  120 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	// Wait for interrupt signal for graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-quit:
		slog.Info("shutting down server", "signal", sig.String())
	case err := <-serverErr:
		slog.Error("server error", "error", err)
	}

	// Give outstanding requests up to 30 seconds to complete
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server forced to shutdown", "error", err)
	}
	routerCleanup(shutdownCtx)
	stopRuntimeDiagnostics()
	shutdownPprofServer(shutdownCtx, pprofSrv)

	slog.Info("server exited")
}

// execDir returns the directory of the running executable.
func execDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}
