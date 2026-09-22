// Package authority holds concrete authority adapters.
//
// HTTPAdapter is the reference implementation, and it exists mainly to be the
// worked example of ADR-0016 §2.2 that every real adapter is written against.
// The interesting code is classify: the decision of which failures are
// uncertain and which are safely retryable is the whole adapter, and getting it
// wrong is how a return gets filed twice.
package authority

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/authority"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
)

// HTTPAdapter files over HTTP.
type HTTPAdapter struct {
	AdapterID authority.AdapterID
	// SubmitURL and ResolveURL are the authority's endpoints. ResolveURL may be
	// empty, which means the authority offers no query facility — and then an
	// uncertain attempt stays uncertain until a person establishes what
	// happened, which is the honest outcome rather than a guess.
	SubmitURL  string
	ResolveURL string

	Client *http.Client
	Clock  clock.Clock
}

// ID names the adapter.
func (a *HTTPAdapter) ID() authority.AdapterID { return a.AdapterID }

// Submit files a return.
func (a *HTTPAdapter) Submit(ctx context.Context, s authority.Submission) (authority.Outcome, error) {
	if a.SubmitURL == "" {
		// A configuration fault, and it definitely did not reach the authority,
		// so it is an error rather than an uncertain outcome.
		return authority.Outcome{}, fmt.Errorf("authority: adapter %s has no submit URL", a.AdapterID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.SubmitURL, bytes.NewReader(s.Payload))
	if err != nil {
		return authority.Outcome{}, fmt.Errorf("authority: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/xml")
	// The digest travels so the authority's own logs and ours can be correlated
	// during a dispute, and so Resolve can match what they hold against what we
	// sent.
	req.Header.Set("X-ZoikoTax-Request-Digest", s.RequestDigest)

	resp, err := a.client().Do(req)
	if err != nil {
		return a.classify(err), nil
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		ref := resp.Header.Get("X-Authority-Reference")
		if ref == "" {
			// A 2xx with no reference is not an acceptance we can prove later.
			// Treating it as accepted would record a filing we cannot evidence;
			// treating it as rejected would invite a retry of something that
			// probably landed. Uncertain is the only honest reading.
			return authority.Outcome{
				State:     authority.StateUncertain,
				Reason:    errs.ReasonUnavailable,
				Detail:    "The authority accepted the submission without returning a reference.",
				SettledAt: a.now(),
			}, nil
		}
		return authority.Outcome{
			State:        authority.StateAccepted,
			AuthorityRef: ref,
			SettledAt:    a.now(),
		}, nil

	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		// A definite refusal. The authority processed the request and declined
		// it, so nothing was filed and a correction can be made.
		return authority.Outcome{
			State:     authority.StateRejected,
			Reason:    errs.ReasonInvalidValue,
			Detail:    fmt.Sprintf("The authority rejected the submission with status %d.", resp.StatusCode),
			SettledAt: a.now(),
		}, nil

	default:
		// A 5xx is the case the whole package is written around. The authority
		// may have accepted and failed to tell us, so this is emphatically not
		// a retry.
		return authority.Outcome{
			State:     authority.StateUncertain,
			Reason:    errs.ReasonUnavailable,
			Detail:    fmt.Sprintf("The authority returned status %d; whether the submission was accepted is unknown.", resp.StatusCode),
			SettledAt: a.now(),
		}, nil
	}
}

// Resolve asks the authority what it holds, without sending a filing.
func (a *HTTPAdapter) Resolve(ctx context.Context, s authority.Submission) (authority.Outcome, error) {
	if a.ResolveURL == "" {
		// No query facility. Saying so plainly is better than a guess: the
		// resolution is a person reading a portal, recorded through the same
		// path, and the attempt stays uncertain until they do.
		return authority.Outcome{
			State:     authority.StateUncertain,
			Reason:    errs.ReasonReviewRequired,
			Detail:    "This authority offers no submission query. Resolution requires a person to establish what was filed.",
			SettledAt: a.now(),
		}, nil
	}

	endpoint, err := url.Parse(a.ResolveURL)
	if err != nil {
		return authority.Outcome{}, fmt.Errorf("authority: parse resolve url: %w", err)
	}
	q := endpoint.Query()
	q.Set("digest", s.RequestDigest)
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return authority.Outcome{}, fmt.Errorf("authority: build request: %w", err)
	}

	resp, err := a.client().Do(req)
	if err != nil {
		// A failed query changes nothing: the attempt was uncertain and still
		// is. This is why Resolve is safe to call repeatedly — it is a read,
		// and a failed read leaves the world where it was.
		return authority.Outcome{
			State:     authority.StateUncertain,
			Reason:    errs.ReasonUnavailable,
			Detail:    "The authority could not be queried. The submission remains uncertain.",
			SettledAt: a.now(),
		}, nil
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		if ref := resp.Header.Get("X-Authority-Reference"); ref != "" {
			return authority.Outcome{
				State:        authority.StateAccepted,
				AuthorityRef: ref,
				Detail:       "The authority holds this submission.",
				SettledAt:    a.now(),
			}, nil
		}
		return authority.Outcome{
			State: authority.StateUncertain, Reason: errs.ReasonReviewRequired,
			Detail:    "The authority answered without naming a reference.",
			SettledAt: a.now(),
		}, nil

	case http.StatusNotFound:
		// The authority does not hold it, so it was never filed and the
		// obligation can be prepared again. This is the one path that converts
		// an uncertain attempt into something safely retryable, and it does so
		// on the authority's own word rather than on an assumption.
		return authority.Outcome{
			State: authority.StateRejected, Reason: errs.ReasonNotFound,
			Detail:    "The authority holds no submission with this digest; it was not filed.",
			SettledAt: a.now(),
		}, nil

	default:
		return authority.Outcome{
			State: authority.StateUncertain, Reason: errs.ReasonUnavailable,
			Detail:    fmt.Sprintf("The authority query returned status %d.", resp.StatusCode),
			SettledAt: a.now(),
		}, nil
	}
}

// classify turns a transport error into an outcome.
//
// This is the function to read carefully when writing a new adapter. The
// distinction it draws is not between kinds of error but between "the request
// certainly never left" and "the request may have arrived", and only the first
// is safe to retry:
//
//   - A DNS failure or a refused connection means nothing was written to a
//     socket. Nothing reached the authority, so the attempt is still PREPARED
//     and can be sent again.
//   - A timeout, a reset, or a closed connection mid-response means bytes were
//     written and we do not know what happened to them. That is UNCERTAIN, and
//     no amount of "it was probably fine" justifies a retry.
//
// When in doubt the answer is UNCERTAIN. The cost of a wrong UNCERTAIN is a
// person spending ten minutes checking a portal; the cost of a wrong PREPARED
// is a duplicate filing.
func (a *HTTPAdapter) classify(err error) authority.Outcome {
	now := a.now()

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return authority.Outcome{
			State: authority.StatePrepared, Reason: errs.ReasonUnavailable,
			Detail:    "The authority's address could not be resolved. Nothing was sent.",
			SettledAt: now,
		}
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return authority.Outcome{
			State: authority.StatePrepared, Reason: errs.ReasonUnavailable,
			Detail:    "The connection to the authority was refused. Nothing was sent.",
			SettledAt: now,
		}
	}

	return authority.Outcome{
		State: authority.StateUncertain, Reason: errs.ReasonUnavailable,
		Detail:    "The submission was sent and no response was received; whether it was accepted is unknown.",
		SettledAt: now,
	}
}

func (a *HTTPAdapter) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	// A bounded default. An unbounded filing request is one that holds a
	// connection until something else times out, and the something else is
	// usually the caller, which turns a slow authority into an outage.
	return &http.Client{Timeout: 60 * time.Second}
}

func (a *HTTPAdapter) now() time.Time {
	if a.Clock != nil {
		return a.Clock.Now()
	}
	return clock.System{}.Now()
}
