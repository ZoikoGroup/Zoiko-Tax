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
        headers: { Accept: "text/plain" },
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
        headers: { Accept: "text/plain" },
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
      "Runs in order: list, create, grant, disable, then read the sessions and the audit trail, then undo. `createUser` captures the new user's id into `userId`, so the requests after it act on something that exists.\n\nThen webhooks: `createWebhook` subscribes an endpoint and captures `webhookId`; the secret is shown once and asserted to be in the Standard Webhooks form. A fresh run has delivered nothing, so the delivery read and replay answer `404` for the example delivery id, and the run ends by pausing the webhook. The local stack sets `ZTAX_WEBHOOK_KEY_REF`; a cell without one answers `503`.\n\nLast, period seals: `sealPeriod` seals the example day and captures `sealId`, then the seal is read with its signed payload and verified. A cell with no seal keyring answers `503`, one with a keyring and no signer refuses the seal with `503`, and a period sealed already answers `409`.\n\nThen retention: `recordRetentionPolicy` records the next version of the example policy, so each run adds one. `placeLegalHold` places a hold on the example line and captures `holdId`; the hold is read, its scope widened and then released, which is final — the next run places a new one. Each hold read is written to the audit trail as privileged evidence access. `getDecisionRetention` asks for the verdict on `{{decisionId}}`, which this folder has not made, so it answers `404`.\n\nLast, legal authorization: the matrix the cell loaded is read — the local stack mounts the development one, a draft — then `grantAuthorization` records a filing mandate for the tenant's default legal entity and captures `authorizationId`. `checkAuthorization` asks whether September may be filed with the authority under it, and `revokeAuthorization` revokes it, after which the same check would be blocked. Then payee master data: `createBeneficiary` records an authority's payee details and captures `beneficiaryId`; the same administrator verifying them is refused with `MAKER_CHECKER` (ZTAX-REM-REQ-0014), and the version is suspended. Then a migration: `createMigrationProgram` creates a program for 2025 and captures `migrationProgramId`; a snapshot of two historical results is registered, a mapping that names only one of their legacy codes is recorded, and `executeMigrationRun` converts the snapshot AS_KNOWN — one record converts, the other raises an `UNMAPPED_CODE` exception, captured into `migrationExceptionId`, and the monetary reconciliation does not pass. The exception is accepted as a known residual and the cutover moves to `PRECHECK`.\n\nLast, the dashboard: `getDashboard` reads the Tax Command Center for the current month. It needs `OPERATOR`, `ANALYST` or `AUDITOR`; the local stack grants its bootstrap administrator `ANALYST` for exactly this, and a cell that does not answers `403`.",
    operations: [
      "listUsers",
      "createUser",
      "grantRole",
      "setUserStatus",
      "listSessions",
      "listAudit",
      "revokeRole",
      "revokeSession",
      "listWebhooks",
      "createWebhook",
      "getWebhook",
      "rotateWebhookSecret",
      "listWebhookDeliveries",
      "getWebhookDelivery",
      "replayWebhookDelivery",
      "setWebhookStatus",
      "listSeals",
      "sealPeriod",
      "getSeal",
      "verifySeal",
      "recordRetentionPolicy",
      "listRetentionPolicies",
      "placeLegalHold",
      "listLegalHolds",
      "getLegalHold",
      "changeLegalHoldScope",
      "releaseLegalHold",
      "getDecisionRetention",
      "getLegalMatrix",
      "grantAuthorization",
      "listAuthorizations",
      "getAuthorization",
      "checkAuthorization",
      "revokeAuthorization",
      "listBeneficiaries",
      "createBeneficiary",
      "verifyBeneficiary",
      "suspendBeneficiary",
      "listMigrationPrograms",
      "createMigrationProgram",
      "registerMigrationSnapshot",
      "createMigrationMapping",
      "executeMigrationRun",
      "getMigrationProgram",
      "getMigrationRun",
      "decideMigrationException",
      "moveMigrationCutover",
      "getDashboard",
    ],
  },
  {
    name: "06 · Determination — needs content and an OPERATOR",
    description:
      "Opens with two setup requests: `createUser` makes a fresh OPERATOR (ADMIN still holds the session from `02`), and signing in as it replaces the session for everything below, through `07` — `08 · Sign out` ends whichever session is active, so it does not need to know which.\n\nQuote, commit, read and replay one line against the cell's active pack. `commitTransaction` captures the decision into `decisionId`, and the replay after it must say `MATCH` — the W2 exit gate in four requests. `listObligations` captures the period's return into `obligationId`, and `transitionObligation` marks it `READY`. `createCompliancePackage` instantiates the return's compliance package and captures `packageId`, and `prepareCompliancePackage` prepares version 1; the same OPERATOR reviewing it is refused with `SEGREGATION_OF_DUTIES` (ZTAX-COMP-REQ-0041), so approval, submission, acknowledgement and resolution answer `409`, and the package is cancelled. `createRemittance` is refused with `PAYMENT_BLOCKED` — the return was never accepted by an authority, so nothing is payable (ZTAX-REM-REQ-0005) — and the payment reads and actions answer `404` for the example payment. `createComparator` records an incumbent engine and captures `comparatorId`; `executeShadowRun` compares its example result with the folder's committed decision and captures `shadowRunId` and, when the run found a material difference, `discrepancyId`, which `moveDiscrepancy` triages. `adjustTransaction` then corrects the decision under the bundle that made it (ZTAX-DET-REQ-0030), which moves the return back to `OPEN`, and becomes `decisionId`. Before the adjustment, `issueDocument` invoices the committed decision and captures `documentId`, and `correctDocument` voids it, which `getDocumentLineage` shows as a chain of two. `refundTransaction` refunds that correction's VAT and captures `refundId`; `reportRefund` reports the provider paid it, which posts the `REFUND` journal (ZTAX-FIN-REQ-0059). `submitBatch` queues two more lines as one job and captures `jobId`; `getJob` reads its progress, which a worker advances asynchronously. Last, the subledger period `{{period}}` — `2000-01` by default, a month nothing posts into, so a run never closes one that matters — is hard-closed, a reopen is requested, and the requester's own approval is refused (ZTAX-FIN-REQ-0091). `runReconciliation` reconciles September and captures `runId`; the resolution of the example item answers `404` in a fresh run, since its id is the example's.\n\nSkipped unless `runDetermination` is `\"true\"`, because it needs two things the local stack does not give the bootstrap administrator: a content bundle (`ZTAX_CONTENT_DIR`, see `make content`) and the `OPERATOR` role, which the two setup requests above provide. The request bodies are the worked pack's inputs.",
    literal: [
      {
        name: "Provision an OPERATOR for determination",
        method: "POST",
        path: "/v1/admin/users",
        body: {
          email: "{{operatorEmail}}",
          displayName: "Determination Operator",
          roles: ["OPERATOR"],
          password: "{{operatorPassword}}",
        },
        description:
          "Still the ADMIN session from `02`. A password at creation makes the user `ACTIVE` immediately — no separate invite-acceptance step — so the next request can sign in as it straight away.\n\n`{{operatorEmail}}` is fresh every run (collection.pre.js), so this never collides with `04`'s own `createUser` or with a second run against the same cell.",
        test: [
          "pm.test('the OPERATOR is created', function () {",
          "  pm.response.to.have.status(201);",
          "});",
        ],
      },
      {
        name: "Sign in as the OPERATOR",
        method: "POST",
        path: "/v1/auth/sign-in",
        noSession: true,
        body: {
          tenant: "{{tenantSlug}}",
          email: "{{operatorEmail}}",
          password: "{{operatorPassword}}",
        },
        description:
          "Replaces the ADMIN session with the OPERATOR's. `noSession` clears the cookie jar first, so this arrives unauthenticated like any other sign-in and the response's cookie is the only session after it.",
        test: [
          "pm.test('the operator session opens', function () {",
          "  pm.response.to.have.status(200);",
          "});",
        ],
      },
    ],
    operations: [
      "createQuote", "commitTransaction", "getDecision", "replayDecision", "listDecisionJournals",
      "getSubledgerBalances", "listObligations", "getObligation", "transitionObligation",
      "createCompliancePackage", "getCompliancePackage", "listCompliancePackages", "prepareCompliancePackage",
      "reviewCompliancePackage", "approveCompliancePackage", "submitCompliancePackage",
      "acknowledgeCompliancePackage", "resolveCompliancePackage", "cancelCompliancePackage",
      "listRemittances", "createRemittance", "getRemittance", "approveRemittance", "submitRemittance",
      "recordRemittanceStatus", "resolveRemittance", "returnRemittance", "cancelRemittance",
      "createComparator", "listComparators", "executeShadowRun", "listShadowRuns", "getShadowRun", "moveDiscrepancy",
      "issueDocument", "getDocument", "correctDocument", "getDocumentLineage",
      "adjustTransaction", "refundTransaction", "getRefund", "reportRefund", "proposeClassification",
      "submitBatch", "getJob", "getDecisionInclusion",
      "getSubledgerPeriod", "transitionSubledgerPeriod", "requestPeriodReopen", "approvePeriodReopen",
      "runReconciliation", "getReconciliation", "resolveReconciliationItem",
    ],
    prerequest: [
      "if (pm.variables.get('runDetermination') !== 'true') {",
      "  console.info('skipped: set runDetermination=true against a cell with content, signed in as an OPERATOR');",
      "  pm.execution.skipRequest();",
      "}",
    ],
  },
  {
    name: "08 · Sign out",
    description:
      "After everything that needs the session this ends — including the conformance folders `05` and `07`, which sit before it for that reason.",
    operations: ["signOut"],
  },
  {
    name: "98 · Destructive — run deliberately, not as part of a pass",
    description:
      "`changePassword` revokes every session for the subject, including the calling one — a password change is what you do when a credential is believed compromised, and leaving other sessions alive would defeat it.\n\nIt also leaves the bootstrap administrator on `{{newPassword}}`, which the collection's `adminPassword` no longer names. Run it when you mean to, then update `adminPassword` or re-bootstrap the tenant.",
    operations: ["changePassword"],
    // Skipped unless asked for. It runs after sign-out, so in a full pass it
    // could only ever 401 — a red row that says nothing about the API — and a
    // pass where it succeeded would leave the next pass unable to sign in.
    prerequest: [
      "if (pm.variables.get('runDestructive') !== 'true') {",
      "  console.info('skipped: set runDestructive=true and run this folder with 02 · Sign in');",
      "  pm.execution.skipRequest();",
      "}",
    ],
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
  // The adjust example's supersedes: the decision the folder just committed.
  "01920a4b-7c3e-7d21-9f40-3c1a2b4d5e6f": "{{decisionId}}",
  // The obligation examples' id: the return the folder's commit assessed into.
  "01920a4d-1b2c-7d3e-8f40-5a6b7c8d9e01": "{{obligationId}}",
  // The refund examples' id: the refund the folder just requested.
  "01920a50-2a3b-7c4d-8e5f-6a7b8c9d0e1f": "{{refundId}}",
  // The shadow examples' ids: the comparator and run folder 06 just made.
  "01920b40-1a2b-7c3d-8e4f-5a6b7c8d9e01": "{{comparatorId}}",
  "01920b50-1a2b-7c3d-8e4f-5a6b7c8d9e01": "{{shadowRunId}}",
  "01920b60-1a2b-7c3d-8e4f-5a6b7c8d9e01": "{{discrepancyId}}",
  // The migration examples' ids: what folder 04 just made.
  "01920b70-1a2b-7c3d-8e4f-5a6b7c8d9e01": "{{migrationProgramId}}",
  "01920b71-1a2b-7c3d-8e4f-5a6b7c8d9e01": "{{migrationSnapshotId}}",
  "01920b72-1a2b-7c3d-8e4f-5a6b7c8d9e01": "{{migrationMappingId}}",
  "01920b73-1a2b-7c3d-8e4f-5a6b7c8d9e01": "{{migrationRunId}}",
  "01920b74-1a2b-7c3d-8e4f-5a6b7c8d9e01": "{{migrationExceptionId}}",
  // The beneficiary examples' id: the payee version folder 04 just recorded.
  "01920b20-1a2b-7c3d-8e4f-5a6b7c8d9e01": "{{beneficiaryId}}",
  // The compliance package examples' id: the package folder 06 just made.
  "01920b10-1a2b-7c3d-8e4f-5a6b7c8d9e01": "{{packageId}}",
  // The seal examples' id: the seal folder 04 just made.
  "01920a80-1a2b-7c3d-8e4f-5a6b7c8d9e0f": "{{sealId}}",
  // The document examples' id: the invoice the folder just issued.
  "01920a90-1a2b-7c3d-8e4f-5a6b7c8d9e0f": "{{documentId}}",
  // The reconciliation examples' run id: the run the folder just made.
  "01920ab0-1a2b-7c3d-8e4f-5a6b7c8d9e0f": "{{runId}}",
  // The job example's id: the batch the folder just queued.
  "01920a70-1a2b-7c3d-8e4f-5a6b7c8d9e0f": "{{jobId}}",
  // The webhook examples' id: the subscription folder 04 just created.
  "01920a60-1a2b-7c3d-8e4f-5a6b7c8d9e0f": "{{webhookId}}",
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
  createQuote: [
    "pm.test('ADR-0004 §2.7 — a quote is never authoritative', function () {",
    "  pm.response.to.have.status(200);",
    "  pm.expect(pm.response.json().authoritative).to.eql(false);",
    "});",
    "pm.test('ADR-0010 §2.9 — every emitted amount is a decimal string', function () {",
    "  Object.values(pm.response.json().emitted).forEach(function (v) {",
    "    if (v.type === 'MONEY') { pm.expect(v.amount).to.be.a('string'); }",
    "  });",
    "});",
  ],
  commitTransaction: [
    "pm.test('the decision is recorded, and advisory before A4', function () {",
    "  pm.response.to.have.status(201);",
    "  pm.expect(pm.response.json().authoritative).to.eql(false);",
    "});",
    "pm.collectionVariables.set('decisionId', pm.response.json().id);",
  ],
  listDecisionJournals: [
    "pm.test('ZTAX-FIN-REQ-0077 — the commit posted a balanced journal', function () {",
    "  pm.response.to.have.status(200);",
    "  pm.expect(pm.response.json().journals.length).to.be.at.least(1);",
    "});",
  ],
  getSubledgerBalances: [
    "pm.test('control balances are reported, debits and credits apart', function () {",
    "  pm.response.to.have.status(200);",
    "  pm.expect(pm.response.json().balances).to.be.an('array');",
    "});",
  ],
  listObligations: [
    "pm.test('ZTAX-OBL-001 §4 — the commit assessed into the return for its period', function () {",
    "  pm.response.to.have.status(200);",
    "  pm.expect(pm.response.json().obligations.length).to.be.at.least(1);",
    "});",
    "pm.collectionVariables.set('obligationId', pm.response.json().obligations[0].id);",
  ],
  getObligation: [
    "pm.test('the obligation names its authority, definition and content', function () {",
    "  pm.response.to.have.status(200);",
    "  const o = pm.response.json();",
    "  pm.expect(o.definition.id).to.be.a('string');",
    "  pm.expect(o.content.bundleDigest).to.match(/^zt1:[0-9a-f]{64}$/);",
    "});",
  ],
  transitionObligation: [
    "pm.test('ADR-0003 §2.2 — a move is a new row superseding the old one', function () {",
    "  pm.response.to.have.status(200);",
    "  pm.expect(pm.response.json().status).to.eql('READY');",
    "  pm.expect(pm.response.json().supersedes).to.eql(pm.collectionVariables.get('obligationId'));",
    "});",
  ],
  createCompliancePackage: [
    "pm.test('ZTAX-COMP-REQ-0004 — one package per obligation: created, or the one already made', function () {",
    "  pm.expect([200, 201]).to.include(pm.response.code);",
    "  pm.expect(pm.response.json().history[0].kind).to.eql('CREATED');",
    "});",
    "pm.collectionVariables.set('packageId', pm.response.json().id);",
    "pm.collectionVariables.set('packageFresh', pm.response.code === 201 ? 'true' : 'false');",
  ],
  prepareCompliancePackage: [
    "if (pm.collectionVariables.get('packageFresh') === 'true') {",
    "  pm.test('ZTAX-COMP-REQ-0035 — a preparation is an immutable version with its payload hash', function () {",
    "    pm.response.to.have.status(200);",
    "    const p = pm.response.json();",
    "    pm.expect(p.status).to.eql('IN_REVIEW');",
    "    pm.expect(p.history[p.history.length - 1].payloadHash).to.match(/^zt1:[0-9a-f]{64}$/);",
    "  });",
    "}",
  ],
  reviewCompliancePackage: [
    "if (pm.collectionVariables.get('packageFresh') === 'true') {",
    "  pm.test('ZTAX-COMP-REQ-0041 — the preparer cannot review their own version', function () {",
    "    pm.response.to.have.status(403);",
    "    pm.expect(pm.response.json().ztx_reason_code).to.eql('SEGREGATION_OF_DUTIES');",
    "  });",
    "}",
  ],
  submitCompliancePackage: [
    "pm.test('ZTAX-COMP-REQ-0044 — nothing unapproved is submitted', function () {",
    "  pm.response.to.have.status(409);",
    "});",
  ],
  cancelCompliancePackage: [
    "if (pm.collectionVariables.get('packageFresh') === 'true') {",
    "  pm.test('a package not yet submitted is cancelled with its reason, and kept', function () {",
    "    pm.response.to.have.status(200);",
    "    pm.expect(pm.response.json().status).to.eql('CANCELLED');",
    "  });",
    "}",
  ],
  createBeneficiary: [
    "pm.test('ZTAX-REM-REQ-0012 — a payee version is recorded unverified', function () {",
    "  pm.response.to.have.status(201);",
    "  pm.expect(pm.response.json().state).to.eql('UNVERIFIED');",
    "});",
    "pm.collectionVariables.set('beneficiaryId', pm.response.json().id);",
  ],
  verifyBeneficiary: [
    "pm.test('ZTAX-REM-REQ-0014 — the author of payee details cannot verify them', function () {",
    "  pm.response.to.have.status(403);",
    "  pm.expect(pm.response.json().ztx_reason_code).to.eql('MAKER_CHECKER');",
    "});",
  ],
  createRemittance: [
    "pm.test('ZTAX-REM-REQ-0005 — only an accepted return is paid', function () {",
    "  pm.response.to.have.status(409);",
    "  pm.expect(pm.response.json().ztx_reason_code).to.eql('PAYMENT_BLOCKED');",
    "});",
  ],
  createComparator: [
    "pm.test('ZTAX-SHD-001 §1 — a commercial engine supports a match claim, never verified correctness', function () {",
    "  pm.response.to.have.status(201);",
    "  pm.expect(pm.response.json().claim).to.eql('MATCHES_COMPARATOR');",
    "  pm.expect(pm.response.json().version).to.eql('UNKNOWN');",
    "});",
    "pm.collectionVariables.set('comparatorId', pm.response.json().id);",
  ],
  executeShadowRun: [
    "pm.test('ZTAX-SHD-001 §6 — a run records its input hash and its metrics', function () {",
    "  pm.response.to.have.status(201);",
    "  const r = pm.response.json();",
    "  pm.expect(r.inputHash).to.match(/^zt1:[0-9a-f]{64}$/);",
    "  pm.expect(r.metrics.cases).to.be.at.least(1);",
    "  pm.expect(r.cutoverBlockers).to.be.an('array').that.is.not.empty;",
    "});",
    "pm.collectionVariables.set('shadowRunId', pm.response.json().id);",
    "const d = pm.response.json().discrepancies || [];",
    "if (d.length > 0) { pm.collectionVariables.set('discrepancyId', d[0].id); }",
  ],
  createMigrationProgram: [
    "pm.response.to.have.status(201);",
    "pm.collectionVariables.set('migrationProgramId', pm.response.json().id);",
  ],
  registerMigrationSnapshot: [
    "pm.test('ZTAX-MIG-001 §7 — a snapshot is hashed and counted as received', function () {",
    "  pm.response.to.have.status(201);",
    "  pm.expect(pm.response.json().hash).to.match(/^zt1:[0-9a-f]{64}$/);",
    "  pm.expect(pm.response.json().recordCount).to.eql(2);",
    "});",
    "pm.collectionVariables.set('migrationSnapshotId', pm.response.json().id);",
  ],
  createMigrationMapping: [
    "pm.response.to.have.status(201);",
    "pm.collectionVariables.set('migrationMappingId', pm.response.json().id);",
  ],
  executeMigrationRun: [
    "pm.test('ZTAX-MIG-001 §15, §16 — an unmapped code is an exception, and the totals do not reconcile', function () {",
    "  pm.response.to.have.status(201);",
    "  const r = pm.response.json();",
    "  pm.expect(r.reconciliation.convertedCount).to.eql(1);",
    "  pm.expect(r.reconciliation.passed).to.eql(false);",
    "  pm.expect(r.exceptions[0].kind).to.eql('UNMAPPED_CODE');",
    "});",
    "pm.collectionVariables.set('migrationRunId', pm.response.json().id);",
    "pm.collectionVariables.set('migrationExceptionId', pm.response.json().exceptions[0].id);",
  ],
  proposeClassification: [
    "pm.test('ADR-0006 — advisory, or not available here; never authoritative', function () {",
    "  pm.expect([200, 422]).to.include(pm.response.code);",
    "  if (pm.response.code === 200) { pm.expect(pm.response.json().authoritative).to.eql(false); }",
    "  else { pm.expect(pm.response.json().ztx_reason_code).to.eql('AI_GATEWAY_NOT_CONFIGURED'); }",
    "});",
  ],
  adjustTransaction: [
    "pm.test('ZTAX-DET-REQ-0030 — the correction is recorded against the original decision', function () {",
    "  pm.response.to.have.status(201);",
    "  pm.expect(pm.response.json().authoritative).to.eql(false);",
    "  pm.expect(pm.response.json().id).to.not.eql(pm.collectionVariables.get('decisionId'));",
    "});",
    "// The original is superseded now; a refund is made against the current version.",
    "pm.collectionVariables.set('decisionId', pm.response.json().id);",
  ],
  listSeals: [
    "// A seal made by an earlier run makes this run's sealPeriod a 409, which",
    "// captures nothing; the latest seal stands in, so the reads after it",
    "// still have one to read and verify.",
    "if (pm.response.code === 200 && pm.response.json().seals.length > 0) {",
    "  pm.collectionVariables.set('sealId', pm.response.json().seals[0].id);",
    "}",
  ],
  sealPeriod: [
    "pm.test('ADR-0011 §2.4 — sealed, or refused for a reason the cell states', function () {",
    "  pm.expect([201, 409, 503]).to.include(pm.response.code);",
    "});",
    "if (pm.response.code === 201) { pm.collectionVariables.set('sealId', pm.response.json().id); }",
  ],
  getSeal: [
    "pm.test('the seal carries the bytes its signature covers', function () {",
    "  pm.expect([200, 404, 503]).to.include(pm.response.code);",
    "  if (pm.response.code === 200) { pm.expect(pm.response.json().signedPayload).to.be.a('string'); }",
    "});",
  ],
  verifySeal: [
    "pm.test('a seal this cell made verifies', function () {",
    "  pm.expect([200, 404, 503]).to.include(pm.response.code);",
    "  if (pm.response.code === 200) { pm.expect(pm.response.json().verdict).to.eql('VALID'); }",
    "});",
  ],
  getDecisionInclusion: [
    "pm.test('a sealed decision proves its inclusion; an unsealed one is not found', function () {",
    "  pm.expect([200, 404, 503]).to.include(pm.response.code);",
    "});",
  ],
  issueDocument: [
    "pm.test('ZTAX-FIN-REQ-0011 — the invoice presents exactly what was decided', function () {",
    "  pm.response.to.have.status(201);",
    "  pm.expect(pm.response.json().status).to.eql('COMMITTED');",
    "});",
    "pm.collectionVariables.set('documentId', pm.response.json().id);",
  ],
  correctDocument: [
    "pm.test('ZTAX-FIN-REQ-0004 — the void is a linked successor', function () {",
    "  pm.response.to.have.status(201);",
    "  pm.expect(pm.response.json().predecessors).to.include(pm.collectionVariables.get('documentId'));",
    "});",
  ],
  getDocumentLineage: [
    "pm.test('one root, the invoice first', function () {",
    "  pm.response.to.have.status(200);",
    "  pm.expect(pm.response.json().documents[0].status).to.eql('VOIDED');",
    "});",
  ],
  transitionSubledgerPeriod: [
    "pm.test('ZTAX-FIN-REQ-0089 — closed with a manifest, or already closed by an earlier run', function () {",
    "  pm.expect([200, 409]).to.include(pm.response.code);",
    "  if (pm.response.code === 200) { pm.expect(pm.response.json().manifest.digest).to.match(/^zt1:/); }",
    "});",
  ],
  requestPeriodReopen: [
    "pm.test('a reopen is requested and nothing moves yet', function () {",
    "  pm.expect([201, 409]).to.include(pm.response.code);",
    "});",
    "if (pm.response.code === 201) { pm.collectionVariables.set('requestId', pm.response.json().id); }",
  ],
  approvePeriodReopen: [
    "pm.test('ZTAX-FIN-REQ-0091 — the requester cannot approve their own reopen', function () {",
    "  pm.expect([403, 404]).to.include(pm.response.code);",
    "});",
  ],
  runReconciliation: [
    "pm.test('ZTAX-FIN-REQ-0077 — every stage not compared is named', function () {",
    "  pm.response.to.have.status(201);",
    "  pm.expect(pm.response.json().unavailable).to.include('R2_DOCUMENT_TO_COLLECTION');",
    "});",
    "pm.collectionVariables.set('runId', pm.response.json().id);",
  ],
  resolveReconciliationItem: [
    "pm.test('a resolution needs an item of the run', function () {",
    "  pm.expect([200, 404, 409]).to.include(pm.response.code);",
    "});",
  ],
  submitBatch: [
    "pm.test('the batch is queued as one job, every item pending', function () {",
    "  pm.response.to.have.status(202);",
    "  pm.expect(pm.response.json().pending).to.eql(pm.response.json().itemCount);",
    "});",
    "pm.collectionVariables.set('jobId', pm.response.json().id);",
  ],
  getJob: [
    "pm.test('the job reports each item, and a finished one its decision or its reason', function () {",
    "  pm.response.to.have.status(200);",
    "  pm.response.json().items.forEach(function (it) {",
    "    if (it.status === 'SUCCEEDED') { pm.expect(it.decisionId).to.be.a('string'); }",
    "    if (it.status === 'FAILED') { pm.expect(it.reasonCode).to.be.a('string'); }",
    "  });",
    "});",
  ],
  placeLegalHold: [
    "pm.test('ZTAX-EVID-REQ-0023 — a hold is placed ACTIVE with its first history entry', function () {",
    "  pm.response.to.have.status(201);",
    "  pm.expect(pm.response.json().status).to.eql('ACTIVE');",
    "  pm.expect(pm.response.json().history[0].kind).to.eql('PLACED');",
    "});",
    "pm.collectionVariables.set('holdId', pm.response.json().id);",
  ],
  changeLegalHoldScope: [
    "pm.test('ZTAX-EVID-REQ-0053 — a scope change is a new history entry, the old one kept', function () {",
    "  pm.response.to.have.status(200);",
    "  pm.expect(pm.response.json().history.map(e => e.kind)).to.eql(['PLACED', 'SCOPE_CHANGED']);",
    "});",
  ],
  releaseLegalHold: [
    "pm.test('a release is final', function () {",
    "  pm.response.to.have.status(200);",
    "  pm.expect(pm.response.json().status).to.eql('RELEASED');",
    "});",
  ],
  getLegalMatrix: [
    "pm.test('ZTAX-LEG-REQ-0102 — every rule cites the opinion it rests on', function () {",
    "  pm.response.to.have.status(200);",
    "  pm.response.json().rules.forEach(r => pm.expect(r.opinionRef).to.not.be.empty);",
    "});",
  ],
  grantAuthorization: [
    "pm.test('ZTAX-LEG-REQ-0014 — the grant is recorded ACTIVE with its history', function () {",
    "  pm.response.to.have.status(201);",
    "  pm.expect(pm.response.json().status).to.eql('ACTIVE');",
    "  pm.expect(pm.response.json().history[0].kind).to.eql('GRANTED');",
    "});",
    "pm.collectionVariables.set('authorizationId', pm.response.json().id);",
  ],
  checkAuthorization: [
    "pm.test('ZTAX-LEG-REQ-0004 — filing resolves under the mandate rule, against a named matrix', function () {",
    "  pm.response.to.have.status(200);",
    "  pm.expect(pm.response.json().status).to.eql('DIRECT_WITH_AUTH');",
    "  pm.expect(pm.response.json().matrixDigest).to.match(/^zt1:/);",
    "});",
  ],
  revokeAuthorization: [
    "pm.test('ZTAX-LEG-REQ-0016 — a revoked grant is no longer in force', function () {",
    "  pm.response.to.have.status(200);",
    "  pm.expect(pm.response.json().status).to.eql('REVOKED');",
    "  pm.expect(pm.response.json().inForce).to.eql(false);",
    "});",
  ],
  createWebhook: [
    "pm.test('the webhook is created ACTIVE, and its secret is shown once in Standard Webhooks form', function () {",
    "  pm.response.to.have.status(201);",
    "  pm.expect(pm.response.json().webhook.status).to.eql('ACTIVE');",
    "  pm.expect(pm.response.json().secret.secret).to.match(/^whsec_/);",
    "});",
    "pm.collectionVariables.set('webhookId', pm.response.json().webhook.id);",
  ],
  getWebhook: [
    "pm.test('a read never returns the signing secret', function () {",
    "  pm.response.to.have.status(200);",
    "  pm.expect(pm.response.text()).to.not.include('whsec_');",
    "});",
  ],
  rotateWebhookSecret: [
    "pm.test('rotation issues version 2, and the previous secret retires later', function () {",
    "  pm.response.to.have.status(200);",
    "  pm.expect(pm.response.json().secret.version).to.eql(2);",
    "});",
  ],
  getWebhookDelivery: [
    "pm.test('nothing has been delivered in a fresh run', function () {",
    "  pm.expect([200, 404]).to.include(pm.response.code);",
    "});",
  ],
  replayWebhookDelivery: [
    "pm.test('a replay needs a delivery to replay', function () {",
    "  pm.expect([202, 404]).to.include(pm.response.code);",
    "});",
  ],
  setWebhookStatus: [
    "pm.test('the webhook is paused', function () {",
    "  pm.response.to.have.status(200);",
    "  pm.expect(pm.response.json().status).to.eql('PAUSED');",
    "});",
  ],
  refundTransaction: [
    "pm.test('ZTAX-FIN-REQ-0015 — a refund is its own record, requested and not yet posted', function () {",
    "  pm.response.to.have.status(201);",
    "  pm.expect(pm.response.json().status).to.eql('REQUESTED');",
    "  pm.expect(pm.response.json().decisionId).to.eql(pm.collectionVariables.get('decisionId'));",
    "});",
    "pm.collectionVariables.set('refundId', pm.response.json().id);",
  ],
  getRefund: [
    "pm.test('the refund carries its history', function () {",
    "  pm.response.to.have.status(200);",
    "  pm.expect(pm.response.json().history[0].status).to.eql('REQUESTED');",
    "});",
  ],
  reportRefund: [
    "pm.test('ZTAX-FIN-REQ-0059 — the provider report moves the refund, never the decision', function () {",
    "  pm.response.to.have.status(200);",
    "  pm.expect(pm.response.json().status).to.eql('COMPLETED');",
    "});",
  ],
  replayDecision: [
    "pm.test('ADR-0011 §2.8 — the decision just committed replays exactly', function () {",
    "  pm.response.to.have.status(200);",
    "  pm.expect(pm.response.json().verdict).to.eql('MATCH');",
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
  getDashboard: [
    "pm.test('the dashboard names its period and never sums across currencies', function () {",
    "  pm.response.to.have.status(200);",
    "  const d = pm.response.json();",
    "  pm.expect(d.period).to.match(/^[0-9]{4}-(0[1-9]|1[0-2])$/);",
    "  const seen = d.determinations.tax.map(function (m) { return m.currency; });",
    "  pm.expect(seen.length).to.eql(new Set(seen).size);",
    "});",
    "pm.test('the trend is eight periods, oldest first, each indexed 0 to 100', function () {",
    "  const pts = pm.response.json().trend.points;",
    "  pm.expect(pts).to.have.lengthOf(8);",
    "  pts.forEach(function (p) { pm.expect(p.liabilityIndex).to.be.within(0, 100); });",
    "});",
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

// Non-2xx statuses that are a correct answer from a development cell, by
// operation. Kept short and explained: proposeClassification answers 422
// AI_GATEWAY_NOT_CONFIGURED from any cell whose Gateway has no approved
// provider, which is every local stack.
const ACCEPTED_OUTCOMES = {
  proposeClassification: ["422"],
  // A fresh stack has one session, the run's own, which listSessions does not
  // capture; there is then no other session to revoke.
  revokeSession: ["404"],
  // A fresh run has delivered nothing, so the example delivery is not found.
  getWebhookDelivery: ["404"],
  replayWebhookDelivery: ["404"],
  // The local stack configures no seal keyring: the seal surface says so
  // with 503 rather than pretending, and a seal made by an earlier run
  // makes the next run's seal of the same period a conflict.
  listSeals: ["503"],
  sealPeriod: ["409", "503"],
  getSeal: ["404", "503"],
  verifySeal: ["404", "503"],
  getDecisionInclusion: ["404", "503"],
  // {{period}} is closed by the first run; later runs find it closed.
  transitionSubledgerPeriod: ["409"],
  requestPeriodReopen: ["409"],
  // The requester cannot approve their own reopen.
  approvePeriodReopen: ["403", "404"],
  resolveReconciliationItem: ["404", "409"],
  // The administration folder commits nothing, so {{decisionId}} names no
  // decision there.
  getDecisionRetention: ["404"],
  // Folder 06 runs as one OPERATOR, so the package it prepares cannot be
  // reviewed by the same person (ZTAX-COMP-REQ-0041), and everything after
  // review is refused as out of order. Later runs find the package their
  // first run cancelled.
  prepareCompliancePackage: ["409"],
  reviewCompliancePackage: ["403", "409"],
  approveCompliancePackage: ["409"],
  submitCompliancePackage: ["409"],
  acknowledgeCompliancePackage: ["409"],
  resolveCompliancePackage: ["409"],
  cancelCompliancePackage: ["409"],
  // The administrator who records a payee version cannot verify it
  // (ZTAX-REM-REQ-0014). Folder 06's return is never accepted by an
  // authority, so there is nothing to pay, and no payment to act on.
  verifyBeneficiary: ["403"],
  createRemittance: ["409"],
  getRemittance: ["404"],
  approveRemittance: ["404"],
  submitRemittance: ["404"],
  recordRemittanceStatus: ["404"],
  resolveRemittance: ["404"],
  returnRemittance: ["404"],
  cancelRemittance: ["404"],
  moveDiscrepancy: ["404"],
};

/** The statuses an operation declares, for the generated status assertion. */
function successStatuses(operation) {
  const ok = Object.keys(operation.responses ?? {}).filter((s) => /^2/.test(s));
  return [...ok, ...(ACCEPTED_OUTCOMES[operation.operationId] ?? [])];
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
  for (const parameter of operation.parameters ?? []) {
    const p = resolve(doc, parameter);
    if (p?.in !== "header") continue;
    // A fresh key per request. The collection is run repeatedly against the
    // same cell, and a fixed key would replay the first run's decision on every
    // run after it — which is correct behaviour and a useless test.
    const value = p.name.toLowerCase() === "idempotency-key" ? "{{$guid}}" : `{{${p.name}}}`;
    header.push({ key: p.name, value, description: (p.description ?? "").trim() });
  }
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
      children.push(overlayRequest(literal));
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
    const item = { name: folder.name, description: folder.description, item: children };
    if (folder.prerequest) {
      item.event = [{ listen: "prerequest", script: { type: "text/javascript", exec: folder.prerequest } }];
    }
    items.push(item);
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
    const item = {
      name: folder.name,
      description: folder.description,
      item: (folder.requests ?? []).map(overlayRequest),
    };
    if (folder.prerequest) {
      item.event = [{ listen: "prerequest", script: { type: "text/javascript", exec: folder.prerequest } }];
    }
    items.push(item);
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
        "Run `npm run postman` in `contracts/` after changing the contract or the overlay. A hand edit fails CI (ADR-0010 §2.1): a hand-maintained collection is a second, kinder statement of what the API is.",
        "",
        `Contract digest: \`${contractDigest}\``,
        "",
        "Import and run; there is no environment to select. `baseUrl` defaults to the local cell and every other value rides along as a collection variable.",
        "",
        "## Before you run it",
        "",
        "Bring the stack up (`docker compose up --build`), which bootstraps the `acme` tenant and its administrator. Then run the whole collection: `02 · Sign in` opens the session everything below depends on.",
        "",
        "Folders `05`, `07` and `90` are the ones worth reading. Each request there sends something a well-behaved client would never send, and each asserts a control from the ADRs against an endpoint that exists today.",
        "",
        "Folders `06` and `07` exercise the determination surface and are skipped unless `runDetermination` is `true`: they need a cell with a content bundle and a session holding `OPERATOR`, which the local stack's bootstrap administrator does not have. `98 · Destructive` is skipped unless `runDestructive` is `true`.",
      ].join("\n"),
      schema: "https://schema.getpostman.com/json/collection/v2.1.0/collection.json",
    },
    item: items,
    event: [
      { listen: "prerequest", script: { type: "text/javascript", exec: scripts.pre } },
      { listen: "test", script: { type: "text/javascript", exec: scripts.test } },
    ],
    variable: variables(overlay, contractDigest, sourceName.local),
  };
}

