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
    "adjustTransaction", "getRefund", "reportRefund", "submitBatch", "getJob", "listDecisionJournals",
    "getDecisionInclusion", "getSubledgerBalances", "transitionSubledgerPeriod", "requestPeriodReopen",
    "getObligation", "transitionObligation", "issueDocument", "getDocument", "getDocumentLineage",
    "runReconciliation", "getReconciliation", "listSeals", "sealPeriod", "getSeal", "verifySeal",
    "listRetentionPolicies", "recordRetentionPolicy", "listLegalHolds", "placeLegalHold", "getLegalHold",
    "changeLegalHoldScope", "releaseLegalHold", "getDecisionRetention", "getLegalMatrix", "listAuthorizations",
    "grantAuthorization", "getAuthorization", "revokeAuthorization", "checkAuthorization", "proposeClassification",
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
