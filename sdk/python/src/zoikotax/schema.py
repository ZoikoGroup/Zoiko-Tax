# GENERATED — do not edit.
#
# TypedDicts for the ZoikoTax v1 API, generated from
# contracts/openapi/export/ztax.v1.3.1.yaml by datamodel-code-generator 0.83.0
# (sdk/python/scripts/generate.py).
#
# A hand edit here fails CI: `python scripts/check.py` regenerates this file
# and fails on any diff. Edit the contract, then regenerate.

from __future__ import annotations

from typing import Literal, TypeAlias, TypedDict

from zoikotax._compat import NotRequired

ReasonCode: TypeAlias = str
"""
A registered code from a closed vocabulary (ADR-0016 §2.4). Codes are
never renumbered, never reused and never redefined; a retired code stops
being emitted and keeps its meaning so historical evidence still
resolves.

This is the field to branch on. The set this deployment can emit is
reported by `GET /v1/capabilities`; the enum is deliberately not closed
here, because the register grows by addition and a client that rejects
an unrecognised code would break on an additive change (ADR-0010 §2.6).

"""


Timestamp: TypeAlias = str
"""
RFC 3339 UTC with exactly six fractional digits and a literal `Z`
(ADR-0011 §2.1 P2). No offsets and no variable precision, because two
encodings of one instant must not produce two digests.

"""


Digest: TypeAlias = str
"""
A canonical digest: SHA-256 over the canonical bytes, lowercase hex,
carrying its profile-and-algorithm prefix (ADR-0011 §2.3). The prefix is
not decoration — a future `zt2:` digest of the same document is a
different digest, and comparing the two without it would be wrong.

"""


TenantId: TypeAlias = str
"""
A tenant identifier (ADR-0012). Prefixed, sortable, never recycled.
"""


UserId: TypeAlias = str
"""
A user identifier (ADR-0012).
"""


SessionId: TypeAlias = str
"""
A session identifier (ADR-0012). It is **not** the session secret: the
secret is in the cookie, is never returned by this API, and is not
derivable from this.

"""


AuditId: TypeAlias = str
"""
An audit record identifier (ADR-0012).
"""


Role: TypeAlias = Literal['ADMIN', 'OPERATOR', 'ANALYST', 'AUDITOR']
"""
A role within a tenant. Closed: adding one is a reviewed change with an
authorization matrix entry, not a string a caller may invent.

"""


TenantStatus: TypeAlias = Literal['ACTIVE', 'SUSPENDED', 'CLOSED']


UserStatus: TypeAlias = Literal['ACTIVE', 'INVITED', 'DISABLED']
"""
`INVITED` is a user who exists and has no password, and therefore cannot
sign in. It is a distinct state rather than a disabled user, because the
two lead to different administrative actions.

"""


class Tenant(TypedDict):
    id: TenantId
    slug: str
    """
    The stable, human-usable name a client signs in with.
    """
    displayName: str
    residencyRegion: str
    """
    The region whose cell holds this tenant's data. It never leaves it,
    which is why this is reported rather than inferred.

    """
    status: TenantStatus


class User(TypedDict):
    """
    A user as this API represents one. Note what is absent and always will
    be: there is no password, no verifier, no salt and no parameter set. A
    domain type serialized directly is a domain type whose every future
    field becomes public API by accident.

    """

    id: UserId
    email: str
    displayName: str
    status: UserStatus
    roles: list[Role]
    """
    The roles this user holds, which may be empty.
    """
    createdAt: Timestamp


class Session(TypedDict):
    """
    The current session. It carries no token: the session secret is in an
    `HttpOnly` cookie and is never in a response body.

    """

    tenant: Tenant
    user: User
    expiresAt: Timestamp
    """
    The session's absolute expiry. Absolute, not idle: activity does not
    extend it, so a session ends at a time that was fixed when it began.

    """


class SessionSummary(TypedDict):
    id: SessionId
    userId: UserId
    createdAt: Timestamp
    expiresAt: Timestamp
    userAgent: NotRequired[str]
    """
    The user agent recorded when the session opened, where one was sent.
    """
    clientIp: NotRequired[str]
    """
    The client address recorded when the session opened.
    """
    revoked: bool
    """
    Whether the session has been revoked. A revoked session is kept
    rather than deleted; a session that vanished would leave nothing for
    an audit to look at.

    """
    current: bool
    """
    Whether this is the calling session.
    """


