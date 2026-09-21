# Golden decimal vectors

The decimal corpus for [ADR-0002](../../../../adr/ADR-0002-decimal-rounding-context-policy.md) §2.7. These files are a build artifact, not a test fixture: they are read by the Go runner and, independently, by a Python implementation, and both fail the build on any divergence.

Two readers, on purpose:

| Reader | What it proves |
|---|---|
| [`internal/domain/fiscal/golden_test.go`](../../../internal/domain/fiscal/golden_test.go) | This runtime produces the registered answer, exactly. Tier 2 of ADR-0018 §2.1. |
| [`tools/decimalcrosscheck/crosscheck.py`](../../../tools/decimalcrosscheck/crosscheck.py) | A second implementation of the same specification agrees. ADR-0002 §5.1 control 2, which is what makes ADR-0001 §3.1's cross-language claim testable. |

The Python side is a second reading of the ADR, not a binding to the Go. A binding would agree by construction and would prove nothing.

```bash
make golden             # the Go runner
make golden-crosscheck  # the Python cross-check
make golden-register    # re-baseline the digests — a reviewed act, see below
```

## The sets

| File | Cases | What it covers |
|---|---|---|
| `arithmetic.json` | add, sub, mul | The operations that must be exact (§2.1). No case carries a policy, because none of them may round. |
| `rounding-modes.json` | round | Mode-by-mode boundary behaviour at a tie, in both signs, for the closed enum of §2.4. |
| `rate-application.json` | apply_rate, quo | The two operations that may round, in the shapes a determination performs: a rate on a net amount, an inclusive extraction, a per-unit factor, a second-step tax on a tax. |
| `allocation.json` | allocate | Largest-remainder with the ascending-ordinal tie-break (§2.6), including which line receives each residual unit. |
| `errors.json` | all | Inputs the runtime must refuse, and the substring the error has to contain. |

## Case format

```json
{
  "id": "rate-tie-half-even",
  "op": "apply_rate",
  "inputs": ["12.00", "0.06375"],
  "policy": {"mode": "HALF_EVEN", "scale": 2, "basis": "LINE"},
  "expect": ["0.76"],
  "oracle": "The exact product, 0.7650000, under half-even."
}
```

- **Inputs and expectations are strings**, always, and `expect` is always a list — one element for a scalar result, one per part for an allocation. A JSON number would be parsed as a binary float on the way in, which is the failure the corpus exists to detect.
- **`op`** is `add`, `sub`, `mul` (exact, no policy), `round`, `quo`, `apply_rate` or `allocate`. For `allocate`, the first input is the total and the rest are the weights.
- **`policy`** is the content-bundle form, decoded through the same path a signed bundle takes. `policyRaw` carries a malformed policy verbatim, for cases that assert what the decoder refuses.
- **`expectError`** names a substring the error must contain. The Python cross-check skips those cases and reports how many it skipped: how a runtime reports a refusal is a property of its API, while the arithmetic is what has to agree. Every skipped case must carry `expectError`, so nothing goes unevaluated by accident.
- **`oracle` is mandatory and load-bearing.** It names where the expected answer comes from — a specification clause, a worked example, arithmetic anyone can redo by hand. A case without one proves that we are consistent with ourselves, which is not what pack certification claims (ADR-0018 §3.1). Both readers fail a case with an empty oracle.

Set-level `provenance` carries the oracle, source, reviewer, registration date and content version for the set as a whole. The engine sets here are authored against the General Decimal Arithmetic Specification and the ADR rather than against a content bundle, and say so; a vector that claims legal force for a rate needs an authority instrument behind it, and that arrives with the first country pack.

## Registration

`manifest.json` records the sha256 of each set's exact bytes. Both readers verify the digests *before* reading a set, so an unregistered edit fails the build rather than quietly relaxing an assertion in the same run that reports the corpus green.

Changing an expected answer therefore takes two steps — edit the set, then `make golden-register` — and the second one produces a diff that says a golden answer moved. That is the point. ADR-0002 §6: precision and trap settings can change up to A4 with a re-baseline, and after A4 they cannot, because historical decisions must replay byte-identically.

## What is not here yet

- **Multi-step sequences.** Every case is one operation. Tax-on-tax ordering and inclusive extraction as a *composition* belong to the determination runtime and are blocked on `ZTAX-DET-001` (ADR-0005 §7.1); `rate-second-step-tax-on-tax` shows the shape of the second step without claiming an ordering.
- **Currency minor units.** Scale arrives from content in every case. The ISO 4217 exponent table is reference data with an authority and belongs on the `CONTENT` train, not in a Go map (ADR-0002 §7.1).
- **Authority-sourced rates.** The rates here are illustrative magnitudes. A vector whose expected answer is a legal claim needs an instrument reference, and those arrive with the country packs in W3.
