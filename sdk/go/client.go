package zoikotax

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Version is this SDK's version. It is sent in the User-Agent header, which the
// service records against each session (SessionSummary.UserAgent).
const Version = "1.0.0"

// UserList is the body of ListUsers. The type is generated from the contract;
// this is a name a reader can say.
type UserList = ListUsers200JSONResponseBody

// SessionList is the body of ListSessions.
type SessionList = ListSessions200JSONResponseBody

// AuditList is the body of ListAudit.
type AuditList = ListAudit200JSONResponseBody

// ListOptions bounds a list call.
type ListOptions struct {
	// Limit is the maximum number of items to return. Zero means unset: no
	// limit parameter is sent and the server's default applies. Zero is never
	// a valid limit (the contract's minimum is 1), so it cannot be confused
	// with one. The server caps the value independently, so a larger value is
	// not an error and does not return more.
	Limit Limit
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient makes the Client send every request through hc, unchanged.
//
// It is how a server-side caller substitutes an instrumented client. hc's Jar
// is what holds the session: an *http.Client with no Jar signs in successfully
// and then presents no cookie, so every later call is UNAUTHENTICATED.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.http = hc }
}

// WithTransport keeps the default cookie-jar client but sends requests through
// rt. It is how a test drives this client without a network. It has no effect
// alongside WithHTTPClient, whose client already names its transport.
func WithTransport(rt http.RoundTripper) Option {
	return func(c *Client) { c.transport = rt }
}

// WithHeader adds a header to every request. It is for a correlation header a
// platform requires; it is not where a credential goes, because there is no
// credential. It may replace Accept or User-Agent; it cannot replace
// Content-Type, which follows from the body.
func WithHeader(key, value string) Option {
	return func(c *Client) { c.headers.Set(key, value) }
}

// WithTimeout bounds every request, including reading its body. Zero, the
// default, means no client-side timeout; a deadline on the call's context
// applies either way, and the earlier of the two wins.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.timeout = d }
}

// Client is a client for one cell.
//
// One client per cell, because a cell is a residency boundary: data never
// leaves its region, and a client that transparently failed over to another
// cell would be moving a tenant's data across one.
//
// A Client is safe for concurrent use. Its session is its cookie jar, so two
// Clients are two sessions.
type Client struct {
	baseURL   string
	http      *http.Client
	transport http.RoundTripper
	headers   http.Header
	timeout   time.Duration
}

