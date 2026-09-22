// Package secrets resolves a credential reference into a credential.
//
// ADR-0017 §2.4 is the rule this package exists to keep: configuration may name
// *where* a secret lives, never carry the secret. So ZTAX_DATABASE_URL_REF
// holds something like vault://cells/eu-west-1/db or local://postgres, and this
// package turns that into a DSN at process start. The resolved value is held in
// memory, never written to disk, never logged, and never placed back into the
// environment.
//
// Two resolvers exist today. A vault resolver is W1 lane B's work and slots in
// as a third; the interface is here so that the call site in cmd/ztax-core does
// not change when it does.
package secrets

import (
	"fmt"
	"os"
	"strings"
)

// Ref is a credential reference: a scheme and a path, never a credential.
type Ref struct {
	Scheme string
	Path   string
}

// ParseRef reads a reference.
func ParseRef(s string) (Ref, error) {
	scheme, path, ok := strings.Cut(s, "://")
	if !ok || scheme == "" || path == "" {
		return Ref{}, fmt.Errorf("secrets: %q is not a credential reference (want scheme://path)", s)
	}
	return Ref{Scheme: scheme, Path: path}, nil
}

// String renders the reference. It is safe to log, which is the entire point of
// references existing: an incident investigation can establish which credential
// a process was configured to use without the log holding the credential.
func (r Ref) String() string { return r.Scheme + "://" + r.Path }

// Resolver turns a reference into a credential.
type Resolver interface {
	Resolve(ref Ref) (string, error)
}

// EnvResolver resolves local:// references from the environment.
//
// This is development-only and it is exactly the pattern ADR-0017 §2.4 forbids
// in a cell — the credential is in an environment variable at rest, where a
// crash dump, a process listing or a debug endpoint can reach it. It exists so
// that `docker compose up` works on a laptop, and Resolve refuses outright
// outside development so it cannot be the thing that ships.
type EnvResolver struct {
	// Environment is the process's ZTAX_ENVIRONMENT.
	Environment string
}

// Resolve reads local://name from ZTAX_LOCAL_SECRET_NAME.
func (e EnvResolver) Resolve(ref Ref) (string, error) {
	if ref.Scheme != "local" {
		return "", fmt.Errorf("secrets: no resolver for scheme %q", ref.Scheme)
	}
	if e.Environment != "development" {
		// Fail closed, loudly, at startup. A cell that somehow reached
		// production carrying a local:// reference should refuse to start
		// rather than run on a credential from its own environment.
		return "", fmt.Errorf("secrets: local:// references are refused in environment %q (ADR-0017 §2.4)", e.Environment)
	}
	name := "ZTAX_LOCAL_SECRET_" + strings.ToUpper(strings.NewReplacer("-", "_", "/", "_").Replace(ref.Path))
	value := os.Getenv(name)
	if value == "" {
		return "", fmt.Errorf("secrets: %s is not set, so %s cannot be resolved", name, ref)
	}
	return value, nil
}

// Resolve is the entry point cmd/ztax-core calls.
func Resolve(resolver Resolver, reference string) (string, error) {
	ref, err := ParseRef(reference)
	if err != nil {
		return "", err
	}
	value, err := resolver.Resolve(ref)
	if err != nil {
		// The reference is safe to name in the error; the value is not, and is
		// not in it.
		return "", fmt.Errorf("resolve %s: %w", ref, err)
	}
	return value, nil
}
