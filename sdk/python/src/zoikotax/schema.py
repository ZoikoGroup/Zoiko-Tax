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


class DecisionDigests(TypedDict):
    """
    The digests a decision names (ADR-0011 §2.8). `envelope` and `result`
    are the evidence objects; `input` is the canonical input inside the
    envelope, which is what a search by input matches.

    """

    input: Digest
    envelope: Digest
    result: Digest


JurisdictionId: TypeAlias = str
"""
A jurisdiction's content identifier (ADR-0012 §2.4), such as
`jurisdiction:ca-qc`. Classified as location evidence: on a consumer
document, the deepest jurisdiction is where the customer is.

"""


LocationCode: TypeAlias = str
"""
An ISO 3166-1 alpha-2 country code (`DE`) or ISO 3166-2 subdivision code (`CA-QC`).
"""


TaxDocumentType: TypeAlias = Literal['INVOICE', 'CREDIT_NOTE']
"""
An `INVOICE` carries non-negative amounts; a `CREDIT_NOTE` carries non-positive ones.
"""


PriceMode: TypeAlias = Literal['EXCLUSIVE', 'INCLUSIVE']
"""
`EXCLUSIVE`: amounts are net and customer charges are added.
`INCLUSIVE`: amounts include the customer-charged taxes, which are
extracted (DET-001 §7) — never computed on the gross and added.

"""


ServiceFamily: TypeAlias = Literal['MOBILE_VOICE', 'FIXED_VOICE', 'MESSAGING', 'MOBILE_DATA', 'FIXED_BROADBAND', 'VOIP', 'SIP_TRUNKING', 'UCAAS', 'CCAAS', 'CPAAS', 'IOT_CONNECTIVITY', 'SUBSCRIPTION_IDENTITY', 'SATELLITE_CONNECTIVITY', 'ROAMING_RETAIL', 'ROAMING_WHOLESALE', 'INTERCONNECT', 'WHOLESALE_ACCESS', 'EQUIPMENT_SALE', 'EQUIPMENT_RENTAL', 'INSTALLATION_ACTIVATION', 'PROFESSIONAL_SERVICES', 'CONTENT_MEDIA', 'SOFTWARE', 'OTHER_NON_TELECOM']
"""
A ZTAX-CLS-001 ontology family. A tenant's SKU is mapped to one before
determination; a line without one is held as `CLASSIFICATION_REQUIRED`
rather than guessed at.

"""


SitusEvidenceType: TypeAlias = Literal['PRIMARY_PLACE_OF_USE', 'SERVICE_ADDRESS', 'BILLING_ADDRESS', 'FIXED_LINE_LOCATION', 'NETWORK_LOCATION', 'SIM_COUNTRY', 'IP_GEOLOCATION', 'BANK_LOCATION', 'CUSTOMER_DECLARATION']
"""
The JUR-001 §3.2 evidence vocabulary a caller may supply directly.
Seller and customer establishment and VAT identification arrive on the
parties instead, so one fact cannot arrive twice and disagree.

"""


SitusEvidenceSource: TypeAlias = Literal['CUSTOMER_ASSERTED', 'SELLER_RECORDED', 'NETWORK_DERIVED', 'THIRD_PARTY_VERIFIED', 'AUTHORITY_VERIFIED']


VatValidation: TypeAlias = Literal['VALIDATED', 'UNVALIDATED', 'VALIDATION_UNAVAILABLE']
"""
`VALIDATED` establishes a business customer where the pack recognises
it. `UNVALIDATED` and `VALIDATION_UNAVAILABLE` are different facts and
a pack may treat them differently (JUR-001 §7.3).

"""


CertificateVerification: TypeAlias = Literal['VERIFIED', 'UNVERIFIED', 'UNAVAILABLE']
"""
Only `VERIFIED` exempts. `UNAVAILABLE` holds the line for review; a
verification is never assumed to have succeeded.

"""


PartyReference: TypeAlias = str
"""
A party's reference in the caller's systems. Not interpreted; a certificate's holder must equal the customer's.
"""


ContentLabel: TypeAlias = str
"""
An identifier or label from the active content — a component, regime, authority, tax type, slot or certificate type.
"""


ContentText: TypeAlias = str
"""
Text from the active content, such as a source citation. Not interpreted.
"""


class Place(TypedDict):
    """
    A location named by its jurisdiction — exactly one of a jurisdiction
    identifier and an ISO code. There is no street address: resolution
    never geocodes (JUR-001 §8.2), so an address is mapped to a
    jurisdiction by the system that records it.

    """

    jurisdiction: NotRequired[JurisdictionId]
    subdivision: NotRequired[LocationCode]
    country: NotRequired[LocationCode]


class VatIdentification(TypedDict):
    """
    The customer's VAT registration country and validation status. The number itself is not needed and not retained.
    """

    country: LocationCode
    validation: VatValidation


class TaxParty(TypedDict):
    id: PartyReference
    establishment: NotRequired[Place]
    vat: NotRequired[VatIdentification]


class SitusEvidenceItem(TypedDict):
    """
    One typed, attributed signal about where the supply took place (JUR-001 §3.1).
    """

    type: SitusEvidenceType
    source: SitusEvidenceSource
    collectedAt: NotRequired[Timestamp]
    place: Place


class TaxLinePart(TypedDict):
    """
    One component of an approved bundle decomposition.
    """

    key: ContentLabel
    service: NotRequired[ServiceFamily]
    amount: Decimal
    quantity: NotRequired[QuantityValue]


class TaxLine(TypedDict):
    """
    One rated line, in the document currency. A bundle line carries
    `parts` instead of its own `service` and `quantity`, and the parts
    must sum to `amount` exactly.

    """

    key: ContentLabel
    sku: NotRequired[ContentLabel]
    service: NotRequired[ServiceFamily]
    amount: Decimal
    quantity: NotRequired[QuantityValue]
    evidence: NotRequired[list[SitusEvidenceItem]]
    parts: NotRequired[list[TaxLinePart]]


DeterminationState: TypeAlias = Literal['RESOLVED', 'REVIEW_REQUIRED', 'UNSUPPORTED', 'AMBIGUOUS', 'CONFLICTED']
"""
`RESOLVED` is the only state that carries amounts. Every other state
is recorded with its evidence and reason and is never a zero tax.

"""


Treatment: TypeAlias = Literal['CUSTOMER_CHARGE', 'PROVIDER_COST', 'REVERSE_CHARGE', 'EXEMPT']
"""
`CUSTOMER_CHARGE` is on the invoice. `PROVIDER_COST` is borne by the
seller and never on it. `REVERSE_CHARGE` is self-assessed by the
customer: disclosed, neither charged nor remitted by the seller.
`EXEMPT` is a customer charge a verified certificate suppressed.

"""


ResponsibleParty: TypeAlias = Literal['SELLER', 'BUYER', 'NONE']


class ResponsibilityAssignment(TypedDict):
    """
    The five roles, each answered separately (ZTAX-OBL-REQ-0002 to -0006).
    """

    id: ContentLabel
    incidence: ResponsibleParty
    collection: ResponsibleParty
    reporting: ResponsibleParty
    remittance: ResponsibleParty
    economicBearer: ResponsibleParty


class ExemptionApplied(TypedDict):
    """
    A charge a certificate suppressed, and the assessment it would have been.
    """

    certificate: PartyReference
    type: ContentLabel
    assessed: NotRequired[Decimal]


class DocumentTotals(TypedDict):
    """
    The figures that must stay apart (playbook §7): what the customer is
    charged, what the provider bears, and what the customer self-assesses.

    """

    net: Decimal
    customerTax: Decimal
    customerTotal: Decimal
    providerAccrual: Decimal
    reverseCharge: Decimal


class DeterminationStep(TypedDict):
    """
    One step of the deterministic trace — selection, situs, exemption and DET-001 evaluation steps.
    """

    item: NotRequired[ContentLabel]
    component: NotRequired[ContentLabel]
    step: ContentLabel
    detail: str


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


RefundId: TypeAlias = str
"""
A refund identifier. A UUIDv7 in lowercase canonical form (ADR-0012 §2.1).
"""


RefundStatus: TypeAlias = Literal['REQUESTED', 'PENDING', 'COMPLETED', 'FAILED', 'UNCERTAIN']
"""
A refund's own lifecycle (ZTAX-FIN-REQ-0059), separate from any credit.
`UNCERTAIN` means the provider's answer did not say whether the money
moved; it is neither paid nor failed.

"""


RefundOutcome: TypeAlias = Literal['ACCEPTED', 'SUCCEEDED', 'DECLINED', 'TIMED_OUT', 'UNKNOWN']
"""
What the payment provider reported.
"""


PaymentReference: TypeAlias = str
"""
A payment provider's reference. It can identify a payer's transaction, so logs carry it redacted.
"""


class RefundRequest(TypedDict):
    decisionId: DecisionId
    amount: MoneyValue
    paymentReference: PaymentReference
    reason: NotRequired[str]
    """
    Why the refund is made. Free text; it may name the customer.
    """


class RefundEvent(TypedDict):
    """
    One entry in a refund's history. The first is the request; each later one is a provider report that moved it.
    """

    seq: int
    status: RefundStatus
    outcome: NotRequired[RefundOutcome]
    externalReference: NotRequired[PaymentReference]
    recordedAt: Timestamp
    recordedBy: NotRequired[str]
    """
    The user who recorded the event. Absent for system work.
    """


