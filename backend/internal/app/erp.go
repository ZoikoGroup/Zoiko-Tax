package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/erp"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/outbox"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// The ERP journal bridge and GL reconciliation (LED-23, LED-18; R6 of
// ZTAX-FIN-001 §17). The rules are internal/domain/erp's; this is where a
// bridge meets the subledger's posted journals, the outbox and the store.
//
// Dispatch leaves through the transactional outbox, in the transaction that
// records the attempt (ADR-0014 §2.1): the gl.export-dispatched event exists
// exactly when the attempt does, and a connector subscribed to it — through a
// webhook endpoint, say — reads the batch and sends it to the ERP under the
// batch's one external reference. No such connector is wired to a real ERP
// here, and nothing here invents the ERP's answer: receipts, postings,
// rejections, unknown outcomes and status queries are recorded by a person,
// with the ERP's references as evidence.
//
// Idempotency is natural rather than keyed. A batch is its own identity: a
// dispatch of a batch already sent is a no-op, an ERP answer recorded twice
// is one event, and preparing again while the batch it would make is already
// prepared returns that batch.

// The ERP events.
const (
	EventGLExportDispatched     = outbox.TypeNamespace + ".gl.export-dispatched"
	SchemaGLExportDispatchedRef = "ztax:events/gl-export-dispatched/1.0.0"
)

// ERPService runs ERP journal bridges and GL reconciliations.
type ERPService struct {
	store    port.ERPRepository
	entities port.LegalEntityRepository
	outbox   port.OutboxWriter
	tx       port.TxManager
	clock    clock.Clock
	ids      idgen.Generator
}

// NewERPService wires the service.
func NewERPService(store port.ERPRepository, entities port.LegalEntityRepository, out port.OutboxWriter,
	tx port.TxManager, clk clock.Clock, ids idgen.Generator) *ERPService {
	return &ERPService{store: store, entities: entities, outbox: out, tx: tx, clock: clk, ids: ids}
}

func (s *ERPService) now() time.Time { return s.clock.Now().UTC().Truncate(time.Microsecond) }

// The roles. LED-23 names the ERP Integrations Lead and the Finance Checker:
// operators configure bridges, prepare batches and record what the ERP
// reported; an operator or analyst other than the preparer authorises a
// dispatch; all three fiscal roles read. ADMIN reads no fiscal record here,
// as elsewhere.
var (
	erpReaders     = []security.Role{security.RoleOperator, security.RoleAnalyst, security.RoleAuditor}
	erpOperators   = []security.Role{security.RoleOperator}
	erpCheckers    = []security.Role{security.RoleOperator, security.RoleAnalyst}
	erpResponders  = []security.Role{security.RoleOperator, security.RoleAnalyst}
	erpReconcilers = []security.Role{security.RoleOperator}
)

// ERPBridgeInput is a bridge to create.
type ERPBridgeInput struct {
	Name        string
	Vendor      erp.Vendor
	CompanyCode string
	LegalEntity id.LegalEntityID // zero for the tenant's default entity
}

// ERPMappingInput is a bridge's next mapping version.
type ERPMappingInput struct {
	ChartVersion string
	Accounts     []erp.AccountMap
}

// ERPPrepareInput is an export batch to prepare.
type ERPPrepareInput struct {
	Period      string
	Currency    string
	PostingDate string // YYYY-MM-DD
}

// GLSnapshotBalance is one reported GL balance; amounts are canonical decimal
// strings, parsed here and nowhere else.
type GLSnapshotBalance struct {
	GLAccount string
	Debits    string
	Credits   string
}

// GLSnapshotInput is a set of GL balances an ERP reported.
type GLSnapshotInput struct {
	Period       string
	Currency     string
	AsOf         string // RFC 3339
	SourceRef    string
	SourceDigest string
	Balances     []GLSnapshotBalance
}

// ERPBatchView is a batch with the correction task its rejection opened, if it
// was rejected.
type ERPBatchView struct {
	Batch      erp.Batch
	Correction *erp.CorrectionTask
}