function variables(overlay, contractDigest, local) {
  return [
    { key: "baseUrl", value: "http://localhost:8080", type: "string" },

    // The local stack's bootstrap tenant and administrator, read from
    // .env.example (the stack's own source for them) rather than copied. A copy drifted once: the
    // collection carried a password the stack never set, so a plain `newman run`
    // failed at sign-in and every request after it was a 401.
    { key: "tenantSlug", value: local.tenant, type: "string" },
    { key: "adminEmail", value: local.email, type: "string" },
    { key: "adminPassword", value: local.password, type: "string" },
    { key: "newPassword", value: "a considerably longer passphrase than that one", type: "string" },
    // `06`'s own OPERATOR, created and signed in as only when runDetermination
    // is "true". Not a secret the stack ever holds outside this run.
    { key: "operatorPassword", value: "local-dev-only-operator-password", type: "string" },
    // `98 · Destructive` runs only when this is "true".
    { key: "runDestructive", value: "false", type: "string" },
    // `06` and `07`, the determination folders, run only when this is "true".
    { key: "runDetermination", value: "false", type: "string" },

    // Set by the pre-request script and by the requests that create things.
    { key: "newUserEmail", value: "", type: "string" },
    { key: "operatorEmail", value: "", type: "string" },
    { key: "userId", value: "", type: "string" },
    { key: "sessionId", value: "", type: "string" },
    // Query filters, empty so a request lists everything unless a run sets one.
    { key: "status", value: "", type: "string" },
    // A well-formed id nothing has: a fresh run has no delivery to name.
    { key: "deliveryId", value: "00000000-0000-7000-8000-000000000000", type: "string" },
    { key: "role", value: "AUDITOR", type: "string" },
    { key: "limit", value: "50", type: "string" },
    { key: "bundleDigest", value: "", type: "string" },
    { key: "decisionTime", value: "", type: "string" },
    { key: "eventTime", value: "", type: "string" },
    // Well-formed and nothing's: captured by `06`'s commit, and until then
    // the retention verdict in `04` asks about a decision that does not exist.
    { key: "decisionId", value: "00000000-0000-7000-8000-000000000000", type: "string" },
    { key: "obligationId", value: "", type: "string" },
    { key: "refundId", value: "", type: "string" },
    { key: "packageId", value: "", type: "string" },
    { key: "beneficiaryId", value: "", type: "string" },
    { key: "comparatorId", value: "", type: "string" },
    { key: "migrationProgramId", value: "", type: "string" },
    { key: "migrationSnapshotId", value: "", type: "string" },
    { key: "migrationMappingId", value: "", type: "string" },
    { key: "migrationRunId", value: "", type: "string" },
    { key: "migrationExceptionId", value: "", type: "string" },
    { key: "shadowRunId", value: "", type: "string" },
    // A run with no material difference opens no discrepancy; the move then
    // names one that does not exist, and answers 404.
    { key: "discrepancyId", value: "00000000-0000-7000-8000-000000000000", type: "string" },
    // No folder makes a payment, so the payment reads and actions name one
    // that does not exist, and answer 404.
    { key: "remittanceId", value: "00000000-0000-7000-8000-000000000000", type: "string" },
    { key: "webhookId", value: "", type: "string" },
    { key: "jobId", value: "", type: "string" },
    { key: "sealId", value: "", type: "string" },
    { key: "documentId", value: "", type: "string" },
    { key: "period", value: "2000-01", type: "string" },
    { key: "requestId", value: "", type: "string" },
    { key: "runId", value: "", type: "string" },
    // Well-formed and nothing's, the same pattern as `decisionId`: a fresh
    // run's own reconciliation has no exception to resolve yet, so
    // `resolveReconciliationItem` names an item that does not exist and the
    // response is 404, exactly as its description says.
    { key: "itemId", value: "00000000-0000-7000-8000-000000000000", type: "string" },
    { key: "holdId", value: "", type: "string" },
    { key: "authorizationId", value: "", type: "string" },
    // Minted once per run by `07`, and reused by the requests that must share it.
    { key: "reusedKey", value: "", type: "string" },

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

/**
 * The bootstrap tenant and administrator the local stack creates, read from
 * the committed .env.example that docker-compose.yml takes them from (via
 * .env). Never from .env itself: that file is a developer's own and is not in
 * the tree, so a collection generated from it would differ by machine and fail
 * its own check in CI.
 *
 * A missing value is an error rather than a default, because a default is how
 * the collection came to carry a password the stack does not use.
 */
function localBootstrap(envPath) {
  const env = {};
  for (const line of readFileSync(envPath, "utf8").split(/\r?\n/)) {
    const m = /^([A-Z][A-Z0-9_]*)=(.*)$/.exec(line);
    if (m) env[m[1]] = m[2];
  }
  const local = {
    tenant: env.ZTAX_BOOTSTRAP_TENANT,
    email: env.ZTAX_BOOTSTRAP_ADMIN_EMAIL,
    password: env.ZTAX_BOOTSTRAP_ADMIN_PASSWORD,
  };
  for (const [name, value] of Object.entries(local)) {
    if (typeof value !== "string" || value === "") {
      throw new Error(`${envPath}: sets no bootstrap ${name}, and the collection cannot sign in without it.`);
    }
  }
  return local;
}

// A collection-level "function (){ ... }" script that fails to parse stays
// quiet until newman actually runs that request -- a missing-paren or a
// stray apostrophe inside a single-quoted test name shows up as a cryptic
// "SyntaxError: missing ) after argument list" in CI, with no indication of
// which request it came from. Compiling every event script here, right
// after the collection is built, turns that into a generation-time failure
// that names the request. `new Function` only compiles the body -- it is
// never called, so this cannot have a side effect even if a script does.
function checkScriptsParse(collection) {
  const walk = (items, trail) => {
    for (const it of items) {
      const here = trail.concat(it.name);
      for (const ev of it.event ?? []) {
        const code = (ev.script?.exec ?? []).join("\n");
        try {
          new Function(code); // eslint-disable-line no-new-func -- parsed, never called
        } catch (err) {
          throw new Error(`${ev.listen} script for "${here.join(" / ")}" does not parse: ${err.message}`);
        }
      }
      if (it.item) walk(it.item, here);
    }
  };
  walk(collection.item, []);
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

  const local = localBootstrap(join(here, "..", "..", ".env.example"));
  const collection = build(doc, overlay, scripts, { path: source, local });
  checkScriptsParse(collection);
  const rendered = JSON.stringify(collection, null, 2) + "\n";
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
  const folders = build(doc, overlay, scripts, { path: source, local }).item;
  const requests = folders.reduce((n, f) => n + f.item.length, 0);
  process.stdout.write(`wrote ${target}: ${requests} requests in ${folders.length} folders (${digest})\n`);
}

try {
  main(process.argv.slice(2));
} catch (err) {
  process.stderr.write(`postman: ${err.message}\n`);
  process.exit(1);
}
