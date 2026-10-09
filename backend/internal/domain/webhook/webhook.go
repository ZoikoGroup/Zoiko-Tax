// Package webhook is the delivery of a cell's events to an endpoint a tenant
// names (Build Plan W2 lane K: "webhook subscription, signing, rotation,
// retry, dead-letter, replay and SSRF controls").
//
// A webhook is the one place a cell makes an outbound request on a
// customer's say-so, which is what makes it dangerous twice over. It is an
// egress of classified data to a system nobody here operates, so a
// subscription names the event types it receives — there is no wildcard, and
// an event kind added later is not delivered to anyone who did not ask for it
// by name. And it is a request to an address someone else chose, so the
// address is policed: HTTPS only, no credentials in the URL, and no
// destination inside the cell's own network, checked again at the moment of
// connection because a name that resolved to a public address at
// subscription can resolve to a private one at delivery (ZTAX-SEC-001's SSRF
// control).
//
// Delivery is at-least-once, as everything the outbox carries is (ADR-0014
// §2.3). Each delivery carries the CloudEvents id as its message id, which is
// the receiver's deduplication key, and is signed under the Standard Webhooks
// scheme so a receiver can verify it with an off-the-shelf library.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// ---------------------------------------------------------------------------
// subscriptions
// ---------------------------------------------------------------------------

// Subscription is the immutable header of one webhook: where, and which
// events. Its status is a history of its own (StatusChange).
type Subscription struct {
	ID       id.WebhookID
	TenantID id.TenantID
	// URL is the endpoint, as ValidateEndpoint normalised it.
	URL string
	// EventTypes are the event types delivered, sorted and distinct. Never
	// empty: there is no "all events" subscription.
	EventTypes  []string
	Description string
	CreatedAt   time.Time
	CreatedBy   id.UserID
}

// MaxURLLength bounds an endpoint.
const MaxURLLength = 2048

// MaxDescriptionLength bounds the free-text description.
const MaxDescriptionLength = 500

// Wants reports whether the subscription receives events of type t.
func (s Subscription) Wants(t string) bool {
	_, found := slices.BinarySearch(s.EventTypes, t)
	return found
}

// NormaliseEventTypes sorts and de-duplicates a requested set, refusing an
// empty set and a type outside known.
func NormaliseEventTypes(requested []string, known []string) ([]string, error) {
	if len(requested) == 0 {
		return nil, fmt.Errorf("webhook: a subscription names the event types it receives; there is no wildcard")
	}
	out := make([]string, 0, len(requested))
	for _, t := range requested {
		if !slices.Contains(known, t) {
			return nil, fmt.Errorf("webhook: %q is not an event this cell emits", t)
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	slices.Sort(out)
	return out, nil
}

// Status is a subscription's state.
type Status string

// The subscription statuses.
const (
	// StatusActive receives deliveries.
	StatusActive Status = "ACTIVE"
	// StatusPaused receives none, and keeps nothing for later: an event
	// committed while a subscription is paused is not delivered when it
	// resumes. Replay is how a receiver catches up, deliberately.
	StatusPaused Status = "PAUSED"
	// StatusDisabled is final.
	StatusDisabled Status = "DISABLED"
)

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	return s == StatusActive || s == StatusPaused || s == StatusDisabled
}

// CanMoveTo reports whether a subscription in s may move to next.
func (s Status) CanMoveTo(next Status) bool {
	switch s {
	case StatusActive:
		return next == StatusPaused || next == StatusDisabled
	case StatusPaused:
		return next == StatusActive || next == StatusDisabled
	}
	return false
}

// StatusChange is one entry in a subscription's status history. Seq 1 is the
// ACTIVE status the subscription is created with.
type StatusChange struct {
	Webhook    id.WebhookID
	Seq        int
	Status     Status
	RecordedAt time.Time
	RecordedBy id.UserID
}

// ---------------------------------------------------------------------------
// the endpoint and the egress policy
// ---------------------------------------------------------------------------

// ValidateEndpoint checks an endpoint URL's shape and returns it normalised.
//
// It refuses what can be refused from the text alone: a scheme other than
// https (http only with allowInsecure, which is development's), credentials
// in the URL, a fragment, and a literal address the egress policy forbids. A
// host name is accepted here and policed again at connection time, by the
// address it resolves to then (Forbidden).
func ValidateEndpoint(raw string, allowInsecure bool) (string, error) {
	if raw == "" || len(raw) > MaxURLLength {
		return "", fmt.Errorf("webhook: an endpoint is a URL of at most %d characters", MaxURLLength)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("webhook: the endpoint is not a URL")
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && allowInsecure:
	default:
		return "", fmt.Errorf("webhook: an endpoint is https; %q is refused", u.Scheme)
	}
	if u.User != nil {
		return "", fmt.Errorf("webhook: an endpoint carries no credentials; the signature authenticates the delivery")
	}
	if u.Fragment != "" || u.Opaque != "" {
		return "", fmt.Errorf("webhook: an endpoint has no fragment")
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("webhook: the endpoint names no host")
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("webhook: the endpoint's port is not a port")
		}
	}
	if ip, err := netip.ParseAddr(host); err == nil && Forbidden(ip) && !allowInsecure {
		return "", fmt.Errorf("webhook: %s is not a public address", ip)
	}
	if !allowInsecure && (strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost")) {
		return "", fmt.Errorf("webhook: localhost is not a public address")
	}
	u.Host = strings.ToLower(u.Host)
	return u.String(), nil
}

