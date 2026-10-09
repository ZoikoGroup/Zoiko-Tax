// Package webhook is the outbound HTTP side of webhook delivery, and the
// egress guard on it.
//
// The guard runs where it cannot be talked round: in the dialer's Control
// hook, which sees the address the connection is about to be made to, after
// name resolution. A host that resolved to a public address when the
// subscription was created and resolves to 169.254.169.254 now is refused at
// the moment it would have mattered — the DNS-rebinding variant of server-side
// request forgery that a check at subscription time alone does not stop.
package webhook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/webhook"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Timeout bounds one delivery attempt end to end. A receiver that takes
// longer is a failed attempt, retried later, rather than a dispatcher held.
const Timeout = 10 * time.Second

// maxResponseRead is how much of a response is read before the connection is
// released. The body itself is never kept.
const maxResponseRead = 64 << 10

// ErrForbiddenDestination is the guard refusing a connection.
var ErrForbiddenDestination = errors.New("webhook: the destination address is not public")

// Sender delivers over HTTP with the egress guard in the dialer.
type Sender struct {
	client *http.Client
}

var _ port.WebhookSender = (*Sender)(nil)

// NewSender builds a sender. allowPrivate disables the address check, and is
// development's alone (config refuses it elsewhere).
func NewSender(allowPrivate bool) *Sender {
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			if allowPrivate {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return ErrForbiddenDestination
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || webhook.Forbidden(ip) {
				return fmt.Errorf("%w: %s", ErrForbiddenDestination, host)
			}
			return nil
		},
	}
	transport := &http.Transport{
		// No proxy, even one the environment names: a proxy would make the
		// connection the guard inspects the proxy's, and the destination
		// would go unchecked.
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          50,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: Timeout,
	}
	return &Sender{client: &http.Client{
		Transport: transport,
		Timeout:   Timeout,
		// A redirect is not followed: it would deliver the event to an
		// address nobody subscribed, and a 3xx is a failed attempt.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Send POSTs one delivery.
func (s *Sender) Send(ctx context.Context, endpoint string, headers map[string]string, body []byte) (int, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return 0, fmt.Errorf("webhook: %q is not a deliverable endpoint", endpoint)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/cloudevents+json")
	req.Header.Set("User-Agent", "ZoikoTax-Webhooks/1")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseRead))
	return resp.StatusCode, nil
}
