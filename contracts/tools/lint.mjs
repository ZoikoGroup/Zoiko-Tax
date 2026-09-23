// The API lint: ADR-0010 §5.1 control 3, plus the rules around it.
//
// A general-purpose OpenAPI linter would check that the document is valid
// OpenAPI. That is worth having and is not what this is. This checks the rules
// that are specific to *this* contract and that no off-the-shelf ruleset knows
// about — the ones where a violation is a fiscal or governance defect rather
// than a style problem:
//
//   fiscal-amounts-are-strings   A JSON number cannot carry decimal precision,
//                                and we do not control the client's parser at
//                                all (ADR-0010 §2.9, ADR-0011 §1.1).
//   errors-are-problems          Every error response references the shared
//                                Problem schema (§2.8). An ad-hoc error shape
//                                is a client that has to special-case one
//                                endpoint.
//   operations-are-documented    operationId, summary, and at least one example
//                                on every response that has a body (§2.10).
//   idempotent-writes            Every endpoint ADR-0013 makes idempotent
//                                declares a required Idempotency-Key header
//                                (§5.1 control 6).
//   no-orphan-schemas            A component nothing references is a component
//                                nobody reviewed the effect of removing.
//
// Failures are reported all at once rather than one per run: a contract author
// fixing five rule violations should see five, not run the gate five times.

import { readFileSync } from "node:fs";
import { parseDocument } from "yaml";

const PROBLEM_REF = "#/components/schemas/Problem";

// Endpoints ADR-0013 §2.1 makes idempotent. The list lives here rather than
// being inferred from the method, because "it is a POST" is not the test —
// `POST /v1/auth/sign-in` is a POST and is not idempotent, and
// `POST /v1/transactions:commit` is a POST that must never execute twice.
const IDEMPOTENT = [
  "/v1/transactions:commit",
  "/v1/transactions:adjust",
  "/v1/transactions:refund",
  "/v1/batches",
];

// Field names whose value is a fiscal quantity wherever it appears. A `type:
// number` under any of these is the failure this lint exists for.
const FISCAL_NAMES =
  /^(amount|net|gross|tax|taxable|base|rate|price|total|subtotal|value|quantity|threshold|credit|debit|balance|fee|levy|duty|charge)/i;

const problems = [];
function fail(where, rule, message) {
  problems.push({ where, rule, message });
}

/** Walk every node, depth first, reporting the JSON-pointer path to each. */
function* walk(node, path = []) {
  yield [node, path];
  if (Array.isArray(node)) {
    for (const [i, item] of node.entries()) yield* walk(item, [...path, String(i)]);
  } else if (node && typeof node === "object") {
    for (const [key, value] of Object.entries(node)) yield* walk(value, [...path, key]);
  }
}

const pointer = (path) => "/" + path.join("/");

// ---------------------------------------------------------------------------

function checkFiscalTypes(doc) {
  for (const [node, path] of walk(doc)) {
    if (!node || typeof node !== "object" || Array.isArray(node)) continue;
    if (node.type !== "number") continue;

    // `properties` then the name, so the name is the last path segment.
    const name = path.at(-1);
    const inProperties = path.at(-2) === "properties";
    if (inProperties && FISCAL_NAMES.test(name)) {
      fail(
        pointer(path),
        "fiscal-amounts-are-strings",
        `"${name}" is typed as a JSON number. A fiscal amount is a string in canonical decimal form, ` +
          `because a number round-trips through an IEEE 754 double in every conformant JSON parser — ` +
          `which is the failure ADR-0011 §1.1 describes and ADR-0010 §2.9 exists to prevent.`,
      );
    }
    // `format: float`, `format: double` and unbounded `number` are wrong even
    // where the name is not obviously fiscal, because a schema reused later is
    // a schema whose name stops describing it.
    if (node.format === "float" || node.format === "double") {
      fail(
        pointer(path),
        "fiscal-amounts-are-strings",
        `format: ${node.format} is a binary float. Nothing in this contract may be one.`,
      );
    }
  }
}

function checkErrorsAreProblems(doc) {
  for (const [operation, where] of operations(doc)) {
    for (const [status, response] of Object.entries(operation.responses ?? {})) {
      if (!/^[45]/.test(status)) continue;
      const resolved = resolve(doc, response);
      const media = resolved?.content ?? {};

      if (!media["application/problem+json"]) {
        fail(
          `${where} → ${status}`,
          "errors-are-problems",
          `an error response must be application/problem+json (RFC 9457, ADR-0016 §2.5); found ${
            Object.keys(media).join(", ") || "no content"
          }.`,
        );
        continue;
      }
      const schema = media["application/problem+json"].schema;
      if (schema?.$ref !== PROBLEM_REF) {
        fail(
          `${where} → ${status}`,
          "errors-are-problems",
          `an error response references the shared Problem schema, not an ad-hoc shape. ` +
            `Found ${schema?.$ref ?? "an inline schema"}.`,
        );
      }
    }
  }
}

