// SDK behaviour, against a stub transport.
//
// These test the decisions in the package documentation — that an error is a
// value, that nothing retries by itself, that a non-Problem error body is not
// attributed to the service. They are not contract tests: what the *server*
// does is asserted in Go, against the same contract file
// (backend/internal/transport/http/contract_test.go).
//
// The cases are ported one for one from sdk/typescript/test/client.test.js, so
// that the SDKs are held to the same behaviour; the ones after the marker below
// are specific to Java.

package com.zoikotax.sdk;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertInstanceOf;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import com.sun.net.httpserver.HttpServer;
import com.zoikotax.sdk.model.Capabilities;
import com.zoikotax.sdk.model.CreateUserRequest;
import com.zoikotax.sdk.model.Role;
import com.zoikotax.sdk.model.Session;
import com.zoikotax.sdk.model.SignInRequest;
import com.zoikotax.sdk.model.User;
import com.zoikotax.sdk.model.UserList;
import java.io.IOException;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.UnknownHostException;
import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.function.Consumer;
import org.junit.jupiter.api.Test;

class ZoikoTaxClientTest {

  private static final ObjectMapper JSON = new ObjectMapper();
  private static final String BASE = "https://eu-west-1.zoikotax.com";

  /** A transport that answers every call with one response, and records what it was asked. */
  static final class Stub implements Transport {
    final List<Request> calls = new ArrayList<>();
    private final Response response;

    Stub(Response response) {
      this.response = response;
    }

    @Override
    public Response send(Request request) {
      calls.add(request);
      return response;
    }
  }

  static Transport.Response json(int status, Object body) throws IOException {
    return json(status, body, Map.of());
  }

  static Transport.Response json(int status, Object body, Map<String, String> headers)
      throws IOException {
    Map<String, String> all = new java.util.HashMap<>(headers);
    all.put("Content-Type", status >= 400 ? "application/problem+json" : "application/json");
    return Transport.Response.of(status, all, JSON.writeValueAsString(body));
  }

  static Transport.Response empty(int status) {
    return Transport.Response.of(status, Map.of(), "");
  }

  static ObjectNode problem() {
    return problem(p -> {});
  }

  static ObjectNode problem(Consumer<ObjectNode> overrides) {
    ObjectNode p = JSON.createObjectNode();
    p.put("type", "https://errors.zoikotax.com/v1/forbidden");
    p.put("title", "Forbidden");
    p.put("status", 403);
    p.put("detail", "The authenticated subject does not hold a role permitting this action.");
    p.put("instance", "/v1/admin/users");
    p.put("ztx_reason_code", "FORBIDDEN");
    p.put("ztx_request_id", "01JBQ0S9C3X8Q1H6M2KX5R7F4K");
    p.put("ztx_retryable", false);
    overrides.accept(p);
    return p;
  }

  static ZoikoTaxClient client(Transport transport) {
    return new ZoikoTaxClient(ClientOptions.of(BASE).withTransport(transport));
  }

  static ObjectNode capabilitiesBody() {
    ObjectNode c = JSON.createObjectNode();
    c.put("cell", "eu-west-1");
    c.put("region", "eu-west");
    c.put("environment", "production");
    ObjectNode trains = c.putObject("trains");
    for (String t : List.of("app", "content", "ai", "adapter", "infra", "schema", "migration")) {
      trains.put(t, "0.1.0");
    }
    c.put("canonProfile", "canon/v1");
    c.put("authoritative", false);
    c.putArray("reasonCodes").add("FORBIDDEN");
    return c;
  }

  @Test
  void aSuccessReturnsTheBody() throws IOException {
    Stub stub = new Stub(json(200, capabilitiesBody()));
    ZoikoTaxClient client =
        new ZoikoTaxClient(ClientOptions.of(BASE + "/").withTransport(stub));

    Result<Capabilities> result = client.getCapabilities();

    assertTrue(result.isOk());
    Capabilities capabilities = result.unwrap();
    assertEquals("eu-west-1", capabilities.getCell());
    assertFalse(capabilities.getAuthoritative());
    assertEquals(BASE + "/v1/capabilities", stub.calls.get(0).uri().toString());
    assertEquals("GET", stub.calls.get(0).method());
    // The session cookie is the analogue of TS `credentials: "include"`; it is
    // asserted against a real HttpClient in theDefaultTransportKeepsTheSessionCookie.
  }

