package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"
	runtimepprof "runtime/pprof"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/shirou/gopsutil/v4/cpu"
)

func startPprofServer(cfg platform.DiagnosticsConfig) *http.Server {
	if cfg.PprofAddr == "" {
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{
		Addr:              cfg.PprofAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		slog.Info("pprof server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("pprof server error", "error", err)
		}
	}()

	return srv
}

func startPprofCPUMonitor(ctx context.Context, cfg platform.DiagnosticsConfig) {
	if cfg.PprofAddr == "" || cfg.PprofCPUProfileDir == "" || cfg.PprofCPUThreshold <= 0 {
		return
	}

	interval := cfg.PprofCPUCheckInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	duration := cfg.PprofCPUProfileDuration
	if duration <= 0 {
		duration = 10 * time.Second
	}

	go func() {
		timer := time.NewTimer(0)
		defer timer.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				checkAndCaptureCPUProfile(ctx, cfg.PprofCPUThreshold, cfg.PprofCPUProfileDir, duration)
				timer.Reset(interval)
			}
		}
	}()
}

func checkAndCaptureCPUProfile(ctx context.Context, threshold float64, profileDir string, duration time.Duration) {
	percent, err := cpu.Percent(time.Second, false)
	if err != nil {
		slog.Warn("pprof cpu monitor failed to read cpu usage", "error", err)
		return
	}
	if len(percent) == 0 || percent[0] < threshold {
		return
	}

	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		slog.Warn("pprof cpu monitor failed to create profile dir", "dir", profileDir, "error", err)
		return
	}

	filename := filepath.Join(profileDir, "cpu-"+time.Now().Format("20060102150405")+".pprof")
	file, err := os.Create(filename)
	if err != nil {
		slog.Warn("pprof cpu monitor failed to create profile file", "file", filename, "error", err)
		return
	}
	defer file.Close()

	slog.Warn(
		"cpu usage threshold exceeded; capturing pprof profile",
		"cpu_percent", percent[0],
		"threshold_percent", threshold,
		"file", filename,
		"duration", duration.String(),
	)
	if err := runtimepprof.StartCPUProfile(file); err != nil {
		slog.Warn("pprof cpu monitor failed to start profile", "file", filename, "error", err)
		return
	}

	select {
	case <-ctx.Done():
	case <-time.After(duration):
	}
	runtimepprof.StopCPUProfile()
	slog.Info("cpu pprof profile captured", "file", filename)
}

func shutdownPprofServer(ctx context.Context, srv *http.Server) {
	if srv == nil {
		return
	}
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("pprof server forced to shutdown", "error", err)
	}
}
