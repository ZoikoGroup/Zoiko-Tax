package telemetry

import (
	"context"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
)

// Tracing — ADR-0015 §2.1, §2.5, §2.6.
//
// OpenTelemetry SDK, OTLP to a cell-local collector, no vendor SDK. The
// collector owns tail sampling, enrichment, the redaction denylist backstop and
// routing to a backend permitted for the cell's residency (§2.9); this process
// owns the head sampling decision and the resource identity.

// Resource is the identity every span carries (ADR-0015 §2.6). All seven
// train versions are resource attributes, so "did the regression start with
// the content release or the app release" is a dashboard question rather than
// a forensic one.
type Resource struct {
	ServiceName string
	Environment string
	Cell        string
	Region      string

	TrainApp       string
	TrainContent   string
	TrainAI        string
	TrainAdapter   string
	TrainInfra     string
	TrainSchema    string
	TrainMigration string

	CanonProfile string
	// BundleDigest and IRVersion name the content bundle active at startup.
	// Empty in a cell with no content.
	BundleDigest string
	IRVersion    int
}

// Validate is ADR-0015 §5.1 control 5: a missing train version refuses to
// start, rather than starting a process whose telemetry cannot say which
// combination produced it.
func (r Resource) Validate() error {
	missing := []string{}
	for name, v := range map[string]string{
		"service name": r.ServiceName, "environment": r.Environment, "cell": r.Cell, "region": r.Region,
		"APP train": r.TrainApp, "CONTENT train": r.TrainContent, "AI train": r.TrainAI,
		"ADAPTER train": r.TrainAdapter, "INFRA train": r.TrainInfra, "SCHEMA train": r.TrainSchema,
		"MIGRATION train": r.TrainMigration, "canon profile": r.CanonProfile,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("telemetry: resource is missing %s", strings.Join(missing, ", "))
	}
	return nil
}

func (r Resource) attributes() []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("service.name", r.ServiceName),
		attribute.String("service.version", r.TrainApp),
		attribute.String("deployment.environment", r.Environment),
		attribute.String("ztax.cell", r.Cell),
		attribute.String("ztax.region", r.Region),
		attribute.String("ztax.train.app", r.TrainApp),
		attribute.String("ztax.train.content", r.TrainContent),
		attribute.String("ztax.train.ai", r.TrainAI),
		attribute.String("ztax.train.adapter", r.TrainAdapter),
		attribute.String("ztax.train.infra", r.TrainInfra),
		attribute.String("ztax.train.schema", r.TrainSchema),
		attribute.String("ztax.train.migration", r.TrainMigration),
		attribute.String("ztax.canon_version", r.CanonProfile),
	}
	if r.BundleDigest != "" {
		attrs = append(attrs,
			attribute.String("ztax.bundle.digest", r.BundleDigest),
			attribute.Int("ztax.ir_version", r.IRVersion))
	}
	return attrs
}

// TracingConfig configures the exporter and the head sampler.
type TracingConfig struct {
	// Endpoint is the cell-local collector's OTLP/gRPC address. Empty disables
	// tracing: the provider is a no-op and nothing is exported.
	Endpoint string
	// Insecure sends OTLP without TLS. Development only; config refuses it
	// elsewhere.
	Insecure bool
	// SampleRatio is the head-sampling ratio for everything the fiscal write
	// rule does not force (§2.5), in [0, 1].
	SampleRatio float64
}

// Tracing is a configured tracer provider and the function that flushes it.
type Tracing struct {
	Provider trace.TracerProvider
	Shutdown func(context.Context) error
}

// SetupTracing builds the tracer provider and installs the W3C trace-context
// propagator.
//
// The exporter connects lazily: an unreachable collector costs dropped spans,
// never a failed startup or a slower request. Telemetry is operational, and an
// observability outage must not become a fiscal outage.
func SetupTracing(ctx context.Context, cfg TracingConfig, res Resource) (Tracing, error) {
	if err := res.Validate(); err != nil {
		return Tracing{}, err
	}
	if cfg.SampleRatio < 0 || cfg.SampleRatio > 1 {
		return Tracing{}, fmt.Errorf("telemetry: sample ratio %v is outside [0, 1]", cfg.SampleRatio)
	}
	otel.SetTextMapPropagator(propagation.TraceContext{})

	if cfg.Endpoint == "" {
		return Tracing{Provider: noop.NewTracerProvider(), Shutdown: func(context.Context) error { return nil }}, nil
	}

	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(cfg.Endpoint)}
	if cfg.Insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	exporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return Tracing{}, fmt.Errorf("telemetry: OTLP exporter: %w", err)
	}
	tp := NewProvider(sdktrace.WithBatcher(exporter), res, cfg.SampleRatio)
	return Tracing{Provider: tp, Shutdown: tp.Shutdown}, nil
}

// NewProvider builds a provider over any span processor, with the estate's
// resource and sampler. SetupTracing uses it with the OTLP batcher; tests use
// it with an in-memory recorder, so they exercise the same sampler and
// resource rather than a parallel configuration.
func NewProvider(processor sdktrace.TracerProviderOption, res Resource, ratio float64) *sdktrace.TracerProvider {
	return sdktrace.NewTracerProvider(
		processor,
		sdktrace.WithResource(resource.NewSchemaless(res.attributes()...)),
		sdktrace.WithSampler(sdktrace.ParentBased(FiscalWriteSampler(ratio))),
	)
}

// FiscalWriteSampler is ADR-0015 §2.5's head sampler: a span for an
// authoritative write — commit, adjust, refund — or one carrying a fiscal
// decision id is always recorded and sampled; everything else is sampled at
// ratio.
//
// Error paths are the part of §2.5 a head sampler cannot see, because a
// request is not known to fail when its span starts. They are retained by the
// collector's tail sampling on span status, which is where that decision can
// actually be made.
func FiscalWriteSampler(ratio float64) sdktrace.Sampler {
	return fiscalWriteSampler{ratio: sdktrace.TraceIDRatioBased(ratio)}
}

// FiscalWriteRoutes are the route suffixes that are never sampled out.
var FiscalWriteRoutes = []string{":commit", ":adjust", ":refund"}

type fiscalWriteSampler struct{ ratio sdktrace.Sampler }

func (s fiscalWriteSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	for _, a := range p.Attributes {
		switch a.Key {
		case "ztx.decision_id":
			return s.always(p)
		case "http.route":
			for _, suffix := range FiscalWriteRoutes {
				if strings.HasSuffix(a.Value.AsString(), suffix) {
					return s.always(p)
				}
			}
		}
	}
	return s.ratio.ShouldSample(p)
}

func (s fiscalWriteSampler) always(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	return sdktrace.SamplingResult{
		Decision:   sdktrace.RecordAndSample,
		Tracestate: trace.SpanContextFromContext(p.ParentContext).TraceState(),
	}
}

func (s fiscalWriteSampler) Description() string {
	return "FiscalWriteSampler{always: " + strings.Join(FiscalWriteRoutes, ",") + ", ztx.decision_id; else " + s.ratio.Description() + "}"
}

// Classified is ADR-0015 §5.1 control 1's attribute form of a classified
// value: the redacted rendering, never the content. There is deliberately no
// helper that attaches a privacy.Value's content to a span.
func Classified(key string, v privacy.Value) attribute.KeyValue {
	return attribute.String(key, v.Redacted())
}
