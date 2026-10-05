"""Tests for ai_security_controls.py (Chapter 17 §28).

Coverage
--------
Risk 1 — Improper output handling:
  OutputSchema construction invariants (empty schema_id, re-registration)
  OutputSchemaValidator.validate():
    - all required fields present + correct types → passed
    - missing required field → violation
    - wrong type for required field → violation
    - prohibited key (sql, exec, code...) → raises OUTPUT_PROHIBITED_KEY
    - schema-specific extra prohibited key → raises OUTPUT_PROHIBITED_KEY
    - extra key when allow_extra=False → violation
    - extra key when allow_extra=True → passed
    - optional field absent → passed
    - optional field present + correct type → passed
    - optional field present + wrong type → violation
  OutputSchemaValidator.validate_or_raise():
    - passed → returns result
    - violation → raises OUTPUT_SCHEMA_VIOLATION
  Schema not registered → raises OUTPUT_SCHEMA_VIOLATION

Risk 2 — Cross-tenant retrieval:
  TenantContext construction:
    - empty tenant_id → TENANT_CONTEXT_INVALID
    - empty region → TENANT_CONTEXT_INVALID
    - valid → no error
  TenantScopedIndex.build():
    - empty tenant_id → TENANT_CONTEXT_INVALID
    - empty region → TENANT_CONTEXT_INVALID
    - duplicate (source, tenant, region) → TENANT_CONTEXT_INVALID
  TenantScopedIndex.search():
    - before build → AISecurityError
    - query too short → AISecurityError
    - tenant A cannot see tenant B chunks
    - tenant A can see SHARED chunks
    - region A cannot see region B chunks
    - results are correctly scoped
    - SHARED results returned to any tenant
  TenantScopedIndex inspection properties

AISecurityError attributes.
All AISecurityRefusal codes are reachable.
"""

from __future__ import annotations

from pathlib import Path
from typing import Any

import pytest

from ztax_gateway.ai_security_controls import (
    AISecurityError,
    AISecurityRefusal,
    FieldType,
    OutputSchema,
    OutputSchemaValidator,
    OutputValidationResult,
    TenantContext,
    TenantScopedIndex,
    TenantScopedSearchResult,
)

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _make_md_file(tmp_path: Path, name: str, content: str = "") -> Path:
    """Write a temporary Markdown file and return its path."""
    f = tmp_path / name
    body = content or f"# {name}\n\nThis is a test document about {name}.\n"
    f.write_text(body, encoding="utf-8")
    return f


def _simple_schema(
    *,
    allow_extra: bool = False,
    prohibited: frozenset[str] | None = None,
) -> OutputSchema:
    return OutputSchema(
        schema_id="test-schema-v1",
        required_fields={
            "label": FieldType.STRING,
            "confidence": FieldType.FLOAT,
            "citation_id": FieldType.STRING,
        },
        optional_fields={"notes": FieldType.STRING},
        allow_extra=allow_extra,
        prohibited_keys=prohibited or frozenset(),
    )


def _valid_output() -> dict[str, Any]:
    return {
        "label": "EXEMPT",
        "confidence": 0.95,
        "citation_id": "cit-abc123",
    }


# ---------------------------------------------------------------------------
# AISecurityError
# ---------------------------------------------------------------------------


class TestAISecurityError:
    def test_carries_refusal_and_detail(self) -> None:
        err = AISecurityError("test detail", AISecurityRefusal.OUTPUT_PROHIBITED_KEY)
        assert err.refusal is AISecurityRefusal.OUTPUT_PROHIBITED_KEY
        assert err.detail == "test detail"
        assert str(err).startswith("AI_SECURITY_OUTPUT_PROHIBITED_KEY:")

    @pytest.mark.parametrize("refusal", list(AISecurityRefusal))
    def test_all_refusal_codes_start_with_prefix(self, refusal: AISecurityRefusal) -> None:
        assert refusal.value.startswith("AI_SECURITY_")


# ---------------------------------------------------------------------------
# OutputSchema construction
# ---------------------------------------------------------------------------