// CreateBridge records a bridge from a member legal entity's subledger to
// one customer general ledger. It exports nothing until a mapping is
// recorded.
func (s *ERPService) CreateBridge(ctx context.Context, in ERPBridgeInput) (erp.Bridge, error) {
	sc, err := requirePerson(ctx, erpOperators...)
	if err != nil {
		return erp.Bridge{}, err
	}
	b := erp.Bridge{
		TenantID: sc.Tenant(), Name: strings.TrimSpace(in.Name), Vendor: in.Vendor,
		CompanyCode: strings.TrimSpace(in.CompanyCode), CreatedBy: sc.Subject(),
	}
	var out erp.Bridge
	err = s.inTx(ctx, func(ctx context.Context) error {
		if in.LegalEntity.IsZero() {
			le, err := s.entities.Default(ctx)
			if err != nil {
				return err
			}
			b.LegalEntity = le.ID
		} else {
			le, err := s.entities.ByID(ctx, in.LegalEntity)
			if err != nil {
				return err
			}
			b.LegalEntity = le.ID
		}
		if err := b.Validate(); err != nil {
			return err
		}
		bid, err := idgen.ERPBridgeID(s.ids)
		if err != nil {
			return internal(err, "The bridge could not be recorded.")
		}
		b.ID, b.CreatedAt = bid, s.now()
		if err := s.store.CreateBridge(ctx, b); err != nil {
			return err
		}
		out, err = s.store.Bridge(ctx, bid)
		return err
	})
	return out, err
}

// RecordMapping records a bridge's next account mapping version. A mapping
// identical to the current one records nothing and returns the bridge as it
// is, with false. A batch prepared under an earlier version is not
// dispatched after this (QA-LED-068).
func (s *ERPService) RecordMapping(ctx context.Context, bridgeID id.ERPBridgeID, in ERPMappingInput) (erp.Bridge, bool, error) {
	sc, err := requirePerson(ctx, erpOperators...)
	if err != nil {
		return erp.Bridge{}, false, err
	}
	var (
		out     erp.Bridge
		created bool
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "bridge/"+bridgeID.String()); err != nil {
			return err
		}
		b, err := s.store.Bridge(ctx, bridgeID)
		if err != nil {
			return err
		}
		m := erp.Mapping{ChartVersion: strings.TrimSpace(in.ChartVersion), Version: len(b.Mappings) + 1,
			RecordedBy: sc.Subject()}
		for _, am := range in.Accounts {
			m.Accounts = append(m.Accounts, erp.AccountMap{Account: am.Account, GLAccount: strings.TrimSpace(am.GLAccount)})
		}
		m = m.Normalise()
		if err := m.Validate(); err != nil {
			return err
		}
		if cur, ok := b.Current(); ok && cur.ChartVersion == m.ChartVersion && slices.Equal(cur.Accounts, m.Accounts) {
			out = b
			return nil
		}
		if m, err = m.Seal(b.ID); err != nil {
			return internal(err, "The mapping could not be sealed.")
		}
		m.RecordedAt = s.now()
		if err := s.store.AppendMapping(ctx, b.ID, m); err != nil {
			return err
		}
		created = true
		out, err = s.store.Bridge(ctx, b.ID)
		return err
	})
	return out, created, err
}

// Bridges lists the tenant's bridges with their mapping versions.
func (s *ERPService) Bridges(ctx context.Context) ([]erp.Bridge, error) {
	if _, err := requireRoleOrSystem(ctx, erpReaders...); err != nil {
		return nil, err
	}
	return s.store.Bridges(ctx)
}

