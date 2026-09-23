// Emit the contract's route table, for the server-side conformance gate.
//
// ADR-0010 §2.1 makes the contract the source of truth and generates the
// handlers from it, with a regenerate-and-diff check so drift is a build
// failure rather than a discovery. That generator is not wired up yet. This
// closes the same gap from the other side, and does it mechanically:
//
//   contract ──routes.mjs──► routes.json ──contract_test.go──► the Go router
//
// The Go test compares in both directions. An endpoint the server routes and
// the contract does not declare fails, because an undocumented endpoint is a
// surface nobody reviewed; a declared endpoint nothing routes fails too,
// because five SDKs will have generated a method for it.
//
// JSON rather than YAML for one reason: the Go side reads it with the standard
// library. A YAML dependency in the backend module would be a dependency in the
// graph the replay manifest names (ADR-0001 §3.3), added to run one test.
//
// It is generated and hash-checked exactly as the 3.1 export is, so it cannot
// become a second, kinder statement of what the surface is.

import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { basename } from "node:path";
import { parseDocument } from "yaml";

const HTTP_METHODS = ["get", "put", "post", "delete", "options", "head", "patch", "trace"];

function extract(doc) {
  const routes = [];
  for (const [path, item] of Object.entries(doc.paths ?? {})) {
    for (const method of HTTP_METHODS) {
      const operation = item?.[method];
      if (!operation) continue;

      // A route is public when the operation overrides the document's security
      // with an empty list. That is the contract's way of saying "no session",
      // and the server's Route.Public must agree — an endpoint the contract
      // publishes as open and the server closes is an integration that fails in
      // production, and the reverse is an endpoint serving without a session.
      const security = operation.security ?? doc.security ?? [];
      routes.push({
        method: method.toUpperCase(),
        path,
        operationId: operation.operationId,
        public: Array.isArray(security) && security.length === 0,
      });
    }
  }
  // Sorted, so the file is a function of the contract rather than of the order
  // its paths happen to be written in.
  routes.sort((a, b) => (a.path === b.path ? a.method.localeCompare(b.method) : a.path.localeCompare(b.path)));
  return routes;
}

function render(routes, sourceName) {
  return (
    JSON.stringify(
      {
        _generated: `by contracts/tools/routes.mjs from ${sourceName}; do not edit`,
        routes,
      },
      null,
      2,
    ) + "\n"
  );
}

function main(argv) {
  const check = argv.includes("--check");
  const [source, target] = argv.filter((a) => !a.startsWith("--"));
  if (!source || !target) throw new Error("usage: routes.mjs [--check] <contract.yaml> <routes.json>");

  const doc = parseDocument(readFileSync(source, "utf8")).toJS();
  const rendered = render(extract(doc), basename(source));
  const digest = createHash("sha256").update(rendered).digest("hex");
  const hashLine = `${digest}  ${basename(target)}\n`;

  if (check) {
    let existing;
    try {
      existing = readFileSync(target, "utf8");
    } catch {
      throw new Error(`${target} does not exist. Run \`npm run routes\`.`);
    }
    if (existing !== rendered) {
      throw new Error(
        `${target} is not what this contract generates. Run \`npm run routes\` and commit the result.`,
      );
    }
    if (readFileSync(`${target}.sha256`, "utf8") !== hashLine) {
      throw new Error(`${target}.sha256 does not match the file it names.`);
    }
    process.stdout.write(`routes are current: ${digest}\n`);
    return;
  }

  writeFileSync(target, rendered);
  writeFileSync(`${target}.sha256`, hashLine);
  process.stdout.write(`wrote ${target} (${digest})\n`);
}

try {
  main(process.argv.slice(2));
} catch (err) {
  process.stderr.write(`contract routes: ${err.message}\n`);
  process.exit(1);
}
