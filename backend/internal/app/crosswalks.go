package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/crosswalk"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/registration"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Vendor crosswalks (Doc 02 WF-06/WF-07 VXC; Doc 12 FED-04). The rules are
// internal/domain/crosswalk's; this is where a crosswalk meets the people
// acting and the store.
//
// The service holds a crosswalk repository and nothing else: no rule,
// decision, classification or fiscal repository. A crosswalk translates a
// vendor's code into a canonical service code for a person to classify
// with; it is never read by the determination engine and never becomes
// fiscal authority (Doc 02 §02; Doc 03 §01).
//
// Roles. OPERATOR and ANALYST write crosswalk versions and submit them (the
// integration steward). ANALYST reviews: approves, requests changes,
// rejects, suspends and reinstates — and never a version they wrote or
// submitted. AUDITOR and ADMIN read.

// CrosswalkService runs the vendor crosswalk library.
type CrosswalkService struct {
	store port.CrosswalkRepository
	tx    port.TxManager
	clock clock.Clock
	ids   idgen.Generator
}

// NewCrosswalkService wires the service.
func NewCrosswalkService(store port.CrosswalkRepository, tx port.TxManager, clk clock.Clock, ids idgen.Generator) *CrosswalkService {
	return &CrosswalkService{store: store, tx: tx, clock: clk, ids: ids}
}

var (
	crosswalkReaders   = []security.Role{security.RoleOperator, security.RoleAnalyst, security.RoleAuditor, security.RoleAdmin}
	crosswalkAuthors   = []security.Role{security.RoleOperator, security.RoleAnalyst}
	crosswalkReviewers = []security.Role{security.RoleAnalyst}
)

const crosswalkListCap = 500

func (s *CrosswalkService) now() time.Time { return s.clock.Now().UTC().Truncate(time.Microsecond) }

// CrosswalkView is a crosswalk as of now.
type CrosswalkView struct {
	crosswalk.Crosswalk
	AsOf time.Time
}

// CrosswalkVersionInput is one version to write.
type CrosswalkVersionInput struct {
	IntakeKey     string
	Rights        crosswalk.Rights
	EffectiveFrom time.Time
	EffectiveTo   time.Time
	Entries       []crosswalk.Entry
	// Submit submits the version for review as it is written.
	Submit bool
}

// CrosswalkInput is a new crosswalk and its first version.
type CrosswalkInput struct {
	Vendor        string
	Namespace     string
	VendorRelease string
	Title         string
	Version       CrosswalkVersionInput
}

func (s *CrosswalkService) inTx(ctx context.Context, fn func(context.Context) error) error {
	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(txCtx) }()
	if err := fn(txCtx); err != nil {
		return err
	}
	return tx.Commit(txCtx)
}

// prepare builds and validates a version, and its intake hash.
func prepareCrosswalkVersion(c crosswalk.Crosswalk, in CrosswalkVersionInput, number int) (crosswalk.Version, error) {
	v := crosswalk.Version{
		Number: number, IntakeKey: strings.TrimSpace(in.IntakeKey),
		Rights: crosswalk.Rights{Basis: in.Rights.Basis, Reference: strings.TrimSpace(in.Rights.Reference),
			EvidenceSHA256: strings.ToLower(strings.TrimSpace(in.Rights.EvidenceSHA256))},
	}
	if !in.Rights.ExpiresOn.IsZero() {
		v.Rights.ExpiresOn = registration.Day(in.Rights.ExpiresOn)
	}
	if !in.EffectiveFrom.IsZero() {
		v.EffectiveFrom = registration.Day(in.EffectiveFrom)
	}
	if !in.EffectiveTo.IsZero() {
		v.EffectiveTo = registration.Day(in.EffectiveTo)
	}
	v.Entries = make([]crosswalk.Entry, len(in.Entries))
	for i, e := range in.Entries {
		v.Entries[i] = e.Normalize()
	}
	if err := v.Validate(); err != nil {
		return crosswalk.Version{}, err
	}
	v.Entries = crosswalk.SortEntries(v.Entries)
	var err error
	if v.Digest, err = canonical.Sum(crosswalk.Content(c, v)); err != nil {
		return crosswalk.Version{}, internal(err, "The crosswalk version could not be hashed.")
	}
	if v.IntakeHash, err = canonical.Sum(crosswalk.Payload(c, v, in.Submit)); err != nil {
		return crosswalk.Version{}, internal(err, "The crosswalk version could not be hashed.")
	}
	return v, nil
}

