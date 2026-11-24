// SDK behaviour, against a stub fetch.
//
// These test the decisions in the client's own doc comment — that an error is a
// value, that nothing retries by itself, that a non-Problem error body is not
// attributed to the service. They are not contract tests: what the *server*
// does is asserted in Go, against the same contract file
// (backend/internal/transport/http/contract_test.go).

import assert from "node:assert/strict";
import test from "node:test";

import { ZoikoTaxClient, ZoikoTaxError, ZoikoTaxTransportError, unwrap } from "../dist/index.js";

/** A fetch that answers once, and records what it was asked. */
function stub(response) {
  const calls = [];
  const fetch = async (url, init) => {
    calls.push({ url, init });
    return response;
  };
  return { fetch, calls };
}

const json = (status, body, headers = {}) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": status >= 400 ? "application/problem+json" : "application/json", ...headers },
  });

const problem = (overrides = {}) => ({
  type: "https://errors.zoikotax.com/v1/forbidden",
  title: "Forbidden",
  status: 403,
  detail: "The authenticated subject does not hold a role permitting this action.",
  instance: "/v1/admin/users",
  ztx_reason_code: "FORBIDDEN",
  ztx_request_id: "01JBQ0S9C3X8Q1H6M2KX5R7F4K",
  ztx_retryable: false,
  ...overrides,
});

test("a success returns the body", async () => {
  const capabilities = { cell: "eu-west-1", authoritative: false };
  const { fetch, calls } = stub(json(200, capabilities));
  const client = new ZoikoTaxClient({ baseUrl: "https://eu-west-1.zoikotax.com/", fetch });

  const result = await client.getCapabilities();

  assert.equal(result.ok, true);
  assert.deepEqual(result.data, capabilities);
  assert.equal(calls[0].url, "https://eu-west-1.zoikotax.com/v1/capabilities");
  // The session is an HttpOnly cookie; without this every call is
  // UNAUTHENTICATED for a reason that looks like a server problem.
  assert.equal(calls[0].init.credentials, "include");
});

test("an error is a value carrying the reason code, not an exception", async () => {
  const { fetch } = stub(json(403, problem()));
  const client = new ZoikoTaxClient({ baseUrl: "https://eu-west-1.zoikotax.com", fetch });

  const result = await client.listUsers();

  assert.equal(result.ok, false);
  assert.ok(result.error instanceof ZoikoTaxError);
  assert.equal(result.error.reasonCode, "FORBIDDEN");
  assert.equal(result.error.status, 403);
  assert.equal(result.error.retryable, false);
  assert.equal(result.error.requestId, "01JBQ0S9C3X8Q1H6M2KX5R7F4K");
  // The whole document survives, so a caller needing a field this SDK release
  // predates can still reach it.
  assert.equal(result.error.problem.type, "https://errors.zoikotax.com/v1/forbidden");
});

test("retryable and Retry-After are reported, and nothing is retried", async () => {
  const { fetch, calls } = stub(
    json(503, problem({ status: 503, ztx_reason_code: "DATABASE_UNAVAILABLE", ztx_retryable: true }), {
      "Retry-After": "2",
    }),
  );
  const client = new ZoikoTaxClient({ baseUrl: "https://eu-west-1.zoikotax.com", fetch });

  const result = await client.getTenant();

  assert.equal(result.ok, false);
  assert.equal(result.error.retryable, true);
  assert.equal(result.error.retryAfter, 2);
  // One attempt. Retrying is the caller's decision, because on the endpoints
  // this surface is about to grow, an automatic retry submits a transaction
  // twice.
  assert.equal(calls.length, 1);
});

test("a 204 yields no body rather than a parse failure", async () => {
  const { fetch } = stub(new Response(null, { status: 204 }));
  const client = new ZoikoTaxClient({ baseUrl: "https://eu-west-1.zoikotax.com", fetch });

  const result = await client.revokeRole("ztu_01JBQ0S9C3X8Q1H6M2KX5R7F4C", "AUDITOR");

  assert.equal(result.ok, true);
  assert.equal(result.data, undefined);
});

test("a non-Problem error body is not attributed to the service", async () => {
  // What a load balancer, a proxy or a WAF returns. Reporting it as a
  // ZoikoTaxError would give it a reason code nobody registered.
  const { fetch } = stub(new Response("<html>502 Bad Gateway</html>", { status: 502 }));
  const client = new ZoikoTaxClient({ baseUrl: "https://eu-west-1.zoikotax.com", fetch });

  const result = await client.getSession();

  assert.equal(result.ok, false);
  assert.ok(result.error instanceof ZoikoTaxTransportError);
  assert.ok(!(result.error instanceof ZoikoTaxError));
  assert.equal(result.error.status, 502);
});

test("a request that never reaches the service is a transport error", async () => {
  const client = new ZoikoTaxClient({
    baseUrl: "https://eu-west-1.zoikotax.com",
    fetch: async () => {
      throw new TypeError("getaddrinfo ENOTFOUND");
    },
  });

  const result = await client.getCapabilities();

  assert.equal(result.ok, false);
  assert.ok(result.error instanceof ZoikoTaxTransportError);
  assert.ok(result.error.cause instanceof TypeError);
});

test("path parameters are encoded", async () => {
  const { fetch, calls } = stub(new Response(null, { status: 204 }));
  const client = new ZoikoTaxClient({ baseUrl: "https://eu-west-1.zoikotax.com", fetch });

  await client.revokeSession("zts_01/../../admin");

  assert.ok(calls[0].url.endsWith("/v1/admin/sessions/zts_01%2F..%2F..%2Fadmin"));
});

