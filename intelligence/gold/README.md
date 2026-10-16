# Gold-Set Reviewer Guide

This document is for the **telecom-tax specialist** who will review the cases
in this folder. It explains what a gold-set case is, what you need to check,
how to approve a case, and the rules you must follow.

---

## What is a gold-set case?

A gold-set case is a labelled test example. Each case contains:

| Field | What it means |
|---|---|
| `case_id` | A unique identifier, e.g. `TC-001`. Do not change this. |
| `sku_description` | The product description exactly as a customer would see it on a bill. |
| `expected_class` | The canonical ontology class this product belongs to, e.g. `ontology:telecom/voice/mobile`. |
| `notes` | Any context about why this class was chosen, or any known ambiguity. |
| `origin` | How the expected class was derived. See the Origin section below. |
| `author` | Who created this draft case. |
| `review_status` | `DRAFT` means not yet reviewed. `REVIEWED` means a qualified reviewer approved it. |
| `reviewer` | Your name or identifier — filled in when you approve. |
| `reviewed_on` | The date you approved it, in YYYY-MM-DD format. |
| `source_ref` | The tax statute, guidance note or ruling that justifies the expected class. |

---

## What you need to check for each case

Before approving a case, you must satisfy yourself on **all** of the following:

1. **The SKU description is realistic.** It should describe a real telecom product
   or service that would appear on an actual customer invoice.

2. **The expected class is correct.** Look up the canonical ontology class in
   the ZoikoTax telecom product ontology (ZTAX-CLS-001). The class must
   accurately describe what the product *is*, independent of how it is taxed
   in any specific jurisdiction.

3. **There is a source to cite.** For `SOURCE_DERIVED` or `PRODUCTION_DERIVED`
   cases, you must provide the exact statute, guidance note, ruling or court
   decision that supports the expected class in `source_ref`. Do not cite a
   model output, a classifier output, or an internal assumption.

4. **The notes are honest.** If the case is ambiguous or borderline, the notes
   must say so. Do not approve a case where the notes conceal a genuine
   disagreement.

---

## How to approve a case

To change a case from `DRAFT` to `REVIEWED`, edit the case object in the JSON
file and make exactly these four changes:

```json
"review_status": "REVIEWED",
"reviewer": "your-name-or-id",
"reviewed_on": "2026-10-16",
"source_ref": "HMRC VAT Notice 741A section 12.3"
```

> **Important:** Remove the empty `"reviewer": ""` and `"reviewed_on": ""`
> placeholders and fill them with real values. A date of `""` or a reviewer of
> `""` will cause the loader to reject the file.

For `SYNTHETIC` cases that you are promoting to `SOURCE_DERIVED`, also change:

```json
"origin": "SOURCE_DERIVED",
"source_ref": "the exact reference"
```

---

## Rules you must follow

These rules are enforced by the loader and cannot be bypassed:

1. **The reviewer must not be the author.** If the `author` field says
   `engineering-draft` or any other name, your `reviewer` value must be
   different. You cannot approve your own cases.

2. **Expected answers must come from the source text, never from a model or
   from the classifier output.** If you ran the ZoikoTax classifier and it
   suggested a class, that is not a valid source. The expected class must come
   from a tax statute, a binding ruling, official guidance, or your own
   professional judgement documented with a real citation.

3. **DRAFT cases are never used as evidence.** The system will refuse to load
   DRAFT cases into any evaluation run. Only REVIEWED cases with a real
   reviewer and a real date can be used to measure model accuracy.

4. **SYNTHETIC cases require an explicit opt-in to use.** Even if you mark a
   SYNTHETIC case as REVIEWED, it will only be included in evaluation runs
   where the operator explicitly enables synthetic data. Prefer changing the
   origin to `SOURCE_DERIVED` or `PRODUCTION_DERIVED` once you have a real
   citation.

5. **Do not add new fields.** The loader rejects any unknown keys. If you need
   to record additional information, use the `notes` field.

---

## Origin values explained

| Value | Meaning |
|---|---|
| `SYNTHETIC` | Invented by the engineering team as a placeholder. No real product or ruling behind it. Use for smoke tests only. |
| `SOURCE_DERIVED` | Based on a published tax statute, guidance document or ruling. You must fill in `source_ref` with the exact reference. |
| `PRODUCTION_DERIVED` | Based on a real product from an operator's live catalog, with consent. You must fill in `source_ref`. |

---

## What to do if you disagree with an expected class

Edit the `expected_class` field to the correct ontology node, update `notes`
to explain why the original was wrong, fill in your `source_ref`, and set
`review_status` to `REVIEWED`. You do not need to preserve the original
classification — the git history records what changed and when.

If the product is genuinely ambiguous and the expected class cannot be
determined without a jurisdiction-specific ruling, leave the case as `DRAFT`
and add a detailed note explaining the ambiguity. Do not guess.

---

## Contact

If you have questions about the ontology class identifiers or the file format,
contact the engineering team. Do not change the `case_id` values or the file
name — these are used by the CI pipeline to track which cases have been
evaluated.