class Refund(TypedDict):
    """
    A refund of tax a committed decision charged. `status` is the latest
    event's; `history` is every event, oldest first.

    """

    id: RefundId
    decisionId: DecisionId
    legalEntityId: LegalEntityId
    amount: MoneyValue
    paymentReference: PaymentReference
    reason: NotRequired[str]
    status: RefundStatus
    requestedAt: Timestamp
    requestedBy: NotRequired[str]
    """
    The user who requested the refund. Absent for system work.
    """
    history: list[RefundEvent]


class RefundReportRequest(TypedDict):
    outcome: RefundOutcome
    externalReference: NotRequired[PaymentReference]


WebhookId: TypeAlias = str
"""
A webhook subscription. A UUIDv7 in lowercase canonical form.
"""


DeliveryId: TypeAlias = str
"""
One delivery of one event to one webhook.
"""


EventType: TypeAlias = Literal['com.zoikotax.decision.committed', 'com.zoikotax.decision.corrected', 'com.zoikotax.obligation.status-changed', 'com.zoikotax.accumulator.threshold-crossed', 'com.zoikotax.refund.requested', 'com.zoikotax.refund.status-changed', 'com.zoikotax.document.committed']
"""
An event type this cell emits, as the AsyncAPI contract names it.
"""


WebhookStatus: TypeAlias = Literal['ACTIVE', 'PAUSED', 'DISABLED']


DeliveryStatus: TypeAlias = Literal['PENDING', 'DELIVERED', 'DEAD']
"""
`PENDING` awaits its next attempt; `DEAD` is the dead-letter state, kept and replayable.
"""


WebhookUrl: TypeAlias = str
"""
An HTTPS endpoint at a public address, with no credentials in it.
"""


class WebhookCreateRequest(TypedDict):
    url: WebhookUrl
    eventTypes: list[EventType]
    description: NotRequired[str]


class Webhook(TypedDict):
    id: WebhookId
    url: WebhookUrl
    eventTypes: list[EventType]
    description: NotRequired[str]
    status: WebhookStatus
    createdAt: Timestamp
    statusChangedAt: Timestamp


class WebhookList(TypedDict):
    webhooks: list[Webhook]


class WebhookSecret(TypedDict):
    """
    A signing secret, in the form Standard Webhooks libraries take. Returned once, by the response that issued it.
    """

    version: int
    secret: str


class WebhookCreated(TypedDict):
    webhook: Webhook
    secret: WebhookSecret


class WebhookStatusRequest(TypedDict):
    status: WebhookStatus


class WebhookSecretRotation(TypedDict):
    secret: WebhookSecret
    previousRetiresAt: Timestamp


class WebhookDelivery(TypedDict):
    id: DeliveryId
    webhookId: WebhookId
    eventId: str
    """
    The CloudEvents id, sent as `webhook-id`. The receiver's deduplication key.
    """
    eventType: EventType
    status: DeliveryStatus
    attempts: int
    nextAttemptAt: NotRequired[Timestamp]
    createdAt: Timestamp
    deliveredAt: NotRequired[Timestamp]
    replayOf: NotRequired[DeliveryId]


class WebhookDeliveryList(TypedDict):
    deliveries: list[WebhookDelivery]


class WebhookAttempt(TypedDict):
    attempt: int
    startedAt: Timestamp
    durationMs: int
    statusCode: NotRequired[int]
    """
    The receiver's HTTP status. Absent when no response arrived.
    """
    error: NotRequired[str]
    """
    The transport's error, when no 2xx arrived. Never the receiver's response body.
    """


class WebhookDeliveryDetail(TypedDict):
    delivery: WebhookDelivery
    attempts: list[WebhookAttempt]


JobId: TypeAlias = str
"""
An asynchronous job. A UUIDv7 in lowercase canonical form.
"""


class JobItem(TypedDict):
    index: int
    businessKey: BusinessKey
    status: Literal['PENDING', 'SUCCEEDED', 'FAILED']
    decisionId: NotRequired[DecisionId]
    reasonCode: NotRequired[ReasonCode]


class Job(TypedDict):
    id: JobId
    operation: Literal['COMMIT']
    status: Literal['QUEUED', 'RUNNING', 'COMPLETED']
    itemCount: int
    succeeded: int
    failed: int
    pending: int
    requestedAt: Timestamp
    startedAt: NotRequired[Timestamp]
    finishedAt: NotRequired[Timestamp]
    items: list[JobItem]


SealId: TypeAlias = str
"""
A period seal. A UUIDv7 in lowercase canonical form.
"""


class SealRequest(TypedDict):
    periodStart: Timestamp
    periodEnd: Timestamp


class Seal(TypedDict):
    """
    A signed statement that the decisions recorded in `[periodStart, periodEnd)` were exactly these.
    """

    id: SealId
    periodStart: Timestamp
    periodEnd: Timestamp
    leafCount: int
    merkleRoot: Digest
    keyId: str
    cell: str
    sealedAt: Timestamp


class SealList(TypedDict):
    seals: list[Seal]


class SealSignature(TypedDict):
    keyId: str
    algorithm: Literal['ECDSA_P384_SHA384']
    value: str
    """
    The DER signature, base64.
    """


class SealDocument(TypedDict):
    seal: Seal
    signedPayload: str
    """
    The canonical payload exactly as signed, base64. It names the tenant, cell, period, leaf count, leaf order and root.
    """
    signature: SealSignature


class SealVerification(TypedDict):
    sealId: SealId
    verdict: Literal['VALID', 'SIGNATURE_INVALID', 'RECORD_MISMATCH', 'ROOT_MISMATCH', 'EVIDENCE_MISSING']
    keyId: str
    leafCount: int
    signedRoot: Digest
    recomputedRoot: NotRequired[Digest]
    detail: NotRequired[str]
    """
    What failed, for the operator. Absent when VALID.
    """


class SealLeaf(TypedDict):
    """
    One decision's leaf. Its digest is the canonical (canon/v1) digest of exactly these three members.
    """

    decisionId: DecisionId
    recordedAt: Timestamp
    resultDigest: Digest


class InclusionProof(TypedDict):
    sealId: SealId
    merkleRoot: Digest
    leafCount: int
    leafIndex: int
    leaf: SealLeaf
    leafDigest: Digest
    auditPath: list[Digest]
    """
    Sibling node hashes from the leaf's level to the root, in the digest form.
    """


DocumentId: TypeAlias = str
"""
A fiscal document. A UUIDv7 in lowercase canonical form, never the legal document number.
"""


DocumentLineId: TypeAlias = str


DocumentNumber: TypeAlias = str
"""
The legal, customer-visible document number (ZTAX-FIN-REQ-0006). Kept apart from the id.
"""


class ExternalReference(TypedDict):
    """
    Another system's identifier for the document (ZTAX-DOM-REQ-0003). Never a key, never parsed, never assumed unique.
    """

    sourceSystem: str
    namespace: str
    value: str


class DocumentTax(TypedDict):
    """
    One tax on one line, naming the committed decision it comes from and the component the decision's content posts.
    """

    decisionId: DecisionId
    component: str
    amount: Decimal


SourceLineRef: TypeAlias = str
"""
The billing or ERP line a document line came from (ZTAX-FIN-REQ-0020).
"""


class DocumentLineRequest(TypedDict):
    sourceLineRef: NotRequired[SourceLineRef]
    componentInstance: NotRequired[str]
    net: Decimal
    discount: NotRequired[Decimal]
    allocationRef: NotRequired[str]
    """
    Required with a discount — the allocation that produced it (ZTAX-FIN-REQ-0021).
    """
    predecessorLineId: NotRequired[DocumentLineId]
    taxes: list[DocumentTax]


class DocumentRequest(TypedDict):
    type: Literal['INVOICE', 'DEBIT_NOTE', 'ADJUSTMENT']
    number: NotRequired[DocumentNumber]
    issueDate: CivilDate
    taxPoint: Timestamp
    currency: CurrencyCode
    externalRef: NotRequired[ExternalReference]
    lines: list[DocumentLineRequest]


class CorrectionRequest(TypedDict):
    type: Literal['VOID', 'CREDIT_NOTE', 'REBILL']
    reason: ReasonCode
    number: NotRequired[DocumentNumber]
    issueDate: CivilDate
    taxPoint: Timestamp
    cancelLines: NotRequired[list[DocumentLineId]]
    """
    A credit note's lines to cancel; absent is every line not yet cancelled.
    """
    lines: NotRequired[list[DocumentLineRequest]]
    """
    A rebill's lines, each naming the original line it replaces.
    """


class DocumentLine(TypedDict):
    id: DocumentLineId
    sourceLineRef: NotRequired[SourceLineRef]
    componentInstance: NotRequired[str]
    net: Decimal
    discount: NotRequired[Decimal]
    allocationRef: NotRequired[str]
    predecessorLineId: NotRequired[DocumentLineId]
    taxes: list[DocumentTax]


class DocumentStatusEvent(TypedDict):
    seq: int
    status: Literal['COMMITTED', 'ISSUED', 'DELIVERED', 'ACCEPTED', 'PARTIALLY_CREDITED', 'FULLY_CREDITED', 'VOIDED', 'REFUNDED', 'AMENDED', 'DISPUTED', 'CLOSED', 'SUSPENDED']
    causeId: NotRequired[DocumentId]
    recordedAt: Timestamp
    recordedBy: NotRequired[str]


