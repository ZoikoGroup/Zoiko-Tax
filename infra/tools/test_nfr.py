"""Tests for infra/tools/nfr.py.

Each negative case breaks a copy of the real ztax-core documents one way and
asserts the refusal, so a check that stops firing fails here rather than
passing silently in CI (the same discipline as contracts/tools/lint.test.mjs).

    python -m unittest discover -s infra/tools -p "test_*.py"
"""

from __future__ import annotations

import json
import shutil
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import nfr  # noqa: E402

REAL = nfr.DOCUMENTS / "ztax-core"


class Workspace:
    """A temporary copy of the real documents, edited per test."""

    def __init__(self) -> None:
        self.tmp = Path(tempfile.mkdtemp(prefix="nfr-test-"))
        # Keep the same relative layout so "$schema" paths still resolve to
        # contracts/schemas/nfr: <tmp>/infra/nfr/ztax-core/*.json.
        self.docs = self.tmp / "infra" / "nfr"
        shutil.copytree(REAL, self.docs / "ztax-core")
        shutil.copytree(nfr.SCHEMAS, self.tmp / "contracts" / "schemas" / "nfr")
        self.schemas = self.tmp / "contracts" / "schemas" / "nfr"

    def path(self, name: str) -> Path:
        return self.docs / "ztax-core" / name

    def edit(self, name: str, fn) -> None:
        p = self.path(name)
        data = json.loads(p.read_text(encoding="utf-8"))
        fn(data)
        p.write_text(json.dumps(data, indent=2), encoding="utf-8")

    def add(self, name: str, data: dict) -> None:
        self.path(name).write_text(json.dumps(data, indent=2), encoding="utf-8")

    def errors(self) -> list[str]:
        _, errors = nfr.check_all(self.docs, self.schemas)
        return errors

    def close(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)


class RealDocuments(unittest.TestCase):
    def test_the_committed_documents_are_valid(self):
        corpus, errors = nfr.check_all()
        self.assertEqual(errors, [])
        self.assertEqual(len(corpus.of("SLO")), 4)

    def test_ztax_core_carries_the_w2_c0_targets(self):
        corpus, _ = nfr.check_all()
        slos = corpus.of("SLO")
        latency = slos["ztax-core.c0-server-latency-p99"].data["objective"]
        runtime = slos["ztax-core.single-line-runtime-p99"].data["objective"]
        self.assertEqual((latency["percentile"], latency["threshold_ms"]), (99, 150))
        self.assertEqual((runtime["percentile"], runtime["threshold_ms"]), (99, 10))

    def test_capacity_numbers_are_marked_as_placeholders(self):
        corpus, _ = nfr.check_all()
        profile = corpus.of("CapacityProfile")["ztax-core.c0-typical-invoice"].data
        self.assertEqual(profile["status"], "placeholder")
        self.assertIn("/load/steady_rps", profile["placeholders"])
        self.assertIsNone(profile["benchmark_ref"])

    def test_committed_rules_are_what_the_slos_generate(self):
        corpus, _ = nfr.check_all()
        out = nfr.RULES_OUT / "ztax-core.slo.rules.yaml"
        self.assertEqual(out.read_text(encoding="utf-8"), nfr.rules_for("ztax-core", corpus))

    def test_rules_are_deterministic(self):
        corpus, _ = nfr.check_all()
        self.assertEqual(nfr.rules_for("ztax-core", corpus), nfr.rules_for("ztax-core", corpus))