class AuditRecord(TypedDict):
    id: AuditId
    actorUserId: NotRequired[UserId]
    """
    Absent where the action had no human actor — a bootstrap at startup,
    or a scheduled sweep. Absent rather than a placeholder identifier,
    because "the system did this" and "some user did this" are different
    facts.

    """
    action: str
    """
    A dotted action name from a closed vocabulary.
    """
    subjectType: str
    subjectId: str
    detail: str
    """
    A canonical JSON document, as a string. It is the exact bytes that
    were recorded and digested, so it is passed through rather than
    re-encoded (ADR-0011 §2.7).

    """
    recordedAt: Timestamp


class Trains(TypedDict):
    """
    The seven release-train versions. Together they name the exact
    combination of code, content, models, adapters, infrastructure, schemas
    and migrations that produced a response — which is what makes a decision
    replayable years later.

    """

    app: str
    content: str
    ai: str
    adapter: str
    infra: str
    schema: str
    migration: str


class ContentCapability(TypedDict):
    """
    The rule bundle this cell is running. Absent where no bundle is loaded.

    The digest is here because it is what a replay names: quoting the bundle
    identifier alone would not distinguish two builds of one pack, and the
    difference between them is a difference in law.

    """

    bundleId: str
    digest: Digest
    irVersion: int
    """
    The rule-IR instruction-set version the bundle was compiled for. A
    runtime that cannot execute a bundle refuses to load it and stays on
    the previous one rather than degrading (ADR-0005 §2.8).

    """
    nodeCount: int
    """
    The size of the rule graph, for telemetry.
    """


class SignInRequest(TypedDict):
    tenant: str
    """
    The tenant slug.
    """
    email: str
    password: str


class ChangePasswordRequest(TypedDict):
    currentPassword: str
    newPassword: str


class CreateUserRequest(TypedDict):
    email: str
    displayName: str
    roles: NotRequired[list[Role]]
    """
    Roles to grant on creation. A user may be created with none.
    """
    password: NotRequired[str]
    """
    Optional. Omitting it creates an `INVITED` user who cannot sign in
    until a password is set.

    """


class SetUserStatusRequest(TypedDict):
    status: UserStatus


class RoleRequest(TypedDict):
    role: Role


DecisionId: TypeAlias = str
"""
A decision identifier: a UUIDv7 in lowercase canonical form
(ADR-0012 §2.1). Sortable by creation, never recycled.

"""


Decimal: TypeAlias = str
"""
A decimal in canonical string form (ADR-0010 §2.9): an optional minus,
digits, and an optional fraction. No exponent, no leading `+`, no
leading zeros. **Scale is significant**: `"1.50"` and `"1.5"` are
different assertions and digest differently.

Classified as fiscal personal data because on a consumer transaction
it is: logs carry it redacted, and it is kept for the fiscal record
period as evidence.

"""


CurrencyCode: TypeAlias = str
"""
An ISO 4217 alphabetic currency code.
"""


RateBasis: TypeAlias = Literal['NET', 'GROSS', 'PER_UNIT', 'COMPOUND']
"""
What a rate applies to (ADR-0002). A rate without a basis is not a rate.
"""


UnitCode: TypeAlias = str
"""
A unit of measure, as the content pack names it.
"""


ValueType: TypeAlias = Literal['MONEY', 'RATE', 'QUANTITY', 'BOOL', 'STRING', 'REASON_CODE']
"""
The type of an emitted value. It says which of a value's other members are present.
"""


Outcome: TypeAlias = Literal['AUTHORITATIVE', 'ADVISORY', 'AMBIGUOUS', 'CONFLICTED', 'UNSUPPORTED', 'REVIEW_REQUIRED']
"""
What a determination concluded (ADR-0016 §2.1). Only `AUTHORITATIVE`
may be filed, and nothing produces it before A4. The refusals —
`AMBIGUOUS`, `CONFLICTED`, `UNSUPPORTED`, `REVIEW_REQUIRED` — are
outcomes with evidence, not errors: "we do not support this" is a fact
about coverage that is recorded against the transaction.

"""


ReplayVerdict: TypeAlias = Literal['MATCH', 'DIVERGED', 'BUNDLE_UNAVAILABLE']


BusinessKey: TypeAlias = str
"""
The caller's stable reference for what is being determined — a line, a
transaction. It is the identity across corrections: every version of a
decision shares it (ADR-0003). Not an identifier this service
interprets.

"""