// Prepare seals an export batch of a bridge: every posted journal of the
// bridge's legal entity, the period and the currency that is not already in
// a live batch, mapped by the current mapping version. A journal line on an
// unmapped account holds the whole export (ERP_MAPPING_INCOMPLETE).
//
// When nothing is left to export and a prepared batch with the same posting
// date and mapping version already holds the journals, that batch is
// returned with false: preparing is safe to retry.
func (s *ERPService) Prepare(ctx context.Context, bridgeID id.ERPBridgeID, in ERPPrepareInput) (ERPBatchView, bool, error) {
	sc, err := requirePerson(ctx, erpOperators...)
	if err != nil {
		return ERPBatchView{}, false, err
	}
	period := strings.TrimSpace(in.Period)
	if !erp.ValidPeriod(period) {
		return ERPBatchView{}, false, errs.Invalid("period", errs.ReasonInvalidValue, "The period is a month, YYYY-MM.")
	}
	cur := strings.ToUpper(strings.TrimSpace(in.Currency))
	if !erp.ValidCurrency(cur) {
		return ERPBatchView{}, false, errs.Invalid("currency", errs.ReasonInvalidValue, "The currency is an ISO 4217 code, such as USD.")
	}
	posting, err := time.Parse(time.DateOnly, strings.TrimSpace(in.PostingDate))
	if err != nil {
		return ERPBatchView{}, false, errs.Invalid("postingDate", errs.ReasonInvalidValue, "The posting date is a calendar date, YYYY-MM-DD.")
	}
	var (
		out     ERPBatchView
		created bool
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "bridge/"+bridgeID.String()); err != nil {
			return err
		}
		b, err := s.store.Bridge(ctx, bridgeID)
		if err != nil {
			return err
		}
		mapping, ok := b.Current()
		if !ok {
			return errs.New(errs.CategoryConflict, erp.ReasonMappingIncomplete,
				"This bridge has no account mapping yet. Record one before preparing an export.")
		}
		journals, err := s.store.PeriodJournals(ctx, b.LegalEntity, period, fiscal.Currency(cur))
		if err != nil {
			return err
		}
		batches, err := s.store.Batches(ctx, b.ID, erp.Filter{Period: period, Currency: fiscal.Currency(cur)})
		if err != nil {
			return err
		}
		held := map[id.JournalID]bool{}
		for _, bt := range batches {
			if bt.Status.Live() {
				for _, j := range bt.Journals {
					held[j] = true
				}
			}
		}
		eligible := slices.DeleteFunc(slices.Clone(journals), func(j subledger.Journal) bool { return held[j.ID] })
		if len(eligible) == 0 {
			for _, bt := range batches {
				if bt.Status == erp.StatusPrepared && bt.MappingVersion == mapping.Version && bt.PostingDate.Equal(posting) {
					out = ERPBatchView{Batch: bt}
					return nil
				}
			}
		}
		bid, err := idgen.ERPExportID(s.ids)
		if err != nil {
			return internal(err, "The batch could not be prepared.")
		}
		now := s.now()
		batch, err := erp.Prepare(erp.PrepareInput{
			ID: bid, TenantID: sc.Tenant(), Bridge: b, Mapping: mapping, Period: period, Currency: fiscal.Currency(cur),
			PostingDate: posting, Journals: eligible, PreparedAt: now, PreparedBy: sc.Subject(),
		})
		if err != nil {
			if errs.IsCategory(err, errs.CategoryConflict) || errs.IsCategory(err, errs.CategoryValidation) {
				return err
			}
			return internal(err, "The batch could not be prepared.")
		}
		if err := s.store.CreateBatch(ctx, batch, erp.Prepared(sc.Subject(), now)); err != nil {
			return err
		}
		created = true
		got, err := s.store.Batch(ctx, bid)
		out = ERPBatchView{Batch: got}
		return err
	})
	return out, created, err
}