test("a limit becomes a query parameter and an absent one does not", async () => {
  const { fetch, calls } = stub(json(200, { users: [] }));
  const client = new ZoikoTaxClient({ baseUrl: "https://eu-west-1.zoikotax.com", fetch });

  await client.listUsers({ limit: 50 });
  await client.listUsers();

  assert.ok(calls[0].url.endsWith("/v1/admin/users?limit=50"));
  assert.ok(calls[1].url.endsWith("/v1/admin/users"));
});

test("fetch is not called as a method of the client, as a browser requires", async () => {
  // A browser's fetch throws "Illegal invocation" when `this` is not the
  // window; this stand-in does the same for any `this` but globalThis or
  // undefined, so the default and a supplied fetch are both held to it.
  const original = globalThis.fetch;
  const strict = function (url) {
    if (this !== undefined && this !== globalThis) {
      throw new TypeError("Illegal invocation");
    }
    return Promise.resolve(json(200, { url }));
  };
  globalThis.fetch = strict;
  try {
    const byDefault = await new ZoikoTaxClient({ baseUrl: "/api" }).getCapabilities();
    const supplied = await new ZoikoTaxClient({ baseUrl: "/api", fetch: strict }).getCapabilities();
    assert.equal(byDefault.ok, true, byDefault.ok ? "" : byDefault.error.message);
    assert.equal(supplied.ok, true, supplied.ok ? "" : supplied.error.message);
  } finally {
    globalThis.fetch = original;
  }
});

test("the fiscal surface is reachable: paths, query strings and idempotency keys", async () => {
  const { fetch, calls } = stub(json(200, {}));
  const client = new ZoikoTaxClient({ baseUrl: "/api", fetch });

  await client.listObligations({ status: "OVERDUE", limit: 10 });
  await client.getSubledgerPeriod("2026-09");
  await client.approvePeriodReopen("2026-09", "r/1");
  await client.correctDocument("d1", "key-1", { type: "VOID", reason: "CORRECTION_PRICING_ERROR" });
  await client.refundTransaction("key-2", { decisionId: "x", amount: { amount: "1.00", currency: "EUR" } });
  await client.resolveReconciliationItem("run", "item", { reason: "r", action: "WAIT", rootCause: "TIMING", evidence: ["e"] });

  assert.equal(calls[0].url, "/api/v1/obligations?status=OVERDUE&limit=10");
  assert.equal(calls[1].url, "/api/v1/subledger/periods/2026-09");
  assert.equal(calls[2].url, "/api/v1/subledger/periods/2026-09/reopen-requests/r%2F1/approval");
  assert.equal(calls[3].url, "/api/v1/documents/d1/corrections");
  assert.equal(calls[3].init.headers["Idempotency-Key"], "key-1");
  assert.equal(calls[4].init.headers["Idempotency-Key"], "key-2");
  assert.equal(calls[5].url, "/api/v1/reconciliations/run/items/item/resolution");
  assert.equal(calls[5].init.method, "POST");

  for (const name of [
    "listDecisions", "listRefunds", "listJobs", "listDocuments", "listReconciliations",
    "adjustTransaction", "getRefund", "reportRefund", "submitBatch", "getJob", "listDecisionJournals", "listDecisionResponsibility",
    "getDecisionInclusion", "getSubledgerBalances", "transitionSubledgerPeriod", "requestPeriodReopen",
    "getObligation", "transitionObligation", "issueDocument", "getDocument", "getDocumentLineage",
    "runReconciliation", "getReconciliation", "listSeals", "sealPeriod", "getSeal", "verifySeal",
    "listRetentionPolicies", "recordRetentionPolicy", "listLegalHolds", "placeLegalHold", "getLegalHold",
    "changeLegalHoldScope", "releaseLegalHold", "getDecisionRetention", "getLegalMatrix", "listAuthorizations",
    "grantAuthorization", "getAuthorization", "revokeAuthorization", "checkAuthorization", "proposeClassification",
    "recordClassificationDecision", "getClassificationDecision", "explainObligation",
    "listCompliancePackages", "createCompliancePackage", "getCompliancePackage", "prepareCompliancePackage",
    "reviewCompliancePackage", "approveCompliancePackage", "submitCompliancePackage", "acknowledgeCompliancePackage",
    "resolveCompliancePackage", "cancelCompliancePackage",
    "listBeneficiaries", "createBeneficiary", "verifyBeneficiary", "suspendBeneficiary", "listRemittances",
    "createRemittance", "getRemittance", "approveRemittance", "submitRemittance", "recordRemittanceStatus",
    "resolveRemittance", "returnRemittance", "cancelRemittance",
    "listComparators", "createComparator", "listShadowRuns", "executeShadowRun", "getShadowRun", "moveDiscrepancy",
    "listMigrationPrograms", "createMigrationProgram", "getMigrationProgram", "registerMigrationSnapshot",
    "createMigrationMapping", "executeMigrationRun", "moveMigrationCutover", "getMigrationRun", "decideMigrationException",
  ]) {
    assert.equal(typeof client[name], "function", name);
  }
});

test("the dashboard is read relative to a same-origin prefix, with its period", async () => {
  const dashboard = { period: "2026-09", determinations: { tax: [{ amount: "882.00", currency: "EUR" }] } };
  const { fetch, calls } = stub(json(200, dashboard));
  // A browser client on the web tier's origin, which proxies /api to the cell.
  const client = new ZoikoTaxClient({ baseUrl: "/api", fetch });

  const result = await client.getDashboard({ period: "2026-09" });
  await client.getDashboard();

  assert.equal(result.ok, true);
  assert.equal(result.data.determinations.tax[0].amount, "882.00");
  assert.equal(calls[0].url, "/api/v1/dashboard?period=2026-09");
  assert.equal(calls[0].init.method, "GET");
  assert.equal(calls[1].url, "/api/v1/dashboard");
});

