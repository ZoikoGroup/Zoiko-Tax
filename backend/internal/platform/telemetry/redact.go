// Package telemetry is the cell's operational signal: logs now, OpenTelemetry
// traces and metrics as ADR-0015 §2.1 lands.
//
// Telemetry is not evidence (ADR-0015 §2.4). Evidence is sealed, retained under
// statute and read to prove what was decided; telemetry is lossy, short-lived
// and read to find out how the system behaved. Nothing here writes anything
// a decision depends on.
package telemetry

import (
	"context"
	"log/slog"
	"regexp"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// NewLogHandler is the handler every binary's logger is built on: the
// redaction backstop, with trace correlation. ADR-0015 §2.7: every record
// carries the trace id and span id, so a log line and the span that produced
// it can be joined without either carrying data.
func NewLogHandler(next slog.Handler) slog.Handler {
	return NewRedactingHandler(correlating{next: next})
}

// correlating adds trace_id and span_id from the record's context.
type correlating struct{ next slog.Handler }

func (c correlating) Enabled(ctx context.Context, l slog.Level) bool { return c.next.Enabled(ctx, l) }

func (c correlating) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r = r.Clone()
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return c.next.Handle(ctx, r)
}

func (c correlating) WithAttrs(attrs []slog.Attr) slog.Handler {
	return correlating{next: c.next.WithAttrs(attrs)}
}

func (c correlating) WithGroup(name string) slog.Handler {
	return correlating{next: c.next.WithGroup(name)}
}

// RedactingHandler is the log-side backstop of ADR-0015 §2.2.
//
// The mechanism is structural: a privacy-classified value travels as a
// privacy.Value, whose only log rendering is its redacted form, so it is
// withheld wherever it is logged without anyone remembering to withhold it.
// This handler is the second line, for the case where someone logs a raw
// string anyway. It withholds:
//
//   - attributes whose key names personal data or a secret (the denylist
//     ADR-0015 §2.2 retains as a backstop);
//   - attributes whose key names a fiscal amount, which never appear in
//     telemetry at all (ADR-0015 §2.3);
//   - email addresses and bearer credentials found inside any string value or
//     message, which is how an error string carries data past a key denylist.
//
// It is deliberately a backstop and not the mechanism. A denylist protects the
// keys somebody thought of; the collector-side denylist is the third line.
type RedactingHandler struct {
	next slog.Handler
}

// NewRedactingHandler wraps next.
func NewRedactingHandler(next slog.Handler) *RedactingHandler {
	return &RedactingHandler{next: next}
}

// Enabled implements slog.Handler.
func (h *RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle implements slog.Handler.
func (h *RedactingHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, scrubString(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redactAttr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

// WithAttrs implements slog.Handler. Attributes bound with Logger.With are
// redacted once, here, rather than on every record.
func (h *RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		redacted[i] = redactAttr(a)
	}
	return &RedactingHandler{next: h.next.WithAttrs(redacted)}
}

// WithGroup implements slog.Handler.
func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{next: h.next.WithGroup(name)}
}

// Key tokens that name personal data or a secret. A key is split on '.', '_'
// and '-' and matched token by token, so "admin_email", "user.email" and
// "client-ip" are all caught and "emailed_count" is not.
var personalTokens = map[string]string{
	"password": "secret", "passwd": "secret", "secret": "secret", "token": "secret",
	"authorization": "secret", "cookie": "secret", "apikey": "secret", "credential": "secret",
	"private": "secret", "verifier": "secret", "salt": "secret",

	"email": "email", "phone": "phone", "msisdn": "phone", "imsi": "telecom", "imei": "telecom",
	"iccid": "telecom", "ip": "network", "useragent": "network", "agent": "network",
	"address": "address", "street": "address", "postcode": "address", "zip": "address",
	"latitude": "location", "longitude": "location", "lat": "location", "lon": "location",
	"lng": "location", "gps": "location", "coordinates": "location",
	"iban": "financial", "card": "financial", "pan": "financial",
	"ssn": "government_id", "passport": "government_id",
}

// Key tokens that name a fiscal amount (ADR-0015 §2.3). Only the key's last
// token is tested, so "tax" in "tax_rule_id" does not match while "line.tax"
// and "total_tax" do.
var fiscalTokens = map[string]bool{
	"amount": true, "net": true, "gross": true, "tax": true, "taxable": true, "total": true,
	"subtotal": true, "price": true, "fee": true, "levy": true, "balance": true, "base": true,
}

var (
	emailPattern  = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	bearerPattern = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=\-]+`)
)

func redactAttr(a slog.Attr) slog.Attr {
	// Resolve first, so a privacy.Value has already become its redacted form
	// and is not redacted a second time into something less informative.
	a.Value = a.Value.Resolve()

	if a.Value.Kind() == slog.KindGroup {
		group := a.Value.Group()
		redacted := make([]slog.Attr, len(group))
		for i, g := range group {
			redacted[i] = redactAttr(g)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(redacted...)}
	}

	if kind, ok := personalKind(a.Key); ok {
		if a.Value.Kind() == slog.KindString && strings.HasPrefix(a.Value.String(), "[redacted:") {
			return a // already a classified value's own rendering
		}
		return slog.String(a.Key, "[redacted:"+kind+"]")
	}
	if isFiscalKey(a.Key) {
		return slog.String(a.Key, "[redacted:fiscal]")
	}
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, scrubString(a.Value.String()))
	case slog.KindAny:
		// An error or a panic value renders through its own String/Error.
		// Scrub the rendering rather than trusting it.
		if err, ok := a.Value.Any().(error); ok {
			return slog.String(a.Key, scrubString(err.Error()))
		}
		if s, ok := a.Value.Any().(interface{ String() string }); ok {
			return slog.String(a.Key, scrubString(s.String()))
		}
	}
	return a
}

func tokens(key string) []string {
	return strings.FieldsFunc(strings.ToLower(key), func(r rune) bool {
		return r == '.' || r == '_' || r == '-' || r == ' '
	})
}

func personalKind(key string) (string, bool) {
	ts := tokens(key)
	for i, t := range ts {
		if kind, ok := personalTokens[t]; ok {
			return kind, true
		}
		// Two-token names that are one idea: "user_agent", "api_key".
		if i+1 < len(ts) {
			if kind, ok := personalTokens[t+ts[i+1]]; ok {
				return kind, true
			}
		}
	}
	return "", false
}

func isFiscalKey(key string) bool {
	ts := tokens(key)
	return len(ts) > 0 && fiscalTokens[ts[len(ts)-1]]
}

func scrubString(s string) string {
	if !strings.ContainsAny(s, "@") && !strings.Contains(strings.ToLower(s), "bearer") &&
		!strings.Contains(strings.ToLower(s), "basic") {
		return s // the common case costs one scan, not two regex passes
	}
	s = emailPattern.ReplaceAllString(s, "[redacted:email]")
	return bearerPattern.ReplaceAllString(s, "[redacted:credential]")
}
