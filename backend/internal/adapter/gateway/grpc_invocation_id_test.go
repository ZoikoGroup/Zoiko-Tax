package gateway

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
)

// answerWithTrailer is a successful Gateway answer that sets the given
// "ztax-invocation-id" trailer values, one trailer entry per value.
func answerWithTrailer(values ...string) func(context.Context, wireCall) (*wireReply, error) {
	return func(ctx context.Context, _ wireCall) (*wireReply, error) {
		md := metadata.MD{}
		for _, v := range values {
			md.Append(invocationKey, v)
		}
		_ = grpc.SetTrailer(ctx, md)
		return &wireReply{ModelProfile: "m", ProviderProfile: "p", PromptProfile: "q", AiTrainVersion: "ai-1",
			ProposedCode: "ontology:telecom/voice/mobile", Confidence: "0.97"}, nil
	}
}

func TestInvocationIDIsReadFromTheTrailer(t *testing.T) {
	const uuidHex = "0190f3a21b2c7d3e8f405a6b7c8d9e0f"
	for _, tc := range []struct {
		name   string
		values []string
		want   string
	}{
		{"present", []string{uuidHex}, uuidHex},
		{"letters digits and dashes", []string{"Ab-09-zZ"}, "Ab-09-zZ"},
		{"exactly 64", []string{strings.Repeat("a", 64)}, strings.Repeat("a", 64)},
		{"absent", nil, ""},
		{"too long", []string{strings.Repeat("a", 65)}, ""},
		{"underscore", []string{"abc_def"}, ""},
		{"space", []string{"abc def"}, ""},
		{"slash", []string{"abc/def"}, ""},
		{"non-ASCII", []string{"abcé"}, ""},
		{"empty value", []string{""}, ""},
		{"two values, the first is used", []string{"first-id", "second-id"}, "first-id"},
		{"two values, a bad first is not replaced by a good second", []string{"bad id", "good-id"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeGateway{answer: answerWithTrailer(tc.values...)}
			reply, err := transport(t, f.serve(t)).Invoke(ctx(t), testCall())
			if err != nil {
				t.Fatalf("a bad or missing id must never fail the call: %v", err)
			}
			if reply.InvocationID != tc.want {
				t.Fatalf("InvocationID = %q, want %q", reply.InvocationID, tc.want)
			}
			// The rest of the reply is untouched.
			if reply.ProposedCode != "ontology:telecom/voice/mobile" || reply.Confidence != "0.97" || reply.AiTrainVersion != "ai-1" {
				t.Fatalf("reply %+v", reply)
			}
		})
	}
}

// An error call is exactly what it was: the same category, reason and message,
// with or without an id in the trailer, and no id on the error.
func TestAnErrorCallIsUnchangedByAnInvocationID(t *testing.T) {
	run := func(withID bool) error {
		f := &fakeGateway{answer: func(c context.Context, _ wireCall) (*wireReply, error) {
			md := metadata.Pairs(reasonKey, "AI_AUTHORITY_REFUSED")
			if withID {
				md.Append(invocationKey, "err-call-id-1")
			}
			_ = grpc.SetTrailer(c, md)
			return nil, status.Error(codes.PermissionDenied, "refused")
		}}
		reply, err := transport(t, f.serve(t)).Invoke(ctx(t), testCall())
		if reply.InvocationID != "" {
			t.Fatalf("an error call returned an id: %q", reply.InvocationID)
		}
		return err
	}
	without, with := run(false), run(true)
	if without == nil || with == nil {
		t.Fatal("want an error both times")
	}
	if errs.CategoryOf(with) != errs.CategoryOf(without) || errs.ReasonOf(with) != errs.ReasonOf(without) ||
		with.Error() != without.Error() {
		t.Fatalf("an id changed the error: %v vs %v", with, without)
	}
	if strings.Contains(with.Error(), "err-call-id-1") {
		t.Fatalf("the id leaked into the error text: %v", with)
	}
}
