// Derive the 3.1.0 export from the authoritative 3.2.0 contract.
//
// ADR-0010 §2.2 and its registered item §5.3. OpenAPI 3.2.0 is mandated and its
// tooling ecosystem is immature, so the reviewed and published contract stays
// 3.2.0 and the generators run from a mechanically derived 3.1.0 export. The
// export is a build artifact, not a second contract.
//
// Two properties make that claim true rather than aspirational, and both are
// this script's job:
//
//   Generated, not edited.  `--check` regenerates and compares. A hand-edited
//                           export fails the build, which is the only thing
//                           stopping the export becoming a fork.
//   Refuses, not degrades.  A 3.2-only construct this script cannot represent
//                           in 3.1 stops the export rather than being dropped.
//                           A silently dropped construct is a contract the SDKs
//                           do not implement and nobody noticed.
//
// When a 3.2.0-capable Go generator and the SDK toolchains are certified, this
// file and the export directory are deleted, and nothing else changes.

import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { basename } from "node:path";
import { parseDocument, stringify } from "yaml";

const SOURCE_VERSION = "3.2.0";
const TARGET_VERSION = "3.1.0";

// The 3.2.0 additions this script would have to invent a 3.1 equivalent for.
// Each entry is a predicate over a node and its path; a hit is fatal.
//
// The list is not a guess at what a future author might write — it is the set
// of 3.2 constructs that have no 3.1 representation at all. Anything with a
// mechanical equivalent belongs in `downgrade` below, not here.
const UNREPRESENTABLE = [
  {
    match: (node, path) => path.length === 1 && path[0] === "$self",
    why: "$self gives a document its own identity; 3.1 has no such field, and dropping it changes how every relative $ref in the document resolves.",
  },
  {
    match: (node, path) => path.at(-1) === "additionalOperations",
    why: "additionalOperations declares operations under HTTP methods 3.1's Path Item Object cannot name. There is nowhere to put them.",
  },
  {
    match: (node, path) => path.at(-1) === "querystring",
    why: "the querystring field describes whole-query serialization, which 3.1 can only approximate per parameter — and an approximation of a serialization rule is a client that encodes requests differently from the server.",
  },
  {
    match: (node, path) => path.at(-2) === "tags" && node && typeof node === "object" && ("parent" in node || "kind" in node),
    why: "3.2 tags carry a parent and a kind, which structure generated SDK namespaces. 3.1 tags are flat, so dropping them would silently reshape every SDK.",
  },
  {
    match: (node, path) => path.at(-1) === "deviceAuthorization",
    why: "the device authorization OAuth flow is new in 3.2 and has no 3.1 flow object.",
  },
  {
    match: (node, path) => path.at(-2) === "xml" && path.at(-1) === "nodeType",
    why: "xml.nodeType replaces 3.1's attribute/wrapped booleans and does not map onto them in general.",
  },
];

/** Walk every node, depth first, reporting the path to each. */
function* walk(node, path = []) {
  yield [node, path];
  if (Array.isArray(node)) {
    for (const [i, item] of node.entries()) yield* walk(item, [...path, i]);
  } else if (node && typeof node === "object") {
    for (const [key, value] of Object.entries(node)) yield* walk(value, [...path, key]);
  }
}

/** Refuse anything the export cannot carry. */
function assertRepresentable(doc) {
  const refusals = [];
  for (const [node, path] of walk(doc)) {
    for (const rule of UNREPRESENTABLE) {
      if (rule.match(node, path)) {
        refusals.push(`  at /${path.join("/")}\n    ${rule.why}`);
      }
    }
  }
  if (refusals.length > 0) {
    throw new Error(
      `the contract uses ${refusals.length} OpenAPI ${SOURCE_VERSION} construct(s) the ${TARGET_VERSION} export cannot represent:\n\n` +
        refusals.join("\n\n") +
        `\n\nThe export is how the toolchain reads the contract (ADR-0010 §2.2). Either express this differently, or close the registered item in ADR-0010 §5.3 by certifying a ${SOURCE_VERSION}-capable toolchain.`,
    );
  }
}

