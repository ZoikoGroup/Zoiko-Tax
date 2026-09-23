// Generate the Postman collection from the contract.
//
// The collection used to be maintained by hand and said so, with an instruction
// in its own README: *when contracts/openapi lands, regenerate this from the
// contract; do not maintain it by hand.* ADR-0010 §2.1 makes the contract the
// source of truth and treats drift as a build failure, and a hand-edited
// collection is drift with extra steps — a second, kinder statement of what the
// API is, which is precisely what §3.1 rejects.
//
// Three inputs:
//
//   openapi/ztax.v1.yaml        the operations, their bodies and their examples
//   postman/overlay.json        folders the contract cannot express — requests a
//                               well-behaved client would never send, and the
//                               surface that is specified but not built
//   postman/scripts/*.js        the collection-level pre-request and test
//                               scripts, kept as JavaScript so they are
//                               reviewable as code rather than as a JSON array
//                               of strings
//
// One output, hash-checked exactly as the 3.1 export is, so it cannot drift.
//
// **The run plan is here, not in the contract.** A collection is run top to
// bottom, so sign-in has to precede the administrative calls and sign-out has to
// follow them — an ordering the contract has no business carrying, because it is
// a fact about a test run rather than about the API. FOLDERS below is that plan,
// and generation *fails* if an operation is missing from it. A new endpoint
// therefore cannot be silently dropped from the collection; somebody has to
// decide where in the run it belongs.

import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { basename, dirname, join } from "node:path";
import { parseDocument } from "yaml";

const HTTP_METHODS = ["get", "put", "post", "delete", "options", "head", "patch", "trace"];

// The run plan: which folder each operation belongs to, in order.
//
// Two placements are worth their comment. `signOut` is in a folder of its own
// after administration, because ending the session before the administrative
// calls would make every one of them a 401. `changePassword` is quarantined,
// because it revokes every session including this one *and* leaves the
// bootstrap administrator on a password the collection's variables no longer
// name — so a Collection Runner pass that included it would pass once and fail
// forever after.
const FOLDERS = [
  {
    name: "00 · Health — served, and deliberately not in the contract",
    description:
      "An orchestrator calls these; no SDK does. They carry no session, their bodies are plain text, and declaring them in the contract would generate client methods nobody should call and make a liveness probe a compatibility commitment (ADR-0010 §2.6).\n\nThey are added by the generator rather than derived from the contract, and `contract_test.go` keeps the same exception register on the server side.",
    literal: [
      {
        name: "GET /healthz — liveness",
        method: "GET",
        path: "/healthz",
        description:
          "Liveness: the process is running and can serve. It touches no dependency on purpose — a liveness probe that failed when the database was down would get the process killed, which does not fix a database and does lose the in-memory content bundle.",
        test: [
          "pm.test('the process is alive', function () {",
          "  pm.response.to.have.status(200);",
          "  pm.expect(pm.response.text()).to.eql('ok\\n');",
          "});",
        ],
      },
      {
        name: "GET /readyz — readiness",
        method: "GET",
        path: "/readyz",
        description:
          "Readiness: this replica can take traffic right now. Unlike liveness it does check the cell store, because a replica that cannot reach it should leave the rotation rather than serve one 503 per caller.",
        test: [
          "pm.test('this replica can take traffic', function () {",
          "  pm.response.to.have.status(200);",
          "  pm.expect(pm.response.text()).to.eql('ready\\n');",
          "});",
        ],
      },
    ],
  },
  {
    name: "01 · Discovery",
    description: "What this deployment can actually do, before you have a session.",
    operations: ["getCapabilities"],
  },
  {
    name: "02 · Sign in",
    description:
      "Opens the session every folder below depends on. The response carries no token — the session is an `HttpOnly` cookie Postman's cookie jar keeps for you.",
    operations: ["signIn"],
  },
  {
    name: "03 · Who am I",
    description: "What the client learns about itself, which is the only way it can learn it (ADR-0019 C8).",
    operations: ["getSession", "getTenant"],
  },
  {
    name: "04 · Administration",
    description:
      "Runs in order: list, create, grant, disable, then read the sessions and the audit trail, then undo. `createUser` captures the new user's id into `userId`, so the requests after it act on something that exists.",
    operations: [
      "listUsers",
      "createUser",
      "grantRole",
      "setUserStatus",
      "listSessions",
      "listAudit",
      "revokeRole",
      "revokeSession",
    ],
  },
  {
    name: "05 · Sign out",
    description: "Last, because everything above needs the session this ends.",
    operations: ["signOut"],
  },
  {
    name: "98 · Destructive — run deliberately, not as part of a pass",
    description:
      "`changePassword` revokes every session for the subject, including the calling one — a password change is what you do when a credential is believed compromised, and leaving other sessions alive would defeat it.\n\nIt also leaves the bootstrap administrator on `{{newPassword}}`, which the collection's `adminPassword` no longer names. Run it when you mean to, then update `adminPassword` or re-bootstrap the tenant.",
    operations: ["changePassword"],
  },
];