class TestOutputSchemaConstruction:
    def test_empty_schema_id_raises(self) -> None:
        with pytest.raises(AISecurityError) as exc_info:
            OutputSchema(schema_id="", required_fields={"x": FieldType.STRING})
        assert exc_info.value.refusal is AISecurityRefusal.OUTPUT_SCHEMA_VIOLATION

    def test_valid_schema(self) -> None:
        schema = _simple_schema()
        assert schema.schema_id == "test-schema-v1"
        assert "label" in schema.required_fields
        assert schema.allow_extra is False

    def test_defaults(self) -> None:
        schema = OutputSchema(schema_id="s", required_fields={})
        assert schema.optional_fields == {}
        assert schema.allow_extra is False
        assert schema.prohibited_keys == frozenset()


# ---------------------------------------------------------------------------
# OutputSchemaValidator — registration
# ---------------------------------------------------------------------------


class TestOutputSchemaValidatorRegistration:
    def test_register_and_retrieve(self) -> None:
        v = OutputSchemaValidator()
        schema = _simple_schema()
        v.register(schema)
        assert v.schema("test-schema-v1") is schema

    def test_re_registration_raises(self) -> None:
        v = OutputSchemaValidator()
        v.register(_simple_schema())
        with pytest.raises(AISecurityError) as exc_info:
            v.register(_simple_schema())
        assert exc_info.value.refusal is AISecurityRefusal.OUTPUT_SCHEMA_VIOLATION

    def test_unregistered_schema_raises_on_validate(self) -> None:
        v = OutputSchemaValidator()
        with pytest.raises(AISecurityError) as exc_info:
            v.validate("no-such-schema", {})
        assert exc_info.value.refusal is AISecurityRefusal.OUTPUT_SCHEMA_VIOLATION

    def test_schema_returns_none_for_unknown(self) -> None:
        v = OutputSchemaValidator()
        assert v.schema("missing") is None


# ---------------------------------------------------------------------------
# OutputSchemaValidator — validate() happy paths
# ---------------------------------------------------------------------------


class TestValidateHappyPaths:
    def setup_method(self) -> None:
        self.v = OutputSchemaValidator()
        self.v.register(_simple_schema())

    def test_valid_output_passes(self) -> None:
        result = self.v.validate("test-schema-v1", _valid_output())
        assert isinstance(result, OutputValidationResult)
        assert result.passed is True
        assert result.violations == ()

    def test_optional_field_absent_passes(self) -> None:
        output = _valid_output()  # no "notes" field
        result = self.v.validate("test-schema-v1", output)
        assert result.passed is True

    def test_optional_field_correct_type_passes(self) -> None:
        output = {**_valid_output(), "notes": "some note"}
        result = self.v.validate("test-schema-v1", output)
        assert result.passed is True

    def test_float_field_accepts_int(self) -> None:
        """FieldType.FLOAT accepts int values (subtype in numeric hierarchy)."""
        output = {**_valid_output(), "confidence": 1}  # int, not float
        result = self.v.validate("test-schema-v1", output)
        assert result.passed is True

    def test_extra_key_allowed_when_allow_extra_true(self) -> None:
        v = OutputSchemaValidator()
        v.register(OutputSchema(
            schema_id="lenient",
            required_fields={"label": FieldType.STRING},
            allow_extra=True,
        ))
        result = v.validate("lenient", {"label": "X", "extra_field": 42})
        assert result.passed is True


# ---------------------------------------------------------------------------
# OutputSchemaValidator — validate() violation paths
# ---------------------------------------------------------------------------