function checkOperationsAreDocumented(doc) {
  const ids = new Map();
  for (const [operation, where] of operations(doc)) {
    if (!operation.operationId) {
      fail(where, "operations-are-documented", "no operationId; it is the generated SDK's method name.");
    } else if (ids.has(operation.operationId)) {
      fail(
        where,
        "operations-are-documented",
        `operationId "${operation.operationId}" is already used by ${ids.get(operation.operationId)}; ` +
          `a duplicate collides in every generated SDK.`,
      );
    } else {
      ids.set(operation.operationId, where);
    }

    if (!operation.summary) {
      fail(where, "operations-are-documented", "no summary; it is what a reader sees in the SDK and the reference.");
    }

    // ADR-0010 §2.10: examples are drawn from the golden corpus rather than
    // hand-written, so that one that stops matching reality fails the build.
    // Until that corpus covers this surface, the rule enforced here is the
    // weaker one it will grow out of: an example must exist.
    for (const [status, response] of Object.entries(operation.responses ?? {})) {
      const resolved = resolve(doc, response);
      for (const [type, media] of Object.entries(resolved?.content ?? {})) {
        if (!media.example && !media.examples) {
          fail(
            `${where} → ${status} (${type})`,
            "operations-are-documented",
            "no example. A wrong example in a tax API is a support incident; a missing one is an integration written by guesswork.",
          );
        }
      }
    }

    const body = operation.requestBody;
    for (const [type, media] of Object.entries(resolve(doc, body)?.content ?? {})) {
      if (!media.example && !media.examples) {
        fail(`${where} → request (${type})`, "operations-are-documented", "no request example.");
      }
    }
  }
}

function checkIdempotentWrites(doc) {
  for (const path of IDEMPOTENT) {
    const item = doc.paths?.[path];
    if (!item) continue; // Not on this surface yet. It is a list of futures too.
    for (const [method, operation] of methods(item)) {
      const params = [...(item.parameters ?? []), ...(operation.parameters ?? [])].map((p) => resolve(doc, p));
      const key = params.find((p) => p?.in === "header" && p?.name?.toLowerCase() === "idempotency-key");
      if (!key) {
        fail(
          `${method.toUpperCase()} ${path}`,
          "idempotent-writes",
          "declares no Idempotency-Key header. ADR-0013 §2.1 makes it mandatory here, and a mandatory " +
            "header absent from the contract is a header no generated SDK sends.",
        );
      } else if (key.required !== true) {
        fail(
          `${method.toUpperCase()} ${path}`,
          "idempotent-writes",
          "declares Idempotency-Key as optional. It is rejected before any work begins when absent (ADR-0013 §2.1).",
        );
      }
    }
  }
}

function checkNoOrphanSchemas(doc) {
  const declared = new Set(Object.keys(doc.components?.schemas ?? {}));
  const referenced = new Set();
  for (const [node] of walk(doc)) {
    if (node && typeof node === "object" && typeof node.$ref === "string") {
      const name = node.$ref.replace("#/components/schemas/", "");
      if (name !== node.$ref) referenced.add(name);
    }
  }
  for (const name of declared) {
    if (!referenced.has(name)) {
      fail(
        `#/components/schemas/${name}`,
        "no-orphan-schemas",
        "nothing references this schema. Generated SDKs still emit a type for it, so an orphan is a public type nobody decided to publish.",
      );
    }
  }
}

// ---------------------------------------------------------------------------

const HTTP_METHODS = ["get", "put", "post", "delete", "options", "head", "patch", "trace"];

function* methods(item) {
  for (const method of HTTP_METHODS) {
    if (item?.[method]) yield [method, item[method]];
  }
}

function* operations(doc) {
  for (const [path, item] of Object.entries(doc.paths ?? {})) {
    for (const [method, operation] of methods(item)) {
      yield [operation, `${method.toUpperCase()} ${path}`];
    }
  }
}

/** Resolve a local $ref one level. The contract uses no remote refs. */
function resolve(doc, node) {
  if (!node || typeof node !== "object") return node;
  if (typeof node.$ref !== "string") return node;
  if (!node.$ref.startsWith("#/")) return node;
  return node.$ref
    .slice(2)
    .split("/")
    .reduce((acc, key) => acc?.[key.replace(/~1/g, "/").replace(/~0/g, "~")], doc);
}

function main(argv) {
  const [source] = argv;
  if (!source) throw new Error("usage: lint.mjs <contract.yaml>");
  const doc = parseDocument(readFileSync(source, "utf8")).toJS();

  checkFiscalTypes(doc);
  checkErrorsAreProblems(doc);
  checkOperationsAreDocumented(doc);
  checkIdempotentWrites(doc);
  checkNoOrphanSchemas(doc);

  if (problems.length > 0) {
    const byRule = new Map();
    for (const p of problems) {
      byRule.set(p.rule, [...(byRule.get(p.rule) ?? []), p]);
    }
    for (const [rule, found] of byRule) {
      process.stderr.write(`\n${rule} (${found.length})\n`);
      for (const p of found) {
        process.stderr.write(`  ${p.where}\n    ${p.message}\n`);
      }
    }
    process.stderr.write(`\n${problems.length} contract violation(s) in ${source}\n`);
    process.exit(1);
  }

  const count = [...operations(doc)].length;
  process.stdout.write(`${source}: ${count} operations, all rules pass\n`);
}

try {
  main(process.argv.slice(2));
} catch (err) {
  process.stderr.write(`contract lint: ${err.message}\n`);
  process.exit(1);
}
