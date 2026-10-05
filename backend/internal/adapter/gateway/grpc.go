package gateway

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
)

// The gRPC Transport: the Go end of the boundary ADR-0006 §2.1 names.
//
// One method, /ztax.gateway.v1.ModelGateway/Invoke, whose messages are the
// JSON of contracts/schemas/ai/gateway-call and gateway-reply (intelligence/README.md,
// "The transport", records why the codec is JSON rather than protobuf).
// The codec is registered under "ztax-json" and used per call, so it never
// becomes the process-wide default for any other gRPC client.
//
// What a reply's status means is decided here and only here:
//
//   - PERMISSION_DENIED is a Gateway refusal. Its reason must be one of the
//     governance codes ai.RefusalCodes names; anything else is a defect.
//   - INVALID_ARGUMENT is a call the Gateway could not read — a defect on this
//     side, because this process built the call.
//   - UNIMPLEMENTED with AI_GATEWAY_NOT_CONFIGURED is a cell whose Gateway has
//     no approved provider: unsupported here, not transient.
//   - UNAVAILABLE, DEADLINE_EXCEEDED, RESOURCE_EXHAUSTED and ABORTED are
//     transient, and the breaker counts them.
//   - Anything else is a defect.

// Method is the full gRPC method name.
const Method = "/ztax.gateway.v1.ModelGateway/Invoke"

// reasonKey is the trailing-metadata key that carries the estate reason code.
const reasonKey = "ztax-reason"

const codecName = "ztax-json"

// jsonCodec carries the wire messages as JSON. It marshals only the two wire
// types, so nothing else can travel on it by accident.
type jsonCodec struct{}

func (jsonCodec) Name() string { return codecName }

func (jsonCodec) Marshal(v any) ([]byte, error) {
	switch v.(type) {
	case *wireCall, *wireReply:
		return json.Marshal(v)
	}
	return nil, fmt.Errorf("gateway: codec refuses %T", v)
}

func (jsonCodec) Unmarshal(data []byte, v any) error {
	switch v.(type) {
	case *wireCall, *wireReply:
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.DisallowUnknownFields()
		return dec.Decode(v)
	}
	return fmt.Errorf("gateway: codec refuses %T", v)
}

func init() { encoding.RegisterCodec(jsonCodec{}) }

// wireGovernance is gateway-call.schema.json's governance object.
type wireGovernance struct {
	TenantID         string   `json:"tenant_id"`
	UseCase          string   `json:"use_case"`
	AuthorityOutcome string   `json:"authority_outcome"`
	RiskTier         string   `json:"risk_tier"`
	Region           string   `json:"region"`
	DataClasses      []string `json:"data_classes"`
}

// wireCall is gateway-call.schema.json.
type wireCall struct {
	Kind       string         `json:"kind"`
	Governance wireGovernance `json:"governance"`
	SubjectRef string         `json:"subject_ref"`
	InputB64   string         `json:"input_b64"`
}

// wireReply is gateway-reply.schema.json.
type wireReply struct {
	ModelProfile    string            `json:"model_profile"`
	ProviderProfile string            `json:"provider_profile"`
	PromptProfile   string            `json:"prompt_profile"`
	AiTrainVersion  string            `json:"ai_train_version"`
	Text            string            `json:"text,omitempty"`
	Payload         string            `json:"payload,omitempty"`
	Fields          map[string]string `json:"fields,omitempty"`
	ProposedCode    string            `json:"proposed_code,omitempty"`
	Confidence      string            `json:"confidence,omitempty"`
}

func toWire(c Call) wireCall {
	classes := make([]string, len(c.Governance.DataClasses))
	for i, dc := range c.Governance.DataClasses {
		classes[i] = string(dc)
	}
	sort.Strings(classes)
	return wireCall{
		Kind: string(c.Kind),
		Governance: wireGovernance{
			TenantID:         c.Governance.Tenant.String(),
			UseCase:          string(c.Governance.UseCase),
			AuthorityOutcome: string(c.Governance.AuthorityOutcome),
			RiskTier:         string(c.Governance.RiskTier),
			Region:           c.Governance.Region,
			DataClasses:      classes,
		},
		SubjectRef: c.SubjectRef,
		InputB64:   base64.StdEncoding.EncodeToString(c.Input),
	}
}