class FiscalDocument(TypedDict):
    """
    A committed fiscal document. Amounts are in `currency`; a cancelling
    document's are negative. `status` is the latest of `history`.

    """

    id: DocumentId
    type: Literal['INVOICE', 'CREDIT_NOTE', 'DEBIT_NOTE', 'PARTIAL_CREDIT', 'VOID', 'REFUND', 'REBILL', 'ADJUSTMENT', 'AMENDMENT', 'RESTATEMENT']
    number: NotRequired[DocumentNumber]
    rootId: DocumentId
    predecessors: list[DocumentId]
    reasonCode: NotRequired[ReasonCode]
    legalEntityId: LegalEntityId
    issueDate: CivilDate
    taxPoint: Timestamp
    currency: CurrencyCode
    externalRef: NotRequired[ExternalReference]
    decisionIds: list[DecisionId]
    lines: list[DocumentLine]
    netTotal: Decimal
    taxTotal: Decimal
    grossTotal: Decimal
    status: Literal['COMMITTED', 'ISSUED', 'DELIVERED', 'ACCEPTED', 'PARTIALLY_CREDITED', 'FULLY_CREDITED', 'VOIDED', 'REFUNDED', 'AMENDED', 'DISPUTED', 'CLOSED', 'SUSPENDED']
    history: list[DocumentStatusEvent]
    recordedAt: Timestamp
    recordedBy: NotRequired[str]


class DocumentLineage(TypedDict):
    documents: list[FiscalDocument]


LegalPeriod: TypeAlias = str
"""
A subledger legal period, a month.
"""


PeriodState: TypeAlias = Literal['OPEN', 'SOFT_CLOSE', 'HARD_CLOSE', 'REOPENED', 'AMENDMENT_ACTIVE', 'SEALED']


PeriodReason: TypeAlias = str
"""
Free text entered by an operator.
"""


class CloseManifestDocument(TypedDict):
    """
    A close manifest exactly as digested, base64 canonical JSON.
    """

    digest: Digest
    body: str


class PeriodTransitionRequest(TypedDict):
    to: Literal['OPEN', 'SOFT_CLOSE', 'HARD_CLOSE', 'AMENDMENT_ACTIVE', 'SEALED']
    reason: NotRequired[PeriodReason]


ReopenRequestId: TypeAlias = str


class ReopenRequestBody(TypedDict):
    reason: PeriodReason


class PeriodReopenRequest(TypedDict):
    id: ReopenRequestId
    period: LegalPeriod
    reason: PeriodReason
    requestedAt: Timestamp
    requestedBy: str


RunId: TypeAlias = str


ItemId: TypeAlias = str


ReconStage: TypeAlias = Literal['R1_CALCULATED_TO_DOCUMENT', 'R2_DOCUMENT_TO_COLLECTION', 'R3_DOCUMENT_TO_SUBLEDGER', 'R4_SUBLEDGER_TO_RETURN', 'R5_RETURN_TO_REMITTANCE', 'R6_SUBLEDGER_TO_GL', 'R7_END_TO_END']


RootCause: TypeAlias = Literal['CLASSIFICATION', 'JURISDICTION', 'TAX_RULE', 'ROUNDING', 'FX', 'DOCUMENT', 'COLLECTION', 'POSTING', 'RETURN', 'REMITTANCE', 'GL', 'TIMING', 'DATA', 'OTHER']
"""
FIN-001 §18's root-cause taxonomy (ZTAX-FIN-REQ-0083).
"""


EvidenceReference: TypeAlias = str
"""
A reference to evidence a resolution relies on — a document id, a ticket, a statement line.
"""


class ReconciliationRequest(TypedDict):
    period: LegalPeriod


class ReconResolution(TypedDict):
    actor: Literal['HUMAN', 'AI_A3']
    resolver: NotRequired[str]
    reason: str
    action: Literal['ADJUST', 'RECLASSIFY', 'AMEND', 'WAIT', 'WAIVE_WITH_APPROVAL', 'EXTERNAL_CORRECTION']
    rootCause: RootCause
    evidence: list[EvidenceReference]
    resolvedAt: Timestamp


class ReconciliationItem(TypedDict):
    """
    One comparison. `variance` is observed minus expected, exact,
    whatever the status (ZTAX-FIN-REQ-0081); absent when one side is.

    """

    id: ItemId
    stage: ReconStage
    matchKey: str
    """
    The canonical id compared on, such as `decision:<id>` or `refund:<id>`.
    """
    expected: NotRequired[MoneyValue]
    observed: NotRequired[MoneyValue]
    variance: NotRequired[MoneyValue]
    status: Literal['MATCHED', 'TOLERANCE_MATCH', 'UNMATCHED', 'PARTIAL', 'DUPLICATE', 'MISSING', 'CONFLICTED', 'PENDING', 'EXPLAINED', 'RESOLVED']
    rootCause: NotRequired[RootCause]
    detail: NotRequired[str]
    resolution: NotRequired[ReconResolution]


class ReconciliationRun(TypedDict):
    id: RunId
    period: LegalPeriod
    legalEntityId: LegalEntityId
    ranAt: Timestamp
    ranBy: NotRequired[str]
    firstBreak: NotRequired[ReconStage]
    unavailable: list[ReconStage]
    items: list[ReconciliationItem]


class ResolutionRequest(TypedDict):
    reason: str
    action: Literal['ADJUST', 'RECLASSIFY', 'AMEND', 'WAIT', 'WAIVE_WITH_APPROVAL', 'EXTERNAL_CORRECTION']
    rootCause: RootCause
    evidence: list[EvidenceReference]


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


RetentionPolicyId: TypeAlias = str
"""
A retention policy's stable name; its versions share it.
"""


RetentionRecordClass: TypeAlias = Literal['DECISION', 'DOCUMENT', 'JOURNAL', 'REFUND']


RetentionTrigger: TypeAlias = Literal['EVENT_TIME', 'RECORDED_AT', 'EVENT_YEAR_END']
"""
What the period runs from: the taxable event, the record's making, or
the end of the calendar year of the event.

"""


CountryCode: TypeAlias = str
"""
ISO 3166-1 alpha-2.
"""


class RetentionPolicyRequest(TypedDict):
    id: RetentionPolicyId
    recordClass: RetentionRecordClass
    country: CountryCode
    years: int
    trigger: RetentionTrigger
    effectiveFrom: Timestamp
    citation: str
    """
    The statute, regulation or contract the period comes from.
    """


class RetentionPolicy(TypedDict):
    id: RetentionPolicyId
    version: int
    recordClass: RetentionRecordClass
    country: CountryCode
    years: int
    trigger: RetentionTrigger
    effectiveFrom: Timestamp
    citation: str
    recordedAt: Timestamp
    recordedBy: str
    """
    The administrator who recorded this version.
    """


class RetentionPolicyList(TypedDict):
    policies: list[RetentionPolicy]


class PolicyVersionRef(TypedDict):
    id: RetentionPolicyId
    version: int


LegalHoldId: TypeAlias = str


LegalHoldReason: TypeAlias = str
"""
Free text entered by an administrator.
"""


class LegalHoldScope(TypedDict):
    """
    What a hold covers. Every criterion given must hold: named decisions
    or business keys, a window of event times `[eventFrom, eventTo)`, and
    a legal entity narrowing either. At least names or a window; a window
    alone spans at most ten years.

    """

    legalEntityId: NotRequired[LegalEntityId]
    businessKeys: NotRequired[list[BusinessKey]]
    decisionIds: NotRequired[list[DecisionId]]
    eventFrom: NotRequired[Timestamp]
    eventTo: NotRequired[Timestamp]


class LegalHoldEvent(TypedDict):
    seq: int
    kind: Literal['PLACED', 'SCOPE_CHANGED', 'RELEASED']
    scope: LegalHoldScope
    reason: LegalHoldReason
    recordedAt: Timestamp
    recordedBy: str


class LegalHold(TypedDict):
    id: LegalHoldId
    matter: str
    """
    The claim, investigation or regulator request the hold serves.
    """
    status: Literal['ACTIVE', 'RELEASED']
    scope: LegalHoldScope
    history: list[LegalHoldEvent]


class LegalHoldList(TypedDict):
    holds: list[LegalHold]


class LegalHoldRequest(TypedDict):
    matter: str
    reason: LegalHoldReason
    scope: LegalHoldScope


class LegalHoldScopeRequest(TypedDict):
    reason: LegalHoldReason
    scope: LegalHoldScope


class LegalHoldReleaseRequest(TypedDict):
    reason: LegalHoldReason


ServiceState: TypeAlias = Literal['INFORM', 'COMPUTE', 'PREPARE', 'FILE', 'REPRESENT', 'ADVISE']


LegalStatus: TypeAlias = Literal['DIRECT_ALLOWED', 'DIRECT_WITH_AUTH', 'QUALIFIED_PERSON_REQUIRED', 'PARTNER_REQUIRED', 'CUSTOMER_ONLY', 'COUNSEL_PENDING', 'PROHIBITED', 'SUSPENDED']


AuthorizationType: TypeAlias = Literal['NONE', 'CONTRACT', 'DECLARATION', 'POA', 'TAX_INFORMATION', 'PORTAL_DELEGATION', 'FILING_MANDATE']


AuthorizationPermission: TypeAlias = Literal['READ_INFO', 'PREPARE', 'SUBMIT', 'RECEIVE_NOTICE', 'REPRESENT', 'SIGN']