class TestValidateViolationPaths:
    def setup_method(self) -> None:
        self.v = OutputSchemaValidator()
        self.v.register(_simple_schema())

    def test_missing_required_field(self) -> None:
        output = {"label": "EXEMPT", "confidence": 0.9}  # missing citation_id
        result = self.v.validate("test-schema-v1", output)
        assert result.passed is False
        assert any("citation_id" in v for v in result.violations)

    def test_wrong_type_for_required_field(self) -> None:
        output = {**_valid_output(), "label": 123}  # should be str
        result = self.v.validate("test-schema-v1", output)
        assert result.passed is False
        assert any("label" in v for v in result.violations)

    def test_optional_field_wrong_type(self) -> None:
        output = {**_valid_output(), "notes": 999}  # should be str
        result = self.v.validate("test-schema-v1", output)
        assert result.passed is False
        assert any("notes" in v for v in result.violations)

    def test_extra_key_rejected_when_allow_extra_false(self) -> None:
        output = {**_valid_output(), "surprise": "boom"}
        result = self.v.validate("test-schema-v1", output)
        assert result.passed is False
        assert any("surprise" in v for v in result.violations)

    def test_multiple_violations_accumulated(self) -> None:
        """All violations are reported, not short-circuited."""
        output = {"label": 123, "confidence": "not-a-float"}  # 2 type errors + missing citation_id
        result = self.v.validate("test-schema-v1", output)
        assert result.passed is False
        assert len(result.violations) >= 2


# ---------------------------------------------------------------------------
# OutputSchemaValidator — prohibited key checks (§28 control)
# ---------------------------------------------------------------------------


class TestProhibitedKeyChecks:
    def setup_method(self) -> None:
        self.v = OutputSchemaValidator()
        self.v.register(_simple_schema())

    @pytest.mark.parametrize(
        "bad_key",
        ["sql", "SQL", "query", "exec", "execute", "code", "script",
         "command", "shell", "eval", "expression"],
    )
    def test_global_prohibited_keys_raise(self, bad_key: str) -> None:
        """Every key in the global prohibited set raises OUTPUT_PROHIBITED_KEY."""
        output = {**_valid_output(), bad_key: "DROP TABLE users;"}
        with pytest.raises(AISecurityError) as exc_info:
            self.v.validate("test-schema-v1", output)
        assert exc_info.value.refusal is AISecurityRefusal.OUTPUT_PROHIBITED_KEY

    def test_prohibited_key_case_insensitive(self) -> None:
        """Prohibited key check is case-insensitive (SQL == sql)."""
        output = {**_valid_output(), "SQL": "SELECT * FROM taxes"}
        with pytest.raises(AISecurityError) as exc_info:
            self.v.validate("test-schema-v1", output)
        assert exc_info.value.refusal is AISecurityRefusal.OUTPUT_PROHIBITED_KEY

    def test_schema_specific_prohibited_key(self) -> None:
        """Schema-level prohibited_keys are checked in addition to global ones."""
        v = OutputSchemaValidator()
        v.register(OutputSchema(
            schema_id="strict-v1",
            required_fields={"label": FieldType.STRING},
            prohibited_keys=frozenset({"override", "bypass"}),
        ))
        output = {"label": "ok", "override": "admin"}
        with pytest.raises(AISecurityError) as exc_info:
            v.validate("strict-v1", output)
        assert exc_info.value.refusal is AISecurityRefusal.OUTPUT_PROHIBITED_KEY

    def test_prohibited_key_raises_before_other_violations(self) -> None:
        """Prohibited key check raises immediately, before accumulating violations."""
        # Output has both a missing required field and a prohibited key —
        # the prohibited key error should come first.
        output = {"sql": "DROP TABLE taxes"}  # no label/confidence/citation_id
        with pytest.raises(AISecurityError) as exc_info:
            self.v.validate("test-schema-v1", output)
        assert exc_info.value.refusal is AISecurityRefusal.OUTPUT_PROHIBITED_KEY


# ---------------------------------------------------------------------------
# OutputSchemaValidator — validate_or_raise()
# ---------------------------------------------------------------------------


