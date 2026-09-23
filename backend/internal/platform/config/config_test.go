package config_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/platform/config"
)

// required is the minimum a cell needs to start. Each test sets these and then
// varies one thing, so a failure names the thing that was varied.
var required = map[string]string{
	"ZTAX_CELL":             "local-dev",
	"ZTAX_REGION":           "local",
	"ZTAX_ENVIRONMENT":      "development",
	"ZTAX_TRAIN_APP":        "0.0.0-dev",
	"ZTAX_TRAIN_CONTENT":    "0.0.0-dev",
	"ZTAX_TRAIN_AI":         "0.0.0-dev",
	"ZTAX_TRAIN_ADAPTER":    "0.0.0-dev",
	"ZTAX_TRAIN_INFRA":      "0.0.0-dev",
	"ZTAX_TRAIN_SCHEMA":     "0.0.0-dev",
	"ZTAX_TRAIN_MIGRATION":  "0.0.0-dev",
	"ZTAX_DATABASE_URL_REF": "local://postgres",
	"ZTAX_SECURE_COOKIES":   "false",
}

func withEnv(t *testing.T, overrides map[string]string) {
	t.Helper()
	for name, value := range required {
		t.Setenv(name, value)
	}
	for name, value := range overrides {
		t.Setenv(name, value)
	}
}

// TestContentIsConfiguredInPairs is the fail-closed rule in ADR-0005 §2.6: a
// cell never loads content it cannot verify.
//
// The half-configured cases are the point. A directory with no keyring would
// either run unverified content or refuse every bundle, and a keyring with no
// directory verifies nothing — both are worse than not starting, and both are
// the shape a partial rollout takes.
func TestContentIsConfiguredInPairs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dir     string
		keyring string
		wantErr bool
	}{
		{name: "neither, which is a cell with no content", wantErr: false},
		{name: "both", dir: "/srv/content", keyring: "/srv/keyring.json", wantErr: false},
		{name: "a directory with no keyring", dir: "/srv/content", wantErr: true},
		{name: "a keyring with no directory", keyring: "/srv/keyring.json", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withEnv(t, map[string]string{
				"ZTAX_CONTENT_DIR":     tc.dir,
				"ZTAX_CONTENT_KEYRING": tc.keyring,
			})

			cfg, err := config.Load()
			if tc.wantErr {
				if err == nil {
					t.Fatal("started; a cell must not be half-configured for content")
				}
				if !errors.Is(err, config.ErrInvalid) {
					t.Errorf("error %v is not ErrInvalid", err)
				}
				if !strings.Contains(err.Error(), "ZTAX_CONTENT_DIR") {
					t.Errorf("error %q does not name the variables at fault", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("did not start: %v", err)
			}
			if cfg.ContentDir != tc.dir || cfg.ContentKeyring != tc.keyring {
				t.Errorf("read %q/%q, set %q/%q", cfg.ContentDir, cfg.ContentKeyring, tc.dir, tc.keyring)
			}
		})
	}
}

// TestUnknownVariablesRefuseToStart is ADR-0017 §2.1. It is tested here because
// adding ZTAX_CONTENT_DIR to the known table is exactly the kind of change that
// breaks it — a variable added to Config and forgotten in `known` fails this
// way round, and one added to `known` and forgotten in Config fails silently.
func TestUnknownVariablesRefuseToStart(t *testing.T) {
	withEnv(t, map[string]string{"ZTAX_CONTENT_DIRECTORY": "/srv/content"})

	_, err := config.Load()
	if err == nil {
		t.Fatal("started with an unrecognised ZTAX_ variable; a typo would silently take the default")
	}
	if !strings.Contains(err.Error(), "ZTAX_CONTENT_DIRECTORY") {
		t.Errorf("error %q does not name the variable", err)
	}
}