// reply converts the wire form. The two structs have the same fields by
// construction — TestWireStructsMatchTheCanonicalSchemas holds wireReply to
// the schema — so this is a conversion, not a copy that could drop one.
func (w wireReply) reply() Reply { return Reply(w) }

// GRPC is the Transport to a Governed Model Gateway.
type GRPC struct {
	conn *grpc.ClientConn
}

// GRPCConfig is the transport's wiring.
type GRPCConfig struct {
	// Target is the Gateway's address, e.g. dns:///ztax-gateway:8443.
	Target string
	// TLS is the mTLS configuration: the client certificate this workload
	// presents and the CA the Gateway's certificate must chain to. Required
	// unless InsecureLocal.
	TLS *tls.Config
	// InsecureLocal permits a plaintext connection, for a local development
	// stack only. The caller decides that from the environment; New refuses
	// both or neither.
	InsecureLocal bool
}

// NewGRPC builds the transport. The connection is lazy: nothing is dialled
// until the first call, so a Gateway that is down at start-up degrades AI
// assistance rather than stopping the cell.
func NewGRPC(cfg GRPCConfig) (*GRPC, error) {
	if cfg.Target == "" {
		return nil, errors.New("gateway: no gRPC target")
	}
	var creds credentials.TransportCredentials
	switch {
	case cfg.TLS != nil && cfg.InsecureLocal:
		return nil, errors.New("gateway: both mTLS and insecure were configured")
	case cfg.TLS != nil:
		if len(cfg.TLS.Certificates) == 0 && cfg.TLS.GetClientCertificate == nil {
			// ADR-0006 §2.1 is mTLS, not TLS: the Gateway authenticates the
			// workload calling it.
			return nil, errors.New("gateway: the TLS configuration presents no client certificate")
		}
		creds = credentials.NewTLS(cfg.TLS)
	case cfg.InsecureLocal:
		creds = insecure.NewCredentials()
	default:
		return nil, errors.New("gateway: refusing a gRPC transport without mTLS (ADR-0006 §2.1)")
	}
	conn, err := grpc.NewClient(cfg.Target, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("gateway: %w", err)
	}
	return &GRPC{conn: conn}, nil
}

// Close releases the connection.
func (g *GRPC) Close() error { return g.conn.Close() }

// Invoke sends one call.
func (g *GRPC) Invoke(ctx context.Context, call Call) (Reply, error) {
	req := toWire(call)
	var resp wireReply
	var trailer metadata.MD
	err := g.conn.Invoke(ctx, Method, &req, &resp, grpc.CallContentSubtype(codecName), grpc.Trailer(&trailer))
	if err != nil {
		return Reply{}, fromStatus(err, trailer)
	}
	return resp.reply(), nil
}

// fromStatus maps a gRPC failure onto the estate taxonomy.
func fromStatus(err error, trailer metadata.MD) error {
	st, ok := status.FromError(err)
	if !ok {
		return errs.Wrap(err, errs.CategoryUnavailable, ReasonGatewayUnavailable,
			"AI assistance is temporarily unavailable.")
	}
	var reason errs.ReasonCode
	if vals := trailer.Get(reasonKey); len(vals) > 0 {
		reason = errs.ReasonCode(vals[0])
	}
	switch st.Code() {
	case codes.PermissionDenied:
		if slices.Contains(ai.RefusalCodes, reason) && reason != ai.ReasonMalformedContext {
			return errs.New(errs.CategoryPolicy, reason, "The AI Gateway refused the request.")
		}
	case codes.InvalidArgument:
		if reason == ai.ReasonMalformedContext {
			return errs.New(errs.CategoryInternal, ai.ReasonMalformedContext,
				"The AI Gateway could not read the request this cell sent.")
		}
	case codes.Unimplemented:
		if reason == ReasonGatewayNotConfigured {
			return errs.New(errs.CategoryUnsupported, ReasonGatewayNotConfigured,
				"AI assistance is not available in this region yet.")
		}
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Aborted, codes.Canceled:
		return errs.New(errs.CategoryUnavailable, ReasonGatewayUnavailable,
			"AI assistance is temporarily unavailable.")
	}
	// A status this side does not understand, or a reason that does not
	// belong with its status: a defect in one half of the boundary.
	return errs.New(errs.CategoryInternal, errs.ReasonInternal,
		fmt.Sprintf("The AI Gateway answered %s with reason %q, which this cell does not recognise.", st.Code(), reason))
}