FundsPosture: TypeAlias = Literal['NO_CUSTODY', 'INSTRUCTION_ONLY', 'PSP_PARTNER', 'LICENSED_PROGRAM']


AuthorityCode: TypeAlias = str
"""
An authority within a country, as the matrix names it.
"""


AuthorizationId: TypeAlias = str


class LegalRule(TypedDict):
    id: str
    version: int
    country: CountryCode
    authority: AuthorityCode
    service: ServiceState
    status: LegalStatus
    provider: str
    """
    The Zoiko entity or approved partner that performs the service.
    """
    authorizationType: AuthorizationType
    requiresPeriods: bool
    """
    The authority accepts no generic grant; an authorization names its periods.
    """
    requiresMatters: bool
    """
    The authority accepts no generic grant; an authorization names its matters.
    """
    qualification: NotRequired[str]
    credential: NotRequired[str]
    """
    The kind of credential the authority needs — never a credential.
    """
    funds: FundsPosture
    opinionRef: str
    """
    The counsel memo or authority source the rule rests on.
    """
    effectiveFrom: Timestamp
    effectiveTo: NotRequired[Timestamp]


class LegalMatrix(TypedDict):
    version: str
    """
    The matrix edition; empty when the cell has none.
    """
    draft: bool
    """
    A matrix nobody has approved. Loads in development only.
    """
    digest: NotRequired[Digest]
    rules: list[LegalRule]


class AuthorizationEvent(TypedDict):
    seq: int
    kind: Literal['GRANTED', 'REVOKED', 'SUPERSEDED']
    reason: NotRequired[str]
    supersededBy: NotRequired[AuthorizationId]
    recordedAt: Timestamp
    recordedBy: str


AuthorizationMatter: TypeAlias = str
"""
A form or matter a grant covers, as the customer named it.
"""


AuthorizationEvidence: TypeAlias = str
"""
A reference to the signed artifact, consent proof or authority acknowledgement.
"""


CredentialRef: TypeAlias = str
"""
Where the authority credential is held in the secrets vault. Never the credential.
"""


Representative: TypeAlias = str
"""
The individual or entity acting under the grant.
"""


class CustomerAuthorization(TypedDict):
    id: AuthorizationId
    legalEntityId: LegalEntityId
    country: CountryCode
    authority: AuthorityCode
    type: AuthorizationType
    permissions: list[AuthorizationPermission]
    matters: list[AuthorizationMatter]
    periodFrom: NotRequired[LegalPeriod]
    periodTo: NotRequired[LegalPeriod]
    representative: NotRequired[Representative]
    effectiveFrom: Timestamp
    expiresAt: NotRequired[Timestamp]
    evidence: list[AuthorizationEvidence]
    credentialRef: NotRequired[CredentialRef]
    supersedes: NotRequired[AuthorizationId]
    status: Literal['ACTIVE', 'REVOKED', 'SUPERSEDED']
    inForce: bool
    """
    Active, effective and not expired, now.
    """
    recordedAt: Timestamp
    recordedBy: str
    history: list[AuthorizationEvent]


class AuthorizationList(TypedDict):
    authorizations: list[CustomerAuthorization]


class AuthorizationRequest(TypedDict):
    legalEntityId: NotRequired[LegalEntityId]
    country: CountryCode
    authority: AuthorityCode
    type: AuthorizationType
    permissions: list[AuthorizationPermission]
    matters: NotRequired[list[AuthorizationMatter]]
    periodFrom: NotRequired[LegalPeriod]
    periodTo: NotRequired[LegalPeriod]
    representative: NotRequired[Representative]
    effectiveFrom: Timestamp
    expiresAt: NotRequired[Timestamp]
    evidence: list[AuthorizationEvidence]
    credentialRef: NotRequired[CredentialRef]
    supersedes: NotRequired[AuthorizationId]


class RevocationRequest(TypedDict):
    reason: str


class AuthorizationCheckRequest(TypedDict):
    country: CountryCode
    authority: AuthorityCode
    service: ServiceState
    legalEntityId: NotRequired[LegalEntityId]
    period: NotRequired[LegalPeriod]
    matter: NotRequired[AuthorizationMatter]


class RuleRef(TypedDict):
    id: str
    version: int


class AuthorizationCheck(TypedDict):
    allowed: bool
    status: LegalStatus
    reason: NotRequired[Literal['NO_RULE', 'RULE_CONFLICT', 'COUNSEL_PENDING', 'PROHIBITED', 'SUSPENDED', 'PARTNER_REQUIRED', 'CUSTOMER_ONLY', 'QUALIFIED_PERSON_REQUIRED', 'AUTHORIZATION_MISSING', 'AUTHORIZATION_EXPIRED', 'AUTHORIZATION_REVOKED', 'AUTHORIZATION_OUT_OF_SCOPE', 'CREDENTIAL_MISSING', 'MATRIX_UNAVAILABLE', 'ACTION_NOT_WELL_FORMED']]
    """
    Why the action is blocked; absent when it is allowed.
    """
    rule: NotRequired[RuleRef]
    authorizationId: NotRequired[AuthorizationId]
    matrixVersion: NotRequired[str]
    matrixDigest: NotRequired[Digest]
    detail: NotRequired[str]
    checkedAt: Timestamp


class DashboardDeterminations(TypedDict):
    """
    Decisions recorded in the period and the one before it, and the tax
    their journals credited to `TAX_COLLECTED_LIABILITY`, per currency.

    """

    decisions: int
    tax: list[MoneyValue]
    previousPeriod: LegalPeriod
    previousDecisions: int
    previousTax: list[MoneyValue]
    reviewRequired: int
    """
    Decisions in the period whose outcome needs a person — REVIEW_REQUIRED, AMBIGUOUS, CONFLICTED or UNSUPPORTED.
    """


class DashboardObligations(TypedDict):
    """
    Current obligations not yet filed whose due date falls within
    `windowDays` of today in their own legal calendar, overdue ones
    included, and the assessed amounts of all of them per currency.

    """

    windowDays: int
    due: int
    overdue: int
    amounts: list[MoneyValue]


class DashboardReconciliation(TypedDict):
    """
    Items of the latest run of each legal period. `matched` counts
    matched, explained and resolved items; `breaks` exceptions nobody has
    resolved; `open` items still pending.

    """

    runs: int
    matched: int
    breaks: int
    open: int


class DashboardEvidence(TypedDict):
    """
    Seal coverage of the tenant's decisions: how many are recorded, and
    how many fall inside a period seal and can therefore prove their
    inclusion (ADR-0011 §2.4). Replay is not counted: its verdict is
    computed on request and never stored.

    """

    decisions: int
    sealed: int
    seals: int


DashboardReadiness: TypeAlias = Literal['READY', 'AT_RISK', 'BLOCKED']
"""
Whether filing with an authority could proceed now: `READY` when the
gate allows it, `AT_RISK` when only a customer authorization or
credential stands in the way, `BLOCKED` otherwise.

"""


class DashboardAuthority(TypedDict):
    country: CountryCode
    authority: AuthorityCode
    readiness: DashboardReadiness
    reason: NotRequired[str]
    """
    The gate's reason when filing is not allowed; absent when it is.
    """


class DashboardJurisdictions(TypedDict):
    """
    Every authority the LegalAuthorization matrix names, resolved through
    the gate for `FILE` in the period against the customer's
    authorizations. The counts are of authorities.

    """

    ready: int
    atRisk: int
    blocked: int
    authorities: list[DashboardAuthority]


DashboardActionKind: TypeAlias = Literal['OBLIGATION_OVERDUE', 'OBLIGATION_ATTENTION', 'OBLIGATION_DUE', 'RECONCILIATION_BREAK', 'REFUND_UNCERTAIN', 'REFUND_FAILED', 'DECISION_REVIEW', 'WEBHOOK_DEAD']


DashboardPriority: TypeAlias = Literal['P0', 'P1', 'P2']
"""
`P0` now, `P1` this week, `P2` this month.
"""


class DashboardAction(TypedDict):
    """
    One thing that needs a person. `subjectId` names the obligation,
    reconciliation item or refund it concerns, so a client can open it;
    it is absent for an action that aggregates many records.

    """

    kind: DashboardActionKind
    priority: DashboardPriority
    title: str
    context: str
    exposure: NotRequired[MoneyValue]
    due: NotRequired[CivilDate]
    subjectId: NotRequired[str]


class DashboardTrendPoint(TypedDict):
    period: LegalPeriod
    decisions: int
    liability: NotRequired[MoneyValue]
    liabilityIndex: int
    """
    The point's liability as a share of the largest in the trend, 0–100, for drawing. Not an amount.
    """


class DashboardTrend(TypedDict):
    """
    The eight periods ending with the reported one, oldest first:
    decisions recorded, and tax credited to `TAX_COLLECTED_LIABILITY` in
    `currency` — the currency most journals posted in over those periods.
    `currency` and each point's `liability` are absent when nothing
    posted.

    """

    currency: NotRequired[CurrencyCode]
    points: list[DashboardTrendPoint]


CompliancePackageId: TypeAlias = str


ComplianceMode: TypeAlias = Literal['SOFTWARE_ONLY', 'ASSISTED', 'MANAGED_PREP', 'MANAGED_FILE', 'FULL_MANAGED_COMPLIANCE', 'PARTNER_LED']
"""
Who does the work (ZTAX-COMP-001 §3), set per package.
"""


