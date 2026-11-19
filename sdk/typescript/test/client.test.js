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
    "recordClassificationDecision", "getClassificationDecision",
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

test("a second classification decision is a 409 error value, not an exception", async () => {
  const { fetch } = stub(json(409, problem({ status: 409, ztx_reason_code: "ALREADY_EXISTS" })));
  const client = new ZoikoTaxClient({ baseUrl: "https://eu-west-1.zoikotax.com", fetch });

  const result = await client.recordClassificationDecision({ proposalId: "p", subjectRef: "s", proposedCode: "c", action: "ACCEPT", reason: "r" });

  assert.equal(result.ok, false);
  assert.equal(result.error.status, 409);
  assert.equal(result.error.reasonCode, "ALREADY_EXISTS");
});
