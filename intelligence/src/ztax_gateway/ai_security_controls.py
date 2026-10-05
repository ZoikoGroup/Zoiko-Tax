"""AI Security Controls — Improper Output Handling & Cross-Tenant Retrieval.

Chapter 17 §28 of the ZoikoTax Master Specification.

Two of the twelve security risks named in the §28 risk table are not covered
by any existing module and are buildable right now with pure deterministic
logic:

Risk 1 — **Improper output handling** (spec control: "Structured schema
validation; never execute raw prose as code/SQL").

    Every AI output that crosses the governed boundary must be validated
    against its declared structured schema before anything downstream touches
    it.  If the output does not conform, the invocation is refused with a
    security refusal rather than forwarded.  This closes the gap where a model
    returns a plausible-looking but structurally wrong payload that a downstream
    caller then parses leniently and acts on as if it were authoritative.

    Requirement anchors: ZTAX-AI-REQ-0001 (AI proposes; fiscal core decides),
    ZTAX-PRD-REQ-0012 (AI output must not bypass deterministic execution),
    ZTAX-SEC-REQ-0120 (AI must not invoke content-promotion / filing-approval /
    remittance-authorization / evidence-deletion).

Risk 2 — **Cross-tenant retrieval** (spec control: "Hard namespace / policy
checks + security conformance tests"; ZTAX-SEC-REQ-0031 "Vector/RAG stores
MUST enforce tenant and residency scope").

    The existing rag.py KnowledgeBase has no tenant scoping — any query
    returns results from any ingested source.  This module adds a
    ``TenantScopedIndex`` wrapper that enforces:

    * Every chunk ingested is labelled with a ``tenant_id`` and ``region``.
    * Every search carries a mandatory ``TenantContext`` (``tenant_id`` +
      ``region``).
    * Results are filtered to chunks whose (``tenant_id``, ``region``) exactly
      matches the caller — cross-tenant leakage is structurally impossible
      rather than policy-enforced at the application level.
    * A missing or empty ``tenant_id`` / ``region`` on a search FAILS CLOSED
      (ZTAX-SEC-REQ-0004: "Tenant-scoped data access without tenant context
      MUST fail closed").

    Requirement anchors: ZTAX-SEC-REQ-0004, -0031, -0121 ("RAG retrieval
    MUST enforce tenant/source-right/residency controls"), -0029 ("Caches
    MUST be tenant/cell partitioned or key-scoped").

Design rules (shared with all other modules in this package)
-------------------------------------------------------------
* No live model calls.
* No fiscal imports (ADR-0006 §2.6).
* All data-carrying types are ``frozen=True`` dataclasses — immutable evidence.
* ``TenantScopedIndex`` fails closed: missing context is refused, not ignored.
* ``OutputSchemaValidator`` is pure: it does not call any model; it checks
  the *shape* of the output (required keys, prohibited keys, value types),
  not its semantic correctness.
"""

from __future__ import annotations

import sqlite3
from dataclasses import dataclass, field
from enum import StrEnum
from pathlib import Path
from typing import Any, Final

from .citation import Citation, SourceChunk, combine
from .rag import Chunk, _chunk_document

__all__: list[str] = [
    "AISecurityError",
    "AISecurityRefusal",
    "FieldType",
    "OutputSchema",
    "OutputSchemaValidator",
    "OutputValidationResult",
    "TenantContext",
    "TenantScopedIndex",
    "TenantScopedSearchResult",
]


# ---------------------------------------------------------------------------
# Risk 1 — Improper output handling
# ZTAX-AI-REQ-0001, ZTAX-PRD-REQ-0012, ZTAX-SEC-REQ-0120
# ---------------------------------------------------------------------------