// replay returns the crosswalk already written under key, if any, refusing
// the key's reuse for a different request.
func (s *CrosswalkService) replay(ctx context.Context, key string, hash canonical.Digest) (crosswalk.Crosswalk, bool, error) {
	if err := s.store.Lock(ctx, "intake/"+key); err != nil {
		return crosswalk.Crosswalk{}, false, err
	}
	prior, found, err := s.store.CrosswalkByIntakeKey(ctx, key)
	if err != nil || !found {
		return crosswalk.Crosswalk{}, false, err
	}
	for _, v := range prior.Versions {
		if v.IntakeKey == key {
			if !v.IntakeHash.Equal(hash) {
				return crosswalk.Crosswalk{}, false, keyReuse()
			}
			return prior, true, nil
		}
	}
	return crosswalk.Crosswalk{}, false, keyReuse()
}

// CreateCrosswalk writes a crosswalk and its first version, as a DRAFT or
// submitted for review — never approved. created is false when the
// Idempotency-Key already wrote this request.
func (s *CrosswalkService) CreateCrosswalk(ctx context.Context, in CrosswalkInput) (CrosswalkView, bool, error) {
	sc, err := requirePerson(ctx, crosswalkAuthors...)
	if err != nil {
		return CrosswalkView{}, false, err
	}
	key := strings.TrimSpace(in.Version.IntakeKey)
	if err := checkIntakeKey(key); err != nil {
		return CrosswalkView{}, false, err
	}
	c := crosswalk.Crosswalk{
		TenantID: sc.Tenant(), Vendor: strings.ToUpper(strings.TrimSpace(in.Vendor)),
		Namespace: strings.ToUpper(strings.TrimSpace(in.Namespace)), VendorRelease: strings.TrimSpace(in.VendorRelease),
		Title: strings.TrimSpace(in.Title), CreatedBy: sc.Subject(),
	}
	if err := c.Validate(); err != nil {
		return CrosswalkView{}, false, err
	}
	v, err := prepareCrosswalkVersion(c, in.Version, 1)
	if err != nil {
		return CrosswalkView{}, false, err
	}
	var (
		out     crosswalk.Crosswalk
		created bool
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		prior, found, err := s.replay(ctx, key, v.IntakeHash)
		if err != nil || found {
			out = prior
			return err
		}
		if err := s.store.Lock(ctx, "identity/"+c.Vendor+"/"+c.Namespace+"/"+c.VendorRelease); err != nil {
			return err
		}
		if other, exists, err := s.store.CrosswalkByIdentity(ctx, c.Vendor, c.Namespace, c.VendorRelease); err != nil {
			return err
		} else if exists {
			return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists,
				fmt.Sprintf("Crosswalk %s already maps %s %s release %s. A change is its next version; nothing is overwritten.", other.ID, c.Vendor, c.Namespace, c.VendorRelease))
		}
		cid, err := idgen.CrosswalkID(s.ids)
		if err != nil {
			return internal(err, "The crosswalk could not be recorded.")
		}
		now := s.now()
		c.ID, c.CreatedAt = cid, now
		v.CreatedAt, v.CreatedBy = now, sc.Subject()
		v.History = crosswalk.Open(sc.Subject(), now, in.Version.Submit)
		if err := s.store.CreateCrosswalk(ctx, c); err != nil {
			return err
		}
		if err := s.store.CreateVersion(ctx, cid, v); err != nil {
			return err
		}
		created = true
		out, err = s.store.Crosswalk(ctx, cid)
		return err
	})
	if err != nil {
		return CrosswalkView{}, false, err
	}
	return CrosswalkView{Crosswalk: out, AsOf: s.now()}, created, nil
}