// forbiddenPrefixes are the ranges a delivery never connects to: the cell's
// own network, the host, link-local (where cloud metadata services listen),
// and the ranges that are not unicast destinations at all.
var forbiddenPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{
		"0.0.0.0/8",       // "this network"
		"10.0.0.0/8",      // private
		"100.64.0.0/10",   // carrier-grade NAT
		"127.0.0.0/8",     // loopback
		"169.254.0.0/16",  // link-local, including 169.254.169.254
		"172.16.0.0/12",   // private
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // documentation
		"192.168.0.0/16",  // private
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
		"224.0.0.0/4",     // multicast
		"240.0.0.0/4",     // reserved, and broadcast
		"::/128",          // unspecified
		"::1/128",         // loopback
		"64:ff9b::/96",    // NAT64, which reaches IPv4 including the above
		"100::/64",        // discard
		"2001:db8::/32",   // documentation
		"fc00::/7",        // unique local
		"fe80::/10",       // link-local
		"ff00::/8",        // multicast
	} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// Forbidden reports whether a delivery may not connect to ip. An IPv4 address
// written as IPv6 is judged as the IPv4 address it is.
func Forbidden(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() {
		return true
	}
	for _, p := range forbiddenPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// signing — the Standard Webhooks scheme
// ---------------------------------------------------------------------------

// The headers a delivery carries.
const (
	HeaderID        = "webhook-id"
	HeaderTimestamp = "webhook-timestamp"
	HeaderSignature = "webhook-signature"
)

// SecretPrefix marks a signing secret, as the Standard Webhooks libraries
// expect it.
const SecretPrefix = "whsec_"

// SecretBytes is a signing secret's length.
const SecretBytes = 32

// EncodeSecret renders raw secret bytes in the form a receiver configures.
func EncodeSecret(raw []byte) string {
	return SecretPrefix + base64.StdEncoding.EncodeToString(raw)
}

// DecodeSecret reads a secret back.
func DecodeSecret(s string) ([]byte, error) {
	if !strings.HasPrefix(s, SecretPrefix) {
		return nil, fmt.Errorf("webhook: a signing secret starts %s", SecretPrefix)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, SecretPrefix))
	if err != nil || len(raw) != SecretBytes {
		return nil, fmt.Errorf("webhook: the signing secret is not %d bytes of base64", SecretBytes)
	}
	return raw, nil
}

// Sign is one signature: HMAC-SHA256 over "<id>.<unix seconds>.<body>",
// base64, versioned v1.
func Sign(secret []byte, messageID string, at time.Time, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(messageID))
	mac.Write([]byte{'.'})
	mac.Write([]byte(strconv.FormatInt(at.Unix(), 10)))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// SignatureHeader signs under every secret, space-separated: during a
// rotation a delivery is verifiable with the old secret and the new one, so a
// receiver can switch at its own pace.
func SignatureHeader(secrets [][]byte, messageID string, at time.Time, body []byte) string {
	sigs := make([]string, len(secrets))
	for i, s := range secrets {
		sigs[i] = Sign(s, messageID, at, body)
	}
	return strings.Join(sigs, " ")
}