  @Test
  void anErrorIsAValueCarryingTheReasonCodeNotAnException() throws IOException {
    ZoikoTaxClient client = client(new Stub(json(403, problem())));

    Result<UserList> result = client.listUsers();

    assertFalse(result.isOk());
    ZoikoTaxError error = assertInstanceOf(ZoikoTaxError.class, result.errorOrNull());
    assertEquals("FORBIDDEN", error.getReasonCode());
    assertEquals(403, error.getStatus());
    assertFalse(error.isRetryable());
    assertEquals("01JBQ0S9C3X8Q1H6M2KX5R7F4K", error.getRequestId());
    // The whole document survives, so a caller needing a field this SDK release
    // predates can still reach it.
    assertEquals("https://errors.zoikotax.com/v1/forbidden", error.getProblem().getType().toString());
  }

  @Test
  void retryableAndRetryAfterAreReportedAndNothingIsRetried() throws IOException {
    Stub stub =
        new Stub(
            json(
                503,
                problem(
                    p -> {
                      p.put("status", 503);
                      p.put("ztx_reason_code", "DATABASE_UNAVAILABLE");
                      p.put("ztx_retryable", true);
                    }),
                Map.of("Retry-After", "2")));
    ZoikoTaxClient client = client(stub);

    Result<?> result = client.getTenant();

    assertFalse(result.isOk());
    ZoikoTaxError error = assertInstanceOf(ZoikoTaxError.class, result.errorOrNull());
    assertTrue(error.isRetryable());
    assertEquals(Duration.ofSeconds(2), error.getRetryAfter());
    // One attempt. Retrying is the caller's decision, because on the endpoints
    // this surface is about to grow, an automatic retry submits a transaction
    // twice.
    assertEquals(1, stub.calls.size());
  }

  @Test
  void a204YieldsNoBodyRatherThanAParseFailure() {
    ZoikoTaxClient client = client(new Stub(empty(204)));

    Result<Void> result = client.revokeRole("ztu_01JBQ0S9C3X8Q1H6M2KX5R7F4C", Role.AUDITOR);

    assertTrue(result.isOk());
    assertNull(result.valueOrNull());
  }

  @Test
  void aNonProblemErrorBodyIsNotAttributedToTheService() {
    // What a load balancer, a proxy or a WAF returns. Reporting it as a
    // ZoikoTaxError would give it a reason code nobody registered.
    ZoikoTaxClient client =
        client(new Stub(Transport.Response.of(502, Map.of(), "<html>502 Bad Gateway</html>")));

    Result<Session> result = client.getSession();

    assertFalse(result.isOk());
    ZoikoTaxTransportError error =
        assertInstanceOf(ZoikoTaxTransportError.class, result.errorOrNull());
    assertEquals(502, error.getStatus());
  }

  @Test
  void aRequestThatNeverReachesTheServiceIsATransportError() {
    ZoikoTaxClient client =
        client(
            request -> {
              throw new UnknownHostException("eu-west-1.zoikotax.com");
            });

    Result<Capabilities> result = client.getCapabilities();

    assertFalse(result.isOk());
    ZoikoTaxTransportError error =
        assertInstanceOf(ZoikoTaxTransportError.class, result.errorOrNull());
    assertInstanceOf(UnknownHostException.class, error.getCause());
    assertNull(error.getStatus());
  }

  @Test
  void pathParametersAreEncoded() {
    Stub stub = new Stub(empty(204));
    ZoikoTaxClient client = client(stub);

    client.revokeSession("zts_01/../../admin");

    assertTrue(
        stub.calls.get(0).uri().toString().endsWith("/v1/admin/sessions/zts_01%2F..%2F..%2Fadmin"),
        stub.calls.get(0).uri().toString());
  }

  @Test
  void aLimitBecomesAQueryParameterAndAnAbsentOneDoesNot() throws IOException {
    Stub stub = new Stub(json(200, Map.of("users", List.of())));
    ZoikoTaxClient client = client(stub);

    client.listUsers(50);
    client.listUsers();

    assertTrue(stub.calls.get(0).uri().toString().endsWith("/v1/admin/users?limit=50"));
    assertTrue(stub.calls.get(1).uri().toString().endsWith("/v1/admin/users"));
  }