class MoneyValue(TypedDict):
    """
    An amount and its currency, never one without the other.
    """

    amount: Decimal
    currency: CurrencyCode


class RateValue(TypedDict):
    value: Decimal
    basis: RateBasis


class QuantityValue(TypedDict):
    value: Decimal
    unit: UnitCode


class DeterminationInput(TypedDict):
    """
    The named values the active content reads, grouped by type. The names
    are the pack's — `line.netAmount`, not a field this contract defines —
    because which values a transaction carries is decided by content, and
    a transaction shape fixed here would be tax logic in the API
    (ZTAX-DET-001 §0.2). At least one value is required.

    """

    money: NotRequired[dict[str, MoneyValue]]
    rates: NotRequired[dict[str, RateValue]]
    quantities: NotRequired[dict[str, QuantityValue]]
    flags: NotRequired[dict[str, bool]]
    strings: NotRequired[dict[str, str]]
    """
    Free-text values, such as a situs attribute. Classified as location
    evidence because that is what content most often reads here.

    """


ReadSet: TypeAlias = dict[str, MoneyValue]
"""
The accumulator values the evaluation reads — exactly the ones the
active bundle declares, no more and no fewer. They are supplied by the
caller until the accumulator store of ADR-0004 lands, and are recorded
in the decision's envelope either way (ZTAX-DET-REQ-0034).

"""


class QuoteRequest(TypedDict):
    eventTime: Timestamp
    input: DeterminationInput
    accumulators: NotRequired[ReadSet]


class CommitRequest(TypedDict):
    businessKey: BusinessKey
    supersedes: NotRequired[DecisionId]
    eventTime: Timestamp
    input: DeterminationInput
    accumulators: NotRequired[ReadSet]


class ResultValue(TypedDict):
    """
    One emitted value. `type` says which other members are present:
    `amount` and `currency` for `MONEY`; `value` and `basis` for `RATE`;
    `value` and `unit` for `QUANTITY`; `flag` for `BOOL`; `text` for
    `STRING`; `reasonCode` for `REASON_CODE`.

    """

    type: ValueType
    amount: NotRequired[Decimal]
    currency: NotRequired[CurrencyCode]
    value: NotRequired[Decimal]
    basis: NotRequired[RateBasis]
    unit: NotRequired[UnitCode]
    flag: NotRequired[bool]
    text: NotRequired[str]
    reasonCode: NotRequired[ReasonCode]


Emitted: TypeAlias = dict[str, ResultValue]
"""
What the content emitted, by result slot. The slots are the pack's.
"""


class BundleRef(TypedDict):
    """
    The content bundle an evaluation ran against.
    """

    bundleId: str
    digest: Digest
    irVersion: int


class Quote(TypedDict):
    authoritative: bool
    """
    Always `false`. A quote is an estimate at every authorization level (ADR-0004 §2.7).
    """
    outcome: Outcome
    reasonCode: ReasonCode
    quotedAt: Timestamp
    bundle: BundleRef
    emitted: Emitted


class DecisionDigests(TypedDict):
    """
    The digests a decision names (ADR-0011 §2.8). `envelope` and `result`
    are the evidence objects; `input` is the canonical input inside the
    envelope, which is what a search by input matches.

    """

    input: Digest
    envelope: Digest
    result: Digest


class Decision(TypedDict):
    """
    One recorded decision. Immutable; a correction is a new decision whose `supersedes` names this one.
    """

    id: DecisionId
    businessKey: BusinessKey
    supersedes: NotRequired[DecisionId]
    authoritative: bool
    """
    Whether this decision may be filed. `false` for every decision before A4.
    """
    outcome: Outcome
    reasonCode: ReasonCode
    eventTime: Timestamp
    recordedAt: Timestamp
    bundle: BundleRef
    digests: DecisionDigests
    emitted: Emitted


LegalEntityId: TypeAlias = str
"""
A legal entity within the tenant (ZTAX-OBL-REQ-0018). A UUID in lowercase canonical form.
"""


ControlAccount: TypeAlias = Literal['TAX_COLLECTED_LIABILITY', 'TAX_ACCRUED_LIABILITY', 'TAX_RECOVERABLE', 'TAX_RECEIVABLE_CONTROL', 'TAX_CASH_CLEARING', 'TAX_RETURN_CLEARING', 'TAX_REMITTANCE_CLEARING', 'TAX_ADJUSTMENT_CONTROL', 'FX_CONTROL', 'ROUNDING_CONTROL', 'SUSPENSE_EXCEPTION', 'CUSTOMER_GL_BRIDGE']
"""
A Tax Control Subledger control account (ZTAX-FIN-001 §9). Never a customer GL account.
"""