/**
 * Downgrade in place.
 *
 * Deliberately almost nothing: the contract is authored so that the two
 * versions differ by the version string and the handful of mechanical rewrites
 * below. That is not luck — it is what keeps §5.3 cheap to close, and it is why
 * `assertRepresentable` runs first rather than this function growing clever.
 */
function downgrade(doc) {
  doc.openapi = TARGET_VERSION;

  for (const [node, path] of walk(doc)) {
    if (!node || typeof node !== "object" || Array.isArray(node)) continue;

    // 3.2 allows a summary on a Response Object; 3.1 does not. Folding it into
    // the description keeps the text rather than losing it, and a generated SDK
    // comment reads the same either way.
    if (path.at(-2) === "responses" && typeof node.summary === "string") {
      node.description = node.description ? `${node.summary}. ${node.description}` : node.summary;
      delete node.summary;
    }
  }
  return doc;
}

function render(doc, sourceName) {
  const banner = [
    `# GENERATED — do not edit.`,
    `#`,
    `# OpenAPI ${TARGET_VERSION}, derived from ${sourceName} by contracts/tools/downgrade.mjs.`,
    `# The authoritative contract is the ${SOURCE_VERSION} document; this export exists only`,
    `# because the toolchain is not ${SOURCE_VERSION}-capable yet (ADR-0010 §2.2, §5.3).`,
    `#`,
    `# A hand edit here fails CI: \`npm run export:check\` regenerates this file and`,
    `# compares. That check is the only thing keeping the export from becoming a`,
    `# second contract.`,
    ``,
  ].join("\n");

  // lineWidth: 0 disables wrapping. A serializer that rewraps prose on a
  // different column would make the export's bytes depend on its own default,
  // and the hash check would then fail on a library upgrade rather than on an
  // edit.
  return banner + stringify(doc, { lineWidth: 0, singleQuote: false });
}

function main(argv) {
  const check = argv.includes("--check");
  const [source, target] = argv.filter((a) => !a.startsWith("--"));
  if (!source || !target) {
    throw new Error("usage: downgrade.mjs [--check] <source.yaml> <export.yaml>");
  }

  const raw = readFileSync(source, "utf8");
  const doc = parseDocument(raw).toJS();
  if (doc.openapi !== SOURCE_VERSION) {
    throw new Error(
      `${source} declares OpenAPI ${doc.openapi}; the authoritative contract is ${SOURCE_VERSION} (ADR-0010 §2.2). ` +
        `Downgrading it silently would breach the specification without recording it.`,
    );
  }

  assertRepresentable(doc);
  const rendered = render(downgrade(doc), basename(source));
  const digest = createHash("sha256").update(rendered).digest("hex");
  const hashLine = `${digest}  ${basename(target)}\n`;

  if (check) {
    let existing;
    try {
      existing = readFileSync(target, "utf8");
    } catch {
      throw new Error(`${target} does not exist. Run \`npm run export\`.`);
    }
    if (existing !== rendered) {
      throw new Error(
        `${target} is not what this contract generates.\n` +
          `Either the contract changed and the export was not regenerated, or the export was edited by hand.\n` +
          `Run \`npm run export\` and commit the result (ADR-0010 §5.1 control 4).`,
      );
    }
    const recorded = readFileSync(`${target}.sha256`, "utf8");
    if (recorded !== hashLine) {
      throw new Error(`${target}.sha256 does not match the export it names.`);
    }
    process.stdout.write(`export is current: ${digest}\n`);
    return;
  }

  writeFileSync(target, rendered);
  writeFileSync(`${target}.sha256`, hashLine);
  process.stdout.write(`wrote ${target} (${digest})\n`);
}

try {
  main(process.argv.slice(2));
} catch (err) {
  process.stderr.write(`contract export: ${err.message}\n`);
  process.exit(1);
}
