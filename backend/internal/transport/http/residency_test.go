package http

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

func residentContext(home string) security.Context {
	return security.New(
		id.NewTenantID(uuid.MustParse("01920000-0000-7000-8000-0000000000a1")),
		id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000a2")),
		id.NewSessionID(uuid.MustParse("01920000-0000-7000-8000-0000000000a3")),
		[]security.Role{security.RoleAdmin}, time.Unix(0, 0),
	).WithHomeCell(home)
}

// SEC-REQ-0037: an authenticated request for a tenant homed elsewhere — or
// homed nowhere, or reaching a process that does not know its cell — is refused
// before any handler runs, as an RFC 9457 Problem with the registered code.
func TestResidencyRefusesFailClosed(t *testing.T) {
	cases := []struct {
		name       string
		sc         *security.Context
		cell       string
		wantServed bool
	}{
		{"resident", ptr(residentContext("eu-west-1")), "eu-west-1", true},
		{"homed in another cell", ptr(residentContext("us-east-1")), "eu-west-1", false},
		{"tenant with no home cell", ptr(residentContext("")), "eu-west-1", false},
		{"process with no cell", ptr(residentContext("eu-west-1")), "", false},
		// Unauthenticated requests name no tenant and pass through to the
		// authorization stage, which refuses them where it must.
		{"unauthenticated", nil, "eu-west-1", true},
		{"unauthenticated, process with no cell", nil, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var logs bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&logs, nil))
			served := false
			h := withResidency(c.cell, log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				served = true
				w.WriteHeader(http.StatusNoContent)
			}))

			req := httptest.NewRequest(http.MethodGet, "/v1/admin/tenant", nil)
			if c.sc != nil {
				req = req.WithContext(security.Into(req.Context(), *c.sc))
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if served != c.wantServed {
				t.Fatalf("served = %v, want %v", served, c.wantServed)
			}
			if c.wantServed {
				return
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Fatalf("content type %q", ct)
			}
			var p Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
			if p.ReasonCode != string(errs.ReasonTenantNotResident) || p.Retryable {
				t.Fatalf("problem %+v", p)
			}
			// The response names no cell; the security log names both.
			for _, cell := range []string{"us-east-1", "eu-west-1"} {
				if strings.Contains(rec.Body.String(), cell) {
					t.Fatalf("the response discloses a cell: %s", rec.Body.String())
				}
			}
			if !strings.Contains(logs.String(), "RESIDENCY_REFUSED") {
				t.Fatalf("no security event logged: %s", logs.String())
			}
		})
	}
}

// The stage is in the router's chain, not merely defined: a request for a
// tenant homed in another cell is refused by the assembled handler.
func TestRouterChainEnforcesResidency(t *testing.T) {
	rt := NewRouter(nil, nil, nil, nil, slog.New(slog.DiscardHandler), idgen.V7{})
	rt.Cell = "eu-west-1"
	h := rt.Handler()

	// The authentication stage resolves a cookie; with none, it passes the
	// context through, so a security context placed on the request stands in
	// for a resolved session.
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/tenant", nil)
	req = req.WithContext(security.Into(req.Context(), residentContext("us-east-1")))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), string(errs.ReasonTenantNotResident)) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func ptr[T any](v T) *T { return &v }
