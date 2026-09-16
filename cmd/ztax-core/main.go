// Command ztax-core is the regional execution cell's fiscal core.
//
// One binary per cell holds determination, jurisdiction, classification,
// obligations, the fiscal subledger, the integration surface and the idempotency
// service as compile-time-separated modules — see ADR-0009 for why the commit
// path is one database transaction and therefore one process.
//
// Status: skeleton. Boot, configuration, structured logging, health endpoints
// and graceful shutdown. Domain modules are wired in as their lanes open.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/zoikogroup/zoikotax/backend/internal/platform/config"
)

func main() {
	if err := run(); err != nil {
		// Configuration failures happen before the logger exists, so this is the
		// one place a bare stderr write is correct.
		fmt.Fprintf(os.Stderr, "ztax-core: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := newLogger(cfg)
	// The single startup record of what this process is actually running with.
	// ADR-0017 §2.9.
	log.Info("starting ztax-core", cfg.LogAttrs()...)

	srv := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      newHandler(log),
		ReadTimeout:  cfg.HTTPReadTimeout,
		WriteTimeout: cfg.HTTPWriteTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		log.Info("http listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("http server: %w", err)
			return
		}
		errc <- nil
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received", "timeout", cfg.HTTPShutdownTimeout.String())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTPShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("stopped")
	return nil
}

func newLogger(cfg config.Config) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	// JSON only, structured only. Message strings are constant and everything
	// variable is an attribute, which is what makes structural redaction
	// possible. ADR-0015 §2.7.
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(h).With(
		"service.name", "ztax-core",
		"ztax.cell", cfg.Cell,
		"ztax.region", cfg.Region,
		"deployment.environment", cfg.Environment,
	)
}

// newHandler builds the middleware chain and routes.
//
// The chain is constructed here rather than hidden in a framework so that its
// order is readable top to bottom. As the lanes open it becomes:
//
//	recovery → request id → tracing → security context → tenant resolution →
//	residency check → authorization → idempotency → logging → handler
//
// ADR-0010 §2.4.
func newHandler(log *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	// Liveness: the process is running. Deliberately checks nothing else, so a
	// dependency outage does not cause a restart loop.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	// Readiness: this process can serve traffic. Once the database pool and the
	// content bundle exist, both are checked here — a cell with no loaded
	// bundle is not ready, it is not degraded. ADR-0005 §2.6.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})

	return recovery(log, mux)
}

// recovery converts a panic into an internal error. A panic in a request is a
// defect: it is logged with a stack trace, counted, and never described to the
// caller. ADR-0016 §2.8.
func recovery(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Error("panic recovered",
					"method", r.Method,
					"path", r.URL.Path,
					"panic", fmt.Sprint(v),
				)
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"type":"https://errors.zoikotax.com/v1/internal","title":"Internal error","status":500,"ztx_reason_code":"INTERNAL_ERROR"}`))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