// Literal example values in the contract, and the collection variable that
// should stand in for each. The contract's examples are consistent, so this is
// a small table rather than a per-operation override — and a request body that
// stopped matching would show up as a literal value in the generated output,
// which is visible in review.
const VALUE_VARIABLES = {
  acme: "{{tenantSlug}}",
  "admin@acme.example": "{{adminEmail}}",
  "correct horse battery staple": "{{adminPassword}}",
  "a considerably longer passphrase than that one": "{{newPassword}}",
  "auditor@acme.example": "{{newUserEmail}}",
};

// Per-operation test scripts, beyond the status assertion every request gets.
//
// These are the assertions that need to know what the operation *means*: which
// variable to capture, and which control the response is evidence for.
const OPERATION_TESTS = {
  getCapabilities: [
    "pm.test('the deployment states whether it may produce authoritative output', function () {",
    "  pm.response.to.have.status(200);",
    "  pm.expect(pm.response.json()).to.have.property('authoritative');",
    "});",
    "pm.test('ADR-0011 §2.6 — it names the canonicalization profile it digests under', function () {",
    "  pm.expect(pm.response.json().canonProfile).to.match(/^canon\\/v\\d+$/);",
    "});",
    "// No authoritative fiscal output is permitted before A4. This is a warning",
    "// rather than a failure, because `false` is the correct answer today.",
    "if (pm.response.json().authoritative === true) {",
    "  console.warn('this deployment claims authoritative output — that requires A4');",
    "}",
    "const content = pm.response.json().content;",
    "if (content) {",
    "  pm.test('ADR-0011 §2.3 — the active bundle is named by digest, not only by id', function () {",
    "    pm.expect(content.digest).to.match(/^zt1:[0-9a-f]{64}$/);",
    "  });",
    "  pm.collectionVariables.set('bundleDigest', content.digest);",
    "} else {",
    "  console.info('no content bundle loaded; determination refuses with NO_CONTENT_BUNDLE');",
    "}",
  ],
  signIn: [
    "pm.test('sign-in succeeds', function () { pm.response.to.have.status(200); });",
    "pm.test('ADR-0020 — the session is a cookie, and the body carries no token', function () {",
    "  // A token a client can read is a token a client can leak, and a stateless",
    "  // one cannot be revoked. The absence of it here is the design.",
    "  pm.expect(pm.response.text()).to.not.match(/\"(token|accessToken|jwt|bearer|sessionToken)\"/i);",
    "});",
    "pm.test('the session cookie is set HttpOnly', function () {",
    "  const header = pm.response.headers.get('Set-Cookie') || '';",
    "  pm.expect(header.toLowerCase()).to.include('httponly');",
    "});",
  ],
  createUser: [
    "pm.test('the user is created', function () { pm.response.to.have.status(201); });",
    "pm.test('a user created without a password cannot sign in', function () {",
    "  // INVITED is a distinct state rather than a disabled user, because the two",
    "  // lead to different administrative actions.",
    "  pm.expect(pm.response.json().status).to.eql('INVITED');",
    "});",
    "pm.test('the response carries no credential material', function () {",
    "  pm.expect(pm.response.text()).to.not.match(/\"(password|verifier|salt|hash|argon)/i);",
    "});",
    "pm.collectionVariables.set('userId', pm.response.json().id);",
  ],
  listSessions: [
    "pm.test('the caller can tell which session is its own', function () {",
    "  pm.response.to.have.status(200);",
    "  const sessions = pm.response.json().sessions;",
    "  pm.expect(sessions.filter(function (s) { return s.current; })).to.have.lengthOf(1);",
    "});",
    "// Capture one that is not the caller's, so `revokeSession` below does not",
    "// sign this run out of the rest of the collection.",
    "const other = pm.response.json().sessions.filter(function (s) { return !s.current && !s.revoked; })[0];",
    "if (other) {",
    "  pm.collectionVariables.set('sessionId', other.id);",
    "} else {",
    "  console.info('no revocable session other than this one; revokeSession will 404');",
    "}",
  ],
  listAudit: [
    "pm.test('the audit trail reads most recent first', function () {",
    "  pm.response.to.have.status(200);",
    "  const at = pm.response.json().records.map(function (r) { return r.recordedAt; });",
    "  pm.expect(at).to.eql(at.slice().sort().reverse());",
    "});",
    "pm.test('ADR-0011 §2.7 — detail is passed through as recorded, not re-encoded', function () {",
    "  // A string, because it is the exact bytes that were digested. An object",
    "  // here would mean a reader sees this build's serializer's opinion of what",
    "  // was written rather than what was written.",
    "  pm.response.json().records.forEach(function (r) {",
    "    pm.expect(r.detail).to.be.a('string');",
    "  });",
    "});",
  ],
};