// Batches lists a bridge's batches, newest first, each rejected one with
// its correction task.
func (s *ERPService) Batches(ctx context.Context, bridgeID id.ERPBridgeID, f erp.Filter) ([]ERPBatchView, error) {
	if _, err := requireRoleOrSystem(ctx, erpReaders...); err != nil {
		return nil, err
	}
	if f.Period != "" && !erp.ValidPeriod(f.Period) {
		return nil, errs.Invalid("period", errs.ReasonInvalidValue, "The period is a month, YYYY-MM.")
	}
	if _, err := s.store.Bridge(ctx, bridgeID); err != nil {
		return nil, err
	}
	// Correction tasks are derived over every batch of the bridge, so a
	// status filter cannot hide the posted batch that closed one.
	all, err := s.store.Batches(ctx, bridgeID, erp.Filter{Period: f.Period})
	if err != nil {
		return nil, err
	}
	tasks := correctionsByBatch(all)
	var out []ERPBatchView
	for _, b := range all {
		if (f.Currency != "" && b.Currency != f.Currency) || (f.Status != "" && b.Status != f.Status) {
			continue
		}
		out = append(out, ERPBatchView{Batch: b, Correction: tasks[b.ID]})
	}
	return out, nil
}

func correctionsByBatch(batches []erp.Batch) map[id.ERPExportID]*erp.CorrectionTask {
	out := map[id.ERPExportID]*erp.CorrectionTask{}
	for _, t := range erp.CorrectionTasks(batches) {
		t := t
		out[t.Batch] = &t
	}
	return out
}

// Batch reads one batch of a bridge, with its lines and delivery evidence.
func (s *ERPService) Batch(ctx context.Context, bridgeID id.ERPBridgeID, batchID id.ERPExportID) (ERPBatchView, error) {
	if _, err := requireRoleOrSystem(ctx, erpReaders...); err != nil {
		return ERPBatchView{}, err
	}
	return s.view(ctx, bridgeID, batchID)
}

func (s *ERPService) view(ctx context.Context, bridgeID id.ERPBridgeID, batchID id.ERPExportID) (ERPBatchView, error) {
	b, err := s.batchOf(ctx, bridgeID, batchID)
	if err != nil {
		return ERPBatchView{}, err
	}
	v := ERPBatchView{Batch: b}
	if b.Status == erp.StatusRejected {
		all, err := s.store.Batches(ctx, bridgeID, erp.Filter{Period: b.Period, Currency: b.Currency})
		if err != nil {
			return ERPBatchView{}, err
		}
		v.Correction = correctionsByBatch(all)[b.ID]
	}
	return v, nil
}

func (s *ERPService) batchOf(ctx context.Context, bridgeID id.ERPBridgeID, batchID id.ERPExportID) (erp.Batch, error) {
	b, err := s.store.Batch(ctx, batchID)
	if err != nil {
		return erp.Batch{}, err
	}
	if b.Bridge != bridgeID {
		return erp.Batch{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such export batch on this bridge.")
	}
	return b, nil
}

// Dispatch authorises and dispatches a batch: one attempt, recorded with the
// gl.export-dispatched event in one transaction. The checker names the digest
// they reviewed and is not the preparer. A batch already transmitted,
// acknowledged or posted is returned as it is, with false — a retry never
// makes a second attempt, so never a second GL journal.
func (s *ERPService) Dispatch(ctx context.Context, bridgeID id.ERPBridgeID, batchID id.ERPExportID, expectedDigest string) (ERPBatchView, bool, error) {
	sc, err := requirePerson(ctx, erpCheckers...)
	if err != nil {
		return ERPBatchView{}, false, err
	}
	var (
		out        ERPBatchView
		dispatched bool
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		// The bridge first, as a mapping change takes it, then the batch.
		if err := s.store.Lock(ctx, "bridge/"+bridgeID.String()); err != nil {
			return err
		}
		if err := s.store.Lock(ctx, "batch/"+batchID.String()); err != nil {
			return err
		}
		bridge, err := s.store.Bridge(ctx, bridgeID)
		if err != nil {
			return err
		}
		b, err := s.batchOf(ctx, bridgeID, batchID)
		if err != nil {
			return err
		}
		current := 0
		if m, ok := bridge.Current(); ok {
			current = m.Version
		}
		attempt, err := idgen.ERPAttemptID(s.ids)
		if err != nil {
			return internal(err, "The dispatch could not be recorded.")
		}
		e, ok, err := b.Dispatch(erp.DispatchInput{ExpectedDigest: expectedDigest, CurrentMapping: current,
			Attempt: attempt, By: sc.Subject(), At: s.now()})
		if err != nil {
			if errs.IsCategory(err, errs.CategoryConflict) || errs.IsCategory(err, errs.CategoryPolicy) {
				return err
			}
			return integrity(err, "The batch's contents no longer match its sealed digest. It was not dispatched.")
		}
		if ok {
			if err := s.store.AppendBatchEvent(ctx, b.ID, e); err != nil {
				return err
			}
			if err := s.emitDispatch(ctx, bridge, b, e); err != nil {
				return err
			}
			dispatched = true
		}
		out, err = s.view(ctx, bridgeID, batchID)
		return err
	})
	return out, dispatched, err
}

