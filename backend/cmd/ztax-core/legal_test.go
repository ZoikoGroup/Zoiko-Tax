package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/platform/config"
)

// The development matrix the local stack mounts loads, in development and
// nowhere else: it is a draft (ADR-LEG-001). With none configured the cell
// starts, holding an empty matrix that blocks everything.
func TestTheDevelopmentLegalMatrixLoadsInDevelopmentOnly(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{Environment: "development", LegalMatrix: "../../../content/legal/authorization-matrix.dev.json"}
	m, err := loadLegalMatrix(cfg, log)
	if err != nil || !m.Draft || len(m.Rules) == 0 || m.Digest.IsZero() {
		t.Fatalf("in development: %v %+v", err, m)
	}
	cfg.Environment = "production"
	if _, err := loadLegalMatrix(cfg, log); err == nil {
		t.Fatal("a draft matrix loaded in production")
	}
	cfg.LegalMatrix = ""
	if m, err := loadLegalMatrix(cfg, log); err != nil || m.Version != "" {
		t.Fatalf("with none: %v %+v", err, m)
	}
}
