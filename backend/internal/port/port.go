// Package port holds the interfaces the app layer depends on.
//
// ADR-0009 §2.3: modules communicate in-process through interfaces, never
// through the network and never through shared mutable state. Keeping the seam
// real is the point — extracting a module later means implementing the same
// interface over a transport, rather than going looking for the boundary after
// the fact.
//
// Every method takes a context.Context carrying the security context, and every
// implementation derives its tenant predicate from that rather than from an
// argument (ADR-0012 §2.7). A repository method with a TenantID parameter would
// be a method a caller can get wrong.
package port

import (
	"context"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/accumulator"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/batch"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/document"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/idempotency"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/outbox"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/reconciliation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/settlement"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/webhook"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Tx is a transaction handle.
//
// ADR-0009 §2.4 puts the transaction boundary in internal/app and nowhere else:
// use-case handlers open the transaction, pass it down, and commit. Domain code
// never sees one and adapters never open one, so there is exactly one place
// where "what is atomic" is decided and it is reviewable.
type Tx interface {
	Commit(ctx context.Context) error
	// Rollback is safe to call after a commit, so a deferred rollback is the
	// correct idiom rather than something to guard with a flag.
	Rollback(ctx context.Context) error
}

// TxManager opens transactions.
type TxManager interface {
	Begin(ctx context.Context) (Tx, context.Context, error)
}

// TenantRepository reads and writes tenants.
//
// Tenants are the one entity not scoped to a tenant, so these methods take
// explicit arguments. Everything else in this file derives its scope from the
// context.
type TenantRepository interface {
	Create(ctx context.Context, t identity.Tenant) error
	ByID(ctx context.Context, tenantID id.TenantID) (identity.Tenant, error)
	BySlug(ctx context.Context, slug string) (identity.Tenant, error)
	List(ctx context.Context, limit int) ([]identity.Tenant, error)
	SetStatus(ctx context.Context, tenantID id.TenantID, status identity.TenantStatus, reason string, actor *id.UserID, at time.Time) error
}

// UserRepository reads and writes users within the context's tenant.
type UserRepository interface {
	Create(ctx context.Context, u identity.User) error
	ByID(ctx context.Context, userID id.UserID) (identity.User, error)
	// ByEmail is the sign-in lookup. It takes an explicit tenant because the
	// caller has resolved a tenant from the request but has no security context
	// yet — there is nobody authenticated at that point.
	ByEmail(ctx context.Context, tenantID id.TenantID, email string) (identity.User, error)
	List(ctx context.Context, limit int) ([]identity.User, error)
	SetStatus(ctx context.Context, userID id.UserID, status identity.UserStatus) error
	SetVerifier(ctx context.Context, userID id.UserID, v identity.Verifier) error
	SetDisplayName(ctx context.Context, userID id.UserID, name string) error
	GrantRole(ctx context.Context, userID id.UserID, role security.Role, grantedBy id.UserID, at time.Time) error
	RevokeRole(ctx context.Context, userID id.UserID, role security.Role) error
	Roles(ctx context.Context, userID id.UserID) ([]security.Role, error)
}

// SessionRepository reads and writes sessions.
type SessionRepository interface {
	Create(ctx context.Context, s identity.Session) error
	// ByTokenDigest resolves a cookie to a session. It takes no tenant, and
	// cannot: the caller has a cookie and nothing else. The tenant comes from
	// the row, which is what makes the lookup safe — a digest resolves to
	// exactly one session or to none.
	ByTokenDigest(ctx context.Context, digest []byte) (identity.Session, error)
	ListForTenant(ctx context.Context, limit int) ([]identity.Session, error)
	Slide(ctx context.Context, sessionID id.SessionID, idleExpiresAt time.Time) error
	Revoke(ctx context.Context, sessionID id.SessionID, reason string, at time.Time) error
	RevokeAllForUser(ctx context.Context, userID id.UserID, reason string, at time.Time) (int, error)
	DeleteExpired(ctx context.Context, before time.Time) (int, error)
}

// AuditRecord is one administrative action.
type AuditRecord struct {
	ID          id.AuditID
	TenantID    id.TenantID
	ActorUserID *id.UserID
	Action      string
	SubjectType string
	SubjectID   string
	// Detail is canonical JSON bytes (ADR-0011), holding no credential material
	// and no fiscal amount.
	Detail     []byte
	RecordedAt time.Time
}

// AuditRepository appends administrative audit records.
//
// There is no update and no delete, and that is the whole point of the
// interface: the database grant does not permit them either, so the constraint
// is expressed twice and neither expression is load-bearing alone.
type AuditRepository interface {
	Append(ctx context.Context, r AuditRecord) error
	List(ctx context.Context, limit int) ([]AuditRecord, error)
	ListForSubject(ctx context.Context, subjectType, subjectID string, limit int) ([]AuditRecord, error)
}

// ---------------------------------------------------------------------------
// evidence
// ---------------------------------------------------------------------------

// EvidenceStore is the regional immutable evidence object store (Build Plan W1
// lane D; ADR-0015 §2.4's "immutable regional object store").
//
// It is content-addressed and write-once. The key is the digest of the bytes,
// computed by the store, so there is no call that can put different bytes
// under an existing key — a second Put of the same bytes is a no-op, and there
// is no Delete. Objects are scoped to the tenant in the context, so a tenant's
// evidence can be retained, held and eventually disposed of as that tenant's.
//
// Get verifies what it returns: bytes that no longer hash to their key are an
// integrity failure, reported as one, never returned.
type EvidenceStore interface {
	Put(ctx context.Context, data []byte) (canonical.Digest, error)
	Get(ctx context.Context, digest canonical.Digest) ([]byte, error)
}

// DecisionRepository indexes recorded decisions (ADR-0003).
//
// Append-only by interface as well as by grant: there is no update and no
// delete, and a correction is a new decision whose Supersedes names the old.
type DecisionRepository interface {
	Append(ctx context.Context, r evidence.Record) error
	ByID(ctx context.Context, decisionID id.DecisionID) (evidence.Record, error)
	// AsOf is ADR-0003 §2.3: the version of a business key that was current
	// at decisionTime, for an event at eventTime. History is reconstructed by
	// ordering, never by a closed range.
	AsOf(ctx context.Context, businessKey string, decisionTime, eventTime time.Time) (evidence.Record, error)
	// History is every version of a business key, oldest first.
	History(ctx context.Context, businessKey string) ([]evidence.Record, error)
	// CurrentInWindow is the current version of every business key whose
	// decision's event fell in [from, to): what a period reconciles.
	CurrentInWindow(ctx context.Context, from, to time.Time) ([]evidence.Record, error)
	// SealLeaves returns the leaves of every decision recorded in
	// [from, to), in evidence.LeafOrder.
	SealLeaves(ctx context.Context, from, to time.Time) ([]evidence.SealLeaf, error)
}

// ---------------------------------------------------------------------------
// idempotency
// ---------------------------------------------------------------------------

// IdempotencyRepository reads and writes idempotency records (ADR-0013).
//
// The record's key carries a tenant because the domain type does; every
// implementation refuses a key whose tenant is not the one in the context,
// rather than trusting it.
type IdempotencyRepository interface {
	// Insert writes a PENDING record. It reports false, and no error, when a
	// record for the key already exists: the primary key refusing the insert is
	// the concurrency control of ADR-0013 §2.5, not a failure.
	Insert(ctx context.Context, r idempotency.Record) (bool, error)
	// Get reads the record for a key, verifying a settled body against its
	// digest. A missing record is CategoryNotFound.
	Get(ctx context.Context, key idempotency.Key) (idempotency.Record, error)
	// Complete settles a PENDING record. It runs in the transaction that
	// applies the domain effect (ADR-0013 §2.6), and refuses a record that is
	// no longer PENDING.
	Complete(ctx context.Context, r idempotency.Record) error
	// Release deletes a PENDING record after a transient failure, so a retry is
	// a genuine new attempt (ADR-0013 §2.7). A settled record is never deleted
	// by this.
	Release(ctx context.Context, key idempotency.Key) error
	// Expire deletes the record for a key if it expired at or before now, and
	// reports whether it did. An expired key behaves as a new key (§2.9).
	Expire(ctx context.Context, key idempotency.Key, now time.Time) (bool, error)
}

// SealRepository indexes period seals.
type SealRepository interface {
	// LockSealing serialises sealing for the tenant in scope until the
	// enclosing transaction ends, so two sealers cannot both find a period
	// unsealed and both seal it.
	LockSealing(ctx context.Context) error
	// Overlapping reports seals whose period intersects [from, to).
	Overlapping(ctx context.Context, from, to time.Time) ([]evidence.SealRecord, error)
	Append(ctx context.Context, r evidence.SealRecord) error
	ByID(ctx context.Context, sealID id.SealID) (evidence.SealRecord, error)
	// List returns the tenant's seals, latest period first.
	List(ctx context.Context, limit int) ([]evidence.SealRecord, error)
}

// ---------------------------------------------------------------------------
// accumulators
// ---------------------------------------------------------------------------

// AccumulatorRepository persists the accumulator pattern of ADR-0004: an
// append-only contribution log, a transactionally maintained snapshot, and the
// threshold crossings the log has caused.
//
// The use case drives one commit like this, inside one transaction it opened
// (ADR-0009 §2.4):
//
//	snaps := LockAll(refs)                     // the serialization point, §2.2–2.3
//	for each contribution:
//	    next, crossings := accumulator.Apply(snap, c, thresholds)
//	    applied := AppendContribution(Event{c, next.LastSeq})
//	    if !applied: the decision already contributed — §2.4, return the original result
//	    SaveSnapshot(next)
//	    for each crossing: AppendCrossing(crossing); outbox.Append(event)   // §2.6
//	commit
//
// Every method derives its tenant from the security context (ADR-0012 §2.7);
// none takes one.
type AccumulatorRepository interface {
	// LockAll takes the row lock on each named accumulator's snapshot, for
	// the rest of the enclosing transaction, and returns the snapshots in
	// accumulator.LockOrder order.
	//
	// It is the only way to lock a snapshot (ADR-0004 §5.1 control 1). Keys
	// are acquired one at a time in canonical order, whatever order refs
	// arrive in, and a snapshot that does not exist yet is created empty as
	// part of acquiring it, so the first commit on a new key serializes
	// exactly like every later one. A key named twice is locked once; named
	// twice with different currencies, or with a currency different from the
	// stored snapshot's, it is refused.
	//
	// The wait for each lock is bounded by an explicit lock_timeout set in
	// the transaction (control 4): a pathological key degrades as a
	// retryable CategoryUnavailable error rather than a hang. Calling it
	// outside a transaction is an error, because the locks would be released
	// before the caller could use them.
	LockAll(ctx context.Context, refs []accumulator.Ref) ([]accumulator.Snapshot, error)

	// AppendContribution writes a contribution to the log at e.Seq. It
	// reports false, and no error, when the source decision has already
	// contributed to the key: UNIQUE (tenant, accumulator_key,
	// source_decision_id) refusing the row is the idempotence guarantee of
	// ADR-0004 §2.4, and the caller treats it as success-already-applied.
	// Nothing is written in that case, and the transaction remains usable.
	AppendContribution(ctx context.Context, e accumulator.Event) (bool, error)

	// Contribution reads the log entry a decision made to a key — the
	// "reads the existing row" half of §2.4's already-applied path.
	Contribution(ctx context.Context, key accumulator.Key, decisionID id.DecisionID) (accumulator.Event, error)

	// SaveSnapshot replaces a locked snapshot with the one Apply or Rebuild
	// produced. It refuses to move a snapshot backwards in the log.
	SaveSnapshot(ctx context.Context, s accumulator.Snapshot) error

	// AppendCrossing records a threshold crossing. A second crossing of one
	// threshold on one key is refused by the primary key (§2.6) and surfaces
	// as an error, failing the transaction that would have announced it
	// twice.
	AppendCrossing(ctx context.Context, c accumulator.Crossing) error

	// Events is the whole contribution log for a key, in sequence order —
	// the input to accumulator.Replay and accumulator.Rebuild (§2.5).
	Events(ctx context.Context, key accumulator.Key) ([]accumulator.Event, error)

	// Crossings is every recorded crossing for a key, in sequence order.
	Crossings(ctx context.Context, key accumulator.Key) ([]accumulator.Crossing, error)

	// ReadUnlocked reads a snapshot without locking it, for a quote (§2.7).
	// The result may be stale by whatever is in flight, and is a distinct
	// type so that it cannot be passed to accumulator.Apply. A key nothing
	// has contributed to reads as an empty total in the named currency.
	ReadUnlocked(ctx context.Context, ref accumulator.Ref) (accumulator.Observation, error)
}

// ---------------------------------------------------------------------------
// model gateway
// ---------------------------------------------------------------------------

// ModelGateway is the only route from the app layer to the AI plane (ADR-0006;
// Build Plan W1 lane L). internal/adapter/gateway implements it.
//
// Every method runs the ai.Evaluate policy gate before anything leaves the
// process, then calls the Governed Model Gateway, which decides again on the
// same governance context and is the enforcement point (§2.5). The tenant
// comes from the security context in ctx and the region from the cell, so
// neither is a parameter (ADR-0012 §2.7, ADR-0006 §2.8).
//
// Results are advisory records only (§2.6). Nothing here returns, accepts or
// can be converted into a fiscal type; the route from a suggestion to a
// decision is a human review workflow.
//
// Synchronous calls are for operator-initiated flows off the C0 path (§2.4).
// An AI_GATEWAY_UNAVAILABLE or AI_GATEWAY_NOT_CONFIGURED error is a degraded
// product state the caller renders as such, not a failure of the request it
// was assisting.
type ModelGateway interface {
	Suggest(ctx context.Context, inv ai.Invocation) (ai.AiSuggestion, error)
	Extract(ctx context.Context, inv ai.Invocation) (ai.AiExtraction, error)
	ProposeClassification(ctx context.Context, inv ai.Invocation) (ai.AiClassificationProposal, error)
}

// ---------------------------------------------------------------------------
// cross-cell transfer
// ---------------------------------------------------------------------------

// TransferLog records cross-cell transfers (ADR-0009 §2.6, SEC-REQ-0042).
//
// Append-only by interface as well as by grant: there is no update and no
// delete, because the record is the evidence that a copy was authorized, and
// evidence that can be edited after the fact is not evidence. The record is
// appended in the source cell, in the transaction that releases the data, so a
// copy with no record cannot commit.
//
// Append takes a privacy.CrossCellTransfer, which only NewCrossCellTransfer
// builds — and that refuses a transfer its TransferProfile does not permit —
// and every implementation re-validates its shape and refuses a record whose
// tenant is not the one in the context.
type TransferLog interface {
	Append(ctx context.Context, t privacy.CrossCellTransfer) error
	ByID(ctx context.Context, transferID id.TransferID) (privacy.CrossCellTransfer, error)
	// List returns the tenant's transfers, newest first — the "what of this
	// tenant's data has left this cell, and on whose approval" query.
	List(ctx context.Context, limit int) ([]privacy.CrossCellTransfer, error)
}

// ---------------------------------------------------------------------------
// Legal entities and the Tax Control Subledger (W2 lanes I and J)
// ---------------------------------------------------------------------------

// LegalEntityRepository holds a tenant's legal entities. Every method reads
// the tenant from the security context.
type LegalEntityRepository interface {
	Create(ctx context.Context, le identity.LegalEntity) error
	// Default returns the tenant's default legal entity.
	Default(ctx context.Context) (identity.LegalEntity, error)
	ByID(ctx context.Context, legalEntityID id.LegalEntityID) (identity.LegalEntity, error)
}

// JournalRepository is the Tax Control Subledger. It appends balanced
// journals and never edits one (ZTAX-FIN-REQ-0036).
type JournalRepository interface {
	// Append writes a journal and its lines. A second journal for the same
	// source event, profile and currency is refused as already existing, so a
	// retried commit cannot post twice.
	Append(ctx context.Context, j subledger.Journal) error
	// BySource returns the journals one source event posted.
	BySource(ctx context.Context, kind, sourceID string) ([]subledger.Journal, error)
	// Balances sums a legal entity's posted lines by account and currency,
	// debits and credits apart (ZTAX-FIN-REQ-0121).
	Balances(ctx context.Context, legalEntityID id.LegalEntityID) (map[subledger.BalanceKey]subledger.Balance, error)
}

// ObligationRepository stores obligation instances as append-only chains of
// rows linked by Supersedes (ADR-0003 §2.2), and the per-decision
// contributions their assessed amounts are the sum of.
type ObligationRepository interface {
	// Lock serializes the writers of one business key until the transaction
	// ends. Commit takes it after the accumulator locks, in business-key
	// order, so every writer acquires in the same order.
	Lock(ctx context.Context, businessKey string) error
	// Current returns the newest row of a business key; not found when the
	// obligation has never been created.
	Current(ctx context.Context, businessKey string) (obligation.Obligation, error)
	// ByID returns one row, current or superseded.
	ByID(ctx context.Context, obligationID id.ObligationID) (obligation.Obligation, error)
	// List returns the current row of every chain in the filter, by due date.
	List(ctx context.Context, f ObligationFilter) ([]obligation.Obligation, error)
	// Append writes a row. A second root for a business key, or a second row
	// superseding the same row, is refused as a conflict: the writer read a
	// row that is no longer current.
	Append(ctx context.Context, o obligation.Obligation) error
	// Contribute records one decision's assessment into, or withdrawal from,
	// a business key. It reports false when that decision already recorded
	// that kind for that key, so a retried commit counts once.
	Contribute(ctx context.Context, c ObligationContribution) (bool, error)
	// Contribution returns what a decision recorded of one kind for a key;
	// not found when it recorded nothing.
	Contribution(ctx context.Context, businessKey string, decisionID id.DecisionID, kind ContributionKind) (fiscal.Money, error)
	// Assessed sums the contributions of each business key. A key with none
	// is absent from the result.
	Assessed(ctx context.Context, businessKeys []string) (map[string]fiscal.Money, error)
}

// ObligationFilter narrows a listing. Zero fields do not filter.
type ObligationFilter struct {
	Status obligation.Status
	Limit  int
}

// ContributionKind distinguishes an assessment from its withdrawal.
type ContributionKind string

// The contribution kinds of migration 000012.
const (
	ContributionAssess   ContributionKind = "ASSESS"
	ContributionWithdraw ContributionKind = "WITHDRAW"
)

// ObligationContribution is one decision's signed amount for one obligation.
type ObligationContribution struct {
	BusinessKey string
	DecisionID  id.DecisionID
	Kind        ContributionKind
	Amount      fiscal.Money
	RecordedAt  time.Time
}

// OutboxWriter appends an event in the caller's transaction (ADR-0014 §2.1):
// the event commits with the state change that caused it, or neither does.
type OutboxWriter interface {
	Append(ctx context.Context, e outbox.Event) error
}

// ---------------------------------------------------------------------------
// refunds (ZTAX-FIN-001 §14)
// ---------------------------------------------------------------------------

// RefundRepository stores refunds: an immutable header and an append-only
// history of what the payment provider reported (ZTAX-FIN-REQ-0059).
type RefundRepository interface {
	// LockDecision serializes the refunds of one decision until the
	// transaction ends, so two refunds admitted concurrently cannot both find
	// the same tax still refundable.
	LockDecision(ctx context.Context, decisionID id.DecisionID) error
	// Create writes the header and its REQUESTED event.
	Create(ctx context.Context, r settlement.Refund) error
	// Append writes the next event. An event whose sequence another writer
	// has already used is refused as a conflict: the writer read a history
	// that is no longer current.
	Append(ctx context.Context, e settlement.RefundEvent) error
	// ByID returns a refund and its history, oldest event first.
	ByID(ctx context.Context, refundID id.RefundID) (settlement.Refund, []settlement.RefundEvent, error)
	// ForDecision returns the refunds of one decision, each with its current
	// event, oldest first.
	ForDecision(ctx context.Context, decisionID id.DecisionID) ([]RefundState, error)
}

// RefundState is a refund and its current event.
type RefundState struct {
	Refund  settlement.Refund
	Current settlement.RefundEvent
}

// ---------------------------------------------------------------------------
// webhooks (W2 lane K)
// ---------------------------------------------------------------------------

// SealedSecret is one version of a webhook's signing secret, sealed under the
// cell's webhook key. Only the app layer, holding the key, opens it.
type SealedSecret struct {
	webhook.SecretVersion
	Sealed []byte
}

// WebhookState is a subscription and its current status.
type WebhookState struct {
	Subscription webhook.Subscription
	Status       webhook.StatusChange
}

// WebhookRepository stores webhooks and their deliveries.
//
// Every method but ClaimDue reads the tenant from the context. ClaimDue is
// the dispatcher's, which serves the whole cell as the outbox relay does, and
// returns each delivery with its tenant so the work on it can be scoped.
type WebhookRepository interface {
	// Create writes a subscription, its ACTIVE status and its first secret.
	Create(ctx context.Context, s webhook.Subscription, first webhook.StatusChange, secret SealedSecret) error
	// Lock serializes the writers of one subscription's status and secrets
	// until the transaction ends.
	Lock(ctx context.Context, webhookID id.WebhookID) error
	ByID(ctx context.Context, webhookID id.WebhookID) (WebhookState, error)
	List(ctx context.Context, limit int) ([]WebhookState, error)
	// Matching returns the ACTIVE subscriptions that receive an event type.
	Matching(ctx context.Context, eventType string) ([]webhook.Subscription, error)
	// AppendStatus writes the next status; a sequence already taken is an
	// optimistic conflict.
	AppendStatus(ctx context.Context, c webhook.StatusChange) error
	// Secrets returns every version, oldest first.
	Secrets(ctx context.Context, webhookID id.WebhookID) ([]SealedSecret, error)
	// AppendSecret writes the next version; a version already taken is an
	// optimistic conflict.
	AppendSecret(ctx context.Context, s SealedSecret) error

	// InsertDelivery writes a delivery. It reports false, and no error, for a
	// second fan-out of one event to one subscription.
	InsertDelivery(ctx context.Context, d webhook.Delivery) (bool, error)
	Delivery(ctx context.Context, deliveryID id.DeliveryID) (webhook.Delivery, []webhook.Attempt, error)
	Deliveries(ctx context.Context, webhookID id.WebhookID, status webhook.DeliveryStatus, limit int) ([]webhook.Delivery, error)
	// ClaimDue leases up to limit PENDING deliveries whose next attempt is
	// due at now, across the cell: each is pushed lease into the future in
	// the same statement, so a second dispatcher skips it and a dispatcher
	// that dies mid-send leaves it to be retried when the lease lapses.
	ClaimDue(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]webhook.Delivery, error)
	// RecordAttempt writes an attempt and the delivery's resulting state.
	RecordAttempt(ctx context.Context, a webhook.Attempt, d webhook.Delivery) error
	// Bury dead-letters a delivery without an attempt: its subscription has
	// stopped receiving.
	Bury(ctx context.Context, deliveryID id.DeliveryID) error
}