test("a request body is sent as JSON with the right content type", async () => {
  const { fetch, calls } = stub(json(200, { tenant: {}, user: {}, expiresAt: "" }));
  const client = new ZoikoTaxClient({ baseUrl: "https://eu-west-1.zoikotax.com", fetch });

  await client.signIn({ tenant: "acme", email: "admin@acme.example", password: "correct horse battery staple" });

  assert.equal(calls[0].init.headers["Content-Type"], "application/json");
  assert.deepEqual(JSON.parse(calls[0].init.body), {
    tenant: "acme",
    email: "admin@acme.example",
    password: "correct horse battery staple",
  });
});

test("a commit sends the caller's Idempotency-Key", async () => {
  const { fetch, calls } = stub(json(201, { id: "01920a4b-7c3e-7d21-9f40-3c1a2b4d5e6f" }));
  const client = new ZoikoTaxClient({ baseUrl: "https://eu-west-1.zoikotax.com", fetch });

  await client.commitTransaction("5f0c2a1e-commit-INV-0001-1", {
    businessKey: "INV-0001/1",
    eventTime: "2026-09-24T18:00:00.000000Z",
    input: { money: { "line.netAmount": { amount: "100.00", currency: "EUR" } } },
  });

  assert.equal(calls[0].url, "https://eu-west-1.zoikotax.com/v1/transactions:commit");
  assert.equal(calls[0].init.method, "POST");
  assert.equal(calls[0].init.headers["Idempotency-Key"], "5f0c2a1e-commit-INV-0001-1");
  // An amount is a string on the wire, with its scale intact.
  assert.equal(JSON.parse(calls[0].init.body).input.money["line.netAmount"].amount, "100.00");
});

test("unwrap throws the error for callers who prefer exceptions", async () => {
  const { fetch } = stub(json(403, problem()));
  const client = new ZoikoTaxClient({ baseUrl: "https://eu-west-1.zoikotax.com", fetch });

  await assert.rejects(async () => unwrap(await client.listUsers()), (err) => {
    assert.ok(err instanceof ZoikoTaxError);
    assert.equal(err.reasonCode, "FORBIDDEN");
    return true;
  });
});

test("a client with no base url is refused at construction", () => {
  assert.throws(() => new ZoikoTaxClient({ baseUrl: "" }), TypeError);
});

test("the listings put their filters in the query and nothing else", async () => {
  const { fetch, calls } = stub(json(200, { decisions: [] }));
  const client = new ZoikoTaxClient({ baseUrl: "/api", fetch });

  await client.listDecisions({ businessKey: "INV-1/1", outcome: "ADVISORY", limit: 5 });
  await client.listDecisions();
  await client.listRefunds({ limit: 20 });
  await client.listJobs();
  await client.listDocuments({ limit: 7 });
  await client.listReconciliations();

  assert.deepEqual(calls.map((c) => c.url), [
    "/api/v1/decisions?businessKey=INV-1%2F1&outcome=ADVISORY&limit=5",
    "/api/v1/decisions",
    "/api/v1/refunds?limit=20",
    "/api/v1/jobs",
    "/api/v1/documents?limit=7",
    "/api/v1/reconciliations",
  ]);
  for (const c of calls) {
    assert.equal(c.init.method, "GET");
    assert.equal(c.init.headers["Idempotency-Key"], undefined);
  }
});

test("support cases: the queue's filters, a case and an action", async () => {
  const { fetch, calls } = stub(json(200, { cases: [] }));
  const client = new ZoikoTaxClient({ baseUrl: "/api", fetch });

  await client.listSupportCases({ type: "LEGAL", slaClock: "AT_RISK", unassigned: true, limit: 10 });
  await client.listSupportCases();
  await client.getSupportCase("a/b");
  await client.openSupportCase({ type: "FILING", severity: "S2", subject: "s", description: "d" });
  await client.actOnSupportCase("c1", { action: "VERIFY" });

  assert.deepEqual(calls.map((c) => [c.init.method, c.url]), [
    ["GET", "/api/v1/support/cases?type=LEGAL&slaClock=AT_RISK&limit=10&unassigned=true"],
    ["GET", "/api/v1/support/cases"],
    ["GET", "/api/v1/support/cases/a%2Fb"],
    ["POST", "/api/v1/support/cases"],
    ["POST", "/api/v1/support/cases/c1/actions"],
  ]);
  assert.deepEqual(JSON.parse(calls[4].init.body), { action: "VERIFY" });
});

test("the contribution calls send amounts as strings and no Idempotency-Key", async () => {
  const { fetch, calls } = stub(json(201, { id: "01920b80-1a2b-7c3d-8e4f-5a6b7c8d9e01" }));
  const client = new ZoikoTaxClient({ baseUrl: "/api", fetch });

  await client.recordContribution({
    sourceEventId: "BSS-INV-1",
    sourceVersion: 1,
    sourceType: "BSS_INVOICE",
    regime: "US_USF",
    period: "2026-09",
    classificationRef: "usf:interstate-telecom",
    situsRef: "US",
    sourceRef: "INV-1",
    ruleVersion: "usf-499a@2026.1",
    currency: "USD",
    eligibleBase: "1250.00",
  });
  await client.listContributions({ period: "2026-09", status: "QUARANTINED" });
  await client.getContributionSummary("2026-09");
  await client.getContributionTrace("01920b80-1a2b-7c3d-8e4f-5a6b7c8d9e01");
  await client.quarantineContribution("01920b80-1a2b-7c3d-8e4f-5a6b7c8d9e01", { reason: "imported twice" });
  await client.releaseContribution("01920b80-1a2b-7c3d-8e4f-5a6b7c8d9e01", { reason: "checked" });

  assert.deepEqual(calls.map((c) => `${c.init.method} ${c.url}`), [
    "POST /api/v1/contributions",
    "GET /api/v1/contributions?period=2026-09&status=QUARANTINED",
    "GET /api/v1/contributions/summary?period=2026-09",
    "GET /api/v1/contributions/01920b80-1a2b-7c3d-8e4f-5a6b7c8d9e01/trace",
    "POST /api/v1/contributions/01920b80-1a2b-7c3d-8e4f-5a6b7c8d9e01/quarantine",
    "POST /api/v1/contributions/01920b80-1a2b-7c3d-8e4f-5a6b7c8d9e01/release",
  ]);
  assert.equal(JSON.parse(calls[0].init.body).eligibleBase, "1250.00");
  for (const c of calls) assert.equal(c.init.headers["Idempotency-Key"], undefined);
});

