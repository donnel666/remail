package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"time"
)

func startPprofServer(addr string) *http.Server {
	if addr == "" {
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		slog.Info("pprof server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("pprof server error", "error", err)
			os.Exit(1)
		}
	}()

	return srv
}

func shutdownPprofServer(ctx context.Context, srv *http.Server) {
	if srv == nil {
		return
	}
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("pprof server forced to shutdown", "error", err)
	}
}