// AddCrosswalkVersion writes the next version of a crosswalk. A crosswalk
// has at most one version in preparation or review at a time, so two
// people never race two corrections to approval.
func (s *CrosswalkService) AddCrosswalkVersion(ctx context.Context, crosswalkID id.CrosswalkID, in CrosswalkVersionInput) (CrosswalkView, bool, error) {
	sc, err := requirePerson(ctx, crosswalkAuthors...)
	if err != nil {
		return CrosswalkView{}, false, err
	}
	key := strings.TrimSpace(in.IntakeKey)
	if err := checkIntakeKey(key); err != nil {
		return CrosswalkView{}, false, err
	}
	var (
		out     crosswalk.Crosswalk
		created bool
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "crosswalk/"+crosswalkID.String()); err != nil {
			return err
		}
		c, err := s.store.Crosswalk(ctx, crosswalkID)
		if err != nil {
			return err
		}
		latest, _ := c.Latest()
		v, err := prepareCrosswalkVersion(c, in, latest.Number+1)
		if err != nil {
			return err
		}
		prior, found, err := s.replay(ctx, key, v.IntakeHash)
		if err != nil || found {
			if found && prior.ID != crosswalkID {
				return keyReuse()
			}
			out = prior
			return err
		}
		if open, ok := c.Open(); ok {
			return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				fmt.Sprintf("Version %d is still %s. Finish or withdraw it before writing another.", open.Number, open.Status))
		}
		now := s.now()
		v.CreatedAt, v.CreatedBy = now, sc.Subject()
		v.History = crosswalk.Open(sc.Subject(), now, in.Submit)
		if err := s.store.CreateVersion(ctx, crosswalkID, v); err != nil {
			return err
		}
		created = true
		out, err = s.store.Crosswalk(ctx, crosswalkID)
		return err
	})
	if err != nil {
		return CrosswalkView{}, false, err
	}
	return CrosswalkView{Crosswalk: out, AsOf: s.now()}, created, nil
}

// Crosswalks lists crosswalks, most recently created first, optionally of
// one vendor and with a version in one status.
func (s *CrosswalkService) Crosswalks(ctx context.Context, vendor string, status crosswalk.Status, limit int) ([]CrosswalkView, error) {
	if _, err := requireRoleOrSystem(ctx, crosswalkReaders...); err != nil {
		return nil, err
	}
	all, err := s.store.Crosswalks(ctx, crosswalkListCap)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > crosswalkListCap {
		limit = crosswalkListCap
	}
	vendor = strings.ToUpper(strings.TrimSpace(vendor))
	now := s.now()
	out := []CrosswalkView{}
	for _, c := range all {
		if len(out) >= limit {
			break
		}
		if vendor != "" && c.Vendor != vendor {
			continue
		}
		if status != "" && !hasVersionIn(c, status) {
			continue
		}
		out = append(out, CrosswalkView{Crosswalk: c, AsOf: now})
	}
	return out, nil
}

func hasVersionIn(c crosswalk.Crosswalk, status crosswalk.Status) bool {
	for _, v := range c.Versions {
		if v.Status == status {
			return true
		}
	}
	return false
}

// Crosswalk reads one crosswalk with every version.
func (s *CrosswalkService) Crosswalk(ctx context.Context, crosswalkID id.CrosswalkID) (CrosswalkView, error) {
	if _, err := requireRoleOrSystem(ctx, crosswalkReaders...); err != nil {
		return CrosswalkView{}, err
	}
	c, err := s.store.Crosswalk(ctx, crosswalkID)
	if err != nil {
		return CrosswalkView{}, err
	}
	return CrosswalkView{Crosswalk: c, AsOf: s.now()}, nil
}