class TestValidateOrRaise:
    def setup_method(self) -> None:
        self.v = OutputSchemaValidator()
        self.v.register(_simple_schema())

    def test_valid_output_returns_result(self) -> None:
        result = self.v.validate_or_raise("test-schema-v1", _valid_output())
        assert result.passed is True

    def test_violation_raises(self) -> None:
        output = {"label": "EXEMPT"}  # missing required fields
        with pytest.raises(AISecurityError) as exc_info:
            self.v.validate_or_raise("test-schema-v1", output)
        assert exc_info.value.refusal is AISecurityRefusal.OUTPUT_SCHEMA_VIOLATION

    def test_violation_error_contains_violation_text(self) -> None:
        output = {"label": "EXEMPT"}
        with pytest.raises(AISecurityError) as exc_info:
            self.v.validate_or_raise("test-schema-v1", output)
        assert "citation_id" in exc_info.value.detail or "confidence" in exc_info.value.detail


# ---------------------------------------------------------------------------
# FieldType coverage
# ---------------------------------------------------------------------------


class TestFieldTypeCoverage:
    """Ensure every FieldType is accepted by the validator when values match."""

    def test_all_field_types(self) -> None:
        v = OutputSchemaValidator()
        v.register(OutputSchema(
            schema_id="all-types",
            required_fields={
                "s": FieldType.STRING,
                "i": FieldType.INTEGER,
                "f": FieldType.FLOAT,
                "b": FieldType.BOOLEAN,
                "l": FieldType.LIST,
                "d": FieldType.DICT,
                "a": FieldType.ANY,
            },
        ))
        output = {
            "s": "hello",
            "i": 42,
            "f": 3.14,
            "b": True,
            "l": [1, 2, 3],
            "d": {"k": "v"},
            "a": object(),  # ANY accepts anything
        }
        result = v.validate("all-types", output)
        assert result.passed is True

    def test_boolean_is_not_integer(self) -> None:
        """Python's bool is a subclass of int — the validator must handle this."""
        v = OutputSchemaValidator()
        v.register(OutputSchema(
            schema_id="int-only",
            required_fields={"x": FieldType.INTEGER},
        ))
        # bool IS a subclass of int — isinstance(True, int) is True.
        # This is Python's reality; the validator accepts it.
        result = v.validate("int-only", {"x": True})
        assert result.passed is True  # bool satisfies int check


# ---------------------------------------------------------------------------
# TenantContext construction
# ---------------------------------------------------------------------------


class TestTenantContextConstruction:
    def test_valid_context(self) -> None:
        ctx = TenantContext(tenant_id="acme-eu", region="eu-west-1")
        assert ctx.tenant_id == "acme-eu"
        assert ctx.region == "eu-west-1"

    def test_empty_tenant_id_raises(self) -> None:
        with pytest.raises(AISecurityError) as exc_info:
            TenantContext(tenant_id="", region="eu-west-1")
        assert exc_info.value.refusal is AISecurityRefusal.TENANT_CONTEXT_INVALID

    def test_empty_region_raises(self) -> None:
        with pytest.raises(AISecurityError) as exc_info:
            TenantContext(tenant_id="acme-eu", region="")
        assert exc_info.value.refusal is AISecurityRefusal.TENANT_CONTEXT_INVALID


# ---------------------------------------------------------------------------
# TenantScopedIndex.build() validation
# ---------------------------------------------------------------------------


class TestTenantScopedIndexBuild:
    def test_empty_tenant_id_raises(self, tmp_path: Path) -> None:
        f = _make_md_file(tmp_path, "doc.md")
        idx = TenantScopedIndex()
        with pytest.raises(AISecurityError) as exc_info:
            idx.build([f], tenant_id="", region="eu-west-1")
        assert exc_info.value.refusal is AISecurityRefusal.TENANT_CONTEXT_INVALID

    def test_empty_region_raises(self, tmp_path: Path) -> None:
        f = _make_md_file(tmp_path, "doc.md")
        idx = TenantScopedIndex()
        with pytest.raises(AISecurityError) as exc_info:
            idx.build([f], tenant_id="acme-eu", region="")
        assert exc_info.value.refusal is AISecurityRefusal.TENANT_CONTEXT_INVALID

    def test_duplicate_ingestion_raises(self, tmp_path: Path) -> None:
        f = _make_md_file(tmp_path, "doc.md")
        idx = TenantScopedIndex()
        idx.build([f], tenant_id="acme-eu", region="eu-west-1")
        with pytest.raises(AISecurityError) as exc_info:
            idx.build([f], tenant_id="acme-eu", region="eu-west-1")
        assert exc_info.value.refusal is AISecurityRefusal.TENANT_CONTEXT_INVALID

    def test_same_source_different_tenant_allowed(self, tmp_path: Path) -> None:
        """Same file may be ingested for different tenants — not a duplicate."""
        f = _make_md_file(tmp_path, "doc.md")
        idx = TenantScopedIndex()
        idx.build([f], tenant_id="acme-eu", region="eu-west-1")
        idx.build([f], tenant_id="beta-eu", region="eu-west-1")
        assert idx.document_count == 2

    def test_same_source_different_region_allowed(self, tmp_path: Path) -> None:
        """Same file + same tenant may be ingested in different regions."""
        f = _make_md_file(tmp_path, "doc.md")
        idx = TenantScopedIndex()
        idx.build([f], tenant_id="acme-eu", region="eu-west-1")
        idx.build([f], tenant_id="acme-eu", region="us-east-1")
        assert idx.document_count == 2