// emitDispatch announces an attempt in the caller's transaction. Thin: it
// names the batch, the attempt, the external reference and the digest, and
// carries no amount; the connector reads the lines from the batch.
func (s *ERPService) emitDispatch(ctx context.Context, bridge erp.Bridge, b erp.Batch, e erp.Event) error {
	if s.outbox == nil {
		return errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
			"The outbox is not wired in this cell, so a dispatch cannot leave it. Nothing was dispatched.")
	}
	eid, err := idgen.OutboxID(s.ids)
	if err != nil {
		return internal(err, "The dispatch event could not be published.")
	}
	return s.outbox.Append(ctx, outbox.Event{
		ID: eid, TenantID: b.TenantID,
		// Partitioned by batch, so a batch's attempts arrive in order.
		AggregateKey: "erp-export/" + b.ID.String(),
		Type:         EventGLExportDispatched, SchemaRef: SchemaGLExportDispatchedRef,
		Payload: canonical.Object(
			canonical.F("batchId", canonical.String(b.ID.String())),
			canonical.F("bridgeId", canonical.String(bridge.ID.String())),
			canonical.F("attemptId", canonical.String(e.Attempt.String())),
			canonical.F("attempt", canonical.Integer(int64(len(b.Attempts())+1))),
			canonical.F("externalRef", canonical.String(b.ExternalRef())),
			canonical.F("batchDigest", canonical.String(b.Digest.String())),
			canonical.F("vendor", canonical.String(string(bridge.Vendor))),
			canonical.F("companyCode", canonical.String(b.CompanyCode)),
			canonical.F("legalEntityId", canonical.String(b.LegalEntity.String())),
			canonical.F("period", canonical.String(b.Period)),
			canonical.F("currency", canonical.String(string(b.Currency))),
			canonical.F("postingDate", canonical.String(b.PostingDate.Format(time.DateOnly))),
			canonical.F("mappingVersion", canonical.Integer(int64(b.MappingVersion))),
			canonical.F("journalCount", canonical.Integer(int64(len(b.Journals)))),
			canonical.F("dispatchedAt", canonical.Time(e.RecordedAt)),
		),
		CreatedAt: e.RecordedAt,
	})
}

// Respond records one ERP answer about a dispatch attempt. The same answer
// again records nothing and returns false.
func (s *ERPService) Respond(ctx context.Context, bridgeID id.ERPBridgeID, batchID id.ERPExportID, r erp.Response) (ERPBatchView, bool, error) {
	sc, err := requirePerson(ctx, erpResponders...)
	if err != nil {
		return ERPBatchView{}, false, err
	}
	r.RemoteCorrelation, r.ERPReference = strings.TrimSpace(r.RemoteCorrelation), strings.TrimSpace(r.ERPReference)
	r.ResponseDigest, r.Detail = strings.TrimSpace(r.ResponseDigest), strings.TrimSpace(r.Detail)
	return s.move(ctx, bridgeID, batchID, func(b erp.Batch) (erp.Event, bool, error) {
		return b.Respond(r, sc.Subject(), s.now())
	})
}