// TransitionCrosswalkVersion moves one version to the status a person
// names, against the revision they read. Submission and withdrawal are the
// author's; everything else is a reviewer's. Approving a version
// supersedes the version in use before it, in the same transaction.
func (s *CrosswalkService) TransitionCrosswalkVersion(ctx context.Context, crosswalkID id.CrosswalkID, number int,
	to crosswalk.Status, reason string, revision int) (CrosswalkView, error) {
	roles := crosswalkReviewers
	if to == crosswalk.StatusInReview || to == crosswalk.StatusWithdrawn {
		roles = crosswalkAuthors
	}
	sc, err := requirePerson(ctx, roles...)
	if err != nil {
		return CrosswalkView{}, err
	}
	var out crosswalk.Crosswalk
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "crosswalk/"+crosswalkID.String()); err != nil {
			return err
		}
		c, err := s.store.Crosswalk(ctx, crosswalkID)
		if err != nil {
			return err
		}
		v, ok := c.Version(number)
		if !ok {
			return errs.New(errs.CategoryNotFound, errs.ReasonNotFound, fmt.Sprintf("Crosswalk %s has no version %d.", crosswalkID, number))
		}
		now := s.now()
		prev, hadPrev := c.Current()
		e, err := v.Transition(to, reason, revision, sc.Subject(), now, registration.Day(now))
		if err != nil {
			return err
		}
		if err := s.store.AppendEvent(ctx, crosswalkID, number, e); err != nil {
			return err
		}
		if e.Kind == crosswalk.EventApproved && hadPrev && prev.Number != number {
			se, err := prev.Supersede(number, sc.Subject(), now)
			if err != nil {
				return err
			}
			if err := s.store.AppendEvent(ctx, crosswalkID, prev.Number, se); err != nil {
				return err
			}
		}
		out, err = s.store.Crosswalk(ctx, crosswalkID)
		return err
	})
	if err != nil {
		return CrosswalkView{}, err
	}
	return CrosswalkView{Crosswalk: out, AsOf: s.now()}, nil
}

// CrosswalkLookup is one external code translated, advisory only.
type CrosswalkLookup struct {
	Crosswalk id.CrosswalkID
	// Version is the version resolved against; zero when there was none.
	Version int
	Code    string
	On      time.Time
	crosswalk.Lookup
	AsOf time.Time
}

// LookupCrosswalk translates one external code, exactly, for a transaction
// on day: against the version in use, or against a named approved or
// superseded version to reproduce a historical translation. A draft, a
// version in review and a rejected one are never resolved against.
func (s *CrosswalkService) LookupCrosswalk(ctx context.Context, crosswalkID id.CrosswalkID, code string, day time.Time,
	number int) (CrosswalkLookup, error) {
	if _, err := requireRoleOrSystem(ctx, crosswalkReaders...); err != nil {
		return CrosswalkLookup{}, err
	}
	if code == "" || len(code) > crosswalk.MaxCode {
		return CrosswalkLookup{}, errs.Invalid("code", errs.ReasonInvalidValue, "Name the external code exactly, at most 128 characters.")
	}
	c, err := s.store.Crosswalk(ctx, crosswalkID)
	if err != nil {
		return CrosswalkLookup{}, err
	}
	now := s.now()
	if day.IsZero() {
		day = now
	}
	day = registration.Day(day)
	out := CrosswalkLookup{Crosswalk: crosswalkID, Code: code, On: day, AsOf: now}
	var v crosswalk.Version
	var ok bool
	if number > 0 {
		v, ok = c.Version(number)
		if ok && v.Status != crosswalk.StatusApproved && v.Status != crosswalk.StatusSuspended && v.Status != crosswalk.StatusSuperseded {
			return CrosswalkLookup{}, errs.Invalid("version", errs.ReasonInvalidValue,
				fmt.Sprintf("Version %d is %s. Only a version that was approved is ever resolved against.", number, v.Status))
		}
		if !ok {
			return CrosswalkLookup{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, fmt.Sprintf("Crosswalk %s has no version %d.", crosswalkID, number))
		}
	} else {
		v, ok = c.Current()
	}
	if !ok {
		out.Lookup = crosswalk.Lookup{Resolution: crosswalk.ResolutionUnmapped, Candidates: []crosswalk.Entry{},
			Detail: "No version of this crosswalk has been approved, so nothing is translated."}
		return out, nil
	}
	out.Version = v.Number
	out.Lookup = v.Resolve(code, day, registration.Day(now))
	return out, nil
}