  @Test
  void aRequestBodyIsSentAsJsonWithTheRightContentType() throws IOException {
    Stub stub = new Stub(json(200, Map.of("tenant", Map.of(), "user", Map.of(), "expiresAt", "")));
    ZoikoTaxClient client = client(stub);

    client.signIn(
        new SignInRequest()
            .tenant("acme")
            .email("admin@acme.example")
            .password("correct horse battery staple"));

    assertEquals("application/json", stub.calls.get(0).header("Content-Type"));
    assertEquals(
        JSON.readTree(
            "{\"tenant\":\"acme\",\"email\":\"admin@acme.example\","
                + "\"password\":\"correct horse battery staple\"}"),
        JSON.readTree(stub.calls.get(0).body()));
  }

  @Test
  void unwrapThrowsTheErrorForCallersWhoPreferExceptions() throws IOException {
    ZoikoTaxClient client = client(new Stub(json(403, problem())));

    ZoikoTaxError error = assertThrows(ZoikoTaxError.class, () -> client.listUsers().unwrap());
    assertEquals("FORBIDDEN", error.getReasonCode());
  }

  @Test
  void aClientWithNoBaseUrlIsRefusedAtConstruction() {
    assertThrows(IllegalArgumentException.class, () -> new ZoikoTaxClient(""));
  }

  // --- Java-specific ------------------------------------------------------

  @Test
  void theDefaultTransportKeepsTheSessionCookie() throws IOException {
    // The session is an HttpOnly cookie. Without a cookie jar every call after
    // sign-in is UNAUTHENTICATED for a reason that looks like a server problem.
    // Loopback only: this is the JDK's own HTTP server, not a network.
    HttpServer server = HttpServer.create(new InetSocketAddress(InetAddress.getLoopbackAddress(), 0), 0);
    List<String> cookiesSeen = new ArrayList<>();
    byte[] session =
        JSON.writeValueAsBytes(Map.of("tenant", Map.of(), "user", Map.of(), "expiresAt", ""));
    server.createContext(
        "/v1/auth/sign-in",
        exchange -> {
          exchange.getResponseHeaders().add("Set-Cookie", "ztax_session=opaque; Path=/; HttpOnly");
          exchange.getResponseHeaders().add("Content-Type", "application/json");
          exchange.sendResponseHeaders(200, session.length);
          exchange.getResponseBody().write(session);
          exchange.close();
        });
    server.createContext(
        "/v1/auth/session",
        exchange -> {
          cookiesSeen.add(String.valueOf(exchange.getRequestHeaders().getFirst("Cookie")));
          exchange.getResponseHeaders().add("Content-Type", "application/json");
          exchange.sendResponseHeaders(200, session.length);
          exchange.getResponseBody().write(session);
          exchange.close();
        });
    server.start();
    try {
      String base = "http://127.0.0.1:" + server.getAddress().getPort();
      ZoikoTaxClient client = new ZoikoTaxClient(base);
      ZoikoTaxClient other = new ZoikoTaxClient(base);

      client.signIn(new SignInRequest().tenant("acme").email("a@acme.example").password("x".repeat(12)))
          .unwrap();
      client.getSession().unwrap();
      other.getSession().unwrap();

      assertEquals("ztax_session=opaque", cookiesSeen.get(0));
      // A jar per client: a session held with one client is not another's.
      assertEquals("null", cookiesSeen.get(1));
    } finally {
      server.stop(0);
    }
  }

  @Test
  void aProblemExtensionThisReleasePredatesSurvivesInTheDocument() throws IOException {
    ZoikoTaxClient client =
        client(new Stub(json(403, problem(p -> p.put("ztx_future_extension", "kept")))));

    ZoikoTaxError error = assertInstanceOf(ZoikoTaxError.class, client.listUsers().errorOrNull());

    JsonNode document = error.getProblemDocument();
    assertEquals("kept", document.path("ztx_future_extension").asText());
  }

  @Test
  void aFieldThisReleasePredatesDoesNotFailASuccess() throws IOException {
    ObjectNode body = capabilitiesBody();
    body.put("addedInALaterMinor", true);
    ZoikoTaxClient client = client(new Stub(json(200, body)));

    assertTrue(client.getCapabilities().isOk());
  }