// ---------------------------------------------------------------------------

function* operations(doc) {
  for (const [path, item] of Object.entries(doc.paths ?? {})) {
    for (const method of HTTP_METHODS) {
      if (item?.[method]) yield [method.toUpperCase(), path, item[method]];
    }
  }
}

/** Resolve a local $ref one level. The contract uses no remote refs. */
function resolve(doc, node) {
  if (!node || typeof node !== "object" || typeof node.$ref !== "string" || !node.$ref.startsWith("#/")) return node;
  return node.$ref
    .slice(2)
    .split("/")
    .reduce((acc, key) => acc?.[key], doc);
}

/** The first example under a content map, or undefined. */
function firstExample(media) {
  if (!media) return undefined;
  if (media.example !== undefined) return media.example;
  const examples = Object.values(media.examples ?? {});
  return examples.length > 0 ? examples[0].value : undefined;
}

/** Replace the contract's literal example values with collection variables. */
function substitute(value) {
  if (typeof value === "string") return VALUE_VARIABLES[value] ?? value;
  if (Array.isArray(value)) return value.map(substitute);
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, substitute(v)]));
  }
  return value;
}

// `/v1/admin/users/{userId}` → `/v1/admin/users/{{userId}}`.
//
// The lookarounds make it idempotent, so an overlay author who writes the
// Postman form directly gets the same result as one who writes the contract's.
const templatePath = (path) => path.replace(/(?<!\{)\{(\w+)\}(?!\})/g, "{{$1}}");

