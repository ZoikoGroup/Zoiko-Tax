// SDK behaviour, against a stub transport.
//
// These test the decisions in the package's own doc comment — that an error is
// a value, that nothing retries by itself, that a non-Problem error body is not
// attributed to the service. They are not contract tests: what the *server*
// does is asserted in the backend, against the same contract file
// (backend/internal/transport/http/contract_test.go).
//
// Each case from the TypeScript SDK's test/client.test.js has a counterpart
// here under the same description, so the two SDKs are held to one behaviour.

package zoikotax_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	zoikotax "github.com/zoikogroup/zoikotax/sdk/go"
)

const base = "https://eu-west-1.zoikotax.com"

// call is one request the stub was asked to make, with its body already read.
type call struct {
	method string
	url    string
	header http.Header
	body   string
}

// stub is a RoundTripper that answers every request the same way, and records
// what it was asked.
type stub struct {
	mu      sync.Mutex
	calls   []call
	respond func(*http.Request) (*http.Response, error)
}

func (s *stub) RoundTrip(req *http.Request) (*http.Response, error) {
	var body string
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		body = string(b)
	}
	s.mu.Lock()
	s.calls = append(s.calls, call{method: req.Method, url: req.URL.String(), header: req.Header.Clone(), body: body})
	s.mu.Unlock()
	return s.respond(req)
}