ComplianceChannel: TypeAlias = Literal['DIRECT_API', 'EFILE_NETWORK', 'FILE_TRANSFER', 'WEB_PORTAL_RPA', 'MANUAL_PORTAL', 'LOCAL_PARTNER', 'PAPER']
"""
How the filing reaches the authority (ZTAX-COMP-001 §13).
"""


ComplianceStatus: TypeAlias = Literal['READY', 'IN_REVIEW', 'APPROVED', 'TRANSMITTING', 'UNCERTAIN_SUBMISSION', 'FILED', 'ACCEPTED', 'REJECTED', 'CANCELLED']
"""
Where the filing is (ZTAX-COMP-001 §14). TRANSMITTING is not FILED, and UNCERTAIN_SUBMISSION leaves only through a recorded resolution.
"""


ComplianceReviewDecision: TypeAlias = Literal['ACCEPT', 'REJECT', 'RETURN_FOR_CHANGE', 'ESCALATE']
"""
A reviewer's verdict on one preparation version.
"""


ComplianceAcknowledgement: TypeAlias = Literal['RECEIVED', 'ACCEPTED', 'ACCEPTED_WITH_WARNINGS', 'REJECTED_VALIDATION', 'REJECTED_AUTHORIZATION', 'REJECTED_DUPLICATE', 'PENDING_AUTHORITY', 'UNKNOWN']
"""
What the authority or network answered (ZTAX-COMP-001 §16).
"""


ComplianceResolution: TypeAlias = Literal['CONFIRMED_FILED', 'CONFIRMED_NOT_FILED', 'DUPLICATE_DETECTED', 'MANUAL_INVESTIGATION']
"""
How an uncertain submission was settled (ZTAX-COMP-001 §15).
"""


CompliancePriority: TypeAlias = Literal['P0', 'P1', 'P2', 'P3', 'P4', 'P5']
"""
Queue priority (ZTAX-COMP-001 §22) — P0 legal deadline imminent, P1 material blocker, P2 required review, P3 normal execution, P4 follow-up, P5 done.
"""


ComplianceStepKind: TypeAlias = Literal['CREATED', 'PREPARED', 'REVIEWED', 'APPROVED', 'SUBMISSION_STARTED', 'ACKNOWLEDGED', 'UNCERTAIN_RESOLVED', 'CANCELLED']
"""
What a step records.
"""


ComplianceNote: TypeAlias = str
"""
Free text from the person recording a step.
"""


ComplianceEvidence: TypeAlias = str
"""
The authority's response as received, or the operator's account of what the authority answered.
"""


AuthorityReference: TypeAlias = str
"""
The authority's own receipt or reference for a submission.
"""


class ComplianceStep(TypedDict):
    seq: int
    kind: ComplianceStepKind
    status: ComplianceStatus
    version: NotRequired[int]
    """
    The preparation version the step acted on.
    """
    payloadHash: NotRequired[Digest]
    controlTotal: NotRequired[Decimal]
    currency: NotRequired[CurrencyCode]
    review: NotRequired[ComplianceReviewDecision]
    submissionId: NotRequired[str]
    """
    The submission attempt the step started or answered.
    """
    channel: NotRequired[ComplianceChannel]
    acknowledgement: NotRequired[ComplianceAcknowledgement]
    resolution: NotRequired[ComplianceResolution]
    authorityReference: NotRequired[AuthorityReference]
    evidence: NotRequired[ComplianceEvidence]
    note: NotRequired[ComplianceNote]
    recordedAt: Timestamp
    recordedBy: str


class ComplianceReadiness(TypedDict):
    dimension: Literal['DATA_READY', 'WORKPAPER_READY', 'REVIEW_READY', 'REPRESENTATION_READY']
    ready: bool
    detail: str


class ComplianceGate(TypedDict):
    allowed: bool
    reason: NotRequired[str]
    """
    Why the gate blocks; absent when it allows.
    """
    detail: NotRequired[str]


class Overdue(TypedDict):
    statutory: bool
    """
    The authority's deadline has passed unfiled.
    """
    internal: bool
    """
    One of our own targets has passed.
    """


class CompliancePackage(TypedDict):
    id: CompliancePackageId
    obligationId: ObligationId
    obligationStatus: ObligationStatus
    responsibilityDecisionId: NotRequired[str]
    """
    The ResponsibilityDecision the obligation names, when it names one.
    """
    legalEntityId: LegalEntityId
    obligationType: str
    jurisdiction: str
    periodStart: CivilDate
    periodEnd: CivilDate
    mode: ComplianceMode
    channel: ComplianceChannel
    filingCountry: CountryCode
    filingAuthority: AuthorityCode
    statutoryDue: Timestamp
    timezone: str
    """
    The authority's IANA timezone the deadline is in.
    """
    prepareDue: NotRequired[Timestamp]
    reviewDue: NotRequired[Timestamp]
    submitTarget: NotRequired[Timestamp]
    status: ComplianceStatus
    priority: CompliancePriority
    overdue: Overdue
    ready: bool
    """
    Every readiness dimension passes. Unknown is not ready.
    """
    readiness: list[ComplianceReadiness]
    gate: ComplianceGate
    createdAt: Timestamp
    createdBy: str
    history: list[ComplianceStep]


class CompliancePackageList(TypedDict):
    packages: list[CompliancePackage]


class CreateCompliancePackageRequest(TypedDict):
    obligationId: ObligationId
    mode: ComplianceMode
    channel: ComplianceChannel
    filingCountry: CountryCode
    filingAuthority: AuthorityCode
    prepareDue: NotRequired[Timestamp]
    reviewDue: NotRequired[Timestamp]
    submitTarget: NotRequired[Timestamp]


class PreparationRequest(TypedDict):
    note: NotRequired[ComplianceNote]


class ComplianceReviewRequest(TypedDict):
    version: int
    decision: ComplianceReviewDecision
    note: NotRequired[ComplianceNote]


class ComplianceApprovalRequest(TypedDict):
    version: int
    payloadHash: Digest
    note: NotRequired[ComplianceNote]


class ComplianceSubmissionRequest(TypedDict):
    note: NotRequired[ComplianceNote]


class ComplianceAcknowledgementRequest(TypedDict):
    acknowledgement: ComplianceAcknowledgement
    authorityReference: NotRequired[AuthorityReference]
    evidence: ComplianceEvidence
    note: NotRequired[ComplianceNote]


class ComplianceResolutionRequest(TypedDict):
    resolution: ComplianceResolution
    authorityReference: NotRequired[AuthorityReference]
    evidence: ComplianceEvidence


class ComplianceCancellationRequest(TypedDict):
    reason: ComplianceNote


BeneficiaryId: TypeAlias = str


RemittanceId: TypeAlias = str


PaymentRail: TypeAlias = Literal['ACH', 'WIRE', 'SEPA', 'RTGS', 'LOCAL_TRANSFER', 'PORTAL', 'DIRECT_DEBIT', 'OTHER']
"""
How money reaches the payee (ZTAX-REM-001 §7).
"""


BeneficiaryState: TypeAlias = Literal['UNVERIFIED', 'CHANGE_PENDING', 'VERIFIED', 'SUSPENDED', 'SUPERSEDED']
"""
Where a payee version stands. Only VERIFIED is paid.
"""


BeneficiaryEventKind: TypeAlias = Literal['CREATED', 'VERIFIED', 'SUSPENDED', 'SUPERSEDED']
"""
What happened to a payee version.
"""


RemittanceMode: TypeAlias = Literal['CUSTOMER_EXECUTES', 'BANK_API_INITIATION', 'TREASURY_PLATFORM', 'DIRECT_DEBIT_AUTHORITY', 'LOCAL_PARTNER', 'MANAGED_ORCHESTRATION', 'OFFLINE_MANUAL']
"""
Who executes the payment (ZTAX-REM-001 §3). None takes custody of funds.
"""


RemittanceStatus: TypeAlias = Literal['APPROVAL_REQUIRED', 'APPROVED', 'SUBMITTED', 'ACCEPTED_BY_PROVIDER', 'PROCESSING', 'UNCERTAIN_PAYMENT', 'SETTLED', 'REJECTED', 'RETURNED', 'CANCELLED']
"""
Where a payment is (ZTAX-REM-001 §10). Submitted is not accepted, and accepted is not settled.
"""


PaymentEvidence: TypeAlias = Literal['PROVIDER_ACCEPTANCE', 'PROCESSING_REFERENCE', 'ACCOUNT_DEBIT', 'BENEFICIARY_CREDIT', 'AUTHORITY_RECEIPT', 'BANK_STATEMENT', 'PROVIDER_SETTLED', 'MANUAL_EVIDENCE', 'NONE']
"""
The kind of proof a provider status carries (ZTAX-REM-001 §18).
"""


PaymentResolution: TypeAlias = Literal['CONFIRMED_SETTLED', 'CONFIRMED_NOT_SENT', 'CONFIRMED_REJECTED', 'RETURNED', 'DUPLICATE_SUSPECTED', 'MANUAL_INVESTIGATION']
"""
How an uncertain payment was settled (ZTAX-REM-001 §19).
"""


RemittanceStepKind: TypeAlias = Literal['CREATED', 'APPROVED', 'SUBMITTED', 'PROVIDER_STATUS', 'UNCERTAIN_RESOLVED', 'RETURNED', 'CANCELLED']
"""
What a payment step records.
"""


