// Command ztax-outbox-relay publishes committed events to the cell's broker.
//
// It is a separate deployable because it has a different lifecycle and a
// different failure mode from request handling, and must not compete with it
// (ADR-0009 §2.2). A relay that fell behind while sharing a process with the
// API would be a relay whose backlog was caused by request load and whose
// recovery was blocked by it.
//
// The loop is deliberately dull: claim a batch with FOR UPDATE SKIP LOCKED,
// publish each, mark each, commit. Several instances run concurrently with no
// leader election and no lease, because SKIP LOCKED means a second instance
// walks past whatever the first is holding.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/postgres"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/outbox"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/config"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/secrets"
)

const (
	batchSize    = 100
	pollInterval = 1 * time.Second
	// LagAlertThreshold is the SLI boundary from ADR-0014 §2.11. Beyond it,
	// committed state is not yet visible externally — which is a
	// correctness-adjacent condition, not a performance one, and is alerted as
	// such rather than graphed and ignored.
	lagAlertThreshold = 30 * time.Second
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ztax-outbox-relay: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})).With(
		"service.name", "ztax-outbox-relay",
		"ztax.cell", cfg.Cell,
		"ztax.region", cfg.Region,
		"deployment.environment", cfg.Environment,
	)
	log.Info("starting ztax-outbox-relay", cfg.LogAttrs()...)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dsn, err := secrets.Resolve(secrets.EnvResolver{Environment: cfg.Environment}, cfg.DatabaseURLRef)
	if err != nil {
		return err
	}

	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// A small pool: this process does one thing at a time on purpose. Claiming
	// several batches concurrently would multiply the lock footprint for no
	// throughput gain, because publishing is the slow half.
	poolCfg := postgres.DefaultConfig(dsn)
	poolCfg.MaxConns = 4
	pool, err := postgres.Open(startCtx, poolCfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := pool.Ping(startCtx); err != nil {
		return fmt.Errorf("database unreachable at startup: %w", err)
	}

	store := postgres.NewStore(pool)
	trains := map[string]string{
		"app": cfg.TrainApp, "content": cfg.TrainContent, "ai": cfg.TrainAI,
		"adapter": cfg.TrainAdapter, "infra": cfg.TrainInfra,
		"schema": cfg.TrainSchema, "migration": cfg.TrainMigration,
	}

	// The broker is not wired yet: ADR-0014 §2.4 puts a Kafka cluster per cell,
	// and that is W1 lane B's infrastructure work. Until it exists the relay
	// runs with a publisher that logs the envelope, so the loop, the claim
	// semantics and the lag SLI are exercised for real and only the last hop is
	// stubbed. It is deliberately loud about being a stub rather than quietly
	// marking rows published against a broker that is not there.
	publisher := &logPublisher{log: log}
	log.Warn("no broker configured; envelopes are logged, not published (ADR-0014 §2.4)")

	relay := &relay{store: store, publisher: publisher, log: log,
		cell: cfg.Cell, region: cfg.Region, trains: trains}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("stopped")
			return nil
		case <-ticker.C:
			if err := relay.drain(ctx); err != nil && !errors.Is(err, context.Canceled) {
				// A failed pass is not fatal. The rows stay unpublished, the
				// next pass retries them, and the lag SLI is what escalates if
				// the failure persists.
				log.Warn("relay pass failed", "error", err.Error())
			}
		}
	}
}

type relay struct {
	store     *postgres.Store
	publisher outbox.Publisher
	log       *slog.Logger

	cell, region string
	trains       map[string]string
}

// drain publishes one batch.
//
// Claim, publish and mark all happen inside one transaction. That coupling is
// the point: the rows are locked for the transaction's life, so a crash
// mid-batch rolls back the marks and leaves the rows claimable by the next
// pass. The cost is that a slow publish holds a lock; the alternative — claim,
// commit, publish — has a window where a crash loses the claim and the event is
// never retried, which is worse than a held lock.
func (r *relay) drain(ctx context.Context) error {
	tx, txCtx, err := r.store.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	events, err := r.store.Outbox().Claim(txCtx, batchSize)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return r.reportLag(ctx)
	}

	published := 0
	for _, e := range events {
		envelope := e.CloudEvent(r.cell, r.region, r.trains)
		if err := r.publisher.Publish(envelope, e.AggregateKey); err != nil {
			// Record and move on. One undeliverable event must not block the
			// rest of the batch, and the row stays unpublished so it is retried.
			if err := r.store.Outbox().RecordFailure(txCtx, e.ID, err.Error()); err != nil {
				return err
			}
			r.log.Warn("publish failed", "event.id", e.ID.String(), "event.type", e.Type, "error", err.Error())
			continue
		}
		if err := r.store.Outbox().MarkPublished(txCtx, e.ID, time.Now().UTC()); err != nil {
			return err
		}
		published++
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	r.log.Info("relay pass", "claimed", len(events), "published", published)
	return r.reportLag(ctx)
}

// reportLag emits the SLI. It runs outside the batch transaction so a long
// batch does not delay the measurement.
func (r *relay) reportLag(ctx context.Context) error {
	lag, err := r.store.Outbox().Lag(ctx)
	if err != nil {
		return err
	}
	if lag > lagAlertThreshold {
		r.log.Error("outbox relay lag exceeds threshold; committed state is not yet visible externally",
			"lag_seconds", lag.Seconds(), "threshold_seconds", lagAlertThreshold.Seconds())
	}
	return nil
}

// logPublisher stands in for the broker until one exists.
type logPublisher struct{ log *slog.Logger }

// Publish writes the envelope to the log in canonical form.
func (p *logPublisher) Publish(envelope canonical.Value, partitionKey string) error {
	body, err := canonical.Encode(envelope)
	if err != nil {
		return err
	}
	p.log.Info("event", "partition_key", partitionKey, "envelope", string(body))
	return nil
}