// WebhookSender makes one delivery request. The implementation is the egress
// guard: it refuses a destination the policy forbids at the moment of
// connection, and follows no redirect.
type WebhookSender interface {
	// Send POSTs body with headers and returns the receiver's status. An
	// error means no status was received.
	Send(ctx context.Context, url string, headers map[string]string, body []byte) (int, error)
}

// ---------------------------------------------------------------------------
// batches and jobs (W2 lane K)
// ---------------------------------------------------------------------------

// BatchRepository stores batch jobs, their items and the items' results.
//
// ClaimNext serves the whole cell, as the outbox relay does, and returns the
// job with its tenant; every other method reads the tenant from the context.
type BatchRepository interface {
	// Create writes a QUEUED job and its items.
	Create(ctx context.Context, j batch.Job, items []batch.Item) error
	// Job returns a job, its items' business keys in order, and the results
	// recorded so far.
	Job(ctx context.Context, jobID id.JobID) (batch.Job, []batch.Item, []batch.Result, error)
	// ClaimNext leases the oldest job that is QUEUED, or RUNNING with a
	// lapsed lease, marking it RUNNING until leaseUntil. False when there is
	// none.
	ClaimNext(ctx context.Context, now, leaseUntil time.Time) (batch.Job, bool, error)
	// Renew extends a held lease. It refuses a job no longer RUNNING.
	Renew(ctx context.Context, jobID id.JobID, leaseUntil time.Time) error
	// Pending returns the items with no result yet, in order, requests
	// included.
	Pending(ctx context.Context, jobID id.JobID) ([]batch.Item, error)
	// AppendResult records an item's outcome. It reports false when the item
	// already has one: a worker that resumed after another finished it.
	AppendResult(ctx context.Context, r batch.Result) (bool, error)
	// Complete marks a job COMPLETED and releases its lease.
	Complete(ctx context.Context, jobID id.JobID, at time.Time) error
}