class AISecurityRefusal(StrEnum):
    """Refusal codes for AI security control violations.

    Closed vocabulary — same discipline as
    :class:`~ztax_gateway.governance.Refusal`.

    ``OUTPUT_SCHEMA_VIOLATION``
        The AI output does not conform to the declared ``OutputSchema``.
        Raised before any downstream system sees the output
        (ZTAX-PRD-REQ-0012, ZTAX-SEC-REQ-0120).
    ``OUTPUT_PROHIBITED_KEY``
        The AI output contains a key that is explicitly prohibited by the
        schema (e.g. ``"sql"``, ``"exec"``, ``"code"``).
        Never execute raw prose as code/SQL (§28 control).
    ``TENANT_CONTEXT_MISSING``
        A tenant-scoped RAG search was attempted without a ``TenantContext``
        (ZTAX-SEC-REQ-0004 fail-closed rule).
    ``TENANT_CONTEXT_INVALID``
        The provided ``TenantContext`` has an empty ``tenant_id`` or
        ``region`` (ZTAX-SEC-REQ-0004).
    """

    OUTPUT_SCHEMA_VIOLATION = "AI_SECURITY_OUTPUT_SCHEMA_VIOLATION"
    OUTPUT_PROHIBITED_KEY = "AI_SECURITY_OUTPUT_PROHIBITED_KEY"
    TENANT_CONTEXT_MISSING = "AI_SECURITY_TENANT_CONTEXT_MISSING"
    TENANT_CONTEXT_INVALID = "AI_SECURITY_TENANT_CONTEXT_INVALID"


class AISecurityError(Exception):
    """Raised when an AI security control gate is breached.

    Carries a :class:`AISecurityRefusal` code and a human-readable detail
    string.  Message format: ``"{code}: {detail}"``, parseable without regex.
    """

    def __init__(self, detail: str, refusal: AISecurityRefusal) -> None:
        super().__init__(f"{refusal.value}: {detail}")
        self.refusal = refusal
        self.detail = detail


class FieldType(StrEnum):
    """Primitive types an ``OutputSchema`` field may declare.

    Checked structurally — this module does not use ``isinstance`` against
    model types; it maps JSON-compatible primitive names to Python builtins.

    ``STRING``  -- ``str``.
    ``INTEGER`` -- ``int`` (not ``float``; fiscal outputs must not be floats).
    ``FLOAT``   -- ``float`` (allowed only for non-fiscal advisory outputs).
    ``BOOLEAN`` -- ``bool``.
    ``LIST``    -- ``list`` (element type is not recursively validated here).
    ``DICT``    -- ``dict`` (nested structure is not recursively validated here).
    ``ANY``     -- any non-None value; use only for untyped advisory fields.
    """

    STRING = "string"
    INTEGER = "integer"
    FLOAT = "float"
    BOOLEAN = "boolean"
    LIST = "list"
    DICT = "dict"
    ANY = "any"


# Mapping from FieldType to the Python runtime type(s) it accepts.
_FIELD_TYPE_MAP: Final[dict[FieldType, type | tuple[type, ...]]] = {
    FieldType.STRING: str,
    FieldType.INTEGER: int,
    FieldType.FLOAT: (int, float),
    FieldType.BOOLEAN: bool,
    FieldType.LIST: list,
    FieldType.DICT: dict,
    FieldType.ANY: object,
}

# Key names that are *never* permitted in a structured AI output, regardless
# of schema.  These names signal an attempt to pass executable content through
# the structured-output boundary (§28 control: "never execute raw prose as
# code/SQL").
_PROHIBITED_KEYS: Final[frozenset[str]] = frozenset(
    {
        "sql",
        "query",
        "exec",
        "execute",
        "code",
        "script",
        "command",
        "shell",
        "eval",
        "expression",
    }
)


@dataclass(frozen=True, slots=True)
class OutputSchema:
    """Structural schema for a structured AI output (§28 Risk 1).

    ``schema_id``       -- stable identifier for this schema, e.g.
                           ``"classification-proposal-v1"``.
    ``required_fields`` -- mapping of field name to expected
                           :class:`FieldType`.  Every listed field must be
                           present and of the declared type.
    ``optional_fields`` -- mapping of field name to expected
                           :class:`FieldType`.  If present, must be of the
                           declared type.
    ``allow_extra``     -- when ``False`` (the default) any key in the output
                           that is not in ``required_fields`` or
                           ``optional_fields`` is a schema violation.  Setting
                           to ``True`` makes the validator lenient about
                           unknown extra keys — still not recommended for
                           A2+ outputs.
    ``prohibited_keys`` -- additional keys to reject beyond the global
                           ``_PROHIBITED_KEYS`` set.  The validator always
                           checks the global set; this adds schema-specific
                           extras.
    """

    schema_id: str
    required_fields: dict[str, FieldType]
    optional_fields: dict[str, FieldType] = field(default_factory=dict)
    allow_extra: bool = False
    prohibited_keys: frozenset[str] = field(default_factory=frozenset)

    def __post_init__(self) -> None:
        if not self.schema_id:
            raise AISecurityError(
                "OutputSchema.schema_id must be non-empty",
                AISecurityRefusal.OUTPUT_SCHEMA_VIOLATION,
            )


