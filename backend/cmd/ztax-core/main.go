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

	"go.opentelemetry.io/otel"

	adaptercontent "github.com/zoikogroup/zoikotax/backend/internal/adapter/content"
	"github.com/zoikogroup/zoikotax/backend/internal/adapter/postgres"
	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/config"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/kms"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/secrets"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/telemetry"
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

	// Content is activated before the listener opens. ADR-0005 §2.6 makes bundle
	// load warm, verified and atomic, and doing it here means the first request
	// to reach this cell finds either a verified bundle or none — never a bundle
	// mid-verification.
	content := &rule.Holder{}
	if err := activateContent(startCtx, cfg, content, log, clk); err != nil {
		return err
	}

	tracing, err := setupTracing(startCtx, cfg, content, log)
	if err != nil {
		return err
	}
	defer func() {
		// Flush what the batcher holds, bounded: a collector that has gone
		// away must not hold the process open past its shutdown budget.
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tracing.Shutdown(flushCtx); err != nil {
			log.Warn("trace flush failed", "error", err.Error())
		}
	}()

	router := ztaxhttp.NewRouter(auth, admin, store.Users(), readiness{store: store}, log, ids)
	router.Tracer = tracing.Provider
	router.Content = content
	router.SecureCookies = cfg.SecureCookies
	router.TrustProxy = cfg.TrustProxy
	router.Cell, router.Region, router.Environment = cfg.Cell, cfg.Region, cfg.Environment
	router.Authoritative = cfg.Authoritative
	router.Trains = ztaxhttp.Trains{
		App: cfg.TrainApp, Content: cfg.TrainContent, Ai: cfg.TrainAI,
		Adapter: cfg.TrainAdapter, Infra: cfg.TrainInfra,
		Schema: cfg.TrainSchema, Migration: cfg.TrainMigration,
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
// them and no call site has to remember. Every record passes through the
// redaction backstop (ADR-0015 §2.2) before it is written.
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
	return slog.New(telemetry.NewLogHandler(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))).With(
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
		// Classified, so the log carries its redacted form: a bootstrap line is
		// retained as operational telemetry, not under the user record's own
		// retention (ADR-0015 §2.2).
		"admin_email", privacy.Email(user.Email),
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

// activateContent loads and publishes this cell's rule bundle.
//
// A cell with no content configured starts anyway. That is not leniency: before
// A4 no authoritative fiscal output is permitted at all, and a cell serving the
// administrative surface with no pack loaded is a legitimate deployment. What it
// must never do is serve determination as though it had content, and it does not
// — rule.Evaluate refuses a nil bundle with NO_CONTENT_BUNDLE, which is
// CategoryUnavailable and therefore safely retryable once content arrives.
//
// Everything else is fatal. A configured content directory that cannot be read,
// a keyring that does not parse, a seal that does not verify: each of those is a
// cell that would run without the content it was deployed to run, and starting
// is worse than not starting.
func activateContent(ctx context.Context, cfg config.Config, holder *rule.Holder, log *slog.Logger, clk clock.Clock) error {
	if cfg.ContentDir == "" {
		log.Warn("no content bundle configured; determination will refuse with " + string(errs.ReasonNoContentBundle))
		return nil
	}

	// #nosec G304 -- a path from this process's own configuration.
	keyringBytes, err := os.ReadFile(cfg.ContentKeyring)
	if err != nil {
		return fmt.Errorf("content keyring: %w", err)
	}
	keyring, err := kms.ParseKeyring(keyringBytes)
	if err != nil {
		return err
	}

	loader := &adaptercontent.Loader{
		Dir:      cfg.ContentDir,
		Verifier: keyring,
		Clock:    clk,
		Cell:     cfg.Cell,
	}
	loaded, err := loader.Activate(ctx, holder)
	if err != nil {
		return fmt.Errorf("content activation: %w", err)
	}

	// These seven values are what a decision names when it says which
	// combination produced it, so they are logged once at startup in the same
	// shape the evidence manifest records them (ADR-0011 §2.8).
	log.Info("content activated",
		"bundle.id", loaded.Seal.BundleID,
		"bundle.digest", loaded.Digest,
		"ir.version", loaded.Seal.IRVersion,
		"canon.profile", loaded.Seal.CanonProfile,
		"content.version", loaded.Seal.ContentVersion,
		"key.id", loaded.KeyID,
		"nodes", loaded.Bundle.NodeCount(),
		"keyring.keys", keyring.KeyIDs())
	return nil
}

// setupTracing builds the tracer provider (ADR-0015 §2.1). It runs after
// content activation so the resource can name the bundle this process started
// with; a later activation is visible in /v1/capabilities, and a restart
// refreshes the resource.
func setupTracing(ctx context.Context, cfg config.Config, content *rule.Holder, log *slog.Logger) (telemetry.Tracing, error) {
	res := telemetry.Resource{
		ServiceName: "ztax-core", Environment: cfg.Environment, Cell: cfg.Cell, Region: cfg.Region,
		TrainApp: cfg.TrainApp, TrainContent: cfg.TrainContent, TrainAI: cfg.TrainAI,
		TrainAdapter: cfg.TrainAdapter, TrainInfra: cfg.TrainInfra, TrainSchema: cfg.TrainSchema,
		TrainMigration: cfg.TrainMigration, CanonProfile: canonical.ProfileVersion,
	}
	if b := content.Current(); b != nil {
		res.BundleDigest, res.IRVersion = b.Digest(), b.IRVersion()
	}
	// The exporter's own failures — a collector that is down, a batch that
	// was dropped — go to the structured log rather than to stderr through the
	// SDK's default handler, so they are redacted and correlated like
	// everything else.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		log.Warn("telemetry export failed", "error", err.Error())
	}))
	tracing, err := telemetry.SetupTracing(ctx, telemetry.TracingConfig{
		Endpoint: cfg.OTLPEndpoint, Insecure: cfg.OTLPInsecure, SampleRatio: cfg.TraceSampleRatio,
	}, res)
	if err != nil {
		return telemetry.Tracing{}, err
	}
	if cfg.OTLPEndpoint == "" {
		log.Info("tracing disabled; no collector configured")
	} else {
		log.Info("tracing to collector", "otlp.endpoint", cfg.OTLPEndpoint, "trace.sample_ratio", cfg.TraceSampleRatio)
	}
	return tracing, nil
}