// ---------------------------------------------------------------------------
// fiscal documents (W2 lane J; ZTAX-FIN-001 §3–§6)
// ---------------------------------------------------------------------------

// DocumentRecord is a committed document as stored: the document, the totals
// computed from its lines when it was committed, and who committed it.
type DocumentRecord struct {
	Document   document.Document
	Net        fiscal.Money
	Tax        fiscal.Money
	Gross      fiscal.Money
	RecordedAt time.Time
	RecordedBy id.UserID
}

// DocumentCitation is a document that pins a decision, and where it stands.
type DocumentCitation struct {
	Document id.FiscalDocumentID
	Type     document.Type
	Status   document.Status
}

// DocumentRepository stores committed fiscal documents and their lifecycle.
// Append-only by interface and by grant: there is no update and no delete.
type DocumentRepository interface {
	// Lock serializes the writers of one document chain until the
	// transaction ends: two corrections of one invoice, or a correction and
	// a rebill, are decided one after the other.
	Lock(ctx context.Context, root id.FiscalDocumentID) error
	// LockDecision serializes the billing of one decision, so it cannot be
	// billed twice by two documents committed at once.
	LockDecision(ctx context.Context, decisionID id.DecisionID) error
	// Create writes a committed document — header, predecessors, pinned
	// decisions, lines and their taxes — and its COMMITTED status.
	Create(ctx context.Context, r DocumentRecord, first document.StatusEvent) error
	// ByID returns a document and its status history, oldest first.
	ByID(ctx context.Context, documentID id.FiscalDocumentID) (DocumentRecord, []document.StatusEvent, error)
	// AppendStatus writes the next status event; a sequence already taken is
	// an optimistic conflict.
	AppendStatus(ctx context.Context, e document.StatusEvent) error
	// Lineage returns every document sharing a root, in commit order.
	Lineage(ctx context.Context, root id.FiscalDocumentID) ([]DocumentRecord, error)
	// CitedBy returns the documents that pin a decision, with each one's
	// current status.
	CitedBy(ctx context.Context, decisionID id.DecisionID) ([]DocumentCitation, error)
	// CancelledLines reports which lines of a document a void or credit note
	// has already cancelled.
	CancelledLines(ctx context.Context, documentID id.FiscalDocumentID) (map[id.FiscalLineID]bool, error)
	// TaxByDecision sums the tax every document presents for each decision —
	// invoices positive, voids and credits negative — in the documents'
	// currency. A decision no document cites is absent.
	TaxByDecision(ctx context.Context, decisions []id.DecisionID) (map[id.DecisionID]fiscal.Money, error)
}