class JournalLine(TypedDict):
    """
    One line. The amount is positive and the side carries the direction (ZTAX-FIN-REQ-0044).
    """

    account: ControlAccount
    side: Literal['DEBIT', 'CREDIT']
    amount: Decimal
    currency: CurrencyCode
    decisionId: NotRequired[DecisionId]


class Profile(TypedDict):
    id: str
    version: str


class Journal(TypedDict):
    """
    One posted Tax Control Subledger journal. Balanced in its one currency; append-only.
    """

    id: str
    type: Literal['INVOICE', 'CREDIT', 'REFUND', 'LIABILITY_ACCRUAL', 'RETURN', 'REMITTANCE', 'FX', 'ROUNDING', 'MIGRATION', 'ADJUSTMENT']
    legalEntityId: LegalEntityId
    sourceKind: str
    """
    The source event kind, e.g. `DECISION_COMMITTED` or `DECISION_SUPERSEDED`.
    """
    sourceId: str
    postingDate: Timestamp
    legalPeriod: str
    currency: CurrencyCode
    reversalOf: NotRequired[str]
    """
    The journal this one reverses, for a reversal.
    """
    profile: Profile
    lines: list[JournalLine]


class JournalList(TypedDict):
    journals: list[Journal]


class ControlBalance(TypedDict):
    account: ControlAccount
    currency: CurrencyCode
    debits: Decimal
    credits: Decimal


class SubledgerBalances(TypedDict):
    legalEntityId: LegalEntityId
    balances: list[ControlBalance]


ObligationId: TypeAlias = str
"""
One row of an obligation's history. A UUID in lowercase canonical form.
"""


CivilDate: TypeAlias = str
"""
A calendar date, `YYYY-MM-DD`, in a legal calendar the enclosing
object names. Not an instant: a due date is a day in the authority's
calendar, and converting it to UTC would move it.

"""


ObligationStatus: TypeAlias = Literal['OPEN', 'DATA_REQUIRED', 'READY', 'FILED', 'ACCEPTED', 'REJECTED', 'UNCERTAIN', 'PAYMENT_DUE', 'PAID', 'AMENDMENT_REQUIRED', 'SUSPENDED', 'CLOSED']
"""
A stored obligation status (ZTAX-OBL-REQ-0084).
"""


EffectiveObligationStatus: TypeAlias = Literal['OPEN', 'DATA_REQUIRED', 'READY', 'FILED', 'ACCEPTED', 'REJECTED', 'UNCERTAIN', 'PAYMENT_DUE', 'PAID', 'AMENDMENT_REQUIRED', 'SUSPENDED', 'CLOSED', 'OVERDUE']
"""
A stored status, or `OVERDUE` derived as of today for an unfiled obligation past its due date.
"""


class Definition(TypedDict):
    id: str
    version: str


class Content(TypedDict):
    bundleId: str
    bundleDigest: Digest


class Obligation(TypedDict):
    """
    One row of an obligation: a periodic duty owed by a legal entity to an
    authority, under a pinned definition version and signed content. The
    dates are civil dates in `timezone`; `periodEnd` is the period's
    last day, inclusive, and `dueDate` the legal due date.

    `assessedAmount` is the sum of the committed decisions' assessments:
    live on the current row, and as written on a superseded one.
    `supersedes` names the row this one replaced; `supersededBy`, on a
    row that is history, the row that replaced it.

    """

    id: ObligationId
    type: str
    """
    The return or duty, as the content names it.
    """
    duty: Literal['TRANSACTION_MONETARY', 'PERIODIC_CONTRIBUTION', 'REGISTRATION', 'INFORMATION_RETURN', 'RECORDKEEPING', 'NOTICE_RESPONSE']
    jurisdiction: str
    authority: str
    legalEntityId: LegalEntityId
    definition: Definition
    content: Content
    periodStart: CivilDate
    periodEnd: CivilDate
    dueDate: CivilDate
    timezone: str
    """
    The legal calendar's IANA timezone.
    """
    status: ObligationStatus
    effectiveStatus: EffectiveObligationStatus
    assessedAmount: NotRequired[Decimal]
    currency: NotRequired[CurrencyCode]
    supersedes: NotRequired[ObligationId]
    supersededBy: NotRequired[ObligationId]
    recordedAt: Timestamp
    recordedBy: NotRequired[str]
    """
    The user whose move wrote this row. Absent when a commit wrote it.
    """


