package http

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
)

// limiter is a fixed-window counter per key, held in memory.
//
// It guards the public routes that make the cell send mail or try a
// credential, where one anonymous client could otherwise fill an inbox or
// grind through links. It is per process: a cell with several replicas
// allows each its own window, which bounds abuse rather than metering it, and
// that is the job here.
type limiter struct {
	max    int
	window time.Duration

	mu   sync.Mutex
	hits map[string]*windowCount
	now  func() time.Time
}

type windowCount struct {
	start time.Time
	n     int
}

// pruneAt is the number of keys at which expired windows are swept, so a
// stream of distinct keys cannot grow the map without bound.
const pruneAt = 10_000

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, hits: map[string]*windowCount{}, now: time.Now}
}

// allow counts one request for key, and reports whether it is within the
// limit — and if not, how long until the window resets.
func (l *limiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.hits) >= pruneAt {
		for k, w := range l.hits {
			if now.Sub(w.start) >= l.window {
				delete(l.hits, k)
			}
		}
	}
	w, ok := l.hits[key]
	if !ok || now.Sub(w.start) >= l.window {
		l.hits[key] = &windowCount{start: now, n: 1}
		return true, 0
	}
	if w.n >= l.max {
		return false, w.start.Add(l.window).Sub(now)
	}
	w.n++
	return true, 0
}

// limits are the windows on the public mail and credential routes.
type limits struct {
	resetByClient   *limiter
	resetByAccount  *limiter
	linkByClient    *limiter
	contactByClient *limiter
}

func newLimits() limits {
	return limits{
		resetByClient:   newLimiter(10, 15*time.Minute),
		resetByAccount:  newLimiter(3, 15*time.Minute),
		linkByClient:    newLimiter(20, 15*time.Minute),
		contactByClient: newLimiter(5, time.Hour),
	}
}

// refuseIfLimited counts the request against l under key and, past the limit,
// answers 429 with the seconds until the window resets.
func (rt *Router) refuseIfLimited(w http.ResponseWriter, r *http.Request, l *limiter, key string) bool {
	ok, wait := l.allow(key)
	if ok {
		return false
	}
	secs := int(wait.Round(time.Second) / time.Second)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeProblem(w, r, rt.log, errs.New(errs.CategoryPolicy, errs.ReasonRateLimited,
		"Too many requests. Retry after the interval given in Retry-After."))
	return true
}