// ---------------------------------------------------------------------------
// subledger period close (ZTAX-FIN-001 §20–§22)
// ---------------------------------------------------------------------------

// ReopenRequest is a request to reopen a hard-closed period, waiting for a
// second person's approval.
type ReopenRequest struct {
	ID          id.ReopenRequestID
	TenantID    id.TenantID
	LegalEntity id.LegalEntityID
	Period      string
	Reason      string
	RequestedAt time.Time
	RequestedBy id.UserID
}

// PeriodPopulation is everything a close manifest seals.
type PeriodPopulation struct {
	Journals   []id.JournalID
	Balances   []subledger.ManifestBalance
	Documents  []subledger.ManifestDocument
	Exceptions []subledger.ManifestException
}

// PeriodRepository holds the subledger's legal periods.
//
// Posting and moving a period serialize on one lock per legal entity and
// period, taken shared by a posting and exclusive by a transition: postings
// into one month do not wait for each other, and a close waits for the
// postings in flight, and they for it.
type PeriodRepository interface {
	LockForPosting(ctx context.Context, legalEntity id.LegalEntityID, period string) error
	LockForTransition(ctx context.Context, legalEntity id.LegalEntityID, period string) error
	// History returns a period's events, oldest first; none is OPEN.
	History(ctx context.Context, legalEntity id.LegalEntityID, period string) ([]subledger.PeriodEvent, error)
	// AppendEvent writes the next event; a sequence already taken is an
	// optimistic conflict.
	AppendEvent(ctx context.Context, e subledger.PeriodEvent) error
	// Population reads what a close of the period would seal.
	Population(ctx context.Context, legalEntity id.LegalEntityID, period string) (PeriodPopulation, error)
	// PutManifest stores a sealed manifest's canonical bytes under its digest.
	PutManifest(ctx context.Context, legalEntity id.LegalEntityID, period string, digest canonical.Digest, body []byte, at time.Time) error
	// Manifest returns a stored manifest's canonical bytes.
	Manifest(ctx context.Context, digest canonical.Digest) ([]byte, error)
	CreateReopenRequest(ctx context.Context, r ReopenRequest) error
	ReopenRequest(ctx context.Context, requestID id.ReopenRequestID) (ReopenRequest, error)
}

// ---------------------------------------------------------------------------
// reconciliation (ZTAX-FIN-001 §17–§20)
// ---------------------------------------------------------------------------

// ReconResolution is a resolution of one item, as stored.
type ReconResolution struct {
	Item id.ReconItemID
	reconciliation.Resolution
}

// ReconciliationRepository stores runs, their items and resolutions.
// Append-only by interface and by grant.
type ReconciliationRepository interface {
	// CreateRun writes a run and every item it compared.
	CreateRun(ctx context.Context, r reconciliation.Run, items []reconciliation.RunItem) error
	// Run returns a run, its items in order, and their resolutions.
	Run(ctx context.Context, runID id.ReconciliationID) (reconciliation.Run, []reconciliation.RunItem, []ReconResolution, error)
	// Item returns one item.
	Item(ctx context.Context, itemID id.ReconItemID) (reconciliation.RunItem, error)
	// Resolve records an item's resolution; a second resolution of one item
	// is refused as a conflict.
	Resolve(ctx context.Context, r ReconResolution) error
}