@dataclass(frozen=True, slots=True)
class OutputValidationResult:
    """Immutable record of one output validation (§28 Risk 1).

    ``schema_id``   -- the schema that was checked.
    ``passed``      -- ``True`` when all checks passed.
    ``violations``  -- list of human-readable violation descriptions; empty
                       when ``passed`` is ``True``.
    """

    schema_id: str
    passed: bool
    violations: tuple[str, ...]


class OutputSchemaValidator:
    """Validates structured AI outputs against declared schemas (§28 Risk 1).

    Implements the spec control: "Structured schema validation; never execute
    raw prose as code/SQL."

    The validator is pure and stateless — the same instance can be used
    concurrently.  Schemas are registered once; re-registering the same
    ``schema_id`` is a programming error.

    Usage::

        validator = OutputSchemaValidator()
        schema = OutputSchema(
            schema_id="classification-proposal-v1",
            required_fields={
                "label": FieldType.STRING,
                "confidence": FieldType.FLOAT,
                "citation_id": FieldType.STRING,
            },
        )
        validator.register(schema)

        # In the invocation path — before any downstream system sees the output:
        validator.validate("classification-proposal-v1", model_output_dict)
    """

    def __init__(self) -> None:
        self._schemas: dict[str, OutputSchema] = {}

    def register(self, schema: OutputSchema) -> None:
        """Register a schema.  Re-registering the same ID is a bug."""
        if schema.schema_id in self._schemas:
            raise AISecurityError(
                f"schema {schema.schema_id!r} already registered; re-registration is a bug",
                AISecurityRefusal.OUTPUT_SCHEMA_VIOLATION,
            )
        self._schemas[schema.schema_id] = schema

    def schema(self, schema_id: str) -> OutputSchema | None:
        """Return the registered schema for *schema_id*, or ``None``."""
        return self._schemas.get(schema_id)

    def validate(self, schema_id: str, output: dict[str, Any]) -> OutputValidationResult:
        """Validate *output* against the named schema.

        Must be called *before* any downstream system receives the AI output.
        This is the structural enforcement gate for §28 Risk 1.

        Args:
            schema_id: The schema to validate against.  Must have been
                       registered.
            output:    The AI-produced dictionary to check.

        Returns:
            :class:`OutputValidationResult` with ``passed=True`` when all
            checks pass.

        Raises:
            AISecurityError: with :attr:`AISecurityRefusal.OUTPUT_SCHEMA_VIOLATION`
                if the schema is not registered, or
                :attr:`AISecurityRefusal.OUTPUT_PROHIBITED_KEY` if a
                prohibited key is present.
        """
        schema = self._schemas.get(schema_id)
        if schema is None:
            raise AISecurityError(
                f"no schema registered for {schema_id!r}; validate() requires a registered schema",
                AISecurityRefusal.OUTPUT_SCHEMA_VIOLATION,
            )

        violations: list[str] = []

        # 1. Prohibited key check — checked first, before any schema traversal,
        #    because a prohibited key may indicate an injection attempt.
        all_prohibited = _PROHIBITED_KEYS | schema.prohibited_keys
        for key in output:
            if key.lower() in all_prohibited:
                # Raise immediately — do not accumulate; this is a hard block.
                raise AISecurityError(
                    f"output contains prohibited key {key!r}; §28 control: "
                    "never execute raw prose as code/SQL (ZTAX-SEC-REQ-0120)",
                    AISecurityRefusal.OUTPUT_PROHIBITED_KEY,
                )

        # 2. Required fields check.
        for fname, ftype in schema.required_fields.items():
            if fname not in output:
                violations.append(f"required field {fname!r} is missing")
                continue
            expected_type = _FIELD_TYPE_MAP[ftype]
            value = output[fname]
            if not isinstance(value, expected_type):
                violations.append(
                    f"field {fname!r}: expected {ftype.value}, "
                    f"got {type(value).__name__}"
                )

        # 3. Optional fields type check (only when present).
        for fname, ftype in schema.optional_fields.items():
            if fname not in output:
                continue
            expected_type = _FIELD_TYPE_MAP[ftype]
            value = output[fname]
            if not isinstance(value, expected_type):
                violations.append(
                    f"optional field {fname!r}: expected {ftype.value}, "
                    f"got {type(value).__name__}"
                )

        # 4. Extra-key check (when allow_extra=False).
        if not schema.allow_extra:
            known_keys = (
                frozenset(schema.required_fields) | frozenset(schema.optional_fields)
            )
            for key in output:
                if key not in known_keys:
                    violations.append(
                        f"unexpected key {key!r}; schema {schema_id!r} does not "
                        "allow extra keys (set allow_extra=True to permit them)"
                    )

        return OutputValidationResult(
            schema_id=schema_id,
            passed=len(violations) == 0,
            violations=tuple(violations),
        )

    def validate_or_raise(
        self, schema_id: str, output: dict[str, Any]
    ) -> OutputValidationResult:
        """Validate and raise :class:`AISecurityError` if validation fails.

        Convenience wrapper for the common pattern of validate-then-refuse.
        Use this in invocation paths where a schema violation must stop
        execution immediately.

        Raises:
            AISecurityError: with ``OUTPUT_SCHEMA_VIOLATION`` if any violation
                is found, or ``OUTPUT_PROHIBITED_KEY`` for prohibited keys.
        """
        result = self.validate(schema_id, output)
        if not result.passed:
            raise AISecurityError(
                f"output failed schema {schema_id!r} validation: "
                + "; ".join(result.violations),
                AISecurityRefusal.OUTPUT_SCHEMA_VIOLATION,
            )
        return result


