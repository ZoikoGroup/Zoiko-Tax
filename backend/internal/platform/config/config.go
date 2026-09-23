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

	// Session and cookie behaviour.
	//
	// SecureCookies is true everywhere the service is reached over TLS, which
	// is everywhere except a laptop. It sets the Secure flag and the __Host-
	// cookie prefix, and the two travel together because a browser silently
	// drops a __Host- cookie that is not Secure.
	SecureCookies bool
	// TrustProxy honours X-Forwarded-For for the recorded client address. It
	// is false unless the deployment actually sits behind a proxy that sets it,
	// because an unconditionally trusted header is one a client can forge.
	TrustProxy bool

	// Bootstrap. A cell with no tenants cannot be administered, because every
	// administrative endpoint requires an administrator. These provision the
	// first one at startup and are a no-op once it exists.
	//
	// BootstrapAdminPasswordRef names where the first administrator's password
	// lives; like DatabaseURLRef it is a reference, never the value.
	BootstrapTenant           string
	BootstrapTenantName       string
	BootstrapAdminEmail       string
	BootstrapAdminName        string
	BootstrapAdminPasswordRef string

	// Content names where this cell's signed rule bundle and the keyring that
	// verifies it live. Both or neither: a bundle directory with no keyring
	// would be content nobody could verify, and a keyring with no bundle
	// verifies nothing. Neither set is a cell with no content, which is a
	// legitimate deployment before A4 — it serves the administrative surface
	// and refuses determination with NO_CONTENT_BUNDLE.
	//
	// The bundle itself is not configuration (ADR-0017's four-way table): these
	// name *where* content lives, exactly as DatabaseURLRef names where a
	// credential lives.
	ContentDir     string
	ContentKeyring string

	// Authoritative records whether this deployment may emit authoritative
	// fiscal output. It is false until A4 and is reported by /v1/capabilities,
	// so a client discovers it from the service rather than from a release
	// note. Setting it true is an authorization act, not a configuration
	// convenience.
	Authoritative bool
}

// LocalSecretPrefix is the one recognised variable family whose members are not
// listed individually.
//
// internal/platform/secrets resolves a local://name reference by reading
// ZTAX_LOCAL_SECRET_NAME, and the set of names is a property of the deployment
// rather than of this build — so it cannot be enumerated here. The family is
// development-only: the resolver refuses local:// references outside
// development, so nothing in this family can be load-bearing in a cell.
//
// It is a deliberate hole in ADR-0017 §2.1's "an unknown ZTAX_ variable refuses
// to start", and it is kept as narrow as the rule allows: a typo in one of these
// names surfaces as an unresolvable reference at startup, which still fails
// closed, rather than as a silent default.
const LocalSecretPrefix = Prefix + "LOCAL_SECRET_"