// Verify reports whether any signature in header is body's under secret. It
// is the receiver's side, kept here so the scheme is tested from both ends.
func Verify(secret []byte, messageID string, at time.Time, body []byte, header string) bool {
	want := Sign(secret, messageID, at, body)
	for _, sig := range strings.Fields(header) {
		if hmac.Equal([]byte(sig), []byte(want)) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// secret rotation
// ---------------------------------------------------------------------------

// RotationOverlap is how long the previous secret keeps signing after a
// rotation, so a receiver has a day to switch.
const RotationOverlap = 24 * time.Hour

// SecretVersion is one version of a subscription's signing secret. The
// secret itself is held encrypted; this package never sees ciphertext.
//
// Versions are append-only. A rotation does not end the old version by
// editing it; the new version says when the one before it retires.
type SecretVersion struct {
	Webhook   id.WebhookID
	Version   int
	CreatedAt time.Time
	CreatedBy id.UserID
	// RetiresPreviousAt is when the version before this one stops signing.
	// Zero on version 1.
	RetiresPreviousAt time.Time
}

// Signing returns the versions that sign at now, newest first: the latest,
// and the one before it while its overlap lasts.
func Signing(versions []SecretVersion, now time.Time) []int {
	if len(versions) == 0 {
		return nil
	}
	vs := slices.Clone(versions)
	slices.SortFunc(vs, func(a, b SecretVersion) int { return b.Version - a.Version })
	out := []int{vs[0].Version}
	if len(vs) > 1 && now.Before(vs[0].RetiresPreviousAt) {
		out = append(out, vs[1].Version)
	}
	return out
}

// ---------------------------------------------------------------------------
// deliveries
// ---------------------------------------------------------------------------

// DeliveryStatus is a delivery's state.
type DeliveryStatus string

// The delivery statuses.
const (
	// DeliveryPending is waiting for its next attempt.
	DeliveryPending DeliveryStatus = "PENDING"
	// DeliveryDelivered got a 2xx.
	DeliveryDelivered DeliveryStatus = "DELIVERED"
	// DeliveryDead exhausted its attempts, or its subscription stopped
	// receiving. It is the dead-letter state: kept, listed, and replayable.
	DeliveryDead DeliveryStatus = "DEAD"
)

// Valid reports whether s is a known status.
func (s DeliveryStatus) Valid() bool {
	return s == DeliveryPending || s == DeliveryDelivered || s == DeliveryDead
}

// Backoff is the wait after each failed attempt: attempt n (from 1) failing
// schedules the next attempt Backoff[n-1] later. Eight attempts span about a
// day, which outlasts an ordinary receiver outage without holding an event
// for a week.
var Backoff = []time.Duration{
	30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute,
	1 * time.Hour, 3 * time.Hour, 6 * time.Hour,
}

// MaxAttempts is the attempts a delivery gets before it is dead-lettered.
var MaxAttempts = len(Backoff) + 1

// AfterFailure is where a delivery goes when attempt n (from 1) fails at at:
// PENDING with its next attempt scheduled, or DEAD after the last.
func AfterFailure(n int, at time.Time) (DeliveryStatus, time.Time) {
	if n >= MaxAttempts {
		return DeliveryDead, time.Time{}
	}
	return DeliveryPending, at.Add(Backoff[n-1])
}

// Succeeded reports whether an HTTP status is a delivery. Only a 2xx is: a
// redirect is not followed, because following it would send the event to an
// address nobody subscribed.
func Succeeded(status int) bool { return status >= 200 && status < 300 }

// Delivery is one event on its way to one webhook.
type Delivery struct {
	ID       id.DeliveryID
	TenantID id.TenantID
	Webhook  id.WebhookID
	// EventID is the outbox row's key, sent as the message id: the receiver
	// deduplicates on it, so a replay is recognisably the same event.
	EventID   id.OutboxID
	EventType string
	// Body is the CloudEvent exactly as every attempt sends it.
	Body          []byte
	Status        DeliveryStatus
	Attempts      int
	NextAttemptAt time.Time
	CreatedAt     time.Time
	DeliveredAt   time.Time
	// ReplayOf names the delivery a replay repeats; nil on a fan-out.
	ReplayOf   *id.DeliveryID
	ReplayedBy id.UserID
}

// Attempt is one try at a delivery.
type Attempt struct {
	Delivery  id.DeliveryID
	N         int
	StartedAt time.Time
	Duration  time.Duration
	// StatusCode is the receiver's HTTP status; zero when no response
	// arrived.
	StatusCode int
	// Error is the transport's error, when there was one. Never the
	// receiver's response body, which is the receiver's data.
	Error string
}

// MaxAttemptError bounds a recorded error.
const MaxAttemptError = 500
