package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"unicode/utf8"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
)

// The states FilePolicy logs. It logs when the state changes, never per call.
const (
	policyStateOK          = "ok"
	policyStateUnreadable  = "unreadable"
	policyStateEmpty       = "empty"
	policyStateUnparseable = "unparseable"
)

// FilePolicy is a PolicySource that follows the registry file's kill switch
// without a restart.
//
// What follows the file, and what does not. Only killed and global_kill are
// read again. The use cases, their ceilings, regions, data classes and
// suspended flag stay as they were loaded at start, so a live edit can narrow
// what a call may do and can never widen it. This is the same cut the Python
// Gateway makes (ztax_gateway.server._refresh_kill_state).
//
// How it reads. On every call it opens the file by path, reads the bytes and
// hashes them. There is no stat or modification-time shortcut: a kill edit can
// keep the file's size, and a mounted file's time can be coarse, and either
// would serve a stale "allowed". Opening by path each time also means a file
// that was replaced (a new inode, by rename) is read, never a handle to the old
// one. The bytes are parsed and the snapshot rebuilt only when the hash
// changes.
//
// Fail closed. A file that cannot be read, is empty, is not JSON, is not an
// object, or has the wrong type for global_kill or killed gives a snapshot with
// the global kill engaged, so every use case is refused. The cached hash is
// cleared on any failure, so a repaired file recovers on the next call even when
// its bytes are the same as the last good ones. A missing key means "not killed".
//
// The snapshot is an immutable ai.Policy, shared between callers. The mutex
// covers the hash and the snapshot, so concurrent calls see one or the other,
// never a mixture.
type FilePolicy struct {
	path     string
	log      *slog.Logger
	useCases []ai.UseCasePolicy // as loaded at start; never changed

	failedClosed ai.Policy // every use case killed

	mu      sync.Mutex
	digest  [sha256.Size]byte
	hasHash bool // digest describes the file the snapshot came from
	current ai.Policy
	state   string
}

// NewFilePolicy loads the registry strictly, as LoadPolicy does, and returns a
// source that then follows the kill switch. A file that cannot be loaded now is
// an error, so a bad file at start still stops the process.
func NewFilePolicy(path string, log *slog.Logger) (*FilePolicy, error) {
	if log == nil {
		log = slog.Default()
	}
	ucs, killed, global, err := loadRegistry(path)
	if err != nil {
		return nil, err
	}
	current, err := ai.NewPolicy(ucs, killed, global)
	if err != nil {
		return nil, err
	}
	failed, err := ai.NewPolicy(ucs, nil, true)
	if err != nil {
		return nil, err
	}
	// hasHash starts false, so the first call reads the file and parses it, and a
	// file changed since the load above is never served from the snapshot.
	return &FilePolicy{path: filepath.Clean(path), log: log, useCases: ucs, failedClosed: failed, current: current, state: policyStateOK}, nil
}

// Policy returns the current snapshot. It is called on every AI call.
func (f *FilePolicy) Policy() ai.Policy {
	f.mu.Lock()
	defer f.mu.Unlock()

	raw, err := os.ReadFile(f.path)
	if err != nil {
		return f.failClosed(policyStateUnreadable)
	}
	if len(raw) == 0 {
		return f.failClosed(policyStateEmpty)
	}
	digest := sha256.Sum256(raw)
	if f.hasHash && digest == f.digest {
		return f.current
	}
	global, killed, ok := parseKillState(raw)
	if !ok {
		return f.failClosed(policyStateUnparseable)
	}
	p, err := ai.NewPolicy(f.useCases, killed, global)
	if err != nil {
		// The use cases were validated at start, so this is not expected; refuse
		// everything rather than serve a snapshot nobody checked.
		return f.failClosed(policyStateUnparseable)
	}
	f.current, f.digest, f.hasHash = p, digest, true
	f.note(policyStateOK)
	return p
}

// failClosed engages the global kill and forgets the hash, so that the next
// readable file is parsed afresh. The caller holds the mutex.
func (f *FilePolicy) failClosed(state string) ai.Policy {
	f.current, f.hasHash = f.failedClosed, false
	f.note(state)
	return f.failedClosed
}

// note logs a change of state once. The text is fixed: it never carries the
// file's content or the OS error text. The caller holds the mutex.
func (f *FilePolicy) note(state string) {
	if state == f.state {
		return
	}
	f.state = state
	if state == policyStateOK {
		f.log.Info("ai policy file is readable again; the kill switch follows it")
		return
	}
	f.log.Error("ai policy file cannot be used; every AI use case is treated as killed", "state", state)
}

// parseKillState reads only global_kill and killed from the registry bytes. ok
// is false for anything unreadable: bytes that are not UTF-8, not JSON, not an
// object, a global_kill that is not a boolean, a killed that is not a list, or
// a killed element that is not a non-empty string. A missing key means "not
// killed", and an explicit null is the wrong type, as it is in Python.
func parseKillState(raw []byte) (global bool, killed []ai.AiUseCase, ok bool) {
	if !utf8.Valid(raw) {
		return false, nil, false
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
		return false, nil, false
	}
	if v, present := doc["global_kill"]; present {
		if err := json.Unmarshal(v, &global); err != nil || isNull(v) {
			return false, nil, false
		}
	}
	if v, present := doc["killed"]; present {
		var items []json.RawMessage
		if isNull(v) || json.Unmarshal(v, &items) != nil {
			return false, nil, false
		}
		for _, item := range items {
			var s string
			if isNull(item) || json.Unmarshal(item, &s) != nil || s == "" {
				return false, nil, false
			}
			killed = append(killed, ai.AiUseCase(s))
		}
	}
	return global, killed, true
}

func isNull(v json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(v), []byte("null")) }

var _ PolicySource = (*FilePolicy)(nil)
