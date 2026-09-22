// Command ztax-core is the regional execution cell's fiscal core.
//
// One binary per cell holds determination, jurisdiction, classification,
// obligations, the fiscal subledger, the integration surface and the idempotency
// service as compile-time-separated modules — see ADR-0009 for why the commit
// path is one database transaction and therefore one process.
//
// This file is the only place that knows about every layer at once. ADR-0007
// §2.5 keeps the layers apart by import lint; wiring them together is a
// composition act and it belongs in main, where it is readable top to bottom.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	nethttp "net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/postgres"
	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/config"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/secrets"
	ztaxhttp "github.com/zoikogroup/zoikotax/backend/internal/transport/http"
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The credential is resolved from its reference once, here, and held in
	// memory. It is never logged and never placed back in the environment
	// (ADR-0017 §2.4).
	dsn, err := secrets.Resolve(secrets.EnvResolver{Environment: cfg.Environment}, cfg.DatabaseURLRef)
	if err != nil {
		return err
	}

	startCtx, cancelStart := context.WithTimeout(ctx, 30*time.Second)
	defer cancelStart()

	pool, err := postgres.Open(startCtx, postgres.DefaultConfig(dsn))
	if err != nil {
		return err
	}
	defer pool.Close()

	// Ping at startup rather than discovering the database on the first
	// request. A cell that cannot reach its store should fail its readiness
	// probe immediately, not serve one 503 per caller until somebody notices.
	if err := pool.Ping(startCtx); err != nil {
		return fmt.Errorf("database unreachable at startup: %w", err)
	}

	store := postgres.NewStore(pool)
	clk := clock.System{}
	ids := idgen.V7{}

	auth := app.NewAuthService(store.Tenants(), store.Users(), store.Sessions(), store.Audit(), store, clk, ids)
	admin := app.NewAdminService(store.Tenants(), store.Users(), store.Sessions(), store.Audit(), store, clk, ids, cfg.Region)

	if err := bootstrap(startCtx, cfg, admin, log); err != nil {
		return err
	}

	router := ztaxhttp.NewRouter(auth, admin, store.Users(), readiness{store: store}, log, ids)
	router.SecureCookies = cfg.SecureCookies
	router.TrustProxy = cfg.TrustProxy
	router.Cell, router.Region, router.Environment = cfg.Cell, cfg.Region, cfg.Environment
	router.Authoritative = cfg.Authoritative
	router.Trains = map[string]string{
		"app": cfg.TrainApp, "content": cfg.TrainContent, "ai": cfg.TrainAI,
		"adapter": cfg.TrainAdapter, "infra": cfg.TrainInfra,
		"schema": cfg.TrainSchema, "migration": cfg.TrainMigration,
	}

	srv := &nethttp.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      router.Handler(),
		ReadTimeout:  cfg.HTTPReadTimeout,
		WriteTimeout: cfg.HTTPWriteTimeout,
	}

	// Expired sessions are swept in-process rather than by a separate
	// deployable: it is one DELETE on an indexed column, with none of the
	// lifecycle or failure-mode differences that ADR-0009 §2.7 makes the test
	// for extracting something.
	sweepDone := startSessionSweep(ctx, auth, log)

	errc := make(chan error, 1)
	go func() {
		log.Info("http listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, nethttp.ErrServerClosed) {
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
	<-sweepDone
	log.Info("stopped")
	return nil
}

// readiness answers /readyz by checking the one dependency a replica cannot
// serve without. Liveness deliberately does not (see Router.handleHealthz).
type readiness struct{ store *postgres.Store }

// Ready reports whether this replica can take traffic.
func (r readiness) Ready(ctx context.Context) error {
	// A short deadline of its own: a readiness probe that blocks until the
	// caller's timeout tells the orchestrator nothing in time to be useful.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return r.store.Pool().Ping(ctx)
}

// newLogger builds the process logger.
//
// JSON to stdout, structured only (ADR-0015 §2.7). The cell, region and
// environment are bound once as resource attributes so that every line carries
// them and no call site has to remember.
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
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})).With(
		"service.name", "ztax-core",
		"ztax.cell", cfg.Cell,
		"ztax.region", cfg.Region,
		"deployment.environment", cfg.Environment,
	)
}

// bootstrap provisions the first tenant and administrator.
//
// A cell with no tenants cannot be administered, because every administrative
// endpoint requires an administrator — so the first one cannot be created
// through the API without an unauthenticated endpoint that creates tenants,
// which is an unauthenticated endpoint that creates residency obligations.
// Doing it at startup from configuration keeps that endpoint from existing.
//
// It is a no-op once the tenant exists, so restarting is safe and the
// configuration can be left in place.
func bootstrap(ctx context.Context, cfg config.Config, admin *app.AdminService, log *slog.Logger) error {
	if cfg.BootstrapTenant == "" {
		return nil
	}
	if cfg.BootstrapAdminPasswordRef == "" {
		return fmt.Errorf("ZTAX_BOOTSTRAP_TENANT is set but ZTAX_BOOTSTRAP_ADMIN_PASSWORD_REF is not")
	}
	password, err := secrets.Resolve(secrets.EnvResolver{Environment: cfg.Environment}, cfg.BootstrapAdminPasswordRef)
	if err != nil {
		return err
	}

	tenant, user, err := admin.ProvisionTenant(ctx, app.ProvisionTenantInput{
		Slug:                  cfg.BootstrapTenant,
		DisplayName:           orElse(cfg.BootstrapTenantName, cfg.BootstrapTenant),
		AdminEmail:            cfg.BootstrapAdminEmail,
		AdminName:             orElse(cfg.BootstrapAdminName, cfg.BootstrapAdminEmail),
		AdminPasswordProvider: app.StaticPassword(password),
	})
	if err != nil {
		// Already provisioned is the steady state after the first start, not a
		// failure. Anything else is.
		if errs.IsCategory(err, errs.CategoryConflict) {
			log.Info("bootstrap tenant already present", "tenant", cfg.BootstrapTenant)
			return nil
		}
		return fmt.Errorf("bootstrap tenant %q: %w", cfg.BootstrapTenant, err)
	}
	// The password is not logged. The identifiers are, so an operator can find
	// what was created.
	log.Info("bootstrap tenant provisioned",
		"tenant", tenant.Slug,
		"tenant_id", tenant.ID.String(),
		"admin_user_id", user.ID.String(),
		"admin_email", user.Email,
	)
	return nil
}

// startSessionSweep removes sessions past their absolute limit, on a timer.
func startSessionSweep(ctx context.Context, auth *app.AuthService, log *slog.Logger) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Derived from ctx, not Background: a shutdown cancels an
				// in-flight sweep rather than leaving it running against a pool
				// that is about to close.
				sweepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				n, err := auth.SweepExpiredSessions(sweepCtx)
				cancel()
				if err != nil {
					// A failed sweep is not worth stopping the process for:
					// expired sessions are already refused at authentication,
					// so this is housekeeping rather than a control.
					log.Warn("session sweep failed", "error", err.Error())
					continue
				}
				if n > 0 {
					log.Info("swept expired sessions", "count", n)
				}
			}
		}
	}()
	return done
}

func orElse(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
