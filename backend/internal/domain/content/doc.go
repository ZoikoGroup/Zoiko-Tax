// Package content is the content object model of ZTAX-CONT-001: the chain
// from an authority's instrument to the signed bundle a cell executes, the
// pack that bundle is released as, and the governance that has to be true of
// it before it is released.
//
//	AuthorityInstrument → SourceArtifact → Interpretation → RuleVersion → RuleBundle
//
// is the provenance chain ZTAX-DOM-001 (§13, §21.1) requires every
// authoritative decision to be able to walk back along. Each
// link is a type here, and each type carries the identifiers of the link
// before it, so the chain is a property of the data rather than of a lookup
// somebody remembers to do.
//
// # Why a domain package
//
// The layout already implies the answer. internal/content/{dsl,compile} is the
// compiler, which the depguard rule content-compiler-is-not-in-the-cell keeps
// out of every cell (ADR-0005 §2.1). internal/content/bundle is the on-disk
// format both sides read. Neither is the place for *what content is*: the
// object model, the package levels and their dependency rule, the pack
// lifecycle and the four-eyes rule are read by the compiler (to refuse a build),
// by the cell's loader (to refuse a bundle), and later by the content service
// and the evidence ledger (to record and report them). So they live in the
// domain, are pure — no I/O, no clock, no encoding — and are importable by all
// of those without dragging the compiler into a cell.
//
// The sourcing model of ZTAX-SRC-001 is a sibling package,
// internal/domain/sourcing, rather than part of this one: authority and licence
// are separate questions (SRC-001 §7, ZTAX-SRC-REQ-0006) and this package
// depends on the answer to the second without being allowed to give it.
//
// # Names
//
// The specification names these objects twice. CONT-001 §3 calls them
// SourceDocument, Instrument, InterpretationRecord and RuleDefinition;
// ZTAX-DOM-001 §13 (the content, rule and pack reference model) calls them
// SourceArtifact, AuthorityInstrument, Interpretation, RuleSemanticId and
// RuleVersion, and that is the vocabulary the provenance chain and the Build
// Plan use. This package uses the second set, and each type's comment names the
// CONT-001 §3 object it is.
package content

import "fmt"

// errorf keeps every error from this package prefixed the same way.
func errorf(format string, args ...any) error { return fmt.Errorf("content: "+format, args...) }
