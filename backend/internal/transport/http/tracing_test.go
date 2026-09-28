package http

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/telemetry"
)

// ADR-0015 §5.1 control 3: a request carrying classified values — an email in
// the query string, a password in the body — is traced and logged, and neither
// the exported span nor the log line carries a raw value.
func TestTracedAndLoggedRequestCarriesNoRawValue(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	var logs bytes.Buffer
	log := slog.New(telemetry.NewLogHandler(slog.NewJSONHandler(&logs, nil)))

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/sign-in", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	h := chain(mux, withRequestID(&idgen.Sequential{}), withTracing(tp, mux), withLogging(log))

	body := `{"tenant":"acme","email":"ada@acme.example","password":"correct horse battery staple"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/sign-in?login_hint=ada@acme.example", strings.NewReader(body))
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	h.ServeHTTP(httptest.NewRecorder(), req)

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("%d spans", len(spans))
	}
	s := spans[0]
	if s.Name() != "POST /v1/auth/sign-in" {
		t.Fatalf("span name %q; the route, not the raw path", s.Name())
	}
	if s.Parent().TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatal("the incoming traceparent was not honoured")
	}
	exported := s.Name()
	for _, kv := range s.Attributes() {
		exported += " " + string(kv.Key) + "=" + kv.Value.String()
	}
	for _, raw := range []string{"ada@acme.example", "correct horse", "login_hint"} {
		if strings.Contains(exported, raw) || strings.Contains(logs.String(), raw) {
			t.Fatalf("%q reached telemetry:\nspan: %s\nlog: %s", raw, exported, logs.String())
		}
	}
	if !strings.Contains(logs.String(), `"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736"`) {
		t.Fatalf("the log line is not correlated with the span: %s", logs.String())
	}
}

// An unrouted path is named "unrouted", never by its caller-controlled text.
func TestUnroutedPathsDoNotNameSpans(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	chain(mux, withTracing(tp, mux)).ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/users/ada@acme.example", nil))
	if got := rec.Ended()[0].Name(); got != "GET unrouted" {
		t.Fatalf("span named %q", got)
	}
}