test("regulatory notices: register filters in the query, actions on the notice's path", async () => {
  const calls = [];
  const fetch = async (url, init) => {
    calls.push({ url, init });
    return json(200, { notices: [] });
  };
  const client = new ZoikoTaxClient({ baseUrl: "/api", fetch });

  await client.listNotices({ authority: "DE-BZST", amountMin: "1000.00", currency: "EUR", urgent: true, limit: 5 });
  await client.listNotices();
  await client.getNotice("01920c10-1a2b-7c3d-8e4f-5a6b7c8d9e01");
  await client.getNoticeDeadlines("01920c10-1a2b-7c3d-8e4f-5a6b7c8d9e01");
  await client.recordNoticeDeadline("01920c10-1a2b-7c3d-8e4f-5a6b7c8d9e01", {
    kind: "EXTENSION", dueDate: "2026-11-30", timezone: "Europe/Berlin", source: "AUTHORITY_CONFIRMATION",
    verified: true, evidence: "Extension letter", authorityReference: "EXT-1",
  });
  await client.approveNoticeResponse("01920c10-1a2b-7c3d-8e4f-5a6b7c8d9e01", { version: 1 });

  assert.deepEqual(calls.map((c) => `${c.init.method} ${c.url}`), [
    "GET /api/v1/notices?authority=DE-BZST&amountMin=1000.00&currency=EUR&limit=5&urgent=true",
    "GET /api/v1/notices",
    "GET /api/v1/notices/01920c10-1a2b-7c3d-8e4f-5a6b7c8d9e01",
    "GET /api/v1/notices/01920c10-1a2b-7c3d-8e4f-5a6b7c8d9e01/deadlines",
    "POST /api/v1/notices/01920c10-1a2b-7c3d-8e4f-5a6b7c8d9e01/deadlines",
    "POST /api/v1/notices/01920c10-1a2b-7c3d-8e4f-5a6b7c8d9e01/response-approval",
  ]);
  // The due date travels as the civil date it was given, never as an instant.
  assert.equal(JSON.parse(calls[4].init.body).dueDate, "2026-11-30");
  assert.deepEqual(JSON.parse(calls[5].init.body), { version: 1 });
});

test("a classification decision is posted and read back by proposal id", async () => {
  const proposalId = "01920a4d-5a6b-7c8d-9e0f-1a2b3c4d5e6f";
  const saved = { proposalId, action: "REJECT", reason: "Not a fit", tenantId: "t", reviewerUserId: "u", decidedAt: "" };
  const { fetch, calls } = stub(json(201, saved));
  const client = new ZoikoTaxClient({ baseUrl: "https://eu-west-1.zoikotax.com", fetch });

  const body = { proposalId, subjectRef: "sku:PLAN-UNL-5G", proposedCode: "ontology:telecom/voice/mobile", action: "REJECT", reason: "Not a fit" };
  const result = await client.recordClassificationDecision(body);
  await client.getClassificationDecision(proposalId);

  assert.equal(result.ok, true);
  assert.equal(result.data.action, "REJECT");
  assert.equal(calls[0].url, "https://eu-west-1.zoikotax.com/v1/classifications/decisions");
  assert.equal(calls[0].init.method, "POST");
  assert.deepEqual(JSON.parse(calls[0].init.body), body);
  assert.equal(calls[1].url, `https://eu-west-1.zoikotax.com/v1/classifications/decisions/${proposalId}`);
  assert.equal(calls[1].init.method, "GET");
});

test("explaining an obligation posts to its path with no body and no Idempotency-Key", async () => {
  const obligationId = "01920a4d-1b2c-7d3e-8f40-5a6b7c8d9e01";
  const reply = { obligationId, status: "REVIEW_REQUIRED", reviewReason: "AI_GATEWAY_UNAVAILABLE", citedIds: [], authoritative: false };
  const { fetch, calls } = stub(json(200, reply));
  const client = new ZoikoTaxClient({ baseUrl: "https://eu-west-1.zoikotax.com", fetch });

  const result = await client.explainObligation(obligationId);

  assert.equal(result.ok, true);
  assert.equal(result.data.status, "REVIEW_REQUIRED");
  assert.equal(calls[0].url, `https://eu-west-1.zoikotax.com/v1/obligations/${obligationId}/explanation`);
  assert.equal(calls[0].init.method, "POST");
  assert.equal(calls[0].init.body ?? null, null);
  assert.equal("Idempotency-Key" in calls[0].init.headers, false);
});

test("a second classification decision is a 409 error value, not an exception", async () => {
  const { fetch } = stub(json(409, problem({ status: 409, ztx_reason_code: "ALREADY_EXISTS" })));
  const client = new ZoikoTaxClient({ baseUrl: "https://eu-west-1.zoikotax.com", fetch });

  const result = await client.recordClassificationDecision({ proposalId: "p", subjectRef: "s", proposedCode: "c", action: "ACCEPT", reason: "r" });

  assert.equal(result.ok, false);
  assert.equal(result.error.status, 409);
  assert.equal(result.error.reasonCode, "ALREADY_EXISTS");
});