// NewClient returns a client for the cell at baseURL, such as
// "https://eu-west-1.zoikotax.com". A trailing slash is tolerated.
//
// It refuses a base URL that is empty or not an absolute http(s) URL, because
// the alternative is a client that fails on every call for a reason that looks
// like a network problem.
func NewClient(baseURL string, opts ...Option) (*Client, error) {
	trimmed := strings.TrimRight(baseURL, "/")
	if trimmed == "" {
		return nil, errors.New("zoikotax: a base URL is required")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("zoikotax: base URL %q: %w", baseURL, err)
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("zoikotax: base URL %q is not an absolute http or https URL", baseURL)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("zoikotax: base URL %q carries a query or fragment", baseURL)
	}

	c := &Client{
		baseURL: trimmed,
		headers: http.Header{
			// Problem Details first, because an error is the response whose
			// shape this client most needs to be sure of.
			"Accept":     {"application/problem+json, application/json"},
			"User-Agent": {"zoikotax-go/" + Version},
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.http == nil {
		// cookiejar.New with no public suffix list never fails, and a list is
		// not needed: this jar only ever holds one cell's cookies.
		jar, _ := cookiejar.New(nil)
		c.http = &http.Client{Jar: jar, Transport: c.transport}
	}
	return c, nil
}

// --- discovery ---------------------------------------------------------------

// GetCapabilities reports this deployment's effective capability.
//
// Call it before treating any figure as authoritative: Authoritative is false
// until A4, and a deployment that reports false produces advisory figures that
// must not be filed.
func (c *Client) GetCapabilities(ctx context.Context) (*Capabilities, error) {
	var out Capabilities
	if err := c.send(ctx, http.MethodGet, "/v1/capabilities", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- authentication ----------------------------------------------------------

// SignIn opens a session. The response carries no token; the cookie is the
// session, and it lands in the Client's cookie jar.
func (c *Client) SignIn(ctx context.Context, body SignInRequest) (*Session, error) {
	var out Session
	if err := c.send(ctx, http.MethodPost, "/v1/auth/sign-in", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SignOut ends the current session.
func (c *Client) SignOut(ctx context.Context) error {
	return c.send(ctx, http.MethodPost, "/v1/auth/sign-out", nil, nil, nil)
}

// GetSession reports who the caller is, and until when.
func (c *Client) GetSession(ctx context.Context) (*Session, error) {
	var out Session
	if err := c.send(ctx, http.MethodGet, "/v1/auth/session", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ChangePassword changes the current subject's password.
//
// Every session is revoked, including this one. Expect the next call to fail
// with UNAUTHENTICATED, and send the user to sign in.
func (c *Client) ChangePassword(ctx context.Context, body ChangePasswordRequest) error {
	return c.send(ctx, http.MethodPost, "/v1/auth/password", nil, body, nil)
}

// --- administration ----------------------------------------------------------

// GetTenant returns the authenticated subject's tenant.
func (c *Client) GetTenant(ctx context.Context) (*Tenant, error) {
	var out Tenant
	if err := c.send(ctx, http.MethodGet, "/v1/admin/tenant", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListUsers lists the tenant's users.
func (c *Client) ListUsers(ctx context.Context, opts ListOptions) (*UserList, error) {
	var out UserList
	if err := c.send(ctx, http.MethodGet, "/v1/admin/users", opts.query(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateUser creates a user. Omitting the password creates an INVITED user.
func (c *Client) CreateUser(ctx context.Context, body CreateUserRequest) (*User, error) {
	var out User
	if err := c.send(ctx, http.MethodPost, "/v1/admin/users", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetUserStatus enables or disables a user. Disabling revokes their sessions.
func (c *Client) SetUserStatus(ctx context.Context, userID UserID, status UserStatus) error {
	path := "/v1/admin/users/" + url.PathEscape(userID) + "/status"
	return c.send(ctx, http.MethodPost, path, nil, SetUserStatusRequest{Status: status}, nil)
}

// GrantRole grants a role. Granting a role the user already holds is not an
// error.
func (c *Client) GrantRole(ctx context.Context, userID UserID, role Role) error {
	path := "/v1/admin/users/" + url.PathEscape(userID) + "/roles"
	return c.send(ctx, http.MethodPost, path, nil, RoleRequest{Role: role}, nil)
}

// RevokeRole revokes a role. Revoking the last ADMIN is refused with
// STATE_TRANSITION_INVALID.
func (c *Client) RevokeRole(ctx context.Context, userID UserID, role Role) error {
	path := "/v1/admin/users/" + url.PathEscape(userID) + "/roles/" + url.PathEscape(string(role))
	return c.send(ctx, http.MethodDelete, path, nil, nil, nil)
}

// ListSessions lists the tenant's sessions. Current marks the caller's own.
func (c *Client) ListSessions(ctx context.Context, opts ListOptions) (*SessionList, error) {
	var out SessionList
	if err := c.send(ctx, http.MethodGet, "/v1/admin/sessions", opts.query(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeSession revokes a session. It is marked revoked, never deleted.
func (c *Client) RevokeSession(ctx context.Context, sessionID SessionID) error {
	return c.send(ctx, http.MethodDelete, "/v1/admin/sessions/"+url.PathEscape(sessionID), nil, nil, nil)
}

// ListAudit reads the tenant's audit trail, most recent first.
//
// AuditRecord.Detail is the recorded canonical JSON as a string. Pass it on as
// it is; decoding and re-encoding it produces this program's serializer's
// opinion of what was written rather than what was written (ADR-0011 §2.7).
func (c *Client) ListAudit(ctx context.Context, opts ListOptions) (*AuditList, error) {
	var out AuditList
	if err := c.send(ctx, http.MethodGet, "/v1/admin/audit", opts.query(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- transport ---------------------------------------------------------------

func (o ListOptions) query() url.Values {
	if o.Limit == 0 {
		return nil
	}
	return url.Values{"limit": {strconv.FormatInt(int64(o.Limit), 10)}}
}

// send performs one request, once. out is nil for an operation whose success
// has no body.
func (c *Client) send(ctx context.Context, method, path string, query url.Values, body, out any) error {
	fail := func(status int, message string, cause error) error {
		return &TransportError{Method: method, Path: path, Status: status, Message: message, Err: cause}
	}

	if ctx == nil {
		return fail(0, "was called with a nil context", nil)
	}
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	// Checked here rather than left to the transport, because an injected
	// RoundTripper need not check it, and a request its caller already gave up
	// on must not reach the service.
	if err := ctx.Err(); err != nil {
		return fail(0, "was not sent: its context is done", err)
	}

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fail(0, "could not encode its request body", err)
		}
		reader = bytes.NewReader(encoded)
	}

	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return fail(0, "could not be built", err)
	}
	for key, values := range c.headers {
		req.Header[key] = append([]string(nil), values...)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fail(0, "did not reach the service", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fail(resp.StatusCode, fmt.Sprintf("returned %d and its body could not be read", resp.StatusCode), err)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out == nil {
			// Success with nothing to decode: a 204, or a body this operation
			// does not define. Neither is a failure of the call.
			return nil
		}
		if len(data) == 0 {
			return fail(resp.StatusCode, fmt.Sprintf("returned %d with no body where the contract defines one", resp.StatusCode), nil)
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fail(resp.StatusCode, fmt.Sprintf("returned %d with a body that is not the JSON the contract defines", resp.StatusCode), err)
		}
		return nil
	}

	if problem, ok := decodeProblem(data); ok {
		return &ZoikoTaxError{
			ReasonCode: problem.ZtxReasonCode,
			Status:     int(problem.Status),
			Retryable:  problem.ZtxRetryable,
			RequestID:  deref(problem.ZtxRequestID),
			Field:      deref(problem.ZtxField),
			RetryAfter: retryAfter(resp.Header.Get("Retry-After")),
			Problem:    problem,
			Raw:        json.RawMessage(data),
		}
	}

	// A non-Problem error body is something between the client and the cell: a
	// load balancer, a proxy, a WAF. Reporting it as a ZoikoTaxError would
	// attribute it to the service and give it a reason code nobody registered.
	return fail(resp.StatusCode, fmt.Sprintf(
		"returned %d without a Problem Details body; something between this client and the cell answered",
		resp.StatusCode), nil)
}

// decodeProblem reads body as a Problem, if it is one.
//
// It requires the two members a caller acts on, ztx_reason_code as a string and
// status as a number, rather than validating the whole document. A stricter
// check would reject a Problem carrying an extension this SDK release
// predates, and ADR-0010 §2.6 makes additions the normal case.
func decodeProblem(body []byte) (Problem, bool) {
	var probe struct {
		ReasonCode *string  `json:"ztx_reason_code"`
		Status     *float64 `json:"status"`
	}
	if err := json.Unmarshal(body, &probe); err != nil || probe.ReasonCode == nil || probe.Status == nil {
		return Problem{}, false
	}
	var problem Problem
	if err := json.Unmarshal(body, &problem); err != nil {
		return Problem{}, false
	}
	return problem, true
}

// retryAfter reads a Retry-After header as delay-seconds, the form the contract
// declares. Anything else is treated as absent rather than guessed at.
func retryAfter(header string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || seconds < 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