// Withdraw releases a prepared batch that was never dispatched.
func (s *ERPService) Withdraw(ctx context.Context, bridgeID id.ERPBridgeID, batchID id.ERPExportID, reason string) (ERPBatchView, error) {
	sc, err := requirePerson(ctx, erpOperators...)
	if err != nil {
		return ERPBatchView{}, err
	}
	v, _, err := s.move(ctx, bridgeID, batchID, func(b erp.Batch) (erp.Event, bool, error) {
		e, err := b.Withdraw(strings.TrimSpace(reason), sc.Subject(), s.now())
		return e, err == nil, err
	})
	return v, err
}

func (s *ERPService) move(ctx context.Context, bridgeID id.ERPBridgeID, batchID id.ERPExportID,
	next func(erp.Batch) (erp.Event, bool, error)) (ERPBatchView, bool, error) {
	var (
		out   ERPBatchView
		moved bool
	)
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "batch/"+batchID.String()); err != nil {
			return err
		}
		b, err := s.batchOf(ctx, bridgeID, batchID)
		if err != nil {
			return err
		}
		e, ok, err := next(b)
		if err != nil {
			return err
		}
		if ok {
			if err := s.store.AppendBatchEvent(ctx, b.ID, e); err != nil {
				return err
			}
			moved = true
		}
		out, err = s.view(ctx, bridgeID, batchID)
		return err
	})
	return out, moved, err
}

// RecordSnapshot records the GL balances an ERP reported for a period and
// currency, with the report they came from.
func (s *ERPService) RecordSnapshot(ctx context.Context, bridgeID id.ERPBridgeID, in GLSnapshotInput) (erp.GLSnapshot, error) {
	sc, err := requirePerson(ctx, erpOperators...)
	if err != nil {
		return erp.GLSnapshot{}, err
	}
	cur := fiscal.Currency(strings.ToUpper(strings.TrimSpace(in.Currency)))
	if !erp.ValidCurrency(string(cur)) {
		return erp.GLSnapshot{}, errs.Invalid("currency", errs.ReasonInvalidValue, "The currency is an ISO 4217 code, such as USD.")
	}
	asOf, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(in.AsOf))
	if err != nil {
		return erp.GLSnapshot{}, errs.Invalid("asOf", errs.ReasonInvalidValue, "asOf is an RFC 3339 timestamp.")
	}
	snap := erp.GLSnapshot{
		TenantID: sc.Tenant(), Bridge: bridgeID, Period: strings.TrimSpace(in.Period), Currency: cur,
		AsOf: asOf.UTC().Truncate(time.Microsecond), SourceRef: strings.TrimSpace(in.SourceRef),
		SourceDigest: strings.TrimSpace(in.SourceDigest), RecordedBy: sc.Subject(),
	}
	if len(in.Balances) > erp.MaxSnapshotLines {
		return erp.GLSnapshot{}, errs.Invalid("balances", errs.ReasonInvalidValue,
			fmt.Sprintf("A snapshot reports at most %d GL accounts.", erp.MaxSnapshotLines))
	}
	for _, l := range in.Balances {
		d, err := erp.ParseAmount("balances.debits", strings.TrimSpace(l.Debits), cur)
		if err != nil {
			return erp.GLSnapshot{}, err
		}
		c, err := erp.ParseAmount("balances.credits", strings.TrimSpace(l.Credits), cur)
		if err != nil {
			return erp.GLSnapshot{}, err
		}
		snap.Lines = append(snap.Lines, erp.GLBalance{GLAccount: strings.TrimSpace(l.GLAccount), Debits: d, Credits: c})
	}
	if err := snap.Validate(); err != nil {
		return erp.GLSnapshot{}, err
	}
	var out erp.GLSnapshot
	err = s.inTx(ctx, func(ctx context.Context) error {
		if _, err := s.store.Bridge(ctx, bridgeID); err != nil {
			return err
		}
		sid, err := idgen.GLSnapshotID(s.ids)
		if err != nil {
			return internal(err, "The snapshot could not be recorded.")
		}
		snap.ID, snap.RecordedAt = sid, s.now()
		if snap, err = snap.Seal(); err != nil {
			return internal(err, "The snapshot could not be sealed.")
		}
		if err := s.store.RecordSnapshot(ctx, snap); err != nil {
			return err
		}
		out = snap
		return nil
	})
	return out, err
}