  @Test
  void timestampsArePassedThroughByteForByte() throws IOException {
    // An OffsetDateTime would re-encode 12:00:00.000000Z as 12:00Z, and two
    // encodings of one instant digest differently (ADR-0011 §2.1 P2).
    ObjectNode user = JSON.createObjectNode();
    user.put("id", "ztu_01JBQ0S9C3X8Q1H6M2KX5R7F4D");
    user.put("email", "auditor@acme.example");
    user.put("displayName", "Katherine Johnson");
    user.put("status", "INVITED");
    user.putArray("roles").add("AUDITOR");
    user.put("createdAt", "2026-09-23T12:00:00.000000Z");
    ZoikoTaxClient client = client(new Stub(json(201, user)));

    User created =
        client.createUser(new CreateUserRequest().email("auditor@acme.example").displayName("K"))
            .unwrap();

    assertEquals("2026-09-23T12:00:00.000000Z", created.getCreatedAt());
  }

  @Test
  void anOptionalFieldTheCallerDidNotSetIsAbsentFromTheBody() throws IOException {
    Stub stub = new Stub(empty(204));
    ZoikoTaxClient client = client(stub);

    client.createUser(
        new CreateUserRequest().email("auditor@acme.example").displayName("Katherine Johnson"));

    // No "password": null, and no "roles": []. Omitting the password is what
    // makes an INVITED user; sending null would be a different request.
    assertEquals(
        JSON.readTree("{\"email\":\"auditor@acme.example\",\"displayName\":\"Katherine Johnson\"}"),
        JSON.readTree(stub.calls.get(0).body()));
  }

  @Test
  void anEnumValueThisReleasePredatesDoesNotFailTheResponse() throws IOException {
    ObjectNode users = JSON.createObjectNode();
    ObjectNode user = users.putArray("users").addObject();
    user.put("id", "ztu_01JBQ0S9C3X8Q1H6M2KX5R7F4D");
    user.put("email", "a@acme.example");
    user.put("displayName", "A");
    user.put("status", "SOME_LATER_STATUS");
    user.putArray("roles");
    user.put("createdAt", "2026-09-23T12:00:00.000000Z");
    ZoikoTaxClient client = client(new Stub(json(200, users)));

    UserList list = client.listUsers().unwrap();

    assertEquals("UNKNOWN_DEFAULT_OPEN_API", list.getUsers().get(0).getStatus().name());
  }

  @Test
  void aTypedOperationWithNoBodyIsATransportErrorNotANullValue() {
    ZoikoTaxClient client = client(new Stub(empty(200)));

    assertInstanceOf(ZoikoTaxTransportError.class, client.getTenant().errorOrNull());
  }

  @Test
  void anInterruptIsReportedAndTheFlagIsKept() {
    ZoikoTaxClient client =
        client(
            request -> {
              throw new InterruptedException();
            });

    try {
      Result<Capabilities> result = client.getCapabilities();
      assertInstanceOf(ZoikoTaxTransportError.class, result.errorOrNull());
      assertTrue(Thread.currentThread().isInterrupted());
    } finally {
      Thread.interrupted();
    }
  }

  @Test
  void callerHeadersAndTheTimeoutReachTheTransport() throws IOException {
    Stub stub = new Stub(json(200, capabilitiesBody()));
    ZoikoTaxClient client =
        new ZoikoTaxClient(
            ClientOptions.of(BASE)
                .withTransport(stub)
                .withHeaders(Map.of("X-Correlation-Id", "abc"))
                .withTimeout(Duration.ofSeconds(5)));

    client.getCapabilities();

    Transport.Request request = stub.calls.get(0);
    assertEquals("abc", request.header("x-correlation-id"));
    assertEquals("application/problem+json, application/json", request.header("Accept"));
    assertNull(request.header("Content-Type"));
    assertEquals(Duration.ofSeconds(5), request.timeout());
  }

  @Test
  void aNonJsonSuccessBodyIsATransportError() {
    ZoikoTaxClient client = client(new Stub(Transport.Response.of(200, Map.of(), "not json")));

    ZoikoTaxTransportError error =
        assertInstanceOf(ZoikoTaxTransportError.class, client.getTenant().errorOrNull());
    assertEquals(200, error.getStatus());
  }

  @Test
  void pathEncodingMatchesEncodeUriComponent() {
    assertEquals("a%20b%2Bc%2F%C3%A9-_.!~*'()", ZoikoTaxClient.encode("a b+c/é-_.!~*'()"));
  }
}