ProviderReportedStatus: TypeAlias = Literal['ACCEPTED_BY_PROVIDER', 'PROCESSING', 'SETTLED', 'REJECTED', 'UNCERTAIN_PAYMENT']
"""
A status a provider reports.
"""


SecureReference: TypeAlias = str
"""
A vault or token reference to an account held at a regulated institution — never an account number.
"""


PaymentNote: TypeAlias = str
"""
Free text from the person recording a step.
"""


PaymentRaw: TypeAlias = str
"""
A provider's or authority's response as received, or the operator's account of it.
"""


ProviderReference: TypeAlias = str
"""
The provider's, bank's or authority's own reference.
"""


class BeneficiaryEvent(TypedDict):
    seq: int
    kind: BeneficiaryEventKind
    evidence: NotRequired[str]
    recordedAt: Timestamp
    recordedBy: str


class Beneficiary(TypedDict):
    id: BeneficiaryId
    supersedes: NotRequired[BeneficiaryId]
    country: CountryCode
    authority: AuthorityCode
    legalName: str
    rail: PaymentRail
    accountRef: SecureReference
    referenceRule: NotRequired[str]
    """
    The authority's payment-reference format, as a regular expression.
    """
    currencies: list[CurrencyCode]
    source: str
    """
    The official instruction the details were taken from.
    """
    state: BeneficiaryState
    createdAt: Timestamp
    createdBy: str
    history: list[BeneficiaryEvent]


class BeneficiaryList(TypedDict):
    beneficiaries: list[Beneficiary]


class BeneficiaryRequest(TypedDict):
    supersedes: NotRequired[BeneficiaryId]
    country: CountryCode
    authority: AuthorityCode
    legalName: str
    rail: PaymentRail
    accountRef: SecureReference
    referenceRule: NotRequired[str]
    currencies: list[CurrencyCode]
    source: str


class BeneficiaryVerificationRequest(TypedDict):
    evidence: PaymentNote


class BeneficiarySuspensionRequest(TypedDict):
    reason: PaymentNote


class RemittanceStep(TypedDict):
    seq: int
    kind: RemittanceStepKind
    status: RemittanceStatus
    instructionHash: NotRequired[Digest]
    submissionId: NotRequired[str]
    evidence: NotRequired[PaymentEvidence]
    resolution: NotRequired[PaymentResolution]
    providerReference: NotRequired[ProviderReference]
    raw: NotRequired[PaymentRaw]
    note: NotRequired[PaymentNote]
    recordedAt: Timestamp
    recordedBy: str


class RemittanceReadiness(TypedDict):
    dimension: Literal['LIABILITY_READY', 'BENEFICIARY_READY', 'FUNDING_READY', 'REFERENCE_READY', 'AUTHORIZATION_READY', 'DEADLINE_READY']
    ready: bool
    detail: str


class Remittance(TypedDict):
    id: RemittanceId
    obligationId: ObligationId
    obligationStatus: ObligationStatus
    legalEntityId: LegalEntityId
    authority: str
    periodStart: CivilDate
    periodEnd: CivilDate
    paymentDue: Timestamp
    timezone: str
    amount: Decimal
    currency: CurrencyCode
    beneficiary: Beneficiary
    fundingRef: SecureReference
    paymentReference: str
    executionDate: CivilDate
    mode: RemittanceMode
    fundsPosture: Literal['NO_CUSTODY']
    """
    ZoikoTax instructs the customer's own provider and never holds the funds.
    """
    supersedes: NotRequired[RemittanceId]
    instructionHash: Digest
    status: RemittanceStatus
    overdue: bool
    """
    The payment due date has passed unpaid.
    """
    ready: bool
    """
    Every readiness dimension passes. Unknown is not ready.
    """
    readiness: list[RemittanceReadiness]
    createdAt: Timestamp
    createdBy: str
    history: list[RemittanceStep]


class RemittanceList(TypedDict):
    remittances: list[Remittance]


class RemittanceRequest(TypedDict):
    obligationId: ObligationId
    beneficiaryId: BeneficiaryId
    fundingRef: SecureReference
    paymentReference: str
    executionDate: CivilDate
    mode: RemittanceMode


class RemittanceApprovalRequest(TypedDict):
    instructionHash: Digest
    note: NotRequired[PaymentNote]


class RemittanceSubmissionRequest(TypedDict):
    note: NotRequired[PaymentNote]


class RemittanceStatusRequest(TypedDict):
    status: ProviderReportedStatus
    evidence: PaymentEvidence
    providerReference: NotRequired[ProviderReference]
    raw: PaymentRaw
    note: NotRequired[PaymentNote]


class RemittanceResolutionRequest(TypedDict):
    resolution: PaymentResolution
    evidence: PaymentEvidence
    providerReference: NotRequired[ProviderReference]
    raw: PaymentRaw


class RemittanceReturnRequest(TypedDict):
    providerReference: NotRequired[ProviderReference]
    raw: PaymentRaw


class RemittanceCancellationRequest(TypedDict):
    reason: PaymentNote


ComparatorId: TypeAlias = str


ShadowRunId: TypeAlias = str


DiscrepancyId: TypeAlias = str


ComparatorType: TypeAlias = Literal['COMMERCIAL_TAX_ENGINE', 'CUSTOMER_LEGACY_ENGINE', 'HISTORICAL_BILLED_RESULT', 'AUTHORITY_CALCULATOR', 'QUALIFIED_REFERENCE_IMPLEMENTATION', 'GOLDEN_VECTOR', 'FILING_RESULT', 'MANUAL_TAX_REVIEW', 'PRIOR_ZOIKO_RELEASE']
"""
What kind of engine or evidence a comparator is (ZTAX-SHD-001 §3).
"""


ShadowClaim: TypeAlias = Literal['MATCHES_COMPARATOR', 'VERIFIED_CORRECT', 'NO_REGRESSION']
"""
The strongest statement a match against the comparator supports.
"""


ShadowMode: TypeAlias = Literal['DUAL_DETERMINATION', 'HISTORICAL_REPLAY', 'LIVE_SHADOW', 'RELEASE_DIFFERENTIAL']
"""
What a run compares (ZTAX-SHD-001 §2).
"""


ShadowVariance: TypeAlias = Literal['EXACT_MATCH', 'EQUIVALENT_MATCH', 'MONETARY_VARIANCE', 'CATEGORY_VARIANCE', 'POPULATION_VARIANCE', 'UNCOMPARABLE']
"""
A case's or component's comparison state (ZTAX-SHD-001 §11).
"""


ShadowComponentState: TypeAlias = Literal['EXACT_MATCH', 'EQUIVALENT_MATCH', 'MONETARY_VARIANCE', 'CATEGORY_VARIANCE']
"""
A component's comparison state.
"""


ShadowPairing: TypeAlias = Literal['EXACT_ID', 'UNPAIRED_COMPARATOR', 'UNPAIRED_ZOIKO']
"""
How a case was paired (ZTAX-SHD-001 §8).
"""


ShadowSeverity: TypeAlias = Literal['SHD-SEV-1', 'SHD-SEV-2', 'SHD-SEV-3', 'SHD-SEV-4']
"""
A discrepancy's materiality (ZTAX-SHD-001 §16).
"""


ShadowRootCause: TypeAlias = Literal['SOURCE_DATA', 'CATALOG_MAPPING', 'ONTOLOGY', 'JURISDICTION_SITUS', 'TAXABILITY', 'REGULATORY_REVENUE', 'EXEMPTION', 'RESPONSIBILITY', 'RATE_RULE', 'EFFECTIVE_DATE', 'ROUNDING_PRECISION', 'BUNDLE_ALLOCATION', 'CUSTOMER_CONFIGURATION', 'COMPARATOR_LIMITATION', 'ZOIKO_DEFECT', 'CONTENT_COVERAGE_GAP', 'LEGAL_AMBIGUITY', 'TIMING_STATE', 'MIGRATION_TRANSFORM', 'UNKNOWN']
"""
Why a discrepancy happened (ZTAX-SHD-001 §14).
"""


DiscrepancyState: TypeAlias = Literal['NEW', 'TRIAGED', 'DATA_REQUIRED', 'ANALYSIS', 'SME_REVIEW', 'COMPARATOR_QUERY', 'ZOIKO_DEFECT_CONFIRMED', 'COMPARATOR_ISSUE_CONFIRMED', 'EXPECTED_DIFFERENCE_APPROVED', 'LEGAL_AMBIGUITY', 'FIX_PENDING', 'RETEST_REQUIRED', 'RESOLVED', 'ACCEPTED_RISK', 'CLOSED']
"""
Where a discrepancy's investigation is (ZTAX-SHD-001 §15).
"""


ShadowText: TypeAlias = str
"""
Free text from a person.
"""


ComparatorCode: TypeAlias = str
"""
A comparator's own tax or component code.
"""


ZoikoSlot: TypeAlias = str
"""
A ZoikoTax emitted slot, or UNMAPPED_EXTERNAL_CODE.
"""


CaseRef: TypeAlias = str
"""
The business key a comparator result is for.
"""


class Comparator(TypedDict):
    id: ComparatorId
    name: str
    type: ComparatorType
    provider: str
    version: str
    """
    The engine or content version, or UNKNOWN.
    """
    environment: NotRequired[str]
    limitations: NotRequired[ShadowText]
    claim: ShadowClaim
    createdAt: Timestamp
    createdBy: str


class ComparatorList(TypedDict):
    comparators: list[Comparator]