// Snapshots lists a bridge's GL balance snapshots, newest first.
func (s *ERPService) Snapshots(ctx context.Context, bridgeID id.ERPBridgeID, period string) ([]erp.GLSnapshot, error) {
	if _, err := requireRoleOrSystem(ctx, erpReaders...); err != nil {
		return nil, err
	}
	if period != "" && !erp.ValidPeriod(period) {
		return nil, errs.Invalid("period", errs.ReasonInvalidValue, "The period is a month, YYYY-MM.")
	}
	if _, err := s.store.Bridge(ctx, bridgeID); err != nil {
		return nil, err
	}
	return s.store.Snapshots(ctx, bridgeID, period)
}

// Reconcile compares the subledger with one GL snapshot under the bridge's
// current mapping, and records the result: each GL account's exact variance,
// and what holds the period while any journal is not in a posted batch.
func (s *ERPService) Reconcile(ctx context.Context, bridgeID id.ERPBridgeID, snapshotID id.GLSnapshotID) (erp.GLReconciliation, error) {
	sc, err := requireRoleOrSystem(ctx, erpReconcilers...)
	if err != nil {
		return erp.GLReconciliation{}, err
	}
	var out erp.GLReconciliation
	err = s.inTx(ctx, func(ctx context.Context) error {
		bridge, err := s.store.Bridge(ctx, bridgeID)
		if err != nil {
			return err
		}
		snap, err := s.store.Snapshot(ctx, snapshotID)
		if err != nil {
			return err
		}
		if snap.Bridge != bridgeID {
			return errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such GL balance snapshot on this bridge.")
		}
		mapping, ok := bridge.Current()
		if !ok {
			return errs.New(errs.CategoryConflict, erp.ReasonMappingIncomplete,
				"This bridge has no account mapping yet. Record one before reconciling.")
		}
		journals, err := s.store.PeriodJournals(ctx, bridge.LegalEntity, snap.Period, snap.Currency)
		if err != nil {
			return err
		}
		batches, err := s.store.Batches(ctx, bridgeID, erp.Filter{Period: snap.Period, Currency: snap.Currency})
		if err != nil {
			return err
		}
		r, err := erp.Reconcile(erp.ReconcileInput{Mapping: mapping, Snapshot: snap, Journals: journals, Batches: batches})
		if err != nil {
			return internal(err, "The reconciliation could not be computed.")
		}
		rid, err := idgen.GLReconciliationID(s.ids)
		if err != nil {
			return internal(err, "The reconciliation could not be recorded.")
		}
		r.ID, r.TenantID, r.LegalEntity, r.RanAt, r.RanBy = rid, sc.Tenant(), bridge.LegalEntity, s.now(), sc.Subject()
		if err := s.store.RecordReconciliation(ctx, r); err != nil {
			return err
		}
		out = r
		return nil
	})
	return out, err
}

// Reconciliations lists a bridge's GL reconciliations, newest first.
func (s *ERPService) Reconciliations(ctx context.Context, bridgeID id.ERPBridgeID, period string) ([]erp.GLReconciliation, error) {
	if _, err := requireRoleOrSystem(ctx, erpReaders...); err != nil {
		return nil, err
	}
	if period != "" && !erp.ValidPeriod(period) {
		return nil, errs.Invalid("period", errs.ReasonInvalidValue, "The period is a month, YYYY-MM.")
	}
	if _, err := s.store.Bridge(ctx, bridgeID); err != nil {
		return nil, err
	}
	return s.store.Reconciliations(ctx, bridgeID, period)
}

func (s *ERPService) inTx(ctx context.Context, fn func(context.Context) error) error {
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