test("the workpaper calls send amounts as strings and no Idempotency-Key", async () => {
  const { fetch, calls } = stub(json(201, { id: "01920c40-1a2b-7c3d-8e4f-5a6b7c8d9e01" }));
  const client = new ZoikoTaxClient({ baseUrl: "/api", fetch });
  const id = "01920c40-1a2b-7c3d-8e4f-5a6b7c8d9e01";

  await client.createWorkpaper({
    duty: "US_USF",
    period: "2026-09",
    authority: "USAC",
    reportingCurrency: "USD",
    materialityThreshold: "500.00",
    contentRelease: "usf-499q@2026.3",
  });
  await client.listWorkpapers({ period: "2026-09", status: "DATA_INCOMPLETE" });
  await client.getWorkpaper(id);
  await client.buildWorkpaper(id, { expectedSeq: 1 });
  await client.checkWorkpaperSources(id);
  await client.actOnWorkpaper(id, {
    action: "PROPOSE_ADJUSTMENT",
    adjustment: { kind: "AUDITED_ADJUSTMENT", category: "Billing correction", amount: "-1200.00", reason: "Credit notes" },
    evidence: { sha256: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", filename: "audit.pdf", mediaType: "application/pdf" },
  });

  assert.deepEqual(calls.map((c) => `${c.init.method} ${c.url}`), [
    "POST /api/v1/workpapers",
    "GET /api/v1/workpapers?period=2026-09&status=DATA_INCOMPLETE",
    `GET /api/v1/workpapers/${id}`,
    `POST /api/v1/workpapers/${id}/build`,
    `GET /api/v1/workpapers/${id}/source-check`,
    `POST /api/v1/workpapers/${id}/actions`,
  ]);
  assert.equal(JSON.parse(calls[0].init.body).materialityThreshold, "500.00");
  assert.equal(JSON.parse(calls[5].init.body).adjustment.amount, "-1200.00");
  for (const c of calls) assert.equal(c.init.headers["Idempotency-Key"], undefined);
});

test("the registration and exemption calls send intake under an Idempotency-Key and nothing else", async () => {
  const { fetch, calls } = stub(json(201, { id: "01920d20-1a2b-7c3d-8e4f-5a6b7c8d9e01" }));
  const client = new ZoikoTaxClient({ baseUrl: "/api", fetch });
  const rid = "01920d20-1a2b-7c3d-8e4f-5a6b7c8d9e01";
  const cid = "01920d30-1a2b-7c3d-8e4f-5a6b7c8d9e01";
  const doc = { sha256: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", filename: "permit.pdf", mediaType: "application/pdf", bytes: 1024 };

  await client.createRegistration("k-reg", {
    scheme: "US_USAC_499_FILER_ID", jurisdiction: "US", authority: "USAC", registrationNumber: "812345",
    effectiveFrom: "2026-01-01", attachments: [doc], submit: true,
  });
  await client.listRegistrations({ status: "ACTIVE", flag: "EXPIRING_SOON" });
  await client.getRegistration(rid);
  await client.transitionRegistration(rid, { to: "ACTIVE", expectedVersion: 4, reason: "ok" });
  await client.recordRegistrationCheck(rid, { source: "USAC", method: "MANUAL_LOOKUP", outcome: "UNAVAILABLE", checkedAt: "2026-11-02T10:00:00.000000Z" });
  await client.recordRegistrationAttachment(rid, doc);
  await client.receiveExemptionCertificate("k-cert", {
    buyerRef: "CUST-0042", buyerName: "Hudson Wireless LLC", certificateType: "RESALE", exemptionReason: "Resale",
    jurisdiction: "US-NY", issuedOn: "2026-09-15", document: doc,
  });
  await client.listExemptionCertificates({ flag: "AWAITING_SECOND_REVIEW" });
  await client.getExemptionCertificate(cid);
  await client.addExemptionCertificateVersion(cid, { document: doc });
  await client.screenExemptionCertificate(cid, {
    screening: {
      sellerMatches: "PASS", buyerMatches: "PASS", jurisdictionMatches: "PASS", formAccepted: "PASS",
      signaturePresent: "PASS", datesValid: "PASS", numberPresent: "PASS", result: "DOCUMENT_COMPLETE",
    },
  });
  await client.recommendExemptionCertificate(cid, { recommendation: "APPROVE", effectiveFrom: "2026-09-15", rationale: "complete" });
  await client.decideExemptionCertificate(cid, { decision: "CONFIRM", expectedEvidenceVersion: 1 });
  await client.transitionExemptionCertificate(cid, { to: "SUSPENDED", reason: "name change" });

  assert.deepEqual(calls.map((c) => `${c.init.method} ${c.url}`), [
    "POST /api/v1/registrations",
    "GET /api/v1/registrations?status=ACTIVE&flag=EXPIRING_SOON",
    `GET /api/v1/registrations/${rid}`,
    `POST /api/v1/registrations/${rid}/transitions`,
    `POST /api/v1/registrations/${rid}/checks`,
    `POST /api/v1/registrations/${rid}/attachments`,
    "POST /api/v1/exemptions",
    "GET /api/v1/exemptions?flag=AWAITING_SECOND_REVIEW",
    `GET /api/v1/exemptions/${cid}`,
    `POST /api/v1/exemptions/${cid}/versions`,
    `POST /api/v1/exemptions/${cid}/screening`,
    `POST /api/v1/exemptions/${cid}/recommendation`,
    `POST /api/v1/exemptions/${cid}/decision`,
    `POST /api/v1/exemptions/${cid}/transitions`,
  ]);
  assert.equal(calls[0].init.headers["Idempotency-Key"], "k-reg");
  assert.equal(calls[6].init.headers["Idempotency-Key"], "k-cert");
  for (const c of calls.filter((_, i) => i !== 0 && i !== 6)) assert.equal(c.init.headers["Idempotency-Key"], undefined);
});

test("the content workbench calls send intake under an Idempotency-Key and nothing else", async () => {
  const { fetch, calls } = stub(json(201, { id: "01920e10-0000-7000-8000-00000000c001" }));
  const client = new ZoikoTaxClient({ baseUrl: "/api", fetch });
  const xw = "01920e10-0000-7000-8000-00000000c001";
  const rc = "01920e20-0000-7000-8000-00000000d001";
  const version = {
    rights: { basis: "CUSTOMER_SUPPLIED", reference: "CFG-1" }, effectiveFrom: "2026-10-01",
    entries: [{ externalCode: "010101", description: "Local", match: "EXACT", serviceTarget: "ontology:telecom/voice/fixed/local" }],
  };

  await client.createCrosswalk("k-xw", { vendor: "SURETAX", namespace: "TRANS_TYPE", vendorRelease: "2026.10", title: "SureTax", version });
  await client.listCrosswalks({ vendor: "SURETAX", status: "IN_REVIEW" });
  await client.getCrosswalk(xw);
  await client.addCrosswalkVersion(xw, "k-v2", version);
  await client.transitionCrosswalkVersion(xw, 2, { to: "APPROVED", expectedRevision: 2, reason: "ok" });
  await client.lookupCrosswalk(xw, { code: "010101", on: "2026-11-20" });
  await client.proposeRuleChange("k-rc", {
    packId: "US-NY-TELECOM", baseVersion: "3.2.0", jurisdiction: "US-NY", changeType: "RATE_CHANGE", summary: "s", detail: "d",
    citation: { sourceId: "NY-186-C", reference: "§1", edition: "2026", authorityClass: "A0" }, effectiveFrom: "2027-01-01",
  });
  await client.listRuleChanges({ status: "RECOMMENDED" });
  await client.getRuleChange(rc);
  await client.recordRuleChangeTestEvidence(rc, { testEvidence: { ref: "GS-1" }, expectedRevision: 1 });
  await client.transitionRuleChange(rc, { to: "APPROVED_FOR_RELEASE", expectedRevision: 3, reason: "ok" });

  assert.deepEqual(calls.map((c) => `${c.init.method} ${c.url}`), [
    "POST /api/v1/content/crosswalks",
    "GET /api/v1/content/crosswalks?vendor=SURETAX&status=IN_REVIEW",
    `GET /api/v1/content/crosswalks/${xw}`,
    `POST /api/v1/content/crosswalks/${xw}/versions`,
    `POST /api/v1/content/crosswalks/${xw}/versions/2/transitions`,
    `GET /api/v1/content/crosswalks/${xw}/lookup?code=010101&on=2026-11-20`,
    "POST /api/v1/content/rule-changes",
    "GET /api/v1/content/rule-changes?status=RECOMMENDED",
    `GET /api/v1/content/rule-changes/${rc}`,
    `POST /api/v1/content/rule-changes/${rc}/test-evidence`,
    `POST /api/v1/content/rule-changes/${rc}/transitions`,
  ]);
  assert.equal(calls[0].init.headers["Idempotency-Key"], "k-xw");
  assert.equal(calls[3].init.headers["Idempotency-Key"], "k-v2");
  assert.equal(calls[6].init.headers["Idempotency-Key"], "k-rc");
  for (const c of calls.filter((_, i) => ![0, 3, 6].includes(i))) assert.equal(c.init.headers["Idempotency-Key"], undefined);
});

test("the regulatory change calls send intake and drafting under an Idempotency-Key and nothing else", async () => {
  const { fetch, calls } = stub(json(201, { id: "01920e10-1a2b-7c3d-8e4f-5a6b7c8d9e01" }));
  const client = new ZoikoTaxClient({ baseUrl: "/api", fetch });
  const ch = "01920e10-1a2b-7c3d-8e4f-5a6b7c8d9e01";
  const as = "01920e20-1a2b-7c3d-8e4f-5a6b7c8d9e01";
  const rec = "01920e30-1a2b-7c3d-8e4f-5a6b7c8d9e01";

  await client.recordRegulatoryChange("k-chg", {
    jurisdiction: "US-NY", kind: "RATE_CHANGE", title: "NY excise rises", detectedBy: "HUMAN",
    source: { authority: "NY DTF", citation: "N.Y. Tax Law §186-e", edition: "TB-ST-2026-11", documentSha256: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08" },
    publishedOn: "2026-11-01", effectiveFrom: "2027-01-01",
  });
  await client.listRegulatoryChanges({ status: "TRIAGED", jurisdiction: "US" });
  await client.getRegulatoryChange(ch);
  await client.transitionRegulatoryChange(ch, { to: "TRIAGED", priority: "MATERIAL", reason: "ok", expectedVersion: 1 });
  await client.assessRegulatoryChange(ch);
  await client.listImpactAssessments({ changeId: ch, minSeverity: "HIGH" });
  await client.getImpactAssessment(as);
  await client.draftReviewRecommendation("k-rec", { assessmentId: as, origin: "HUMAN", action: "MONITOR", rationale: "watch", targets: [] });
  await client.listReviewRecommendations({ status: "RECOMMENDED" });
  await client.getReviewRecommendation(rec);
  await client.reviseReviewRecommendation(rec, { action: "NO_ACTION", rationale: "below threshold", targets: [], expectedVersion: 1 });
  await client.recommendReviewRecommendation(rec, { reason: "checked", expectedVersion: 2 });
  await client.reviewReviewRecommendation(rec, { decision: "ACCEPT", reason: "agreed", expectedVersion: 3 });
  await client.withdrawReviewRecommendation(rec, { reason: "superseded", expectedVersion: 1 });

  assert.deepEqual(calls.map((c) => `${c.init.method} ${c.url}`), [
    "POST /api/v1/regulatory-changes",
    "GET /api/v1/regulatory-changes?status=TRIAGED&jurisdiction=US",
    `GET /api/v1/regulatory-changes/${ch}`,
    `POST /api/v1/regulatory-changes/${ch}/transitions`,
    `POST /api/v1/regulatory-changes/${ch}/assessments`,
    `GET /api/v1/impact-assessments?changeId=${ch}&minSeverity=HIGH`,
    `GET /api/v1/impact-assessments/${as}`,
    "POST /api/v1/review-recommendations",
    "GET /api/v1/review-recommendations?status=RECOMMENDED",
    `GET /api/v1/review-recommendations/${rec}`,
    `POST /api/v1/review-recommendations/${rec}/revisions`,
    `POST /api/v1/review-recommendations/${rec}/recommend`,
    `POST /api/v1/review-recommendations/${rec}/review`,
    `POST /api/v1/review-recommendations/${rec}/withdrawal`,
  ]);
  assert.equal(calls[0].init.headers["Idempotency-Key"], "k-chg");
  assert.equal(calls[7].init.headers["Idempotency-Key"], "k-rec");
  for (const c of calls.filter((_, i) => i !== 0 && i !== 7)) assert.equal(c.init.headers["Idempotency-Key"], undefined);
});

test("the ERP bridge calls put ids in the path, amounts as strings and send no Idempotency-Key", async () => {
  const { fetch, calls } = stub(json(201, { id: "01920c20-0000-7000-8000-0000000000b1" }));
  const client = new ZoikoTaxClient({ baseUrl: "/api", fetch });
  const bridge = "01920c20-0000-7000-8000-0000000000b1";
  const batch = "01920c20-1a2b-7c3d-8e4f-5a6b7c8d9e10";

  await client.listERPBridges();
  await client.createERPBridge({ name: "Acme SAP", vendor: "SAP", companyCode: "AC01" });
  await client.recordERPMapping(bridge, { chartVersion: "COA-2026.2", accounts: [{ account: "TAX_COLLECTED_LIABILITY", glAccount: "2310-00" }] });
  await client.listERPExports(bridge, { period: "2026-09", status: "REJECTED" });
  await client.prepareERPExport(bridge, { period: "2026-09", currency: "USD", postingDate: "2026-10-03" });
  await client.getERPExport(bridge, batch);
  await client.dispatchERPExport(bridge, batch, { expectedDigest: "zt1:" + "0".repeat(64) });
  await client.recordERPResponse(bridge, batch, { kind: "POSTED", attemptId: "01920c20-1a2b-7c3d-8e4f-5a6b7c8d9e20", erpReference: "AC01/2026/1" });
  await client.withdrawERPExport(bridge, batch, { reason: "mapping v2" });
  await client.recordGLSnapshot(bridge, {
    period: "2026-09",
    currency: "USD",
    asOf: "2026-10-05T18:00:00Z",
    sourceRef: "FAGLB03",
    balances: [{ glAccount: "2310-00", debits: "0.00", credits: "10.00" }],
  });
  await client.listGLSnapshots(bridge, { period: "2026-09" });
  await client.runGLReconciliation(bridge, { snapshotId: "01920c20-1a2b-7c3d-8e4f-5a6b7c8d9e30" });
  await client.listGLReconciliations(bridge);

  assert.deepEqual(calls.map((c) => `${c.init.method} ${c.url}`), [
    "GET /api/v1/erp-bridges",
    "POST /api/v1/erp-bridges",
    `POST /api/v1/erp-bridges/${bridge}/mappings`,
    `GET /api/v1/erp-bridges/${bridge}/exports?period=2026-09&status=REJECTED`,
    `POST /api/v1/erp-bridges/${bridge}/exports`,
    `GET /api/v1/erp-bridges/${bridge}/exports/${batch}`,
    `POST /api/v1/erp-bridges/${bridge}/exports/${batch}/dispatch`,
    `POST /api/v1/erp-bridges/${bridge}/exports/${batch}/responses`,
    `POST /api/v1/erp-bridges/${bridge}/exports/${batch}/withdrawal`,
    `POST /api/v1/erp-bridges/${bridge}/gl-balances`,
    `GET /api/v1/erp-bridges/${bridge}/gl-balances?period=2026-09`,
    `POST /api/v1/erp-bridges/${bridge}/gl-reconciliations`,
    `GET /api/v1/erp-bridges/${bridge}/gl-reconciliations`,
  ]);
  assert.equal(JSON.parse(calls[9].init.body).balances[0].credits, "10.00");
  for (const c of calls) assert.equal(c.init.headers["Idempotency-Key"], undefined);
});

test("the entitlement, subscription and usage calls send intake under an Idempotency-Key and nothing else", async () => {
  const { fetch, calls } = stub(json(201, { id: "01920e10-1a2b-7c3d-8e4f-5a6b7c8d9e01" }));
  const client = new ZoikoTaxClient({ baseUrl: "/api", fetch });
  const gid = "01920e10-1a2b-7c3d-8e4f-5a6b7c8d9e01";
  const pid = "01920e20-1a2b-7c3d-8e4f-5a6b7c8d9e01";

  await client.createEntitlement("k-ent", {
    jurisdiction: "GB", service: "MOBILE_VOICE", mode: "NATIVE", operation: "FILE", effectiveFrom: "2026-11-20",
    contractRef: "MSA-2026-014", submit: true,
  });
  await client.listEntitlements({ effective: "RESTRICTED", mismatch: "true" });
  await client.getEntitlement(gid);
  await client.actOnEntitlement(gid, { action: "APPROVE", expectedVersion: 2, reason: "signed" });
  await client.proposeSubscriptionPlan("k-plan", {
    planCode: "PILOT-2026", planName: "Pilot 2026", contractRef: "MSA-2026-014", termStart: "2026-11-20",
    quotas: [{ meter: "COMMITTED_DECISIONS", included: 100000, overage: "ALLOW_OVERAGE" }],
  });
  await client.listSubscriptionPlans();
  await client.getSubscriptionPlan(pid);
  await client.actOnSubscriptionPlan(pid, { action: "APPROVE", expectedVersion: 1 });
  await client.getUsage({ period: "2026-11" });
  await client.getUsageBasis({ meter: "COMMITTED_DECISIONS", period: "2026-11", limit: 50 });

  assert.deepEqual(calls.map((c) => `${c.init.method} ${c.url}`), [
    "POST /api/v1/entitlements",
    "GET /api/v1/entitlements?effective=RESTRICTED&mismatch=true",
    `GET /api/v1/entitlements/${gid}`,
    `POST /api/v1/entitlements/${gid}/actions`,
    "POST /api/v1/subscriptions",
    "GET /api/v1/subscriptions",
    `GET /api/v1/subscriptions/${pid}`,
    `POST /api/v1/subscriptions/${pid}/actions`,
    "GET /api/v1/usage?period=2026-11",
    "GET /api/v1/usage/basis?meter=COMMITTED_DECISIONS&period=2026-11&limit=50",
  ]);
  assert.equal(calls[0].init.headers["Idempotency-Key"], "k-ent");
  assert.equal(calls[4].init.headers["Idempotency-Key"], "k-plan");
  assert.equal(JSON.parse(calls[4].init.body).quotas[0].included, 100000);
  for (const c of calls.filter((_, i) => i !== 0 && i !== 4)) assert.equal(c.init.headers["Idempotency-Key"], undefined);
});

test("the e-invoicing / CTC calls put ids in the path and filters in the query, and send no Idempotency-Key", async () => {
  const { fetch, calls } = stub(json(201, { id: "01920e10-1a2b-7c3d-8e4f-5a6b7c8d9e01" }));
  const client = new ZoikoTaxClient({ baseUrl: "/api", fetch });
  const rec = "01920e10-1a2b-7c3d-8e4f-5a6b7c8d9e01";
  const doc = "01920a90-1a2b-7c3d-8e4f-5a6b7c8d9e0f";
  const sub = "01920e30-1a2b-7c3d-8e4f-5a6b7c8d9e01";
  const terms = { duty: "REQUIRED", model: "CLEARANCE", profile: "IT_FATTURAPA", formatVersion: "FPR12 1.9", authority: "SdI",
    legalSource: "D.Lgs. 127/2015", sourceAsOf: "2026-10-30", effectiveFrom: "2026-01-01" };

  await client.listCTCApplicability({ country: "IT", status: "ACTIVE" });
  await client.createCTCApplicability({ country: "IT", documentType: "INVOICE", buyerKind: "B2B", terms });
  await client.getCTCApplicability(rec);
  await client.reviseCTCApplicability(rec, terms);
  await client.addCTCApplicabilityEvidence(rec, { sha256: "a".repeat(64), filename: "o.pdf", mediaType: "application/pdf", bytes: 1 });
  await client.signCTCApplicabilityGate(rec, { gate: "G0_SCOPE_AUTHORITY", version: 1, signoffRef: "CAB-1" });
  await client.transitionCTCApplicability(rec, { action: "ACTIVATE", version: 1 });
  await client.resolveCTCApplicability({ country: "IT", documentType: "INVOICE", buyerKind: "B2B", date: "2026-11-05" });
  await client.validateCTCDocument({ documentId: doc, applicabilityId: rec });
  await client.listCTCValidations({ documentId: doc });
  await client.prepareCTCSubmission({ documentId: doc, applicabilityId: rec });
  await client.listCTCSubmissions({ status: "AUTHORITY_REJECTED" });
  await client.getCTCSubmission(sub);
  await client.dispatchCTCSubmission(sub, { payloadDigest: "zt1:" + "0".repeat(64) });
  await client.recordCTCResponse(sub, { kind: "TRANSPORT_RECEIVED", attemptId: "01920e30-1a2b-7c3d-8e4f-5a6b7c8d9e20", transportRef: "R-1" });
  await client.withdrawCTCSubmission(sub, { reason: "revised" });

  assert.deepEqual(calls.map((c) => `${c.init.method} ${c.url}`), [
    "GET /api/v1/ctc/applicability?country=IT&status=ACTIVE",
    "POST /api/v1/ctc/applicability",
    `GET /api/v1/ctc/applicability/${rec}`,
    `POST /api/v1/ctc/applicability/${rec}/versions`,
    `POST /api/v1/ctc/applicability/${rec}/evidence`,
    `POST /api/v1/ctc/applicability/${rec}/gates`,
    `POST /api/v1/ctc/applicability/${rec}/transitions`,
    "GET /api/v1/ctc/resolution?country=IT&documentType=INVOICE&buyerKind=B2B&date=2026-11-05",
    "POST /api/v1/ctc/validations",
    `GET /api/v1/ctc/validations?documentId=${doc}`,
    "POST /api/v1/ctc/submissions",
    "GET /api/v1/ctc/submissions?status=AUTHORITY_REJECTED",
    `GET /api/v1/ctc/submissions/${sub}`,
    `POST /api/v1/ctc/submissions/${sub}/dispatch`,
    `POST /api/v1/ctc/submissions/${sub}/responses`,
    `POST /api/v1/ctc/submissions/${sub}/withdrawal`,
  ]);
  for (const c of calls) assert.equal(c.init.headers["Idempotency-Key"], undefined);
});