function urlOf(path, query = []) {
  const templated = templatePath(path);
  const raw = "{{baseUrl}}" + templated + (query.length > 0 ? "?" + query.map((q) => `${q.key}=${q.value}`).join("&") : "");
  return {
    raw,
    host: ["{{baseUrl}}"],
    // Postman splits on "/", and `:commit` is part of a segment rather than a
    // path variable — which is why the contract's colon-suffixed operations
    // need no special handling here.
    path: templated.replace(/^\//, "").split("/"),
    ...(query.length > 0 ? { query } : {}),
  };
}

function jsonBody(value) {
  return {
    mode: "raw",
    raw: JSON.stringify(value, null, 2),
    options: { raw: { language: "json" } },
  };
}

/** The statuses an operation declares, for the generated status assertion. */
function successStatuses(operation) {
  return Object.keys(operation.responses ?? {}).filter((s) => /^2/.test(s));
}

function requestFrom(doc, method, path, operation) {
  const query = [];
  for (const parameter of operation.parameters ?? []) {
    const p = resolve(doc, parameter);
    if (p?.in === "query") {
      query.push({ key: p.name, value: `{{${p.name}}}`, description: p.description ?? "" });
    }
  }

  const header = [{ key: "Accept", value: "application/problem+json, application/json" }];
  const bodyExample = substitute(firstExample(resolve(doc, operation.requestBody)?.content?.["application/json"]));
  if (bodyExample !== undefined) {
    header.push({ key: "Content-Type", value: "application/json" });
  }

  const ok = successStatuses(operation);
  const test = [
    `pm.test('${operation.operationId} returns a status the contract declares', function () {`,
    `  pm.expect(pm.response.code).to.be.oneOf([${ok.join(", ")}]);`,
    `});`,
    ...(OPERATION_TESTS[operation.operationId] ?? []),
  ];

  return {
    name: `${method} ${path} — ${operation.summary}`,
    request: {
      method,
      header,
      ...(bodyExample === undefined ? {} : { body: jsonBody(bodyExample) }),
      url: urlOf(path, query),
      description: `${operation.summary}\n\n${(operation.description ?? "").trim()}`.trim(),
    },
    response: savedExamples(doc, method, path, operation),
    event: [{ listen: "test", script: { type: "text/javascript", exec: test } }],
  };
}

/**
 * Saved responses, so the collection documents what comes back.
 *
 * Only examples defined on the operation itself. The shared error responses
 * carry examples too, and saving those would attach the same Problem document
 * to all fourteen requests — which is noise, and the Problem shape is already
 * asserted after every request by the collection's test script.
 */
function savedExamples(doc, method, path, operation) {
  const saved = [];
  for (const [status, response] of Object.entries(operation.responses ?? {})) {
    if (typeof response?.$ref === "string") continue;
    for (const [mediaType, media] of Object.entries(response.content ?? {})) {
      for (const [name, example] of Object.entries(media.examples ?? {})) {
        saved.push({
          name: `${status} · ${example.summary ?? name}`,
          originalRequest: { method, url: urlOf(path) },
          status: String(status),
          code: Number(status),
          _postman_previewlanguage: "json",
          header: [{ key: "Content-Type", value: mediaType }],
          body: JSON.stringify(example.value, null, 2),
        });
      }
    }
  }
  return saved;
}

function overlayRequest(spec) {
  const header = [{ key: "Accept", value: "application/problem+json, application/json" }];
  for (const [key, value] of Object.entries(spec.headers ?? {})) {
    const existing = header.findIndex((h) => h.key.toLowerCase() === key.toLowerCase());
    if (existing === -1) header.push({ key, value });
    else header[existing] = { key, value };
  }

  let body;
  if (spec.rawBody !== undefined) {
    body = { mode: "raw", raw: spec.rawBody, options: { raw: { language: "json" } } };
    header.push({ key: "Content-Type", value: "application/json" });
  } else if (spec.body !== undefined) {
    body = jsonBody(spec.body);
    header.push({ key: "Content-Type", value: "application/json" });
  }

  const request = {
    method: spec.method,
    header,
    ...(body ? { body } : {}),
    url: urlOf(spec.path),
    description: spec.description ?? "",
  };

  // A request that must arrive without a session. Postman sends the cookie jar
  // automatically, so the only way to test the unauthenticated path is to say
  // so explicitly.
  const item = {
    name: spec.name,
    ...(spec.noSession ? { protocolProfileBehavior: { disabledSystemHeaders: {} } } : {}),
    request,
    response: [],
  };
  const exec = [];
  if (spec.noSession) {
    exec.push(
      "// This request must arrive with no session, so the cookie is cleared for",
      "// it and only it. Postman's jar is shared, so the next request signs in",
      "// again if it needs to.",
    );
  }
  if (spec.test) exec.push(...spec.test);
  if (exec.length > 0) {
    item.event = [{ listen: "test", script: { type: "text/javascript", exec } }];
  }
  if (spec.noSession) {
    item.event = item.event ?? [];
    item.event.unshift({
      listen: "prerequest",
      script: {
        type: "text/javascript",
        exec: [
          "const jar = pm.cookies.jar();",
          "jar.clear(pm.collectionVariables.get('baseUrl'), function () {});",
        ],
      },
    });
  }
  return item;
}

// ---------------------------------------------------------------------------

function build(doc, overlay, scripts, sourceName) {
  const byId = new Map();
  for (const [method, path, operation] of operations(doc)) {
    if (!operation.operationId) throw new Error(`${method} ${path} has no operationId`);
    byId.set(operation.operationId, { method, path, operation });
  }

  const placed = new Set();
  const items = [];
  for (const folder of FOLDERS) {
    const children = [];
    for (const literal of folder.literal ?? []) {
      children.push(
        overlayRequest({
          ...literal,
          headers: { Accept: "text/plain" },
        }),
      );
    }
    for (const id of folder.operations ?? []) {
      const found = byId.get(id);
      if (!found) {
        throw new Error(
          `the run plan places "${id}" but the contract has no such operation; ` +
            `remove it from FOLDERS in tools/postman.mjs, or restore it to the contract`,
        );
      }
      placed.add(id);
      children.push(requestFrom(doc, found.method, found.path, found.operation));
    }
    items.push({ name: folder.name, description: folder.description, item: children });
  }

  const unplaced = [...byId.keys()].filter((id) => !placed.has(id));
  if (unplaced.length > 0) {
    // Refusing rather than appending. A collection is run top to bottom, so
    // where a new endpoint belongs is a decision — appending it after sign-out
    // would produce a request that 401s and a reviewer who assumes the
    // generator knew what it was doing.
    throw new Error(
      `the contract declares ${unplaced.length} operation(s) the run plan does not place: ${unplaced.join(", ")}.\n` +
        `Add each to FOLDERS in tools/postman.mjs, at the point in the run where it belongs.`,
    );
  }

  for (const folder of overlay.folders ?? []) {
    items.push({
      name: folder.name,
      description: folder.description,
      item: (folder.requests ?? []).map(overlayRequest),
    });
  }

  // One ordering for both sources. Every folder name starts with a two-digit
  // number precisely so that the run plan reads the same in this file, in the
  // overlay and in Postman's sidebar — appending the overlay's folders after
  // the plan's would put `98 · Destructive` before `90 · Conformance`, which is
  // the wrong run and a confusing thing to look at.
  items.sort((a, b) => a.name.localeCompare(b.name));
  for (const [i, folder] of items.entries()) {
    const numbered = /^\d{2} /.test(folder.name);
    if (!numbered) {
      throw new Error(`folder "${folder.name}" does not start with a two-digit position; the run order is the folder order`);
    }
    if (i > 0 && items[i - 1].name.slice(0, 2) === folder.name.slice(0, 2)) {
      throw new Error(`folders "${items[i - 1].name}" and "${folder.name}" share position ${folder.name.slice(0, 2)}`);
    }
  }

  const contractDigest = "sha256:" + createHash("sha256").update(readFileSync(sourceName.path, "utf8")).digest("hex");

  return {
    info: {
      // Deterministic, so regenerating an unchanged contract produces an
      // unchanged file. A random id would make the hash check meaningless.
      _postman_id: uuidFrom(`zoikotax:${doc.info.title}:${doc.info.version}`),
      name: `${doc.info.title} API (v${doc.info.version.split(".")[0]})`,
      description: [
        `**Generated from \`contracts/openapi/${basename(sourceName.path)}\` — do not edit.**`,
        "",
        "Run `npm run postman` in `backend/contracts` after changing the contract or the overlay. A hand edit fails CI (ADR-0010 §2.1): a hand-maintained collection is a second, kinder statement of what the API is.",
        "",
        `Contract digest: \`${contractDigest}\``,
        "",
        "Import and run; there is no environment to select. `baseUrl` defaults to the local cell and every other value rides along as a collection variable.",
        "",
        "## Before you run it",
        "",
        "Bring the stack up (`docker compose up --build`), which bootstraps the `acme` tenant and its administrator. Then run the whole collection: `02 · Sign in` opens the session everything below depends on.",
        "",
        "Folder `90` is the one worth reading. Each request there sends something a well-behaved client would never send, and each asserts a control from the ADRs against an endpoint that exists today.",
        "",
        "Folder `99` records the determination surface, which is specified and not built. Its paths are listed in `pendingPaths`, so a 404 there reports as *not implemented yet* rather than as a wall of red.",
      ].join("\n"),
      schema: "https://schema.getpostman.com/json/collection/v2.1.0/collection.json",
    },
    item: items,
    event: [
      { listen: "prerequest", script: { type: "text/javascript", exec: scripts.pre } },
      { listen: "test", script: { type: "text/javascript", exec: scripts.test } },
    ],
    variable: variables(overlay, contractDigest),
  };
}

function variables(overlay, contractDigest) {
  return [
    { key: "baseUrl", value: "http://localhost:8080", type: "string" },

    // The local stack's bootstrap tenant and administrator (docker-compose.yml).
    { key: "tenantSlug", value: "acme", type: "string" },
    { key: "adminEmail", value: "admin@acme.example", type: "string" },
    { key: "adminPassword", value: "local-dev-only-change-me", type: "string" },
    { key: "newPassword", value: "a considerably longer passphrase than that one", type: "string" },

    // Set by the pre-request script and by the requests that create things.
    { key: "newUserEmail", value: "", type: "string" },
    { key: "userId", value: "", type: "string" },
    { key: "sessionId", value: "", type: "string" },
    { key: "role", value: "AUDITOR", type: "string" },
    { key: "limit", value: "50", type: "string" },
    { key: "bundleDigest", value: "", type: "string" },
    { key: "decisionTime", value: "", type: "string" },
    { key: "eventTime", value: "", type: "string" },

    // For the pending determination surface.
    { key: "tenantId", value: "ztn_01JBQ0S9C3X8Q1H6M2KX5R7F4A", type: "string" },
    { key: "reusedKey", value: "postman-idempotency-0001", type: "string" },

    {
      key: "pendingPaths",
      value: JSON.stringify(overlay.pendingPaths ?? []),
      type: "string",
    },
    { key: "contractDigest", value: contractDigest, type: "string" },
  ];
}

/** A deterministic UUID from a name, so regeneration is byte-stable. */
function uuidFrom(name) {
  const h = createHash("sha256").update(name).digest("hex");
  return [h.slice(0, 8), h.slice(8, 12), "5" + h.slice(13, 16), "8" + h.slice(17, 20), h.slice(20, 32)].join("-");
}

function main(argv) {
  const check = argv.includes("--check");
  const [source, target] = argv.filter((a) => !a.startsWith("--"));
  if (!source || !target) throw new Error("usage: postman.mjs [--check] <contract.yaml> <collection.json>");

  const doc = parseDocument(readFileSync(source, "utf8")).toJS();
  const here = dirname(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, "$1"));
  const overlayPath = join(here, "..", "postman", "overlay.json");
  const overlay = JSON.parse(readFileSync(overlayPath, "utf8"));
  const scripts = {
    pre: readFileSync(join(here, "..", "postman", "scripts", "collection.pre.js"), "utf8").replace(/\n$/, "").split("\n"),
    test: readFileSync(join(here, "..", "postman", "scripts", "collection.test.js"), "utf8").replace(/\n$/, "").split("\n"),
  };

  const rendered = JSON.stringify(build(doc, overlay, scripts, { path: source }), null, 2) + "\n";
  const digest = createHash("sha256").update(rendered).digest("hex");
  const hashLine = `${digest}  ${basename(target)}\n`;

  if (check) {
    let existing;
    try {
      existing = readFileSync(target, "utf8");
    } catch {
      throw new Error(`${target} does not exist. Run \`npm run postman\`.`);
    }
    if (existing !== rendered) {
      throw new Error(
        `${target} is not what this contract generates.\n` +
          `Either the contract or the overlay changed and the collection was not regenerated, or the collection was edited by hand.\n` +
          `Run \`npm run postman\` and commit the result.`,
      );
    }
    if (readFileSync(`${target}.sha256`, "utf8") !== hashLine) {
      throw new Error(`${target}.sha256 does not match the collection it names.`);
    }
    process.stdout.write(`collection is current: ${digest}\n`);
    return;
  }

  writeFileSync(target, rendered);
  writeFileSync(`${target}.sha256`, hashLine);
  const folders = build(doc, overlay, scripts, { path: source }).item;
  const requests = folders.reduce((n, f) => n + f.item.length, 0);
  process.stdout.write(`wrote ${target}: ${requests} requests in ${folders.length} folders (${digest})\n`);
}

try {
  main(process.argv.slice(2));
} catch (err) {
  process.stderr.write(`postman: ${err.message}\n`);
  process.exit(1);
}