class ComparatorRequest(TypedDict):
    name: str
    type: ComparatorType
    provider: str
    version: NotRequired[str]
    environment: NotRequired[str]
    limitations: NotRequired[ShadowText]


class ComponentMapping(TypedDict):
    externalCode: ComparatorCode
    slot: ZoikoSlot


class Component(TypedDict):
    code: ComparatorCode
    amount: Decimal


class ShadowObservation(TypedDict):
    caseRef: CaseRef
    total: Decimal
    components: list[Component]


class ShadowRunRequest(TypedDict):
    comparatorId: ComparatorId
    mode: ShadowMode
    windowFrom: Timestamp
    windowTo: Timestamp
    currency: CurrencyCode
    componentMap: list[ComponentMapping]
    tolerance: Decimal
    materiality: Decimal
    observations: list[ShadowObservation]


class ShadowComponent(TypedDict):
    externalCode: NotRequired[ComparatorCode]
    slot: ZoikoSlot
    comparator: NotRequired[Decimal]
    zoiko: NotRequired[Decimal]
    state: ShadowComponentState


class ShadowCase(TypedDict):
    seq: int
    caseRef: CaseRef
    decisionId: NotRequired[str]
    pairing: ShadowPairing
    state: ShadowVariance
    comparatorTotal: NotRequired[Decimal]
    zoikoTotal: NotRequired[Decimal]
    variance: NotRequired[Decimal]
    material: bool
    components: list[ShadowComponent]


class ShadowMetrics(TypedDict):
    cases: int
    paired: int
    exact: int
    equivalent: int
    differing: int
    """
    Cases whose amounts differ beyond tolerance (MONETARY_VARIANCE).
    """
    category: int
    population: int
    uncomparable: int
    material: int
    comparedValue: Decimal
    absoluteVariance: Decimal


class DiscrepancyEvent(TypedDict):
    seq: int
    state: DiscrepancyState
    severity: NotRequired[ShadowSeverity]
    rootCause: NotRequired[ShadowRootCause]
    note: ShadowText
    recordedAt: Timestamp
    recordedBy: str


class Discrepancy(TypedDict):
    id: DiscrepancyId
    runId: ShadowRunId
    caseSeq: int
    state: DiscrepancyState
    severity: ShadowSeverity
    rootCause: ShadowRootCause
    history: list[DiscrepancyEvent]


class DiscrepancyTransitionRequest(TypedDict):
    to: DiscrepancyState
    severity: NotRequired[ShadowSeverity]
    rootCause: NotRequired[ShadowRootCause]
    note: ShadowText


class ShadowRun(TypedDict):
    id: ShadowRunId
    comparator: Comparator
    mode: ShadowMode
    windowFrom: Timestamp
    windowTo: Timestamp
    currency: CurrencyCode
    componentMap: list[ComponentMapping]
    tolerance: Decimal
    materiality: Decimal
    inputHash: Digest
    metrics: ShadowMetrics
    cutoverBlockers: list[str]
    cases: NotRequired[list[ShadowCase]]
    """
    Absent on a run listed as a summary.
    """
    discrepancies: list[Discrepancy]
    createdAt: Timestamp
    createdBy: str


class ShadowRunList(TypedDict):
    runs: list[ShadowRun]


MigrationProgramId: TypeAlias = str


MigrationSnapshotId: TypeAlias = str


MigrationMappingId: TypeAlias = str


MigrationRunId: TypeAlias = str


MigrationExceptionId: TypeAlias = str


MigrationType: TypeAlias = Literal['GREENFIELD_ONBOARD', 'CONFIGURATION_MIGRATION', 'HISTORICAL_DECISION_MIGRATION', 'FULL_LEDGER_MIGRATION', 'INCUMBENT_ENGINE_REPLACEMENT', 'BSS_ERP_TRANSFORMATION', 'COUNTRY_PACK_EXPANSION', 'MERGER_ACQUISITION', 'DATA_CENTER_REGION_MOVE', 'EXIT_EXPORT']
"""
What kind of migration a program is (ZTAX-MIG-001 §3).
"""


ExtractionMethod: TypeAlias = Literal['API', 'DB_EXPORT', 'OBJECT_EXPORT', 'FILE', 'PROVIDER_EXPORT', 'CONTROLLED_MANUAL']
"""
How a snapshot was extracted (ZTAX-MIG-001 §7).
"""


MigrationExceptionKind: TypeAlias = Literal['UNMAPPED_CODE', 'DUPLICATE_SOURCE', 'PRECISION_LOSS', 'DATE_CONFLICT', 'CURRENCY_CONFLICT', 'CONTROL_TOTAL_MISMATCH', 'MISSING_SOURCE', 'INVALID_RELATION']
"""
What a conversion could not carry (ZTAX-MIG-001 §15).
"""


MigrationSeverity: TypeAlias = Literal['HIGH', 'MEDIUM', 'LOW']
"""
An exception's weight. An open HIGH blocks cutover.
"""


MigrationDisposition: TypeAlias = Literal['OPEN', 'RESOLVED', 'ACCEPTED_RESIDUAL']
"""
What was decided about an exception.
"""


MigrationDecision: TypeAlias = Literal['RESOLVED', 'ACCEPTED_RESIDUAL']
"""
A decision on an exception.
"""


CutoverState: TypeAlias = Literal['PLANNED', 'PRECHECK', 'FREEZE_ACTIVE', 'FINAL_DELTA', 'RECONCILING', 'READY_TO_SWITCH', 'ROUTING_SWITCH', 'VALIDATING_LIVE', 'CUTOVER_COMPLETE', 'ABORTED', 'ROLLBACK_ACTIVE', 'ROLLED_BACK', 'FAILED_MANUAL_RECOVERY']
"""
Where a cutover is (ZTAX-MIG-001 §26).
"""


MigrationText: TypeAlias = str
"""
Free text from a person.
"""


LegacyCode: TypeAlias = str
"""
A source system's own tax code.
"""


SourceValue: TypeAlias = str
"""
A value exactly as the source gave it; validated by the conversion, not by the API, so that a bad value becomes an exception rather than a refusal.
"""


class SourceComponent(TypedDict):
    code: LegacyCode
    amount: SourceValue


class MigrationSourceRecord(TypedDict):
    sourceId: SourceValue
    documentDate: SourceValue
    currency: SourceValue
    total: SourceValue
    components: list[SourceComponent]


class ControlTotal(TypedDict):
    currency: CurrencyCode
    total: Decimal


class MigrationProgram(TypedDict):
    id: MigrationProgramId
    name: str
    type: MigrationType
    legalEntityId: LegalEntityId
    periodFrom: CivilDate
    periodTo: CivilDate
    scale: int
    createdAt: Timestamp
    createdBy: str


class MigrationProgramList(TypedDict):
    programs: list[MigrationProgram]


class MigrationProgramRequest(TypedDict):
    name: str
    type: MigrationType
    legalEntityId: NotRequired[LegalEntityId]
    periodFrom: CivilDate
    periodTo: CivilDate
    scale: int


class MigrationSnapshot(TypedDict):
    id: MigrationSnapshotId
    programId: MigrationProgramId
    sourceSystem: str
    sourceVersion: NotRequired[str]
    method: ExtractionMethod
    watermark: NotRequired[str]
    recordCount: int
    controlTotals: list[ControlTotal]
    hash: Digest
    createdAt: Timestamp
    createdBy: str


class MigrationSnapshotRequest(TypedDict):
    sourceSystem: str
    sourceVersion: NotRequired[str]
    method: ExtractionMethod
    watermark: NotRequired[str]
    controlTotals: NotRequired[list[ControlTotal]]
    records: list[MigrationSourceRecord]


class CodeMapping(TypedDict):
    legacyCode: LegacyCode
    slot: ZoikoSlot


class MigrationMapping(TypedDict):
    id: MigrationMappingId
    programId: MigrationProgramId
    version: int
    codes: list[CodeMapping]
    createdAt: Timestamp
    createdBy: str


class MigrationMappingRequest(TypedDict):
    codes: list[CodeMapping]


class MigrationCurrencyTotal(TypedDict):
    currency: CurrencyCode
    declared: NotRequired[Decimal]
    source: Decimal
    converted: Decimal
    matches: bool


class MigrationReconciliation(TypedDict):
    sourceCount: int
    convertedCount: int
    exceptedCount: int
    crosswalkComplete: bool
    passed: bool
    totals: list[MigrationCurrencyTotal]


class MigrationException(TypedDict):
    id: MigrationExceptionId
    seq: int
    kind: MigrationExceptionKind
    severity: MigrationSeverity
    sourceId: NotRequired[str]
    detail: str
    disposition: MigrationDisposition
    note: NotRequired[MigrationText]
    decidedAt: NotRequired[Timestamp]
    decidedBy: NotRequired[str]


class MigratedComponent(TypedDict):
    slot: ZoikoSlot
    amount: Decimal


class MigratedRecord(TypedDict):
    seq: int
    sourceId: str
    canonicalId: str
    documentDate: CivilDate
    currency: CurrencyCode
    total: Decimal
    components: list[MigratedComponent]


class MigrationRun(TypedDict):
    id: MigrationRunId
    programId: MigrationProgramId
    snapshotId: MigrationSnapshotId
    mappingId: MigrationMappingId
    mode: Literal['AS_KNOWN']
    reconciliation: MigrationReconciliation
    exceptions: list[MigrationException]
    records: NotRequired[list[MigratedRecord]]
    """
    Absent on a run listed with its program.
    """
    createdAt: Timestamp
    createdBy: str


