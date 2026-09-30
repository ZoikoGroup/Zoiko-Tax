package telemetry_test

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/telemetry"
)

func resource() telemetry.Resource {
	return telemetry.Resource{
		ServiceName: "ztax-core", Environment: "test", Cell: "c1", Region: "r1",
		TrainApp: "1", TrainContent: "2", TrainAI: "3", TrainAdapter: "4", TrainInfra: "5",
		TrainSchema: "6", TrainMigration: "7", CanonProfile: "canon/v1",
		BundleDigest: "zt1:" + strings.Repeat("a", 64), IRVersion: 1,
	}
}

func provider(ratio float64) (*sdktrace.TracerProvider, *tracetest.SpanRecorder) {
	rec := tracetest.NewSpanRecorder()
	return telemetry.NewProvider(sdktrace.WithSpanProcessor(rec), resource(), ratio), rec
}

// ADR-0015 §2.5: sampling never drops an authoritative write. With the ratio
// at zero, only the forced spans survive.
func TestFiscalWritesAreAlwaysSampled(t *testing.T) {
	tp, rec := provider(0)
	tracer := tp.Tracer("test")
	start := func(name string, attrs ...attribute.KeyValue) {
		_, span := tracer.Start(context.Background(), name, trace.WithAttributes(attrs...))
		span.End()
	}
	start("commit", attribute.String("http.route", "/v1/transactions:commit"))
	start("adjust", attribute.String("http.route", "/v1/transactions:adjust"))
	start("refund", attribute.String("http.route", "/v1/transactions:refund"))
	start("decision", attribute.String("ztx.decision_id", "d-1"))
	start("quote", attribute.String("http.route", "/v1/quotes"))
	start("read", attribute.String("http.route", "/v1/admin/users"))

	var kept []string
	for _, s := range rec.Ended() {
		kept = append(kept, s.Name())
	}
	if strings.Join(kept, ",") != "commit,adjust,refund,decision" {
		t.Fatalf("kept %v; the forced spans must all survive and nothing else at ratio 0", kept)
	}
}

func TestResourceCarriesEveryTrain(t *testing.T) {
	tp, rec := provider(1)
	_, span := tp.Tracer("test").Start(context.Background(), "x")
	span.End()
	got := map[string]string{}
	for _, kv := range rec.Ended()[0].Resource().Attributes() {
		got[string(kv.Key)] = kv.Value.String()
	}
	for _, k := range []string{"ztax.train.app", "ztax.train.content", "ztax.train.ai", "ztax.train.adapter",
		"ztax.train.infra", "ztax.train.schema", "ztax.train.migration", "ztax.cell", "ztax.region",
		"ztax.canon_version", "ztax.bundle.digest", "service.name"} {
		if got[k] == "" {
			t.Errorf("resource is missing %s", k)
		}
	}
}

// ADR-0015 §5.1 control 5.
func TestIncompleteResourceRefusesToStart(t *testing.T) {
	r := resource()
	r.TrainAdapter = ""
	if _, err := telemetry.SetupTracing(context.Background(), telemetry.TracingConfig{}, r); err == nil ||
		!strings.Contains(err.Error(), "ADAPTER train") {
		t.Fatalf("started without a train version: %v", err)
	}
}

func TestNoCollectorMeansNoTracing(t *testing.T) {
	tr, err := telemetry.SetupTracing(context.Background(), telemetry.TracingConfig{SampleRatio: 1}, resource())
	if err != nil {
		t.Fatal(err)
	}
	_, span := tr.Provider.Tracer("x").Start(context.Background(), "x")
	if span.SpanContext().IsSampled() {
		t.Fatal("a cell with no collector configured produced a sampled span")
	}
}

// ADR-0015 §5.1 control 1: the attribute form of a classified value is its
// redacted form.
func TestClassifiedAttributeIsRedacted(t *testing.T) {
	kv := telemetry.Classified("user.email", privacy.Email("ada@acme.example"))
	if kv.Value.AsString() != "[redacted:email:P1]" {
		t.Fatalf("got %q", kv.Value.AsString())
	}
}