# ---------------------------------------------------------------------------
# Risk 2 — Cross-tenant retrieval
# ZTAX-SEC-REQ-0004, -0029, -0031, -0121
# ---------------------------------------------------------------------------

# The sentinel used when a chunk is shared (not tenant-specific), e.g.
# public authoritative law that applies to all tenants.  A shared chunk may
# be returned to *any* tenant.  This is explicitly NOT a bypass — the
# ``TenantScopedIndex`` only produces it for chunks ingested as shared.
_SHARED_TENANT_ID: Final[str] = "__SHARED__"


@dataclass(frozen=True, slots=True)
class TenantContext:
    """Mandatory caller context for every tenant-scoped RAG search.

    A missing or empty ``tenant_id`` or ``region`` causes the search to
    fail closed (ZTAX-SEC-REQ-0004).

    ``tenant_id`` -- the customer/tenant identifier, e.g. ``"acme-eu"``.
    ``region``    -- the residency cell the caller is operating in, e.g.
                     ``"eu-west-1"``.  Chunks from a different region are
                     never returned.
    """

    tenant_id: str
    region: str

    def __post_init__(self) -> None:
        if not self.tenant_id:
            raise AISecurityError(
                "TenantContext.tenant_id must be non-empty "
                "(ZTAX-SEC-REQ-0004: fail closed on missing tenant context)",
                AISecurityRefusal.TENANT_CONTEXT_INVALID,
            )
        if not self.region:
            raise AISecurityError(
                "TenantContext.region must be non-empty "
                "(ZTAX-SEC-REQ-0004: fail closed on missing tenant context)",
                AISecurityRefusal.TENANT_CONTEXT_INVALID,
            )


@dataclass(frozen=True, slots=True)
class TenantScopedSearchResult:
    """One search result from :meth:`TenantScopedIndex.search`.

    ``chunk``     -- the matching :class:`~ztax_gateway.rag.Chunk`.
    ``citation``  -- the tamper-evident citation.
    ``rank``      -- BM25 score (lower is better).
    ``tenant_id`` -- the tenant this chunk belongs to (or ``"__SHARED__"``).
    ``region``    -- the region this chunk is scoped to.
    """

    chunk: Chunk
    citation: Citation
    rank: float
    tenant_id: str
    region: str