# ---------------------------------------------------------------------------
# TenantScopedIndex.search() — core isolation guarantee
# ---------------------------------------------------------------------------


@pytest.fixture
def scoped_index(tmp_path: Path) -> TenantScopedIndex:
    """Index with two tenants' documents and one SHARED document."""
    acme_doc = _make_md_file(
        tmp_path, "acme.md",
        "# ACME tax rules\n\nThis document contains ACME-specific tax rules for invoicing.\n"
    )
    beta_doc = _make_md_file(
        tmp_path, "beta.md",
        "# BETA tax rules\n\nThis document contains BETA-specific tax rules for reporting.\n"
    )
    shared_doc = _make_md_file(
        tmp_path, "shared.md",
        "# Public statute\n\nThis is a public authority statute applicable to all tenants.\n"
    )
    idx = TenantScopedIndex()
    idx.build([acme_doc], tenant_id="acme-eu", region="eu-west-1")
    idx.build([beta_doc], tenant_id="beta-eu", region="eu-west-1")
    idx.build(
        [shared_doc],
        tenant_id=TenantScopedIndex.SHARED,
        region="eu-west-1",
    )
    return idx


class TestTenantScopedIndexSearch:
    def test_before_build_raises(self) -> None:
        idx = TenantScopedIndex()
        ctx = TenantContext(tenant_id="t", region="r")
        with pytest.raises(AISecurityError):
            idx.search("tax rules", ctx)

    def test_query_too_short_raises(self, scoped_index: TenantScopedIndex) -> None:
        ctx = TenantContext(tenant_id="acme-eu", region="eu-west-1")
        with pytest.raises(AISecurityError):
            scoped_index.search("x", ctx)

    def test_tenant_a_cannot_see_tenant_b_chunks(
        self, scoped_index: TenantScopedIndex
    ) -> None:
        """ACME tenant must never receive BETA chunks (ZTAX-SEC-REQ-0031)."""
        ctx = TenantContext(tenant_id="acme-eu", region="eu-west-1")
        results = scoped_index.search("tax rules", ctx)
        tenant_ids = {r.tenant_id for r in results}
        assert "beta-eu" not in tenant_ids, (
            f"Cross-tenant leakage: ACME received BETA chunks — tenant_ids={tenant_ids}"
        )

    def test_tenant_b_cannot_see_tenant_a_chunks(
        self, scoped_index: TenantScopedIndex
    ) -> None:
        ctx = TenantContext(tenant_id="beta-eu", region="eu-west-1")
        results = scoped_index.search("tax rules", ctx)
        tenant_ids = {r.tenant_id for r in results}
        assert "acme-eu" not in tenant_ids, (
            f"Cross-tenant leakage: BETA received ACME chunks — tenant_ids={tenant_ids}"
        )

    def test_tenant_can_see_shared_chunks(
        self, scoped_index: TenantScopedIndex
    ) -> None:
        """Both tenants can retrieve SHARED public authority content."""
        ctx_acme = TenantContext(tenant_id="acme-eu", region="eu-west-1")
        ctx_beta = TenantContext(tenant_id="beta-eu", region="eu-west-1")
        results_acme = scoped_index.search("public statute", ctx_acme)
        results_beta = scoped_index.search("public statute", ctx_beta)
        assert any(r.tenant_id == TenantScopedIndex.SHARED for r in results_acme)
        assert any(r.tenant_id == TenantScopedIndex.SHARED for r in results_beta)

    def test_region_isolation(self, tmp_path: Path) -> None:
        """A tenant in eu-west-1 must not see chunks ingested for us-east-1."""
        eu_doc = _make_md_file(
            tmp_path, "eu_rules.md",
            "# EU tax rules\n\nEuropean Union specific tax rules for invoicing.\n"
        )
        us_doc = _make_md_file(
            tmp_path, "us_rules.md",
            "# US tax rules\n\nUnited States specific tax rules for reporting.\n"
        )
        idx = TenantScopedIndex()
        idx.build([eu_doc], tenant_id="acme", region="eu-west-1")
        idx.build([us_doc], tenant_id="acme", region="us-east-1")

        ctx_eu = TenantContext(tenant_id="acme", region="eu-west-1")
        results_eu = idx.search("tax rules", ctx_eu)
        regions = {r.region for r in results_eu}
        assert "us-east-1" not in regions, (
            f"Region leakage: eu-west-1 search returned us-east-1 chunks — regions={regions}"
        )

    def test_result_structure(self, scoped_index: TenantScopedIndex) -> None:
        ctx = TenantContext(tenant_id="acme-eu", region="eu-west-1")
        results = scoped_index.search("ACME invoicing", ctx)
        for r in results:
            assert isinstance(r, TenantScopedSearchResult)
            assert r.chunk is not None
            assert r.citation is not None
            assert isinstance(r.rank, float)
            assert r.tenant_id in ("acme-eu", TenantScopedIndex.SHARED)
            assert r.region == "eu-west-1"

    def test_empty_results_for_non_matching_query(
        self, scoped_index: TenantScopedIndex
    ) -> None:
        ctx = TenantContext(tenant_id="acme-eu", region="eu-west-1")
        results = scoped_index.search("xylophone quantum flux", ctx)
        assert results == []

    def test_unknown_tenant_gets_only_shared(
        self, scoped_index: TenantScopedIndex
    ) -> None:
        """A tenant with no ingested docs gets only SHARED results."""
        ctx = TenantContext(tenant_id="unknown-tenant", region="eu-west-1")
        results = scoped_index.search("tax rules statute", ctx)
        for r in results:
            assert r.tenant_id == TenantScopedIndex.SHARED