class Refusals(unittest.TestCase):
    def setUp(self):
        self.ws = Workspace()

    def tearDown(self):
        self.ws.close()

    def assertRefused(self, fragment: str):
        errors = self.ws.errors()
        self.assertTrue(any(fragment in e for e in errors), f"expected {fragment!r} in {errors}")

    # --- schema ---------------------------------------------------------------

    def test_missing_required_property(self):
        self.ws.edit("slo.c0-availability.json", lambda d: d.pop("eligible_requests"))
        self.assertRefused("missing required property 'eligible_requests'")

    def test_unknown_property(self):
        self.ws.edit("sli.c0-availability.json", lambda d: d.update(colour="red"))
        self.assertRefused("unexpected property 'colour'")

    def test_enum(self):
        self.ws.edit("criticality.json", lambda d: d.update({"class": "C9"}))
        self.assertRefused("is not one of")

    def test_type(self):
        self.ws.edit("slo.c0-availability.json", lambda d: d["objective"].update(target_percent="99.99"))
        self.assertRefused("expected number")

    def test_pattern(self):
        self.ws.edit("slo.c0-availability.json", lambda d: d.update(window="four weeks"))
        self.assertRefused("does not match")

    def test_bound(self):
        self.ws.edit("slo.c0-availability.json", lambda d: d["objective"].update(target_percent=100))
        self.assertRefused("must be less than 100")

    def test_conditional_latency_needs_a_capacity_profile(self):
        self.ws.edit("slo.c0-server-latency-p99.json", lambda d: d.pop("capacity_profile_ref"))
        self.assertRefused("missing required property 'capacity_profile_ref'")

    def test_conditional_ratio_sli_needs_its_events(self):
        self.ws.edit("sli.c0-availability.json", lambda d: d["measurement"].pop("good_events"))
        self.assertRefused("missing required property 'good_events'")

    def test_cross_region_failover_is_not_expressible(self):
        self.ws.edit("resilience-profile.json", lambda d: d["residency"].update(cross_region_failover=True))
        self.assertRefused("must be false")

    def test_schema_declared_must_match_kind(self):
        self.ws.edit("sli.c0-availability.json", lambda d: d.update({"$schema": "../../../contracts/schemas/nfr/slo.schema.json"}))
        self.assertRefused("is not sli.schema.json")

    def test_unsupported_schema_keyword_is_refused_not_ignored(self):
        p = self.ws.schemas / "sli.schema.json"
        schema = json.loads(p.read_text(encoding="utf-8"))
        schema["properties"]["description"] = {"type": "string", "format": "email"}
        p.write_text(json.dumps(schema), encoding="utf-8")
        with self.assertRaises(nfr.SchemaError):
            self.ws.errors()

    # --- cross-references -------------------------------------------------------

    def test_slo_must_name_an_existing_sli(self):
        self.ws.edit("slo.c0-availability.json", lambda d: d.update(sli_ref="ztax-core.no-such-sli"))
        self.assertRefused("names no SLI")

    def test_slo_must_name_an_existing_capacity_profile(self):
        self.ws.edit("slo.c0-server-latency-p99.json", lambda d: d.update(capacity_profile_ref="ztax-core.nope"))
        self.assertRefused("names no CapacityProfile")

    def test_objective_must_fit_the_sli(self):
        self.ws.edit("slo.c0-availability.json", lambda d: d.update(sli_ref="ztax-core.c0-server-latency"))
        self.assertRefused("a ratio objective needs a ratio SLI")

    def test_duplicate_ids(self):
        dup = json.loads(self.ws.path("sli.c0-availability.json").read_text(encoding="utf-8"))
        self.ws.add("sli.copy.json", dup)
        self.assertRefused("duplicate SLI id")

    def test_approved_slo_cannot_rest_on_a_placeholder_profile(self):
        self.ws.edit("slo.c0-server-latency-p99.json", lambda d: d.update(status="approved"))
        self.ws.edit("sli.c0-server-latency.json", lambda d: d["measurement"].update(instrumented=True))
        self.assertRefused("which is placeholder")

    def test_approved_slo_cannot_rest_on_an_uninstrumented_sli(self):
        self.ws.edit("slo.replay-divergence-zero.json", lambda d: d.update(status="approved"))
        self.assertRefused("not instrumented")

    def test_measured_profile_cannot_keep_placeholders(self):
        self.ws.edit("capacity-profile.json", lambda d: d.update(status="measured", benchmark_ref="bench/c0"))
        self.assertRefused("placeholder value(s)")

    def test_placeholder_pointer_must_resolve(self):
        self.ws.edit("capacity-profile.json", lambda d: d["placeholders"].append("/load/nonexistent"))
        self.assertRefused("points at nothing")

    def test_p95_must_be_below_p99(self):
        self.ws.edit("capacity-profile.json", lambda d: d["success_criteria"].update(server_p95_ms=150))
        self.assertRefused("materially below p99")

    def test_c0_journey_may_not_call_ai_synchronously(self):
        def f(d):
            for dep in d["dependencies"]:
                if dep["kind"] == "model_gateway":
                    dep["synchronous_on"] = ["c0-quote-commit"]
        self.ws.edit("criticality.json", f)
        self.assertRefused("calls model_gateway")

    def test_c0_database_must_fail_closed(self):
        def f(d):
            d["dependencies"][0]["on_failure"] = "degrade"
        self.ws.edit("criticality.json", f)
        self.ws.edit("resilience-profile.json", lambda d: d["dependency_failure"][0].update(behavior="degrade"))
        self.assertRefused("must fail closed")

    def test_resilience_dependency_must_be_declared(self):
        self.ws.edit("resilience-profile.json", lambda d: d["dependency_failure"].append(
            {"dependency": "mystery-cache", "behavior": "degrade", "observable_as": "nothing"}))
        self.assertRefused("is not declared")

    def test_resilience_and_criticality_must_agree(self):
        self.ws.edit("resilience-profile.json", lambda d: d["dependency_failure"][3].update(behavior="fail_closed"))
        self.assertRefused("in the ServiceCriticality")

    def test_c0_recovery_objectives(self):
        self.ws.edit("resilience-profile.json", lambda d: d["recovery"].update(rto="1h", acknowledged_evidence_rpo="1s"))
        self.assertRefused("C0 RTO above 15m")
        self.assertRefused("acknowledged evidence RPO must be 0")

    def test_burn_rate_windows(self):
        self.ws.edit("slo.c0-availability.json", lambda d: d["alerting"]["burn_rates"][0].update(short_window="2h"))
        self.assertRefused("short_window must be shorter")


class Evaluator(unittest.TestCase):
    """The schema subset, in isolation."""

    def setUp(self):
        self.store = nfr.SchemaStore(nfr.SCHEMAS)

    def v(self, value, schema):
        return nfr.validate(value, schema, self.store, "inline")

    def test_bool_is_not_an_integer(self):
        self.assertTrue(self.v(True, {"type": "integer"}))

    def test_type_union(self):
        self.assertFalse(self.v(None, {"type": ["string", "null"]}))
        self.assertTrue(self.v(3, {"type": ["string", "null"]}))

    def test_ref_into_common(self):
        self.assertTrue(self.v("C9", {"$ref": "common.schema.json#/$defs/serviceClass"}))
        self.assertFalse(self.v("C0", {"$ref": "common.schema.json#/$defs/serviceClass"}))

    def test_unique_items(self):
        self.assertTrue(self.v([1, 1], {"type": "array", "uniqueItems": True}))


if __name__ == "__main__":
    unittest.main()
