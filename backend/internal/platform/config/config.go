// Package config loads process configuration from the environment, once, at boot.
//
// It implements ADR-0017. Configuration is environment variables only; there is
// no file, no config service and no runtime reload. A missing required value, an
// unparseable value, or an unrecognised ZTAX_ variable refuses to start — the
// last of those because a typo in a variable name would otherwise silently take
// a default.
//
// Configuration is not content. Nothing that changes a fiscal outcome belongs
// here; that lives in signed content bundles on the CONTENT train.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Prefix scopes every variable this process recognises.
const Prefix = "ZTAX_"

// Config is the immutable effective configuration of the process.
type Config struct {
	// Identity. Fail-closed: a process that cannot name its cell cannot make
	// residency decisions, so it does not start. ADR-0017 §2.10.
	Cell        string
	Region      string
	Environment string

	// Release-train versions, stamped by CI. They travel into telemetry as
	// resource attributes and into evidence manifests. ADR-0015 §2.6.
	TrainApp       string
	TrainContent   string
	TrainAI        string
	TrainAdapter   string
	TrainInfra     string
	TrainSchema    string
	TrainMigration string

	// HTTP surface.
	HTTPAddr            string
	HTTPReadTimeout     time.Duration
	HTTPWriteTimeout    time.Duration
	HTTPShutdownTimeout time.Duration

	// Database. This names where credentials live; it never carries them.
	// ADR-0017 §2.4.
	DatabaseURLRef string

	// Observability.
	OTLPEndpoint string
	LogLevel     string
}

// known lists every variable this process accepts, with whether it is required
// and its default. An environment variable under Prefix that is absent from this
// table is a startup failure.
var known = map[string]struct {
	required bool
	def      string
}{
	"ZTAX_CELL":                  {required: true},
	"ZTAX_REGION":                {required: true},
	"ZTAX_ENVIRONMENT":           {required: true},
	"ZTAX_TRAIN_APP":             {required: true},
	"ZTAX_TRAIN_CONTENT":         {required: true},
	"ZTAX_TRAIN_AI":              {required: true},
	"ZTAX_TRAIN_ADAPTER":         {required: true},
	"ZTAX_TRAIN_INFRA":           {required: true},
	"ZTAX_TRAIN_SCHEMA":          {required: true},
	"ZTAX_TRAIN_MIGRATION":       {required: true},
	"ZTAX_DATABASE_URL_REF":      {required: true},
	"ZTAX_HTTP_ADDR":             {def: ":8080"},
	"ZTAX_HTTP_READ_TIMEOUT":     {def: "10s"},
	"ZTAX_HTTP_WRITE_TIMEOUT":    {def: "30s"},
	"ZTAX_HTTP_SHUTDOWN_TIMEOUT": {def: "30s"},
	"ZTAX_OTLP_ENDPOINT":         {def: "localhost:4317"},
	"ZTAX_LOG_LEVEL":             {def: "info"},
}

// Load reads and validates configuration from the environment. It is called
// once, from main. The returned Config is never mutated.
func Load() (Config, error) {
	var problems []string

	// Reject unknown ZTAX_ variables before reading anything, so a typo is
	// reported as a typo rather than as a missing required value.
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(name, Prefix) {
			continue
		}
		if _, recognised := known[name]; !recognised {
			problems = append(problems, fmt.Sprintf("unrecognised variable %s", name))
		}
	}

	get := func(name string) string {
		spec := known[name]
		v, set := os.LookupEnv(name)
		if !set || v == "" {
			if spec.required {
				problems = append(problems, fmt.Sprintf("%s is required", name))
				return ""
			}
			return spec.def
		}
		return v
	}

	duration := func(name string) time.Duration {
		raw := get(name)
		d, err := time.ParseDuration(raw)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %q is not a duration", name, raw))
			return 0
		}
		if d <= 0 {
			problems = append(problems, fmt.Sprintf("%s: must be positive, got %s", name, d))
			return 0
		}
		return d
	}

	c := Config{
		Cell:                get("ZTAX_CELL"),
		Region:              get("ZTAX_REGION"),
		Environment:         get("ZTAX_ENVIRONMENT"),
		TrainApp:            get("ZTAX_TRAIN_APP"),
		TrainContent:        get("ZTAX_TRAIN_CONTENT"),
		TrainAI:             get("ZTAX_TRAIN_AI"),
		TrainAdapter:        get("ZTAX_TRAIN_ADAPTER"),
		TrainInfra:          get("ZTAX_TRAIN_INFRA"),
		TrainSchema:         get("ZTAX_TRAIN_SCHEMA"),
		TrainMigration:      get("ZTAX_TRAIN_MIGRATION"),
		DatabaseURLRef:      get("ZTAX_DATABASE_URL_REF"),
		HTTPAddr:            get("ZTAX_HTTP_ADDR"),
		HTTPReadTimeout:     duration("ZTAX_HTTP_READ_TIMEOUT"),
		HTTPWriteTimeout:    duration("ZTAX_HTTP_WRITE_TIMEOUT"),
		HTTPShutdownTimeout: duration("ZTAX_HTTP_SHUTDOWN_TIMEOUT"),
		OTLPEndpoint:        get("ZTAX_OTLP_ENDPOINT"),
		LogLevel:            get("ZTAX_LOG_LEVEL"),
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		problems = append(problems, fmt.Sprintf("ZTAX_LOG_LEVEL: %q is not a level", c.LogLevel))
	}

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("config: %w:\n  - %s",
			ErrInvalid, strings.Join(problems, "\n  - "))
	}
	return c, nil
}

// ErrInvalid is the sentinel for a configuration that will not start a process.
var ErrInvalid = errors.New("invalid configuration")

// LogAttrs renders the effective configuration for the single startup log line.
// Nothing here carries a secret: DatabaseURLRef names where a credential lives,
// not what it is. ADR-0017 §2.9.
func (c Config) LogAttrs() []any {
	return []any{
		"cell", c.Cell,
		"region", c.Region,
		"environment", c.Environment,
		"train.app", c.TrainApp,
		"train.content", c.TrainContent,
		"train.ai", c.TrainAI,
		"train.adapter", c.TrainAdapter,
		"train.infra", c.TrainInfra,
		"train.schema", c.TrainSchema,
		"train.migration", c.TrainMigration,
		"http.addr", c.HTTPAddr,
		"http.read_timeout", c.HTTPReadTimeout.String(),
		"http.write_timeout", c.HTTPWriteTimeout.String(),
		"otlp.endpoint", c.OTLPEndpoint,
		"log.level", c.LogLevel,
		"database.url_ref", c.DatabaseURLRef,
		"known_vars", strconv.Itoa(len(known)),
	}
}
