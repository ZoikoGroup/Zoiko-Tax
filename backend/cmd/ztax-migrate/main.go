// Command ztax-migrate applies the cell's schema.
//
// It is a separate deployable because it runs under a DDL role the application
// never holds (ADR-0008 §2.8, ADR-0009 §2.2). That separation is the point: a
// compromise of ztax-core yields no ability to alter the schema, and a
// migration cannot accidentally run inside a request.
//
// It is forward-only in a cell. `down` exists for local development and refuses
// to run unless ZTAX_ENVIRONMENT is development (ADR-0008 §2.5) — rollback of a
// schema change against fiscal data is a new forward migration, not a reversal.
//
//	ztax-migrate up        apply everything outstanding
//	ztax-migrate status    report current version without changing anything
//	ztax-migrate down 1    roll back one step, development only
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/postgres"
	"github.com/zoikogroup/zoikotax/backend/migrations"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})).
		With("service.name", "ztax-migrate")

	if err := run(log, os.Args[1:]); err != nil {
		log.Error("migration failed", "error", err.Error())
		os.Exit(1)
	}
}

func run(log *slog.Logger, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: ztax-migrate up|down N|status")
	}

	// The DDL credential is separate from the application's (ADR-0008 §2.11)
	// and is named by its own variable, so a misconfiguration that points this
	// at the application role fails on a permission error rather than running
	// migrations as the wrong principal.
	dsn := os.Getenv("ZTAX_MIGRATE_DATABASE_URL")
	if dsn == "" {
		return errors.New("ZTAX_MIGRATE_DATABASE_URL is required")
	}
	env := os.Getenv("ZTAX_ENVIRONMENT")
	if env == "" {
		return errors.New("ZTAX_ENVIRONMENT is required")
	}

	// Bootstrap. The migrator keeps its own bookkeeping table, and it keeps it
	// in search_path's first schema — which is ztax, which migration 000001
	// creates. That is a genuine ordering problem rather than a configuration
	// mistake: the tool needs somewhere to record that it has run before it has
	// run anything.
	//
	// Creating the schema here rather than pointing the bookkeeping at public
	// keeps ADR-0008 §2.9 intact: public stays unused and revoked, and a cell's
	// migration state stays inside the cell's own schema. The statement is
	// idempotent and migration 000001 still carries it, so the reviewable
	// record of the schema's existence is still a migration.
	if err := ensureSchema(dsn); err != nil {
		return err
	}

	m, closeFn, err := open(dsn)
	if err != nil {
		return err
	}
	defer closeFn()

	switch args[0] {
	case "up":
		log.Info("applying migrations")
		if err := m.Up(); err != nil {
			if errors.Is(err, migrate.ErrNoChange) {
				log.Info("schema already current")
				return reportVersion(log, m)
			}
			return fmt.Errorf("up: %w", err)
		}
		return reportVersion(log, m)

	case "status":
		return reportVersion(log, m)

	case "down":
		if env != "development" {
			// A cell is forward-only. Refusing here rather than in a runbook
			// means the control cannot be forgotten under incident pressure,
			// which is exactly when somebody would reach for it.
			return fmt.Errorf("down is refused in environment %q: cells are forward-only (ADR-0008 §2.5)", env)
		}
		if len(args) < 2 {
			return errors.New("usage: ztax-migrate down N")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n <= 0 {
			return fmt.Errorf("down takes a positive step count, got %q", args[1])
		}
		log.Warn("rolling back", "steps", n, "environment", env)
		if err := m.Steps(-n); err != nil {
			return fmt.Errorf("down: %w", err)
		}
		return reportVersion(log, m)

	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// ensureSchema creates the cell's schema if it is not there yet.
//
// It connects with pgx rather than reusing the migrator's connection because
// the migrator refuses to open at all when search_path names nothing that
// exists — which is precisely the state this fixes.
func ensureSchema(dsn string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("bootstrap: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// Not parameterised because an identifier cannot be a bind parameter. The
	// value is a compile-time constant in this repository, not caller input.
	if _, err := conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+postgres.Schema); err != nil {
		return fmt.Errorf("bootstrap: create schema %s: %w", postgres.Schema, err)
	}
	return nil
}

func open(dsn string) (*migrate.Migrate, func(), error) {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	// The postgres database driver registers itself on import; the DSN's scheme
	// is what selects it. The bookkeeping table it maintains is placed by the
	// DSN's x-migrations-table and search_path parameters, so a cell's migration
	// state sits inside the cell's own schema like everything else
	// (ADR-0009 §2.6).
	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open migrator: %w", err)
	}
	return m, func() {
		srcErr, dbErr := m.Close()
		_ = srcErr
		_ = dbErr
	}, nil
}

func reportVersion(log *slog.Logger, m *migrate.Migrate) error {
	v, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		log.Info("no migrations applied")
		return nil
	}
	if err != nil {
		return fmt.Errorf("version: %w", err)
	}
	// A dirty version means a migration failed part-way. It is reported as an
	// error rather than logged and shrugged off: the schema is in a state no
	// release describes, and starting an application against it is worse than
	// not starting.
	if dirty {
		return fmt.Errorf("schema is dirty at version %d; a migration failed part-way and needs manual resolution", v)
	}
	log.Info("schema current", "version", v)
	return nil
}
