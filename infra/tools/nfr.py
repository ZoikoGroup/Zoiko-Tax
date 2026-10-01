"""Validate the NFR documents and derive alert rules from them.

ZTAX-NFR-001 and ADR-0015 §2.10 make SLIs, SLOs, alerts and error budgets code
on the INFRA train. This tool is what makes that sentence true:

  validate   every document under infra/nfr/ against its JSON Schema in
             contracts/schemas/nfr/ (draft 2020-12), then across documents:
             every SLO names an SLI that exists and fits its objective, every
             latency SLO names a CapacityProfile that exists, every dependency
             a ResilienceProfile mentions is one the ServiceCriticality
             declares and behaves the same way there, no C0 journey calls AI
             or another forbidden dependency synchronously, and nothing
             approved rests on a placeholder.
  rules      Prometheus alerting rules derived from the SLOs, one file per
             service under infra/observability/prometheus/. --check fails if
             the committed file is not what the SLOs generate, so the alerts
             cannot drift from the objectives they claim to enforce.

Standard library only. The JSON Schema evaluator below implements the subset of
draft 2020-12 the NFR schemas use and refuses any keyword outside it, so a
schema author cannot add a constraint that is silently not enforced.

    python infra/tools/nfr.py validate
    python infra/tools/nfr.py rules [--check]
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from dataclasses import dataclass, field
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCHEMAS = ROOT / "contracts" / "schemas" / "nfr"
DOCUMENTS = ROOT / "infra" / "nfr"
RULES_OUT = ROOT / "infra" / "observability" / "prometheus"

KIND_SCHEMA = {
    "ServiceCriticality": "service-criticality.schema.json",
    "SLI": "sli.schema.json",
    "SLO": "slo.schema.json",
    "CapacityProfile": "capacity-profile.schema.json",
    "ResilienceProfile": "resilience-profile.schema.json",
}
ID_FIELD = {
    "ServiceCriticality": "service_criticality_id",
    "SLI": "sli_id",
    "SLO": "slo_id",
    "CapacityProfile": "capacity_profile_id",
    "ResilienceProfile": "resilience_profile_id",
}

# Build Plan W2 lane H: the C0 hot path makes no synchronous call to AI,
# authority websites, remote content databases or uncached geocoding.
C0_FORBIDDEN_SYNC = {"model_gateway", "authority", "content_plane", "geocoder"}

# ZTAX-NFR-001 §15: regional C0 calculation/commit RPO < 1 minute target, RTO <
# 15 minutes; acknowledged evidence RPO 0.
C0_MAX_RPO_S = 60
C0_MAX_RTO_S = 15 * 60


# =============================================================================
# A JSON Schema 2020-12 subset evaluator
# =============================================================================

SUPPORTED = {
    "$schema", "$id", "$comment", "$defs", "$ref", "title", "description",
    "type", "enum", "const", "properties", "required", "additionalProperties",
    "items", "minItems", "uniqueItems", "minLength", "maxLength", "pattern",
    "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum",
    "allOf", "if", "then", "else",
}


class SchemaError(Exception):
    """The schema itself is not something this evaluator can enforce."""


class SchemaStore:
    def __init__(self, directory: Path):
        self.directory = directory
        self.cache: dict[str, dict] = {}

    def load(self, name: str) -> dict:
        if name not in self.cache:
            path = self.directory / name
            if not path.is_file():
                raise SchemaError(f"schema {name} not found in {self.directory}")
            self.cache[name] = json.loads(path.read_text(encoding="utf-8"))
        return self.cache[name]

    def resolve(self, ref: str, current: str) -> tuple[dict, str]:
        file_part, _, pointer = ref.partition("#")
        doc_name = file_part or current
        node: object = self.load(doc_name)
        for token in [t for t in pointer.split("/") if t]:
            token = token.replace("~1", "/").replace("~0", "~")
            if not isinstance(node, dict) or token not in node:
                raise SchemaError(f"unresolvable $ref {ref!r} from {current}")
            node = node[token]
        if not isinstance(node, dict):
            raise SchemaError(f"$ref {ref!r} does not resolve to a schema")
        return node, doc_name


def _type_ok(value: object, t: str) -> bool:
    if t == "object":
        return isinstance(value, dict)
    if t == "array":
        return isinstance(value, list)
    if t == "string":
        return isinstance(value, str)
    if t == "boolean":
        return isinstance(value, bool)
    if t == "null":
        return value is None
    if t == "integer":
        return isinstance(value, int) and not isinstance(value, bool)
    if t == "number":
        return isinstance(value, (int, float)) and not isinstance(value, bool)
    raise SchemaError(f"unknown type {t!r}")


def validate(value: object, schema: dict, store: SchemaStore, doc: str, path: str = "") -> list[str]:
    """Return the list of violations of `schema` by `value`. `doc` is the
    schema file the schema node came from, for resolving local $refs."""
    unknown = set(schema) - SUPPORTED
    if unknown:
        raise SchemaError(f"{doc}: unsupported keyword(s) {sorted(unknown)} at schema node for {path or '/'}")
    here = path or "/"
    errors: list[str] = []

    if "$ref" in schema:
        target, target_doc = store.resolve(schema["$ref"], doc)
        errors += validate(value, target, store, target_doc, path)

    if "type" in schema:
        types = schema["type"] if isinstance(schema["type"], list) else [schema["type"]]
        if not any(_type_ok(value, t) for t in types):
            return errors + [f"{here}: expected {' or '.join(types)}, got {type(value).__name__}"]

    if "const" in schema and value != schema["const"]:
        errors.append(f"{here}: must be {json.dumps(schema['const'])}")
    if "enum" in schema and value not in schema["enum"]:
        errors.append(f"{here}: {json.dumps(value)} is not one of {json.dumps(schema['enum'])}")

    if isinstance(value, str):
        if "minLength" in schema and len(value) < schema["minLength"]:
            errors.append(f"{here}: shorter than {schema['minLength']}")
        if "maxLength" in schema and len(value) > schema["maxLength"]:
            errors.append(f"{here}: longer than {schema['maxLength']}")
        if "pattern" in schema and not re.search(schema["pattern"], value):
            errors.append(f"{here}: {value!r} does not match {schema['pattern']}")

    if isinstance(value, (int, float)) and not isinstance(value, bool):
        if "minimum" in schema and value < schema["minimum"]:
            errors.append(f"{here}: below minimum {schema['minimum']}")
        if "maximum" in schema and value > schema["maximum"]:
            errors.append(f"{here}: above maximum {schema['maximum']}")
        if "exclusiveMinimum" in schema and value <= schema["exclusiveMinimum"]:
            errors.append(f"{here}: must be greater than {schema['exclusiveMinimum']}")
        if "exclusiveMaximum" in schema and value >= schema["exclusiveMaximum"]:
            errors.append(f"{here}: must be less than {schema['exclusiveMaximum']}")

    if isinstance(value, dict):
        for name in schema.get("required", []):
            if name not in value:
                errors.append(f"{here}: missing required property {name!r}")
        props = schema.get("properties", {})
        for name, sub in value.items():
            child = f"{path}/{name}"
            if name in props:
                errors += validate(sub, props[name], store, doc, child)
            elif "additionalProperties" in schema:
                extra = schema["additionalProperties"]
                if extra is False:
                    errors.append(f"{here}: unexpected property {name!r}")
                elif isinstance(extra, dict):
                    errors += validate(sub, extra, store, doc, child)

    if isinstance(value, list):
        if "minItems" in schema and len(value) < schema["minItems"]:
            errors.append(f"{here}: fewer than {schema['minItems']} items")
        if schema.get("uniqueItems"):
            seen = [json.dumps(v, sort_keys=True) for v in value]
            if len(seen) != len(set(seen)):
                errors.append(f"{here}: items are not unique")
        if "items" in schema:
            for i, item in enumerate(value):
                errors += validate(item, schema["items"], store, doc, f"{path}/{i}")

    for sub in schema.get("allOf", []):
        errors += validate(value, sub, store, doc, path)

    if "if" in schema:
        if not validate(value, schema["if"], store, doc, path):
            if "then" in schema:
                errors += validate(value, schema["then"], store, doc, path)
        elif "else" in schema:
            errors += validate(value, schema["else"], store, doc, path)

    return errors


# =============================================================================
# Documents and cross-references
# =============================================================================

@dataclass
class Document:
    path: Path
    data: dict

    @property
    def kind(self) -> str:
        return self.data.get("kind", "")

    @property
    def id(self) -> str:
        return self.data.get(ID_FIELD.get(self.kind, ""), "")

    @property
    def rel(self) -> str:
        try:
            return self.path.relative_to(ROOT).as_posix()
        except ValueError:
            return str(self.path)


@dataclass
class Corpus:
    docs: list[Document] = field(default_factory=list)

    def of(self, kind: str) -> dict[str, Document]:
        return {d.id: d for d in self.docs if d.kind == kind}


def duration_seconds(d: str) -> int:
    m = re.fullmatch(r"([0-9]+)(ms|s|m|h|d)", d)
    if not m:
        raise ValueError(d)
    n, unit = int(m.group(1)), m.group(2)
    return {"ms": n // 1000, "s": n, "m": n * 60, "h": n * 3600, "d": n * 86400}[unit]


def json_pointer(doc: object, pointer: str) -> bool:
    node = doc
    for token in pointer.split("/")[1:]:
        token = token.replace("~1", "/").replace("~0", "~")
        if isinstance(node, dict) and token in node:
            node = node[token]
        elif isinstance(node, list) and token.isdigit() and int(token) < len(node):
            node = node[int(token)]
        else:
            return False
    return True


def load_corpus(directory: Path) -> tuple[Corpus, list[str]]:
    corpus, errors = Corpus(), []
    for path in sorted(directory.rglob("*.json")):
        try:
            data = json.loads(path.read_text(encoding="utf-8"))
        except json.JSONDecodeError as e:
            errors.append(f"{path}: not JSON: {e}")
            continue
        if not isinstance(data, dict):
            errors.append(f"{path}: top level must be an object")
            continue
        corpus.docs.append(Document(path, data))
    return corpus, errors


def schema_errors(corpus: Corpus, store: SchemaStore) -> list[str]:
    errors = []
    for d in corpus.docs:
        if d.kind not in KIND_SCHEMA:
            errors.append(f"{d.rel}: unknown kind {d.kind!r} (one of {', '.join(KIND_SCHEMA)})")
            continue
        schema_name = KIND_SCHEMA[d.kind]
        declared = d.data.get("$schema")
        if declared is not None:
            if (d.path.parent / declared).resolve() != (store.directory / schema_name).resolve():
                errors.append(f"{d.rel}: $schema {declared!r} is not {schema_name} for kind {d.kind}")
        errors += [f"{d.rel}: {e}" for e in validate(d.data, store.load(schema_name), store, schema_name)]
    return errors


def cross_reference_errors(corpus: Corpus) -> list[str]:
    errors: list[str] = []

    seen: dict[tuple[str, str], str] = {}
    for d in corpus.docs:
        key = (d.kind, d.id)
        if key in seen:
            errors.append(f"{d.rel}: duplicate {d.kind} id {d.id!r} (also in {seen[key]})")
        seen[key] = d.rel

    crit = {d.data["service"]: d for d in corpus.of("ServiceCriticality").values()}
    slis = corpus.of("SLI")
    capacity = corpus.of("CapacityProfile")
    order = ["C0", "C1", "C2", "C3", "C4"]

    # ServiceCriticality
    for d in crit.values():
        journeys = {j["journey_id"]: j for j in d.data["journeys"]}
        highest = min((j["class"] for j in journeys.values()), key=order.index)
        if d.data["class"] != highest:
            errors.append(f"{d.rel}: class {d.data['class']} but the most critical journey is {highest}; a service is operated to its most critical journey")
        names = [dep["name"] for dep in d.data["dependencies"]]
        if len(names) != len(set(names)):
            errors.append(f"{d.rel}: duplicate dependency names")
        for dep in d.data["dependencies"]:
            for jid in dep["synchronous_on"]:
                if jid not in journeys:
                    errors.append(f"{d.rel}: dependency {dep['name']} names unknown journey {jid!r}")
                elif journeys[jid]["class"] == "C0" and dep["kind"] in C0_FORBIDDEN_SYNC:
                    errors.append(f"{d.rel}: C0 journey {jid} calls {dep['kind']} {dep['name']!r} synchronously; the C0 hot path makes no synchronous call to AI, authorities, remote content or uncached geocoding")
            if dep["on_failure"] != "fail_closed" and any(
                journeys.get(j, {}).get("class") == "C0" for j in dep["synchronous_on"]
            ) and dep["kind"] in {"database", "kms", "secrets", "object_store"}:
                errors.append(f"{d.rel}: {dep['name']} is on the C0 path and must fail closed (NFR-001 §11)")

    # SLI -> service
    for s in slis.values():
        if s.data["service"] not in crit:
            errors.append(f"{s.rel}: service {s.data['service']} has no ServiceCriticality")

    # SLO
    for o in corpus.of("SLO").values():
        data = o.data
        svc = data["service"]
        if svc not in crit:
            errors.append(f"{o.rel}: service {svc} has no ServiceCriticality")
        else:
            classes = {j["class"] for j in crit[svc].data["journeys"]}
            if data["class"] not in classes:
                errors.append(f"{o.rel}: class {data['class']} matches no journey of {svc}")
        sli = slis.get(data["sli_ref"])
        if sli is None:
            errors.append(f"{o.rel}: sli_ref {data['sli_ref']!r} names no SLI")
            continue
        if sli.data["service"] != svc:
            errors.append(f"{o.rel}: SLI {sli.id} belongs to {sli.data['service']}, not {svc}")

        kind = data["objective"]["type"]
        mtype = sli.data["measurement"]["type"]
        want = {"ratio": "ratio", "latency": "distribution", "zero": "count"}[kind]
        if mtype != want:
            errors.append(f"{o.rel}: a {kind} objective needs a {want} SLI; {sli.id} is {mtype}")
        if kind == "latency" and sli.data["family"] != "latency":
            errors.append(f"{o.rel}: latency objective over a {sli.data['family']} SLI")

        alerting = data["alerting"]
        if kind == "ratio" and not alerting.get("burn_rates"):
            errors.append(f"{o.rel}: a ratio objective alerts on burn rate; alerting.burn_rates is empty")
        if kind != "ratio" and alerting.get("burn_rates"):
            errors.append(f"{o.rel}: burn_rates apply to ratio objectives only")
        if kind != "ratio" and "for" not in alerting:
            errors.append(f"{o.rel}: {kind} objective needs alerting.for")
        for b in alerting.get("burn_rates", []):
            if duration_seconds(b["short_window"]) >= duration_seconds(b["long_window"]):
                errors.append(f"{o.rel}: burn-rate short_window must be shorter than long_window")
            if duration_seconds(b["long_window"]) > duration_seconds(data["window"]):
                errors.append(f"{o.rel}: burn-rate long_window exceeds the SLO window")

        ref = data.get("capacity_profile_ref")
        profile = capacity.get(ref) if ref else None
        if ref and profile is None:
            errors.append(f"{o.rel}: capacity_profile_ref {ref!r} names no CapacityProfile")
        if profile and profile.data["service"] != svc:
            errors.append(f"{o.rel}: CapacityProfile {ref} belongs to {profile.data['service']}")

        if data["status"] == "approved":
            if not sli.data["measurement"].get("instrumented", False):
                errors.append(f"{o.rel}: approved over SLI {sli.id}, which is not instrumented")
            if profile and profile.data["status"] != "approved":
                errors.append(f"{o.rel}: approved against CapacityProfile {ref}, which is {profile.data['status']}; C0 latency is measured against an approved profile (Build Plan W2 exit gate)")

    # CapacityProfile
    for c in capacity.values():
        data = c.data
        for p in data["placeholders"]:
            if not json_pointer(data, p):
                errors.append(f"{c.rel}: placeholder {p!r} points at nothing")
        if data["status"] in {"measured", "approved"}:
            if data["placeholders"]:
                errors.append(f"{c.rel}: status {data['status']} with {len(data['placeholders'])} placeholder value(s)")
            if not data["benchmark_ref"]:
                errors.append(f"{c.rel}: status {data['status']} with no benchmark_ref; a claim that cannot be reproduced is invalid (NFR-001 §5)")
        sc = data["success_criteria"]
        if sc["server_p95_ms"] >= sc["server_p99_ms"]:
            errors.append(f"{c.rel}: p95 must be materially below p99 (NFR-001 §6)")
        rt = data["runtime"]
        if rt["replicas_max"] < rt["replicas_min"]:
            errors.append(f"{c.rel}: replicas_max below replicas_min")
        lc = data["line_count"]
        if not lc["p50"] <= lc["p95"] <= lc["max"]:
            errors.append(f"{c.rel}: line_count must satisfy p50 <= p95 <= max")
        if data["service_class"] == "C0" and data["topology"]["zones"] < 3 and data["status"] != "placeholder":
            errors.append(f"{c.rel}: a C0 profile is measured on at least three zones")

    # SLOs referencing a profile agree with its success criteria
    for o in corpus.of("SLO").values():
        ref = o.data.get("capacity_profile_ref")
        if not ref or ref not in capacity or o.data["objective"]["type"] != "latency":
            continue
        sc = capacity[ref].data["success_criteria"]
        thr = o.data["objective"]["threshold_ms"]
        if o.data["objective"]["percentile"] == 99 and thr not in (sc["server_p99_ms"], sc["single_line_runtime_p99_ms"]):
            errors.append(f"{o.rel}: p99 threshold {thr} ms appears in no success criterion of {ref}")

    # ResilienceProfile
    for r in corpus.of("ResilienceProfile").values():
        data = r.data
        svc = data["service"]
        if svc not in crit:
            errors.append(f"{r.rel}: service {svc} has no ServiceCriticality")
            continue
        deps = {dep["name"]: dep for dep in crit[svc].data["dependencies"]}
        for df in data["dependency_failure"]:
            dep = deps.get(df["dependency"])
            if dep is None:
                errors.append(f"{r.rel}: dependency {df['dependency']!r} is not declared in {svc}'s ServiceCriticality")
            elif dep["on_failure"] != df["behavior"]:
                errors.append(f"{r.rel}: {df['dependency']} behaves {df['behavior']} here but {dep['on_failure']} in the ServiceCriticality")
        if data["service_class"] == "C0":
            if data["zones_min"] < 3:
                errors.append(f"{r.rel}: a C0 service spans at least three zones")
            if duration_seconds(data["recovery"]["rpo"]) > C0_MAX_RPO_S:
                errors.append(f"{r.rel}: C0 RPO above 1m (NFR-001 §15)")
            if duration_seconds(data["recovery"]["rto"]) > C0_MAX_RTO_S:
                errors.append(f"{r.rel}: C0 RTO above 15m (NFR-001 §15)")
            if duration_seconds(data["recovery"]["acknowledged_evidence_rpo"]) != 0:
                errors.append(f"{r.rel}: acknowledged evidence RPO must be 0 (NFR-001 §12)")
            sync_c0 = {
                dep["name"] for dep in crit[svc].data["dependencies"]
                if dep["synchronous_on"] and dep["kind"] in {"database", "kms", "object_store"}
            }
            covered = {df["dependency"] for df in data["dependency_failure"]}
            for missing in sorted(sync_c0 - covered):
                errors.append(f"{r.rel}: no failure behaviour stated for synchronous dependency {missing}")
    return errors


def check_all(documents: Path = DOCUMENTS, schemas: Path = SCHEMAS) -> tuple[Corpus, list[str]]:
    store = SchemaStore(schemas)
    corpus, errors = load_corpus(documents)
    if not corpus.docs and not errors:
        return corpus, [f"no NFR documents under {documents}"]
    s_errors = schema_errors(corpus, store)
    errors += s_errors
    if not s_errors:
        # Cross-references assume schema-valid documents.
        errors += cross_reference_errors(corpus)
    return corpus, errors


# =============================================================================
# Alert rules
# =============================================================================

def _metric_name(slo_id: str) -> str:
    return re.sub(r"[^a-zA-Z0-9_]", "_", slo_id)


def _q(s: str) -> str:
    # A JSON string is a valid YAML double-quoted scalar.
    return json.dumps(s, ensure_ascii=False)


def _selector(sli: dict) -> str:
    sel = sli["measurement"].get("selector", "")
    return "{" + sel + "}" if sel else ""


def rules_for(service: str, corpus: Corpus) -> str:
    slis = corpus.of("SLI")
    slos = sorted((o for o in corpus.of("SLO").values() if o.data["service"] == service), key=lambda o: o.id)

    lines = [
        f"# GENERATED by infra/tools/nfr.py from infra/nfr/{service}/ — do not edit.",
        "# Change the SLO document and regenerate: python infra/tools/nfr.py rules",
        "# CI fails if this file is not what the SLOs generate (ADR-0015 §2.10).",
        "#",
        "# Metric names follow the OpenTelemetry HTTP semantic conventions as",
        "# exported to Prometheus, and the domain SLIs ADR-0015 §2.8 names. SLIs",
        "# marked instrumented: false are declared here ahead of the emitting code;",
        "# until it lands these rules evaluate to no data, never to a false page.",
        "groups:",
    ]

    for o in slos:
        data = o.data
        sli = slis[data["sli_ref"]].data
        name = _metric_name(o.id)
        objective = data["objective"]
        labels = {"service": service, "slo": o.id, "class": data["class"]}
        rules: list[list[str]] = []

        if objective["type"] == "ratio":
            budget = round(1 - objective["target_percent"] / 100, 10)
            windows = sorted({w for b in data["alerting"]["burn_rates"] for w in (b["long_window"], b["short_window"])}, key=duration_seconds)
            for w in windows:
                good = sli["measurement"]["good_events"].replace("{{window}}", w)
                valid = sli["measurement"]["valid_events"].replace("{{window}}", w)
                rules.append([
                    f"- record: slo:{name}:error_ratio:rate{w}",
                    f"  expr: {_q(f'1 - ({good}) / ({valid})')}",
                    "  labels:",
                    *[f"    {k}: {_q(v)}" for k, v in labels.items()],
                ])
            for b in data["alerting"]["burn_rates"]:
                threshold = round(b["factor"] * budget, 10)
                expr = (f"slo:{name}:error_ratio:rate{b['long_window']} > {threshold}"
                        f" and slo:{name}:error_ratio:rate{b['short_window']} > {threshold}")
                rules.append([
                    f"- alert: {name}_burn_{b['long_window']}_{b['short_window']}",
                    f"  expr: {_q(expr)}",
                    "  labels:",
                    f"    severity: {_q(b['severity'])}",
                    *[f"    {k}: {_q(v)}" for k, v in labels.items()],
                    "  annotations:",
                    f"    summary: {_q(f'{o.id}: error budget burning at {b['factor']}x over {b['long_window']} and {b['short_window']} (objective {objective['target_percent']}% over {data['window']})')}",
                    f"    source: {_q(data.get('source', ''))}",
                ])
        elif objective["type"] == "latency":
            m = sli["measurement"]
            q = objective["percentile"] / 100
            seconds = objective["threshold_ms"] / 1000
            expr = (f"histogram_quantile({q}, sum by (le) (rate({m['histogram']}_bucket{_selector(sli)}[5m])))"
                    f" > {seconds}")
            rules.append([
                f"- alert: {name}",
                f"  expr: {_q(expr)}",
                f"  for: {data['alerting']['for']}",
                "  labels:",
                f"    severity: {_q(data['alerting']['severity'])}",
                *[f"    {k}: {_q(v)}" for k, v in labels.items()],
                "  annotations:",
                f"    summary: {_q(f'{o.id}: p{objective['percentile']:g} above {objective['threshold_ms']:g} ms (CapacityProfile {data.get('capacity_profile_ref')})')}",
                f"    source: {_q(data.get('source', ''))}",
            ])
        else:  # zero
            m = sli["measurement"]
            expr = f"sum(increase({m['counter']}{_selector(sli)}[5m])) > 0"
            rule = [
                f"- alert: {name}",
                f"  expr: {_q(expr)}",
            ]
            if duration_seconds(data["alerting"]["for"]) > 0:
                rule.append(f"  for: {data['alerting']['for']}")
            rule += [
                "  labels:",
                f"    severity: {_q(data['alerting']['severity'])}",
                *[f"    {k}: {_q(v)}" for k, v in labels.items()],
                "  annotations:",
                f"    summary: {_q(f'{o.id}: any non-zero value is a {data['alerting']['severity']}')}",
                f"    source: {_q(data.get('source', ''))}",
            ]
            rules.append(rule)

        lines.append(f"  - name: {_q(o.id)}")
        lines.append("    rules:")
        for r in rules:
            lines += ["      " + line for line in r]

    return "\n".join(lines) + "\n"


def services(corpus: Corpus) -> list[str]:
    return sorted({o.data["service"] for o in corpus.of("SLO").values()})


# =============================================================================
# CLI
# =============================================================================

def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("validate")
    rules = sub.add_parser("rules")
    rules.add_argument("--check", action="store_true", help="fail if the committed rules differ from the generated ones")
    args = parser.parse_args(argv)

    try:
        corpus, errors = check_all()
    except SchemaError as e:
        print(f"nfr: schema error: {e}", file=sys.stderr)
        return 2
    if errors:
        print(f"nfr: {len(errors)} problem(s)", file=sys.stderr)
        for e in errors:
            print(f"  - {e}", file=sys.stderr)
        return 1

    if args.command == "validate":
        counts = {k: len(corpus.of(k)) for k in KIND_SCHEMA}
        print("nfr: ok - " + ", ".join(f"{v} {k}" for k, v in counts.items()))
        return 0

    stale = []
    for svc in services(corpus):
        out = RULES_OUT / f"{svc}.slo.rules.yaml"
        text = rules_for(svc, corpus)
        if args.check:
            current = out.read_text(encoding="utf-8") if out.exists() else None
            if current != text:
                stale.append(out.relative_to(ROOT).as_posix())
        else:
            out.parent.mkdir(parents=True, exist_ok=True)
            out.write_text(text, encoding="utf-8", newline="\n")
            print(f"nfr: wrote {out.relative_to(ROOT).as_posix()}")
    if stale:
        print("nfr: alert rules are stale; run `python infra/tools/nfr.py rules`:", file=sys.stderr)
        for s in stale:
            print(f"  - {s}", file=sys.stderr)
        return 1
    if args.check:
        print("nfr: alert rules match the SLOs")
    return 0


if __name__ == "__main__":
    sys.exit(main())
