#!/usr/bin/env python3
"""Cross-language golden-vector check — ADR-0002 §2.7 and §5.1 control 2.

ADR-0001 §3.1 claims that the estate's decimal arithmetic agrees across Go and
Python because both implement the General Decimal Arithmetic Specification.
This script is what makes that claim testable: it evaluates the same vectors the
Go runner evaluates, using nothing but the standard library's `decimal` module,
and fails on any divergence from the registered expectation.

It is deliberately a second implementation rather than a binding to the first.
A binding would agree with Go by construction and would prove nothing.

    python3 tools/decimalcrosscheck/crosscheck.py testdata/golden/decimal

Cases carrying `expectError` are counted and skipped: error behaviour is a
property of each runtime's API rather than of the arithmetic, and the Go runner
owns those assertions. Every skipped case must carry `expectError`, so a case
cannot go unevaluated by accident.
"""

from __future__ import annotations

import hashlib
import json
import sys
from decimal import (
    Context,
    Decimal,
    DivisionByZero,
    Inexact,
    InvalidOperation,
    Overflow,
    Underflow,
    localcontext,
)
from pathlib import Path

PRECISION = 34  # ADR-0002 §2.1 — the decimal128 coefficient width.

ROUNDERS = {
    "HALF_UP": "ROUND_HALF_UP",
    "HALF_EVEN": "ROUND_HALF_EVEN",
    "HALF_DOWN": "ROUND_HALF_DOWN",
    "DOWN": "ROUND_DOWN",
    "UP": "ROUND_UP",
    "CEILING": "ROUND_CEILING",
    "FLOOR": "ROUND_FLOOR",
}

BASES = {"LINE", "DOCUMENT", "TAX_COMPONENT", "JURISDICTION_TOTAL"}
MAX_SCALE = 12


class VectorError(Exception):
    """A vector the checker refuses to evaluate, as opposed to one that fails."""


def exact_context() -> Context:
    """The context for operations ADR-0002 §2.1 requires to be exact."""
    return Context(
        prec=PRECISION,
        traps=[Inexact, Overflow, Underflow, InvalidOperation, DivisionByZero],
    )


def policy_context(mode: str) -> Context:
    """The context for the one rounding event a policy authorises."""
    return Context(
        prec=PRECISION,
        rounding=ROUNDERS[mode],
        traps=[Overflow, Underflow, InvalidOperation, DivisionByZero],
    )


def read_policy(case: dict) -> tuple[str, int, str]:
    policy = case.get("policy")
    if policy is None:
        raise VectorError("case has no policy")
    if set(policy) != {"mode", "scale", "basis"}:
        raise VectorError(f"policy keys {sorted(policy)} are not mode/scale/basis")
    mode, scale, basis = policy["mode"], policy["scale"], policy["basis"]
    if mode not in ROUNDERS:
        raise VectorError(f"unknown rounding mode {mode!r}")
    if basis not in BASES:
        raise VectorError(f"unknown rounding basis {basis!r}")
    if isinstance(scale, bool) or not isinstance(scale, int) or not 0 <= scale <= MAX_SCALE:
        raise VectorError(f"scale {scale!r} out of range [0,{MAX_SCALE}]")
    return mode, scale, basis


def quantize(value: Decimal, mode: str, scale: int) -> Decimal:
    return policy_context(mode).quantize(value, Decimal(1).scaleb(-scale))


def allocate(total: Decimal, weights: list[Decimal], mode: str, scale: int) -> list[Decimal]:
    """Largest-remainder allocation — the same procedure as fiscal.Allocate.

    Written from ADR-0002 §2.6 rather than transcribed from the Go, because the
    point of the exercise is that two readings of the ADR agree.
    """
    if not weights:
        raise VectorError("allocate with no weights")
    if any(w < 0 for w in weights):
        raise VectorError("negative weight")

    rounded = quantize(total, mode, scale)
    with localcontext(exact_context()):
        units = rounded.scaleb(scale)
        negative = units < 0
        units = abs(units)
        weight_sum = sum(weights, Decimal(0))

    parts = [Decimal(0)] * len(weights)
    if weight_sum != 0:
        with localcontext(exact_context()):
            remainders = []
            for i, weight in enumerate(weights):
                share = units * weight
                parts[i] = share // weight_sum
                remainders.append(share % weight_sum)
            residual = int(units - sum(parts, Decimal(0)))
        if not 0 <= residual <= len(weights):
            raise VectorError(f"residual {residual} outside [0,{len(weights)}]")
        # Largest remainder first, ascending ordinal within a tie.
        order = sorted(range(len(weights)), key=lambda i: (-remainders[i], i))
        for i in order[:residual]:
            parts[i] += 1
    elif units != 0:
        raise VectorError("weights sum to zero")

    with localcontext(exact_context()):
        out = [part.scaleb(-scale) for part in parts]
    if negative:
        out = [-part for part in out]
    return [quantize(part, mode, scale) for part in out]