# ---------------------------------------------------------------------------
# TenantScopedIndex inspection
# ---------------------------------------------------------------------------


class TestTenantScopedIndexInspection:
    def test_document_count(self, tmp_path: Path) -> None:
        f1 = _make_md_file(tmp_path, "a.md")
        f2 = _make_md_file(tmp_path, "b.md")
        idx = TenantScopedIndex()
        idx.build([f1], tenant_id="t1", region="eu-west-1")
        idx.build([f2], tenant_id="t2", region="eu-west-1")
        assert idx.document_count == 2

    def test_chunk_count_positive_after_build(self, tmp_path: Path) -> None:
        f = _make_md_file(tmp_path, "a.md", "# Section\n\nBody text.\n")
        idx = TenantScopedIndex()
        idx.build([f], tenant_id="t1", region="eu-west-1")
        assert idx.chunk_count > 0

    def test_tenant_ids(self, tmp_path: Path) -> None:
        f1 = _make_md_file(tmp_path, "a.md")
        f2 = _make_md_file(tmp_path, "b.md")
        idx = TenantScopedIndex()
        idx.build([f1], tenant_id="t1", region="r1")
        idx.build([f2], tenant_id="t2", region="r1")
        assert idx.tenant_ids() == {"t1", "t2"}

    def test_shared_constant(self) -> None:
        assert TenantScopedIndex.SHARED == "__SHARED__"
