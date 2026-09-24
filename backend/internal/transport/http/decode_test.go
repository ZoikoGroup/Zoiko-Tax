package http

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// ADR-0011 P5: a body carrying anything the contract does not declare is
// refused. Most of these cases were accepted before exactKeys, because
// encoding/json matches keys to fields case-insensitively.
func TestDecodeRefusesWhatTheContractDoesNotDeclare(t *testing.T) {
	cases := []struct {
		name string
		body string
		want errs.ReasonCode
	}{
		{"an undeclared field", `{"email":"a@b.example","displayName":"A","nickname":"x"}`, errs.ReasonUnknownField},
		{"a miscased field alone", `{"email":"a@b.example","displayname":"A"}`, errs.ReasonUnknownField},
		{"a miscased field beside the real one", `{"email":"a@b.example","displayName":"A","displayname":"B"}`, errs.ReasonUnknownField},
		{"an upper-cased field", `{"email":"a@b.example","DISPLAYNAME":"A"}`, errs.ReasonUnknownField},
		{"a repeated field", `{"email":"a@b.example","displayName":"A","displayName":"B"}`, errs.ReasonUnknownField},
		{"not JSON", `{"email":`, errs.ReasonMalformedRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var dst gen.CreateUserRequest
			err := decodeJSON(httptest.NewRequest("POST", "/v1/admin/users", strings.NewReader(tc.body)), &dst)
			var e *errs.Error
			if !errors.As(err, &e) {
				t.Fatalf("decodeJSON(%s) = %v, want a %s refusal", tc.body, err, tc.want)
			}
			if e.Reason != tc.want {
				t.Fatalf("decodeJSON(%s) refused with %s, want %s", tc.body, e.Reason, tc.want)
			}
		})
	}
}

func TestDecodeAcceptsExactlyTheContract(t *testing.T) {
	body := `{"email":"a@b.example","displayName":"A","roles":["AUDITOR"],"password":"correct horse battery staple"}`
	var dst gen.CreateUserRequest
	if err := decodeJSON(httptest.NewRequest("POST", "/v1/admin/users", strings.NewReader(body)), &dst); err != nil {
		t.Fatalf("decodeJSON refused a body the contract declares: %v", err)
	}
	if dst.DisplayName != "A" || len(dst.Roles) != 1 || dst.Password == nil {
		t.Fatalf("decodeJSON decoded %+v", dst)
	}
}