def evaluate(case: dict) -> list[str]:
    op = case["op"]
    inputs = [Decimal(value) for value in case["inputs"]]

    if op in ("add", "sub", "mul"):
        if "policy" in case:
            raise VectorError(f"{op} is exact and takes no policy (ADR-0002 §2.1)")
        if len(inputs) != 2:
            raise VectorError(f"{op} takes two inputs")
        x, y = inputs
        ctx = exact_context()
        operation = {"add": ctx.add, "sub": ctx.subtract, "mul": ctx.multiply}[op]
        return [fmt(operation(x, y))]

    mode, scale, _ = read_policy(case)

    if op == "round":
        if len(inputs) != 1:
            raise VectorError("round takes one input")
        return [fmt(quantize(inputs[0], mode, scale))]

    if op == "quo":
        if len(inputs) != 2:
            raise VectorError("quo takes two inputs")
        quotient = policy_context(mode).divide(inputs[0], inputs[1])
        return [fmt(quantize(quotient, mode, scale))]

    if op == "apply_rate":
        if len(inputs) != 2:
            raise VectorError("apply_rate takes a base and a rate")
        product = exact_context().multiply(inputs[0], inputs[1])
        return [fmt(quantize(product, mode, scale))]

    if op == "allocate":
        if len(inputs) < 2:
            raise VectorError("allocate takes a total and at least one weight")
        return [fmt(part) for part in allocate(inputs[0], inputs[1:], mode, scale)]

    raise VectorError(f"unknown op {op!r}")


def fmt(value: Decimal) -> str:
    """Plain notation, trailing zeros preserved — apd's Text('f'), and the
    canonical decimal string of ADR-0011 §2.2. Scale is semantic here: "1.50"
    and "1.5" are the same number and not the same fiscal statement.
    """
    return format(value, "f")


def check_manifest(root: Path) -> list[Path]:
    """Verify the registered digest of every set before reading it.

    ADR-0002 §2.7 registers a hash per set because a golden vector that can be
    edited without ceremony is a fixture, and a fixture cannot be evidence.
    """
    manifest = json.loads((root / "manifest.json").read_text(encoding="utf-8"))
    files = []
    for entry in manifest["sets"]:
        path = root / entry["file"]
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        if digest != entry["sha256"]:
            raise SystemExit(
                f"{path.name}: digest {digest} does not match the registered "
                f"{entry['sha256']}. Re-baselining a vector set is a reviewed "
                f"act — see ADR-0002 §6 and `make golden-register`."
            )
        files.append(path)

    registered = {entry["file"] for entry in manifest["sets"]}
    present = {p.name for p in root.glob("*.json")} - {"manifest.json"}
    unregistered = present - registered
    if unregistered:
        raise SystemExit(f"unregistered vector set(s): {', '.join(sorted(unregistered))}")
    return files


def main(argv: list[str]) -> int:
    root = Path(argv[1] if len(argv) > 1 else "testdata/golden/decimal")
    evaluated = skipped = 0
    failures: list[str] = []

    for path in check_manifest(root):
        vectors = json.loads(path.read_text(encoding="utf-8"))
        for case in vectors["cases"]:
            case_id = f"{vectors['set']}/{case['id']}"
            if not case.get("oracle"):
                failures.append(
                    f"{case_id}: no oracle. A vector with no legal basis proves "
                    f"only self-consistency (ADR-0018 §3.1)"
                )
                continue
            if "expectError" in case:
                skipped += 1
                continue
            try:
                got = evaluate(case)
            except (VectorError, ArithmeticError) as err:
                failures.append(f"{case_id}: {type(err).__name__}: {err}")
                continue
            evaluated += 1
            want = case["expect"]
            if got != want:
                failures.append(
                    f"{case_id}: Python decimal gives {got}, vectors register {want}"
                )

    for failure in failures:
        print(f"FAIL {failure}", file=sys.stderr)
    print(
        f"{evaluated} vectors agree between Go and Python decimal, "
        f"{skipped} error cases left to the Go runner, {len(failures)} divergences"
    )
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
