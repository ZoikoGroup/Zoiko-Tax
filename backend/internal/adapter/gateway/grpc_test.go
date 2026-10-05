package gateway

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
)

// fakeGateway is an in-process gRPC server speaking the wire on Method. It is
// the Go-side stand-in for the Python Gateway; the cross-language test below
// runs against the real one.
type fakeGateway struct {
	answer func(context.Context, wireCall) (*wireReply, error)
	seen   []wireCall
}

func (f *fakeGateway) serve(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	srv.RegisterService(&grpc.ServiceDesc{
		ServiceName: "ztax.gateway.v1.ModelGateway",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "Invoke",
			Handler: func(_ any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
				var c wireCall
				if err := dec(&c); err != nil {
					return nil, err
				}
				f.seen = append(f.seen, c)
				return f.answer(ctx, c)
			},
		}},
	}, struct{}{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// refuse is a handler error carrying the trailing reason, as the Python
// Gateway sends it. An empty reason sends none.
func refuse(ctx context.Context, code codes.Code, reason string) (*wireReply, error) {
	if reason != "" {
		_ = grpc.SetTrailer(ctx, metadata.Pairs(reasonKey, reason))
	}
	return nil, status.Error(code, "refused")
}

var testTenant = id.NewTenantID(uuid.MustParse("0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f"))

func testCall() Call {
	return Call{
		Kind: KindClassificationProposal,
		Governance: ai.GovernanceContext{
			Tenant: testTenant, UseCase: "classification-review", AuthorityOutcome: "A1", RiskTier: "T1",
			Region: "euc1-dev-01", DataClasses: []privacy.Class{"P1", "P0"},
		},
		SubjectRef: "sku:PLAN-UNL-5G",
		Input:      []byte(`{"sku":"PLAN-UNL-5G"}`),
	}
}

func transport(t *testing.T, addr string) *GRPC {
	t.Helper()
	g, err := NewGRPC(GRPCConfig{Target: "passthrough:///" + addr, InsecureLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestGRPCCarriesTheCallAndTheReply(t *testing.T) {
	f := &fakeGateway{answer: func(context.Context, wireCall) (*wireReply, error) {
		return &wireReply{ModelProfile: "m", ProviderProfile: "p", PromptProfile: "q", AiTrainVersion: "ai-1",
			ProposedCode: "ontology:telecom/voice/mobile", Confidence: "0.97"}, nil
	}}
	reply, err := transport(t, f.serve(t)).Invoke(ctx(t), testCall())
	if err != nil {
		t.Fatal(err)
	}
	if reply.ProposedCode != "ontology:telecom/voice/mobile" || reply.Confidence != "0.97" || reply.AiTrainVersion != "ai-1" {
		t.Fatalf("reply %+v", reply)
	}
	got := f.seen[0]
	if got.Governance.TenantID != testTenant.String() || got.Governance.Region != "euc1-dev-01" ||
		strings.Join(got.Governance.DataClasses, ",") != "P0,P1" {
		t.Fatalf("governance on the wire %+v", got.Governance)
	}
	in, _ := base64.StdEncoding.DecodeString(got.InputB64)
	if string(in) != `{"sku":"PLAN-UNL-5G"}` {
		t.Fatalf("input %q", in)
	}
}

func TestGRPCStatusesMapOntoTheTaxonomy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		code     codes.Code
		reason   string
		category errs.Category
		want     errs.ReasonCode
	}{
		{"refusal", codes.PermissionDenied, "AI_AUTHORITY_REFUSED", errs.CategoryPolicy, ai.ReasonAuthorityRefused},
		{"kill switch", codes.PermissionDenied, "AI_KILL_SWITCH_ENGAGED", errs.CategoryPolicy, ai.ReasonKillSwitchEngaged},
		{"malformed", codes.InvalidArgument, "AI_MALFORMED_CONTEXT", errs.CategoryInternal, ai.ReasonMalformedContext},
		{"not configured", codes.Unimplemented, "AI_GATEWAY_NOT_CONFIGURED", errs.CategoryUnsupported, ReasonGatewayNotConfigured},
		{"unavailable", codes.Unavailable, "AI_GATEWAY_UNAVAILABLE", errs.CategoryUnavailable, ReasonGatewayUnavailable},
		{"deadline", codes.DeadlineExceeded, "", errs.CategoryUnavailable, ReasonGatewayUnavailable},
		{"refusal with a foreign reason", codes.PermissionDenied, "SOMETHING_ELSE", errs.CategoryInternal, errs.ReasonInternal},
		{"unknown status", codes.DataLoss, "", errs.CategoryInternal, errs.ReasonInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, reason := tc.code, tc.reason
			f := &fakeGateway{answer: func(c context.Context, _ wireCall) (*wireReply, error) {
				return refuse(c, code, reason)
			}}
			_, err := transport(t, f.serve(t)).Invoke(ctx(t), testCall())
			if errs.CategoryOf(err) != tc.category || errs.ReasonOf(err) != tc.want {
				t.Fatalf("got %v (%s / %s), want %s / %s", err, errs.CategoryOf(err), errs.ReasonOf(err), tc.category, tc.want)
			}
		})
	}
}

func TestGRPCRefusesToRunWithoutMTLS(t *testing.T) {
	if _, err := NewGRPC(GRPCConfig{Target: "dns:///gateway:8443"}); err == nil {
		t.Fatal("a transport with neither mTLS nor an explicit local flag was built")
	}
	if _, err := NewGRPC(GRPCConfig{Target: "dns:///gateway:8443", TLS: &tls.Config{MinVersion: tls.VersionTLS13}}); err == nil {
		t.Fatal("TLS with no client certificate was accepted as mTLS")
	}
}

func TestCodecCarriesOnlyTheWireTypes(t *testing.T) {
	if _, err := (jsonCodec{}).Marshal(map[string]string{"a": "b"}); err == nil {
		t.Fatal("the codec marshalled a type that is not a wire message")
	}
	var r wireReply
	if err := (jsonCodec{}).Unmarshal([]byte(`{"model_profile":"m","surprise":1}`), &r); err == nil {
		t.Fatal("the codec accepted an unknown field")
	}
}

// The wire structs carry exactly the fields the canonical schemas declare.
func TestWireStructsMatchTheCanonicalSchemas(t *testing.T) {
	for _, tc := range []struct {
		file string
		typ  reflect.Type
		path []string
	}{
		{"gateway-call.schema.json", reflect.TypeOf(wireCall{}), nil},
		{"gateway-call.schema.json", reflect.TypeOf(wireGovernance{}), []string{"properties", "governance"}},
		{"gateway-reply.schema.json", reflect.TypeOf(wireReply{}), nil},
	} {
		raw, err := os.ReadFile(filepath.Join("../../../../contracts/schemas/ai", tc.file))
		if err != nil {
			t.Fatal(err)
		}
		var node map[string]any
		if err := json.Unmarshal(raw, &node); err != nil {
			t.Fatal(err)
		}
		for _, p := range tc.path {
			node = node[p].(map[string]any)
		}
		var schema []string
		for k := range node["properties"].(map[string]any) {
			schema = append(schema, k)
		}
		var fields []string
		for i := 0; i < tc.typ.NumField(); i++ {
			fields = append(fields, strings.Split(tc.typ.Field(i).Tag.Get("json"), ",")[0])
		}
		sort.Strings(schema)
		sort.Strings(fields)
		if strings.Join(schema, ",") != strings.Join(fields, ",") {
			t.Errorf("%s %v: schema %v, Go %v", tc.file, tc.path, schema, fields)
		}
	}
}
