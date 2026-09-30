// The privacy rule's own suite.
//
// A gate nobody has tested is a gate nobody should trust. Each case takes the
// real contract, breaks exactly one thing, and asserts the lint refuses it for
// the right reason. The contract passing unbroken is the first case, so a rule
// that fires on everything fails here too.

import { test } from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const here = fileURLToPath(new URL(".", import.meta.url));
const contract = readFileSync(join(here, "..", "openapi", "ztax.v1.yaml"), "utf8");
const lint = join(here, "lint.mjs");

function run(text) {
  const dir = mkdtempSync(join(tmpdir(), "ztax-lint-"));
  const file = join(dir, "contract.yaml");
  writeFileSync(file, text);
  const r = spawnSync(process.execPath, [lint, file], { encoding: "utf8" });
  return { code: r.status, out: r.stdout + r.stderr };
}

function mutate(from, to) {
  assert.ok(contract.includes(from), `the contract no longer contains ${JSON.stringify(from)}; update the test`);
  return contract.replace(from, to);
}

const EMAIL = "x-ztax-privacy: { class: P1, purposes: [PURP-SEC, PURP-SUPPORT], retention: RET-OPERATIONAL, redaction: REDACT, aiAllowed: NONE }";

test("the contract as committed passes", () => {
  const r = run(contract);
  assert.equal(r.code, 0, r.out);
});

test("an unclassified field fails", () => {
  const r = run(mutate("\n          x-ztax-privacy: { class: P0 }\n", "\n"));
  assert.equal(r.code, 1);
  assert.match(r.out, /privacy-classified-fields/);
  assert.match(r.out, /is unclassified/);
});

test("an unclassified scalar schema fails", () => {
  const r = run(mutate("\n      x-ztax-privacy: { class: P0 }\n", "\n"));
  assert.equal(r.code, 1);
  assert.match(r.out, /a scalar schema with no privacy classification/);
});

test("personal data without a purpose fails", () => {
  const r = run(mutate(EMAIL, "x-ztax-privacy: { class: P1, retention: RET-OPERATIONAL, redaction: REDACT }"));
  assert.equal(r.code, 1);
  assert.match(r.out, /names no purpose/);
});

test("personal data without retention fails", () => {
  const r = run(mutate(EMAIL, "x-ztax-privacy: { class: P1, purposes: [PURP-SEC], redaction: REDACT }"));
  assert.equal(r.code, 1);
  assert.match(r.out, /names no retention policy/);
});

test("an unapproved purpose fails", () => {
  const r = run(mutate(EMAIL, EMAIL.replace("PURP-SUPPORT", "PURP-MARKETING")));
  assert.equal(r.code, 1);
  assert.match(r.out, /"PURP-MARKETING" is not an approved purpose/);
});

test("an account-personal field logged as-is fails", () => {
  const r = run(mutate(EMAIL, "x-ztax-privacy: { class: P2, purposes: [PURP-SEC], retention: RET-OPERATIONAL, redaction: NONE }"));
  assert.equal(r.code, 1);
  assert.match(r.out, /may not be logged as-is/);
});

test("a secret that may be logged in redacted form fails", () => {
  const r = run(mutate("redaction: NO_LOG", "redaction: REDACT"));
  assert.equal(r.code, 1);
  assert.match(r.out, /is a secret; redaction must be NO_LOG/);
});

test("special-category data fails", () => {
  const r = run(mutate(EMAIL, "x-ztax-privacy: { class: P6, purposes: [PURP-SEC], retention: RET-OPERATIONAL, redaction: NO_LOG }"));
  assert.equal(r.code, 1);
  assert.match(r.out, /is prohibited from ordinary schemas/);
});

test("unknown metadata is refused, not ignored", () => {
  const r = run(mutate("x-ztax-privacy: { class: P0 }", "x-ztax-privacy: { class: P0, sharable: true }"));
  assert.equal(r.code, 1);
  assert.match(r.out, /unknown privacy metadata "sharable"/);
});

test("an unknown class fails", () => {
  const r = run(mutate("x-ztax-privacy: { class: P0 }", "x-ztax-privacy: { class: P9 }"));
  assert.equal(r.code, 1);
  assert.match(r.out, /privacy class "P9"/);
});