class MigrationRunRequest(TypedDict):
    snapshotId: MigrationSnapshotId
    mappingId: MigrationMappingId


class CutoverEvent(TypedDict):
    seq: int
    state: CutoverState
    note: MigrationText
    recordedAt: Timestamp
    recordedBy: str


class MigrationReadiness(TypedDict):
    hasRun: bool
    reconciled: bool
    openHigh: int
    openOther: int
    residuals: int
    blockers: list[str]


class MigrationProgramDetail(TypedDict):
    program: MigrationProgram
    snapshots: list[MigrationSnapshot]
    mappings: list[MigrationMapping]
    runs: list[MigrationRun]
    cutover: list[CutoverEvent]
    cutoverState: CutoverState
    readiness: MigrationReadiness


class ExceptionDecisionRequest(TypedDict):
    disposition: MigrationDecision
    note: MigrationText


class CutoverRequest(TypedDict):
    to: CutoverState
    note: MigrationText


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
    eventTypes: list[EventType]
    """
    Every event type this deployment emits, and so the types a webhook may subscribe to.
    """
    content: NotRequired[ContentCapability]


class ExemptionCertificate(TypedDict):
    """
    A certificate is scoped: by issuing jurisdiction, holder, tax types,
    components and validity. One that does not apply on any of those is
    refused visibly in the trace, never silently ignored.

    """

    id: PartyReference
    type: ContentLabel
    holder: PartyReference
    jurisdiction: JurisdictionId
    taxTypes: NotRequired[list[ContentLabel]]
    components: NotRequired[list[ContentLabel]]
    validFrom: CivilDate
    validTo: NotRequired[CivilDate]
    verification: CertificateVerification


class TaxDocument(TypedDict):
    """
    A telecom document for location-aware determination: rated commercial
    facts, the parties, and the evidence of where the supply took place.
    It carries no tax; what is owed is the output. The tax point is the
    request's `eventTime`.

    """

    type: TaxDocumentType
    currency: CurrencyCode
    priceMode: PriceMode
    seller: TaxParty
    customer: TaxParty
    evidence: NotRequired[list[SitusEvidenceItem]]
    certificates: NotRequired[list[ExemptionCertificate]]
    lines: list[TaxLine]


class TaxComponentResult(TypedDict):
    """
    One charge on one line: its legal base, rate version, jurisdiction,
    authority, five roles, recoverability, rounding result and source.
    `charged` is what the customer pays for it.

    """

    id: ContentLabel
    version: CivilDate
    regime: ContentLabel
    jurisdiction: JurisdictionId
    authority: ContentLabel
    taxType: ContentLabel
    name: ContentText
    treatment: Treatment
    inclusive: bool
    rateKind: ContentLabel
    rate: Decimal
    unit: NotRequired[UnitCode]
    base: NotRequired[Decimal]
    raw: Decimal
    amount: Decimal
    charged: Decimal
    legalReason: NotRequired[ContentLabel]
    responsibility: ResponsibilityAssignment
    exemption: NotRequired[ExemptionApplied]
    source: ContentLabel
    citation: ContentText


class DeterminationItem(TypedDict):
    """
    One line, or one part of a bundle line (`key` is `<line>/<part>`):
    its state, the situs decision that placed it, and its components.

    """

    key: ContentLabel
    line: ContentLabel
    part: NotRequired[ContentLabel]
    sku: NotRequired[ContentLabel]
    service: NotRequired[ServiceFamily]
    amount: Decimal
    state: DeterminationState
    reasonCode: NotRequired[ReasonCode]
    situsOutcome: NotRequired[ContentLabel]
    situsRule: NotRequired[ContentLabel]
    applicable: NotRequired[list[JurisdictionId]]
    placeOfSupply: NotRequired[JurisdictionId]
    customerRole: NotRequired[ContentLabel]
    supply: NotRequired[ContentLabel]
    regime: NotRequired[ContentLabel]
    neededEvidence: NotRequired[list[ContentLabel]]
    net: NotRequired[Decimal]
    components: NotRequired[list[TaxComponentResult]]


class DocumentDetermination(TypedDict):
    """
    A document's location-aware determination. `totals` is present only
    when `state` is `RESOLVED`: an unresolved document has no totals
    rather than zero ones.

    """

    state: DeterminationState
    reasonCode: ReasonCode
    currency: CurrencyCode
    priceMode: PriceMode
    profileVersion: ContentLabel
    graphVersion: ContentLabel
    totals: NotRequired[DocumentTotals]
    items: list[DeterminationItem]
    trace: list[DeterminationStep]


CoverageComponent = TypedDict(
    'CoverageComponent',
    {
        'id': ContentLabel,
        'jurisdiction': JurisdictionId,
        'authority': ContentLabel,
        'taxType': ContentLabel,
        'name': ContentText,
        'services': NotRequired[list[ServiceFamily]],
        'treatments': list[Treatment],
        'rateKind': ContentLabel,
        'rate': Decimal,
        'currency': NotRequired[CurrencyCode],
        'unit': NotRequired[UnitCode],
        'legalReason': NotRequired[ContentLabel],
        'from': CivilDate,
        'to': NotRequired[CivilDate],
        'source': ContentLabel,
        'citation': ContentText,
    },
)


class CoverageRegime(TypedDict):
    id: ContentLabel
    country: LocationCode
    name: ContentText
    services: list[ServiceFamily]
    requiresLevel: NotRequired[ContentLabel]
    inclusivePricing: bool
    components: list[CoverageComponent]


class Coverage(TypedDict):
    """
    What the active content determines by location, at an instant: the
    countries covered, the services in each, and every component in force
    with its rate and source. A country absent here is `UNSUPPORTED`.

    """

    bundle: BundleRef
    profileVersion: ContentLabel
    graphVersion: ContentLabel
    at: Timestamp
    regimes: list[CoverageRegime]


class PeriodEvent(TypedDict):
    seq: int
    state: PeriodState
    reason: NotRequired[PeriodReason]
    recordedAt: Timestamp
    recordedBy: NotRequired[str]
    """
    Who moved the period; on a reopening, who requested it.
    """
    approvedBy: NotRequired[str]
    """
    On a reopening, the second person who approved it.
    """
    requestId: NotRequired[ReopenRequestId]
    manifestDigest: NotRequired[Digest]


class SubledgerPeriod(TypedDict):
    period: LegalPeriod
    legalEntityId: LegalEntityId
    state: PeriodState
    history: list[PeriodEvent]
    manifest: NotRequired[CloseManifestDocument]
    intact: NotRequired[bool]
    """
    Whether the period's journals and balances today are exactly the ones its latest manifest sealed. Absent until the period is first hard-closed.
    """


class RetentionVerdict(TypedDict):
    decisionId: DecisionId
    recordClass: RetentionRecordClass
    country: NotRequired[CountryCode]
    outcome: Literal['RETAIN', 'HELD', 'ELIGIBLE', 'NO_POLICY', 'CONFLICTED']
    policy: NotRequired[PolicyVersionRef]
    candidates: NotRequired[list[PolicyVersionRef]]
    """
    For `CONFLICTED`, the policies that disagree.
    """
    retainUntil: NotRequired[Timestamp]
    holds: NotRequired[list[LegalHoldId]]
    """
    The active holds that scope the record.
    """
    detail: NotRequired[str]
    """
    Why a record is `NO_POLICY` or `CONFLICTED`.
    """
    evaluatedAt: Timestamp


class Dashboard(TypedDict):
    """
    The Tax Command Center for one legal period of the tenant's default legal entity.
    """

    asOf: Timestamp
    period: LegalPeriod
    legalEntityId: LegalEntityId
    determinations: DashboardDeterminations
    liability: list[ControlBalance]
    """
    `TAX_COLLECTED_LIABILITY` for the period, per currency, credits
    and debits apart. Empty when nothing posted.

    """
    obligations: DashboardObligations
    reconciliation: DashboardReconciliation
    evidence: DashboardEvidence
    jurisdictions: DashboardJurisdictions
    actions: list[DashboardAction]
    """
    What needs a person, most urgent first. At most 25.
    """
    trend: DashboardTrend


class QuoteRequest(TypedDict):
    """
    Exactly one of `input` (named values for a rule-graph pack) and
    `document` (a telecom document for a pack with location-aware
    content) is required. `eventTime` is the tax point.

    """

    eventTime: Timestamp
    input: NotRequired[DeterminationInput]
    document: NotRequired[TaxDocument]
    accumulators: NotRequired[ReadSet]


class CommitRequest(TypedDict):
    """
    Exactly one of `input` and `document` is required, as for a quote.
    `eventTime` is the tax point: every effective date and legal period is
    resolved at it, in the authority's own calendar.

    """

    businessKey: BusinessKey
    supersedes: NotRequired[DecisionId]
    eventTime: Timestamp
    input: NotRequired[DeterminationInput]
    document: NotRequired[TaxDocument]
    accumulators: NotRequired[ReadSet]


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
    document: NotRequired[DocumentDetermination]


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
    document: NotRequired[DocumentDetermination]


class BatchRequest(TypedDict):
    operation: Literal['COMMIT']
    """
    What each item does. `COMMIT` commits it, or corrects with it when it names `supersedes`.
    """
    items: list[CommitRequest]