class ObligationList(TypedDict):
    obligations: list[Obligation]


class ObligationTransitionRequest(TypedDict):
    to: Literal['OPEN', 'DATA_REQUIRED', 'READY', 'SUSPENDED', 'CLOSED']


class ClassificationProposalRequest(TypedDict):
    subjectRef: str
    """
    The caller's reference for the item, e.g. a catalog SKU.
    """
    description: str
    """
    The item's commercial description. Catalog text, never customer data; it is sent to the Gateway.
    """


class AiProvenance(TypedDict):
    """
    Which governed use case, model, provider, prompt and AI train produced an output (ADR-0006 §2.7).
    """

    useCase: str
    modelProfile: str
    providerProfile: str
    promptProfile: str
    aiTrainVersion: str


class ClassificationProposal(TypedDict):
    """
    An advisory mapping proposal. `authoritative` is always false; a person confirms it or it is nothing.
    """

    id: str
    subjectRef: str
    proposedCode: str
    """
    An existing ontology node, `ontology:...`.
    """
    confidence: NotRequired[str]
    """
    The model's own score as a canonical decimal in [0, 1]. Never a threshold that makes the proposal authoritative.
    """
    authoritative: Literal[False]
    provenance: AiProvenance


class ReplayReport(TypedDict):
    decisionId: DecisionId
    verdict: ReplayVerdict
    envelopeDigest: Digest
    recordedResult: Digest
    replayedResult: NotRequired[Digest]
    divergence: NotRequired[str]
    """
    For `DIVERGED`, the first path at which the results differ. A path, never a value.
    """


class V1AdminUsersGetResponse(TypedDict):
    users: list[User]


class V1AdminSessionsGetResponse(TypedDict):
    sessions: list[SessionSummary]


class V1AdminAuditGetResponse(TypedDict):
    records: list[AuditRecord]


class Problem(TypedDict):
    """
    RFC 9457 Problem Details, with the `ztx_` extensions from ADR-0016 §2.5.
    The extensions carry identifiers rather than data: an error that needs
    data to be understood names a decision or a request, and the data is
    fetched through an audited path.

    """

    type: str
    """
    A stable, versioned, documented URI for this reason code.
    """
    title: str
    """
    A short summary. **Do not match on this**; it may be reworded.
    """
    status: int
    detail: NotRequired[str]
    """
    What happened and what to do about it. Also not for matching.
    """
    instance: NotRequired[str]
    """
    The request path this occurred on.
    """
    ztx_reason_code: ReasonCode
    ztx_request_id: NotRequired[str]
    """
    This request's identifier, also returned in the `X-Request-Id`
    header. Quote it in a support request; it is what correlates the
    response with the server-side log.

    """
    ztx_trace_id: NotRequired[str]
    """
    The OpenTelemetry trace identifier, where tracing is enabled.
    """
    ztx_decision_id: NotRequired[str]
    """
    The decision this error concerns, where there is one.
    """
    ztx_field: NotRequired[str]
    """
    The contract field at fault, for a validation failure. A field name,
    never a value — a value in an error message is a value in a log.

    """
    ztx_retryable: bool
    """
    Whether retrying this request unchanged could succeed. Only a
    transient failure is retryable; everything else fails identically,
    and a client that retries a validation failure is generating load
    rather than making progress.

    """


class Capabilities(TypedDict):
    cell: str
    """
    The cell serving this request.
    """
    region: str
    environment: str
    trains: Trains
    canonProfile: str
    """
    The canonicalization profile this deployment digests under. It is
    never redefined: a change to any profile rule is a new profile, and
    the old one stays implemented for as long as any record references
    it (ADR-0011 §2.6).

    """
    authoritative: bool
    """
    **Whether this deployment may produce authoritative fiscal output.**
    False until A4. A deployment reporting `false` produces advisory
    figures that must not be filed, and says so here rather than leaving
    a client to assume.

    """
    reasonCodes: list[ReasonCode]
    """
    Every reason code this deployment can emit.
    """
    content: NotRequired[ContentCapability]