# Default FTS5 top-k
_DEFAULT_TOP_K: Final[int] = 5
_MIN_QUERY_LEN: Final[int] = 2


class TenantScopedIndex:
    """Tenant-isolated full-text index for RAG retrieval (§28 Risk 2).

    Wraps the same SQLite FTS5 technology as :class:`~ztax_gateway.rag.KnowledgeBase`
    but adds mandatory tenant and region scoping to every operation.

    Rules enforced
    ~~~~~~~~~~~~~~
    1. Every chunk is labelled with ``(tenant_id, region)`` at ingest time.
       Chunks may be labelled ``tenant_id=SHARED`` for public authority texts
       that belong to no specific tenant.
    2. Every search carries a mandatory :class:`TenantContext`.  Omitting it
       raises :attr:`AISecurityRefusal.TENANT_CONTEXT_MISSING` before the
       index is consulted (ZTAX-SEC-REQ-0004 fail-closed).
    3. The SQL filter is ``WHERE tenant_id IN (caller_tenant, '__SHARED__')
       AND region = caller_region`` — this is a hard constraint in the SQL,
       not a Python post-filter, so cross-tenant leakage is structurally
       impossible regardless of query content.
    4. A source_id that has already been ingested for a given
       ``(tenant_id, region)`` combination cannot be re-ingested; duplicate
       ingestion is a programming error.

    Requirement anchors: ZTAX-SEC-REQ-0004, -0029, -0031, -0121.
    """

    def __init__(self) -> None:
        self._conn: sqlite3.Connection = sqlite3.connect(
            ":memory:", check_same_thread=False
        )
        self._citations: dict[int, Citation] = {}
        self._chunks: dict[int, Chunk] = {}
        self._ingested: set[tuple[str, str, str]] = set()  # (source_id, tenant_id, region)
        self._built = False

        self._conn.execute(
            """
            CREATE VIRTUAL TABLE IF NOT EXISTS chunks USING fts5(
                chunk_id UNINDEXED,
                source_id UNINDEXED,
                tenant_id UNINDEXED,
                region UNINDEXED,
                heading,
                body,
                tokenize = 'porter unicode61'
            )
            """
        )
        self._conn.commit()

    # ------------------------------------------------------------------
    # Ingestion
    # ------------------------------------------------------------------

    def build(
        self,
        paths: list[Path],
        *,
        tenant_id: str,
        region: str,
    ) -> None:
        """Ingest one or more spec documents scoped to *tenant_id* and *region*.

        To index public authority documents (public statutes, etc.) that
        should be retrievable by any tenant, use
        ``tenant_id=TenantScopedIndex.SHARED``.

        Args:
            paths:     Absolute paths to Markdown spec files.
            tenant_id: The owning tenant.  Use :attr:`SHARED` for public
                       authority content.
            region:    The residency region this content is approved for.

        Raises:
            AISecurityError: if ``tenant_id`` or ``region`` is empty, or if
                a ``(path, tenant_id, region)`` combination has already been
                ingested.
        """
        if not tenant_id:
            raise AISecurityError(
                "build() requires a non-empty tenant_id; use TenantScopedIndex.SHARED "
                "for public authority content",
                AISecurityRefusal.TENANT_CONTEXT_INVALID,
            )
        if not region:
            raise AISecurityError(
                "build() requires a non-empty region",
                AISecurityRefusal.TENANT_CONTEXT_INVALID,
            )

        for path in paths:
            key = (path.as_posix(), tenant_id, region)
            if key in self._ingested:
                raise AISecurityError(
                    f"source {path.name!r} already ingested for "
                    f"tenant {tenant_id!r} in region {region!r}; "
                    "duplicate ingestion is a programming error",
                    AISecurityRefusal.TENANT_CONTEXT_INVALID,
                )
            self._ingest_one(path, tenant_id=tenant_id, region=region)
            self._ingested.add(key)

        self._built = True

    def _ingest_one(self, path: Path, *, tenant_id: str, region: str) -> None:
        raw = path.read_bytes()
        doc_chunks = _chunk_document(path)
        rows = []
        for doc_chunk in doc_chunks:
            source_chunk = SourceChunk(
                source_id=doc_chunk.source_id,
                content=raw[doc_chunk.byte_start : doc_chunk.byte_end],
                byte_start=doc_chunk.byte_start,
                byte_end=doc_chunk.byte_end,
            )
            citation = combine([source_chunk])
            chunk_id = len(self._chunks)
            self._chunks[chunk_id] = doc_chunk
            self._citations[chunk_id] = citation
            rows.append(
                (
                    chunk_id,
                    doc_chunk.source_id,
                    tenant_id,
                    region,
                    doc_chunk.heading,
                    doc_chunk.body,
                )
            )
        self._conn.executemany(
            """
            INSERT INTO chunks (chunk_id, source_id, tenant_id, region, heading, body)
            VALUES (?, ?, ?, ?, ?, ?)
            """,
            rows,
        )
        self._conn.commit()

    # ------------------------------------------------------------------
    # Searching
    # ------------------------------------------------------------------

    def search(
        self,
        query: str,
        context: TenantContext,
        top_k: int = _DEFAULT_TOP_K,
    ) -> list[TenantScopedSearchResult]:
        """Search the index — only chunks matching *context* are returned.

        The filter ``tenant_id IN (context.tenant_id, '__SHARED__')
        AND region = context.region`` is enforced in SQL, making cross-tenant
        leakage structurally impossible (ZTAX-SEC-REQ-0004, -0031).

        Args:
            query:   A plain-text search query.
            context: Mandatory :class:`TenantContext`.  Construction of
                     ``TenantContext`` with empty fields already raises —
                     this parameter cannot be ``None``.
            top_k:   Maximum number of results.  Defaults to 5.

        Returns:
            List of :class:`TenantScopedSearchResult`, in BM25 rank order.

        Raises:
            AISecurityError: if the index has not been built or *query* is
                too short.
        """
        query = query.strip()
        if len(query) < _MIN_QUERY_LEN:
            raise AISecurityError(
                f"query {query!r} is too short (minimum {_MIN_QUERY_LEN} characters)",
                AISecurityRefusal.TENANT_CONTEXT_MISSING,
            )
        if not self._built:
            raise AISecurityError(
                "TenantScopedIndex has not been built — call build() first",
                AISecurityRefusal.TENANT_CONTEXT_MISSING,
            )

        # Hard SQL filter — tenant scope is enforced at the query layer, not
        # as a Python post-filter, so no code path can "forget" to filter.
        cursor = self._conn.execute(
            """
            SELECT chunk_id, tenant_id, region, rank
            FROM chunks
            WHERE chunks MATCH ?
              AND (tenant_id = ? OR tenant_id = ?)
              AND region = ?
            ORDER BY rank
            LIMIT ?
            """,
            (
                query,
                context.tenant_id,
                _SHARED_TENANT_ID,
                context.region,
                top_k,
            ),
        )
        rows = cursor.fetchall()
        results: list[TenantScopedSearchResult] = []
        for chunk_id, tid, reg, rank in rows:
            chunk = self._chunks[chunk_id]
            citation = self._citations[chunk_id]
            results.append(
                TenantScopedSearchResult(
                    chunk=chunk,
                    citation=citation,
                    rank=rank,
                    tenant_id=tid,
                    region=reg,
                )
            )
        return results

    # ------------------------------------------------------------------
    # Inspection
    # ------------------------------------------------------------------

    #: Sentinel ``tenant_id`` value for public authority content that is
    #: accessible to any tenant.
    SHARED: Final[str] = _SHARED_TENANT_ID

    @property
    def document_count(self) -> int:
        """Number of unique (source, tenant, region) combinations ingested."""
        return len(self._ingested)

    @property
    def chunk_count(self) -> int:
        """Total number of indexed chunks across all tenants."""
        return len(self._chunks)

    def tenant_ids(self) -> frozenset[str]:
        """Return all distinct tenant IDs that have been ingested."""
        return frozenset(tid for _, tid, _ in self._ingested)