func answer(status int, contentType, body string, headers ...string) *stub {
	return &stub{respond: func(req *http.Request) (*http.Response, error) {
		h := http.Header{}
		if contentType != "" {
			h.Set("Content-Type", contentType)
		}
		for i := 0; i+1 < len(headers); i += 2 {
			h.Set(headers[i], headers[i+1])
		}
		return &http.Response{
			StatusCode: status,
			Header:     h,
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}}
}

func jsonAnswer(t *testing.T, status int, v any, headers ...string) *stub {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	ct := "application/json"
	if status >= 400 {
		ct = "application/problem+json"
	}
	return answer(status, ct, string(b), headers...)
}

func problem(overrides map[string]any) map[string]any {
	p := map[string]any{
		"type":            "https://errors.zoikotax.com/v1/forbidden",
		"title":           "Forbidden",
		"status":          403,
		"detail":          "The authenticated subject does not hold a role permitting this action.",
		"instance":        "/v1/admin/users",
		"ztx_reason_code": "FORBIDDEN",
		"ztx_request_id":  "01JBQ0S9C3X8Q1H6M2KX5R7F4K",
		"ztx_retryable":   false,
	}
	for k, v := range overrides {
		p[k] = v
	}
	return p
}

func newClient(t *testing.T, rt http.RoundTripper, opts ...zoikotax.Option) *zoikotax.Client {
	t.Helper()
	c, err := zoikotax.NewClient(base, append([]zoikotax.Option{zoikotax.WithTransport(rt)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestASuccessReturnsTheBody(t *testing.T) {
	s := jsonAnswer(t, 200, map[string]any{
		"cell": "eu-west-1", "region": "eu-west", "environment": "development",
		"trains":       map[string]any{"app": "0.4.0", "content": "none", "ai": "0.1.0", "adapter": "0.2.0", "infra": "0.3.0", "schema": "0.1.0", "migration": "6"},
		"canonProfile": "canon/v1", "authoritative": false, "reasonCodes": []string{"NOT_AUTHORITATIVE"},
	})
	c, err := zoikotax.NewClient(base+"/", zoikotax.WithTransport(s))
	if err != nil {
		t.Fatal(err)
	}

	caps, err := c.GetCapabilities(context.Background())
	if err != nil {
		t.Fatalf("GetCapabilities: %v", err)
	}
	if caps.Cell != "eu-west-1" || caps.Authoritative || caps.Trains.Migration != "6" {
		t.Errorf("capabilities = %+v", caps)
	}
	// Absent, not a zero-valued bundle: "no content" and "content with no
	// identity" are different facts (ADR-0011 P3).
	if caps.Content != nil {
		t.Errorf("Content = %+v, want nil", caps.Content)
	}
	if got := s.calls[0].url; got != base+"/v1/capabilities" {
		t.Errorf("url = %q", got)
	}
	if got := s.calls[0].header.Get("Accept"); !strings.HasPrefix(got, "application/problem+json") {
		t.Errorf("Accept = %q", got)
	}
}

// The Go counterpart of the TypeScript test's `credentials: "include"`: the
// session is an HttpOnly cookie, and without a jar that keeps it every call
// after SignIn is UNAUTHENTICATED for a reason that looks like a server problem.
func TestTheSessionCookieFromSignInIsPresentedAfterwards(t *testing.T) {
	var sawCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/sign-in":
			http.SetCookie(w, &http.Cookie{Name: "ztax_session", Value: "opaque", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"tenant":{"id":"ztn_01JBQ0S9C3X8Q1H6M2KX5R7F4A","slug":"acme","displayName":"Acme Telecom","residencyRegion":"eu-west","status":"ACTIVE"},"user":{"id":"ztu_01JBQ0S9C3X8Q1H6M2KX5R7F4B","email":"admin@acme.example","displayName":"Ada Lovelace","status":"ACTIVE","roles":["ADMIN"],"createdAt":"2026-09-01T09:14:22.104000Z"},"expiresAt":"2026-09-24T09:14:22.104000Z"}`)
		case "/v1/auth/session":
			if ck, err := r.Cookie("ztax_session"); err == nil {
				sawCookie = ck.Value
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	c, err := zoikotax.NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	session, err := c.SignIn(context.Background(), zoikotax.SignInRequest{Tenant: "acme", Email: "admin@acme.example", Password: "correct horse battery staple"})
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if session.User.Roles[0] != zoikotax.RoleADMIN {
		t.Errorf("roles = %v", session.User.Roles)
	}
	want := time.Date(2026, 9, 24, 9, 14, 22, 104000000, time.UTC)
	if !session.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v", session.ExpiresAt, want)
	}

	// The server answers 204 here, which a data operation does not expect; the
	// assertion is only about the cookie, so the error is not examined.
	_, _ = c.GetSession(context.Background())
	if sawCookie != "opaque" {
		t.Errorf("session cookie presented = %q, want %q", sawCookie, "opaque")
	}
}

func TestAnErrorIsAValueCarryingTheReasonCodeNotAPanic(t *testing.T) {
	c := newClient(t, jsonAnswer(t, 403, problem(map[string]any{"ztx_future_extension": "kept"})))

	users, err := c.ListUsers(context.Background(), zoikotax.ListOptions{})

	if users != nil {
		t.Errorf("users = %+v, want nil", users)
	}
	var zerr *zoikotax.ZoikoTaxError
	if !errors.As(err, &zerr) {
		t.Fatalf("err = %T %v, want *ZoikoTaxError", err, err)
	}
	if zerr.ReasonCode != "FORBIDDEN" || zerr.Status != 403 || zerr.Retryable || zerr.RequestID != "01JBQ0S9C3X8Q1H6M2KX5R7F4K" {
		t.Errorf("error = %+v", zerr)
	}
	// The whole document survives, so a caller needing a field this SDK
	// release predates can still reach it.
	if zerr.Problem.Type != "https://errors.zoikotax.com/v1/forbidden" {
		t.Errorf("Problem.Type = %q", zerr.Problem.Type)
	}
	var raw map[string]any
	if err := json.Unmarshal(zerr.Raw, &raw); err != nil || raw["ztx_future_extension"] != "kept" {
		t.Errorf("Raw = %s (%v), want the extension preserved", zerr.Raw, err)
	}
	var terr *zoikotax.TransportError
	if errors.As(err, &terr) {
		t.Error("a Problem was also reported as a TransportError")
	}
}

func TestAValidationFailureNamesTheField(t *testing.T) {
	c := newClient(t, jsonAnswer(t, 400, problem(map[string]any{
		"status": 400, "ztx_reason_code": "UNKNOWN_FIELD", "ztx_field": "displayname",
	})))

	_, err := c.CreateUser(context.Background(), zoikotax.CreateUserRequest{Email: "a@acme.example", DisplayName: "A"})

	var zerr *zoikotax.ZoikoTaxError
	if !errors.As(err, &zerr) || zerr.Field != "displayname" {
		t.Fatalf("err = %v, want a ZoikoTaxError naming displayname", err)
	}
}

func TestRetryableAndRetryAfterAreReportedAndNothingIsRetried(t *testing.T) {
	s := jsonAnswer(t, 503, problem(map[string]any{
		"status": 503, "ztx_reason_code": "DATABASE_UNAVAILABLE", "ztx_retryable": true,
	}), "Retry-After", "2")
	c := newClient(t, s)

	_, err := c.GetTenant(context.Background())

	var zerr *zoikotax.ZoikoTaxError
	if !errors.As(err, &zerr) {
		t.Fatalf("err = %v, want *ZoikoTaxError", err)
	}
	if !zerr.Retryable || zerr.RetryAfter != 2*time.Second {
		t.Errorf("Retryable = %v, RetryAfter = %v", zerr.Retryable, zerr.RetryAfter)
	}
	// One attempt. Retrying is the caller's decision, because on the endpoints
	// this surface is about to grow, an automatic retry submits a transaction
	// twice.
	if len(s.calls) != 1 {
		t.Errorf("attempts = %d, want 1", len(s.calls))
	}
}

func TestA204YieldsNoBodyRatherThanAParseFailure(t *testing.T) {
	c := newClient(t, answer(204, "", ""))

	if err := c.RevokeRole(context.Background(), "ztu_01JBQ0S9C3X8Q1H6M2KX5R7F4C", zoikotax.RoleAUDITOR); err != nil {
		t.Fatalf("RevokeRole: %v", err)
	}
}

func TestANonProblemErrorBodyIsNotAttributedToTheService(t *testing.T) {
	for name, s := range map[string]*stub{
		// What a load balancer, a proxy or a WAF returns. Reporting it as a
		// ZoikoTaxError would give it a reason code nobody registered.
		"html":                answer(502, "text/html", "<html>502 Bad Gateway</html>"),
		"json but no Problem": answer(502, "application/json", `{"message":"upstream connect error"}`),
		"empty":               answer(502, "", ""),
	} {
		t.Run(name, func(t *testing.T) {
			c := newClient(t, s)

			_, err := c.GetSession(context.Background())

			var terr *zoikotax.TransportError
			if !errors.As(err, &terr) {
				t.Fatalf("err = %T %v, want *TransportError", err, err)
			}
			var zerr *zoikotax.ZoikoTaxError
			if errors.As(err, &zerr) {
				t.Error("a non-Problem body was reported as a ZoikoTaxError")
			}
			if terr.Status != 502 {
				t.Errorf("Status = %d, want 502", terr.Status)
			}
		})
	}
}

func TestARequestThatNeverReachesTheServiceIsATransportError(t *testing.T) {
	cause := errors.New("dial tcp: lookup eu-west-1.zoikotax.com: no such host")
	c := newClient(t, &stub{respond: func(*http.Request) (*http.Response, error) { return nil, cause }})

	_, err := c.GetCapabilities(context.Background())

	var terr *zoikotax.TransportError
	if !errors.As(err, &terr) {
		t.Fatalf("err = %T %v, want *TransportError", err, err)
	}
	if terr.Status != 0 {
		t.Errorf("Status = %d, want 0 for no response", terr.Status)
	}
	if !errors.Is(err, cause) {
		t.Errorf("the cause is not reachable through Unwrap: %v", err)
	}
}

func TestACancelledContextIsATransportErrorAndSaysSo(t *testing.T) {
	s := answer(200, "application/json", "{}")
	c := newClient(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.GetCapabilities(ctx)

	var terr *zoikotax.TransportError
	if !errors.As(err, &terr) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want a TransportError wrapping context.Canceled", err)
	}
	if len(s.calls) != 0 {
		t.Errorf("a cancelled request was sent")
	}
}

func TestTheTimeoutOptionBoundsARequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	c, err := zoikotax.NewClient(srv.URL, zoikotax.WithTimeout(20*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.GetCapabilities(context.Background())

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestANonJSONSuccessBodyIsATransportError(t *testing.T) {
	c := newClient(t, answer(200, "text/html", "<html>captive portal</html>"))

	_, err := c.GetCapabilities(context.Background())

	var terr *zoikotax.TransportError
	if !errors.As(err, &terr) || terr.Status != 200 {
		t.Fatalf("err = %v, want a TransportError with status 200", err)
	}
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) {
		t.Errorf("the JSON error is not reachable: %v", err)
	}
}

func TestPathParametersAreEncoded(t *testing.T) {
	s := answer(204, "", "")
	c := newClient(t, s)

	if err := c.RevokeSession(context.Background(), "zts_01/../../admin"); err != nil {
		t.Fatal(err)
	}

	if got := s.calls[0].url; !strings.HasSuffix(got, "/v1/admin/sessions/zts_01%2F..%2F..%2Fadmin") {
		t.Errorf("url = %q", got)
	}
}

func TestALimitBecomesAQueryParameterAndAnAbsentOneDoesNot(t *testing.T) {
	s := jsonAnswer(t, 200, map[string]any{"users": []any{}})
	c := newClient(t, s)

	if _, err := c.ListUsers(context.Background(), zoikotax.ListOptions{Limit: 50}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListUsers(context.Background(), zoikotax.ListOptions{}); err != nil {
		t.Fatal(err)
	}

	if got := s.calls[0].url; !strings.HasSuffix(got, "/v1/admin/users?limit=50") {
		t.Errorf("with a limit: url = %q", got)
	}
	if got := s.calls[1].url; !strings.HasSuffix(got, "/v1/admin/users") {
		t.Errorf("without one: url = %q", got)
	}
}

func TestARequestBodyIsSentAsJSONWithTheRightContentType(t *testing.T) {
	s := answer(200, "application/json", `{"tenant":{},"user":{},"expiresAt":"2026-09-24T09:14:22.104000Z"}`)
	c := newClient(t, s)

	if _, err := c.SignIn(context.Background(), zoikotax.SignInRequest{
		Tenant: "acme", Email: "admin@acme.example", Password: "correct horse battery staple",
	}); err != nil {
		t.Fatal(err)
	}

	if got := s.calls[0].header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(s.calls[0].body), &sent); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"tenant": "acme", "email": "admin@acme.example", "password": "correct horse battery staple"}
	if fmt.Sprint(sent) != fmt.Sprint(want) {
		t.Errorf("body = %v, want %v", sent, want)
	}
}

func TestAnOptionalFieldLeftUnsetIsNotSent(t *testing.T) {
	s := answer(201, "application/json", `{"id":"ztu_01JBQ0S9C3X8Q1H6M2KX5R7F4D","email":"auditor@acme.example","displayName":"Katherine Johnson","status":"INVITED","roles":["AUDITOR"],"createdAt":"2026-09-23T12:00:00.000000Z"}`)
	c := newClient(t, s)

	user, err := c.CreateUser(context.Background(), zoikotax.CreateUserRequest{Email: "auditor@acme.example", DisplayName: "Katherine Johnson"})
	if err != nil {
		t.Fatal(err)
	}

	// No password is an INVITED user; a "password": "" would be a validation
	// failure, and a "roles": null an undeclared shape.
	if got := s.calls[0].body; got != `{"displayName":"Katherine Johnson","email":"auditor@acme.example"}` {
		t.Errorf("body = %s", got)
	}
	if user.Status != zoikotax.UserStatusINVITED {
		t.Errorf("status = %q", user.Status)
	}
}

// The Go counterpart of the TypeScript SDK's unwrap(): a caller that wraps the
// error on its way up still finds the ZoikoTaxError and its reason code.
func TestTheErrorSurvivesWrappingByTheCaller(t *testing.T) {
	c := newClient(t, jsonAnswer(t, 403, problem(nil)))

	_, err := c.ListUsers(context.Background(), zoikotax.ListOptions{})
	wrapped := fmt.Errorf("loading the admin screen: %w", err)

	var zerr *zoikotax.ZoikoTaxError
	if !errors.As(wrapped, &zerr) || zerr.ReasonCode != "FORBIDDEN" {
		t.Fatalf("errors.As through a wrap: %v", wrapped)
	}
	if !strings.Contains(zerr.Error(), "FORBIDDEN") || !strings.Contains(zerr.Error(), "01JBQ0S9C3X8Q1H6M2KX5R7F4K") {
		t.Errorf("Error() = %q, want the reason code and request id", zerr.Error())
	}
}

func TestAClientWithNoBaseURLIsRefusedAtConstruction(t *testing.T) {
	for _, bad := range []string{"", "/", "eu-west-1.zoikotax.com", "ftp://eu-west-1.zoikotax.com", "https://eu-west-1.zoikotax.com/?cell=x"} {
		if c, err := zoikotax.NewClient(bad); err == nil || c != nil {
			t.Errorf("NewClient(%q) = %v, %v; want an error", bad, c, err)
		}
	}
}

func TestHeadersAreAddedToEveryRequest(t *testing.T) {
	s := answer(204, "", "")
	c := newClient(t, s, zoikotax.WithHeader("X-Correlation-Id", "abc"))

	_ = c.SignOut(context.Background())

	if got := s.calls[0].header.Get("X-Correlation-Id"); got != "abc" {
		t.Errorf("X-Correlation-Id = %q", got)
	}
	if got := s.calls[0].header.Get("User-Agent"); got != "zoikotax-go/"+zoikotax.Version {
		t.Errorf("User-Agent = %q", got)
	}
}

func TestAnInjectedHTTPClientIsUsed(t *testing.T) {
	s := answer(204, "", "")
	c, err := zoikotax.NewClient(base, zoikotax.WithHTTPClient(&http.Client{Transport: s}))
	if err != nil {
		t.Fatal(err)
	}

	if err := c.SignOut(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 1 {
		t.Errorf("calls through the injected client = %d, want 1", len(s.calls))
	}
}

// Every operation in the contract, with the method and path it sends. A
// fourteenth-operation drift — a path renamed in the contract and not here — is
// caught by this table rather than by an integration.
func TestEveryOperationSendsItsMethodAndPath(t *testing.T) {
	const user = "ztu_01JBQ0S9C3X8Q1H6M2KX5R7F4C"
	const session = "zts_01JBQ0S9C3X8Q1H6M2KX5R7F4F"
	ctx := context.Background()
	cases := []struct {
		name, method, path string
		body               string
		call               func(*zoikotax.Client) error
	}{
		{"GetCapabilities", "GET", "/v1/capabilities", "", func(c *zoikotax.Client) error { _, err := c.GetCapabilities(ctx); return err }},
		{"SignIn", "POST", "/v1/auth/sign-in", `{"email":"e","password":"p","tenant":"t"}`, func(c *zoikotax.Client) error {
			_, err := c.SignIn(ctx, zoikotax.SignInRequest{Tenant: "t", Email: "e", Password: "p"})
			return err
		}},
		{"SignOut", "POST", "/v1/auth/sign-out", "", func(c *zoikotax.Client) error { return c.SignOut(ctx) }},
		{"GetSession", "GET", "/v1/auth/session", "", func(c *zoikotax.Client) error { _, err := c.GetSession(ctx); return err }},
		{"ChangePassword", "POST", "/v1/auth/password", `{"currentPassword":"a","newPassword":"b"}`, func(c *zoikotax.Client) error {
			return c.ChangePassword(ctx, zoikotax.ChangePasswordRequest{CurrentPassword: "a", NewPassword: "b"})
		}},
		{"GetTenant", "GET", "/v1/admin/tenant", "", func(c *zoikotax.Client) error { _, err := c.GetTenant(ctx); return err }},
		{"ListUsers", "GET", "/v1/admin/users", "", func(c *zoikotax.Client) error {
			_, err := c.ListUsers(ctx, zoikotax.ListOptions{})
			return err
		}},
		{"CreateUser", "POST", "/v1/admin/users", `{"displayName":"d","email":"e","roles":["AUDITOR"]}`, func(c *zoikotax.Client) error {
			_, err := c.CreateUser(ctx, zoikotax.CreateUserRequest{Email: "e", DisplayName: "d", Roles: []zoikotax.Role{zoikotax.RoleAUDITOR}})
			return err
		}},
		{"SetUserStatus", "POST", "/v1/admin/users/" + user + "/status", `{"status":"DISABLED"}`, func(c *zoikotax.Client) error {
			return c.SetUserStatus(ctx, user, zoikotax.UserStatusDISABLED)
		}},
		{"GrantRole", "POST", "/v1/admin/users/" + user + "/roles", `{"role":"AUDITOR"}`, func(c *zoikotax.Client) error {
			return c.GrantRole(ctx, user, zoikotax.RoleAUDITOR)
		}},
		{"RevokeRole", "DELETE", "/v1/admin/users/" + user + "/roles/AUDITOR", "", func(c *zoikotax.Client) error {
			return c.RevokeRole(ctx, user, zoikotax.RoleAUDITOR)
		}},
		{"ListSessions", "GET", "/v1/admin/sessions", "", func(c *zoikotax.Client) error {
			_, err := c.ListSessions(ctx, zoikotax.ListOptions{})
			return err
		}},
		{"RevokeSession", "DELETE", "/v1/admin/sessions/" + session, "", func(c *zoikotax.Client) error {
			return c.RevokeSession(ctx, session)
		}},
		{"ListAudit", "GET", "/v1/admin/audit", "", func(c *zoikotax.Client) error {
			_, err := c.ListAudit(ctx, zoikotax.ListOptions{})
			return err
		}},
	}
	if len(cases) != 14 {
		t.Fatalf("cases = %d, want the contract's 14 operations", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A body every data operation can decode; the void ones ignore it.
			s := answer(200, "application/json", "{}")
			c := newClient(t, s)

			if err := tc.call(c); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			got := s.calls[0]
			if got.method != tc.method || got.url != base+tc.path {
				t.Errorf("sent %s %s, want %s %s", got.method, got.url, tc.method, base+tc.path)
			}
			if got.body != tc.body {
				t.Errorf("body = %q, want %q", got.body, tc.body)
			}
			if hasBody := got.header.Get("Content-Type") == "application/json"; hasBody != (tc.body != "") {
				t.Errorf("Content-Type = %q with body %q", got.header.Get("Content-Type"), got.body)
			}
		})
	}
}

func TestAListDecodesIntoGeneratedTypes(t *testing.T) {
	c := newClient(t, answer(200, "application/json", `{"records":[{"id":"zta_01JBQ0S9C3X8Q1H6M2KX5R7F4G","actorUserId":"ztu_01JBQ0S9C3X8Q1H6M2KX5R7F4B","action":"user.role.granted","subjectType":"user","subjectId":"ztu_01JBQ0S9C3X8Q1H6M2KX5R7F4C","detail":"{\"role\":\"AUDITOR\"}","recordedAt":"2026-09-23T12:00:00.000000Z"}]}`))

	audit, err := c.ListAudit(context.Background(), zoikotax.ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}

	r := audit.Records[0]
	// Detail is the recorded bytes, passed through as a string (ADR-0011 §2.7).
	if r.Detail != `{"role":"AUDITOR"}` || r.ActorUserID == nil || *r.ActorUserID != "ztu_01JBQ0S9C3X8Q1H6M2KX5R7F4B" {
		t.Errorf("record = %+v", r)
	}
}