// known lists every variable this process accepts, with whether it is required
// and its default. An environment variable under Prefix that is absent from this
// table — and outside LocalSecretPrefix — is a startup failure.
var known = map[string]struct {
	required bool
	def      string
}{
	"ZTAX_CELL":                         {required: true},
	"ZTAX_REGION":                       {required: true},
	"ZTAX_ENVIRONMENT":                  {required: true},
	"ZTAX_TRAIN_APP":                    {required: true},
	"ZTAX_TRAIN_CONTENT":                {required: true},
	"ZTAX_TRAIN_AI":                     {required: true},
	"ZTAX_TRAIN_ADAPTER":                {required: true},
	"ZTAX_TRAIN_INFRA":                  {required: true},
	"ZTAX_TRAIN_SCHEMA":                 {required: true},
	"ZTAX_TRAIN_MIGRATION":              {required: true},
	"ZTAX_DATABASE_URL_REF":             {required: true},
	"ZTAX_HTTP_ADDR":                    {def: ":8080"},
	"ZTAX_HTTP_READ_TIMEOUT":            {def: "10s"},
	"ZTAX_HTTP_WRITE_TIMEOUT":           {def: "30s"},
	"ZTAX_HTTP_SHUTDOWN_TIMEOUT":        {def: "30s"},
	"ZTAX_OTLP_ENDPOINT":                {def: "localhost:4317"},
	"ZTAX_LOG_LEVEL":                    {def: "info"},
	"ZTAX_SECURE_COOKIES":               {def: "true"},
	"ZTAX_TRUST_PROXY":                  {def: "false"},
	"ZTAX_AUTHORITATIVE":                {def: "false"},
	"ZTAX_CONTENT_DIR":                  {def: ""},
	"ZTAX_CONTENT_KEYRING":              {def: ""},
	"ZTAX_BOOTSTRAP_TENANT":             {def: ""},
	"ZTAX_BOOTSTRAP_TENANT_NAME":        {def: ""},
	"ZTAX_BOOTSTRAP_ADMIN_EMAIL":        {def: ""},
	"ZTAX_BOOTSTRAP_ADMIN_NAME":         {def: ""},
	"ZTAX_BOOTSTRAP_ADMIN_PASSWORD_REF": {def: ""},
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
		if strings.HasPrefix(name, LocalSecretPrefix) {
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

	boolean := func(name string) bool {
		switch v := get(name); v {
		case "true":
			return true
		case "false", "":
			return false
		default:
			// Not a tolerant parse: "yes", "1" and "TRUE" are all things
			// somebody meant as true, and guessing which is how a security flag
			// ends up silently off.
			problems = append(problems, fmt.Sprintf("%s: %q is not true or false", name, v))
			return false
		}
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

		ContentDir:     get("ZTAX_CONTENT_DIR"),
		ContentKeyring: get("ZTAX_CONTENT_KEYRING"),

		SecureCookies: boolean("ZTAX_SECURE_COOKIES"),
		TrustProxy:    boolean("ZTAX_TRUST_PROXY"),
		Authoritative: boolean("ZTAX_AUTHORITATIVE"),

		BootstrapTenant:           get("ZTAX_BOOTSTRAP_TENANT"),
		BootstrapTenantName:       get("ZTAX_BOOTSTRAP_TENANT_NAME"),
		BootstrapAdminEmail:       get("ZTAX_BOOTSTRAP_ADMIN_EMAIL"),
		BootstrapAdminName:        get("ZTAX_BOOTSTRAP_ADMIN_NAME"),
		BootstrapAdminPasswordRef: get("ZTAX_BOOTSTRAP_ADMIN_PASSWORD_REF"),
	}

	// A deployment claiming authority outside development is refused here
	// rather than at the point of emitting a decision. No authoritative fiscal
	// output is permitted before A4, and a misconfiguration should stop the
	// process rather than produce one filed figure.
	if c.Authoritative && c.Environment != "production" {
		problems = append(problems, fmt.Sprintf(
			"ZTAX_AUTHORITATIVE: refused in environment %q; authoritative output requires A4 in production", c.Environment))
	}

	if (c.ContentDir == "") != (c.ContentKeyring == "") {
		// Failing closed here rather than at load: a cell that started with a
		// content directory and no keyring would either run unverified content
		// or refuse every bundle, and both are worse than not starting.
		problems = append(problems,
			"ZTAX_CONTENT_DIR and ZTAX_CONTENT_KEYRING are set together or not at all; content is never loaded unverified (ADR-0005 §2.6)")
	}

	// Plain-HTTP cookies are a development affordance, and saying so at startup
	// is cheaper than discovering it in a penetration test.
	if !c.SecureCookies && c.Environment != "development" {
		problems = append(problems, fmt.Sprintf(
			"ZTAX_SECURE_COOKIES: refused as false in environment %q; the session cookie would travel in clear", c.Environment))
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
		"content.dir", c.ContentDir,
		"content.keyring", c.ContentKeyring,
		"secure_cookies", c.SecureCookies,
		"trust_proxy", c.TrustProxy,
		"authoritative", c.Authoritative,
		"known_vars", strconv.Itoa(len(known)),
	}
}
