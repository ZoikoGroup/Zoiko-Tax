package com.zoikotax.sdk;

import com.fasterxml.jackson.annotation.JsonInclude;
import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.DeserializationFeature;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.json.JsonMapper;
import com.zoikotax.sdk.model.AuditRecordList;
import com.zoikotax.sdk.model.Capabilities;
import com.zoikotax.sdk.model.ChangePasswordRequest;
import com.zoikotax.sdk.model.CreateUserRequest;
import com.zoikotax.sdk.model.Problem;
import com.zoikotax.sdk.model.Role;
import com.zoikotax.sdk.model.RoleRequest;
import com.zoikotax.sdk.model.Session;
import com.zoikotax.sdk.model.SessionList;
import com.zoikotax.sdk.model.SetUserStatusRequest;
import com.zoikotax.sdk.model.SignInRequest;
import com.zoikotax.sdk.model.Tenant;
import com.zoikotax.sdk.model.User;
import com.zoikotax.sdk.model.UserList;
import com.zoikotax.sdk.model.UserStatus;
import jakarta.annotation.Nonnull;
import jakarta.annotation.Nullable;
import java.io.IOException;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.Objects;

/**
 * A client for one cell.
 *
 * <p>One client per cell, because a cell is a residency boundary: data never
 * leaves its region, and a client that transparently failed over to another
 * cell would be moving a tenant's data across one. For the same reason each
 * client has its own cookie jar — the session it holds is a session with that
 * cell and no other.
 *
 * <p>Every method blocks the calling thread until the response arrives, and
 * returns a {@link Result}; none throws. Interrupting the thread ends the wait
 * and is reported as a {@link ZoikoTaxTransportError}. A client is safe to share
 * between threads.
 *
 * @see com.zoikotax.sdk the package documentation, for what this client
 *     deliberately does not do
 */
public final class ZoikoTaxClient {

  /**
   * Unknown properties are ignored rather than rejected: {@code /v1} grows by
   * addition (ADR-0010 §2.6), and a client that failed on a field it predates
   * would break on the one kind of change it was promised it would survive.
   * Nulls are omitted from request bodies, so an optional field the caller did
   * not set is absent rather than an explicit {@code null}.
   */
  private static final ObjectMapper JSON =
      JsonMapper.builder()
          .disable(DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES)
          .defaultPropertyInclusion(
              JsonInclude.Value.construct(JsonInclude.Include.NON_NULL, JsonInclude.Include.NON_NULL))
          .build();

  /**
   * Problem Details first, because an error is the response whose shape this
   * client most needs to be sure of.
   */
  private static final String ACCEPT = "application/problem+json, application/json";

  private final String baseUrl;
  private final Transport transport;
  private final Map<String, String> headers;
  private final @Nullable Duration timeout;

  /** A client for the cell at {@code baseUrl}, with the default transport. */
  public ZoikoTaxClient(@Nonnull String baseUrl) {
    this(ClientOptions.of(baseUrl));
  }

  /**
   * A client for a cell.
   *
   * @throws IllegalArgumentException if the base URL is blank
   */
  public ZoikoTaxClient(@Nonnull ClientOptions options) {
    Objects.requireNonNull(options, "options");
    if (options.baseUrl().isBlank()) {
      throw new IllegalArgumentException("ZoikoTaxClient: baseUrl is required");
    }
    this.baseUrl = options.baseUrl().replaceAll("/+$", "");
    this.transport = options.transport() != null ? options.transport() : Transport.newDefault();
    this.headers = options.headers();
    this.timeout = options.timeout();
  }

  // --- discovery ----------------------------------------------------------

  /**
   * Report this deployment's effective capability.
   *
   * <p>Call it before treating any figure as authoritative: {@code
   * authoritative} is false until A4, and a deployment that reports false
   * produces advisory figures that must not be filed.
   */
  @Nonnull
  public Result<Capabilities> getCapabilities() {
    return send("GET", "/v1/capabilities", null, Capabilities.class);
  }

  // --- authentication -----------------------------------------------------

  /** Open a session. The response carries no token; the cookie is the session. */
  @Nonnull
  public Result<Session> signIn(@Nonnull SignInRequest body) {
    return send("POST", "/v1/auth/sign-in", body, Session.class);
  }

  /** End the current session. */
  @Nonnull
  public Result<Void> signOut() {
    return send("POST", "/v1/auth/sign-out", null, Void.class);
  }

  /** Who am I, and until when. */
  @Nonnull
  public Result<Session> getSession() {
    return send("GET", "/v1/auth/session", null, Session.class);
  }

  /**
   * Change the current subject's password.
   *
   * <p>Every session is revoked, including this one. Expect the next call to
   * fail with {@code UNAUTHENTICATED}, and send the user to sign in.
   */
  @Nonnull
  public Result<Void> changePassword(@Nonnull ChangePasswordRequest body) {
    return send("POST", "/v1/auth/password", body, Void.class);
  }

  // --- administration -----------------------------------------------------

  /** The authenticated subject's tenant. */
  @Nonnull
  public Result<Tenant> getTenant() {
    return send("GET", "/v1/admin/tenant", null, Tenant.class);
  }

  /** List the tenant's users, up to the server's default page size. */
  @Nonnull
  public Result<UserList> listUsers() {
    return send("GET", "/v1/admin/users", null, UserList.class);
  }

  /**
   * List up to {@code limit} of the tenant's users. The server caps this
   * independently, so a larger value is not an error and does not return more.
   */
  @Nonnull
  public Result<UserList> listUsers(int limit) {
    return send("GET", "/v1/admin/users" + limit(limit), null, UserList.class);
  }

  /** Create a user. Omitting the password creates an {@code INVITED} user. */
  @Nonnull
  public Result<User> createUser(@Nonnull CreateUserRequest body) {
    return send("POST", "/v1/admin/users", body, User.class);
  }

  /** Enable or disable a user. Disabling revokes their sessions. */
  @Nonnull
  public Result<Void> setUserStatus(@Nonnull String userId, @Nonnull UserStatus status) {
    return send(
        "POST",
        "/v1/admin/users/" + encode(userId) + "/status",
        new SetUserStatusRequest().status(status),
        Void.class);
  }

  /** Grant a role. */
  @Nonnull
  public Result<Void> grantRole(@Nonnull String userId, @Nonnull Role role) {
    return send(
        "POST", "/v1/admin/users/" + encode(userId) + "/roles", new RoleRequest().role(role), Void.class);
  }

  /** Revoke a role. Revoking the last {@code ADMIN} is refused. */
  @Nonnull
  public Result<Void> revokeRole(@Nonnull String userId, @Nonnull Role role) {
    return send(
        "DELETE",
        "/v1/admin/users/" + encode(userId) + "/roles/" + encode(role.getValue()),
        null,
        Void.class);
  }

  /** List the tenant's sessions. {@code current} marks the caller's own. */
  @Nonnull
  public Result<SessionList> listSessions() {
    return send("GET", "/v1/admin/sessions", null, SessionList.class);
  }

  /** List up to {@code limit} of the tenant's sessions. */
  @Nonnull
  public Result<SessionList> listSessions(int limit) {
    return send("GET", "/v1/admin/sessions" + limit(limit), null, SessionList.class);
  }

  /** Revoke a session. It is marked revoked, never deleted. */
  @Nonnull
  public Result<Void> revokeSession(@Nonnull String sessionId) {
    return send("DELETE", "/v1/admin/sessions/" + encode(sessionId), null, Void.class);
  }

  /** Read the tenant's audit trail, most recent first. */
  @Nonnull
  public Result<AuditRecordList> listAudit() {
    return send("GET", "/v1/admin/audit", null, AuditRecordList.class);
  }

  /** Read up to {@code limit} records of the tenant's audit trail, most recent first. */
  @Nonnull
  public Result<AuditRecordList> listAudit(int limit) {
    return send("GET", "/v1/admin/audit" + limit(limit), null, AuditRecordList.class);
  }

  // --- transport ----------------------------------------------------------

  private <T> Result<T> send(String method, String path, @Nullable Object body, Class<T> type) {
    Map<String, String> requestHeaders = new LinkedHashMap<>();
    requestHeaders.put("Accept", ACCEPT);
    requestHeaders.putAll(headers);

    Transport.Response response;
    try {
      String json = null;
      if (body != null) {
        json = JSON.writeValueAsString(body);
        requestHeaders.put("Content-Type", "application/json");
      }
      URI uri = URI.create(baseUrl + path);
      response = transport.send(new Transport.Request(method, uri, requestHeaders, json, timeout));
    } catch (InterruptedException cause) {
      // Restore the flag: this method reports the interrupt, it does not consume it.
      Thread.currentThread().interrupt();
      return transportError(method + " " + path + " was interrupted", cause, null);
    } catch (IOException | RuntimeException cause) {
      return transportError(method + " " + path + " did not reach the service", cause, null);
    }

    int status = response.status();
    if (status == 204 || status == 205) {
      return new Result.Ok<>(null);
    }

    String text = response.body();
    JsonNode parsed;
    try {
      parsed = text.isEmpty() ? null : JSON.readTree(text);
    } catch (JsonProcessingException cause) {
      return transportError(
          method + " " + path + " returned " + status + " with a body that is not JSON", cause, status);
    }

    if (status >= 200 && status < 300) {
      if (type == Void.class) {
        return new Result.Ok<>(null);
      }
      if (parsed == null) {
        return transportError(method + " " + path + " returned " + status + " with no body", null, status);
      }
      try {
        return new Result.Ok<>(JSON.treeToValue(parsed, type));
      } catch (JsonProcessingException | IllegalArgumentException cause) {
        return transportError(
            method + " " + path + " returned " + status + " with a body this SDK could not read",
            cause,
            status);
      }
    }

    if (isProblem(parsed)) {
      try {
        Problem problem = JSON.treeToValue(parsed, Problem.class);
        return new Result.Err<>(new ZoikoTaxError(problem, parsed, retryAfter(response)));
      } catch (JsonProcessingException | IllegalArgumentException cause) {
        return transportError(
            method + " " + path + " returned " + status + " with a Problem this SDK could not read",
            cause,
            status);
      }
    }

    // A non-Problem error body is something between the client and the cell: a
    // load balancer, a proxy, a WAF. Reporting it as a ZoikoTaxError would
    // attribute it to the service and give it a reason code nobody registered.
    return transportError(
        method + " " + path + " returned " + status + " without a Problem Details body; "
            + "something between this client and the cell answered",
        null,
        status);
  }

  private static <T> Result<T> transportError(
      String message, @Nullable Throwable cause, @Nullable Integer status) {
    return new Result.Err<>(new ZoikoTaxTransportError(message, cause, status));
  }

  /**
   * Whether a body is a Problem.
   *
   * <p>It checks the two fields a caller acts on rather than validating the
   * whole document. A stricter check would reject a Problem carrying an
   * extension this SDK release predates, and ADR-0010 §2.6 makes additions the
   * normal case.
   */
  private static boolean isProblem(@Nullable JsonNode value) {
    return value != null
        && value.isObject()
        && value.path("ztx_reason_code").isTextual()
        && value.path("status").isNumber();
  }

  /** {@code Retry-After} in its delta-seconds form; anything else is ignored. */
  @Nullable
  private static Duration retryAfter(Transport.Response response) {
    String value = response.header("Retry-After");
    if (value == null || !value.trim().matches("\\d{1,9}")) {
      return null;
    }
    return Duration.ofSeconds(Long.parseLong(value.trim()));
  }

  private static String limit(int limit) {
    return "?limit=" + limit;
  }

  /**
   * Percent-encode a path segment as {@code encodeURIComponent} does, so that
   * an identifier containing {@code /} is one segment rather than a route.
   * {@code URLEncoder} is not used: it encodes form data, where a space is
   * {@code +}, and in a path {@code +} is a plus.
   */
  static String encode(String segment) {
    Objects.requireNonNull(segment, "path parameter");
    StringBuilder out = new StringBuilder(segment.length());
    for (byte b : segment.getBytes(StandardCharsets.UTF_8)) {
      int c = b & 0xff;
      if ((c >= 'A' && c <= 'Z')
          || (c >= 'a' && c <= 'z')
          || (c >= '0' && c <= '9')
          || "-_.!~*'()".indexOf(c) >= 0) {
        out.append((char) c);
      } else {
        out.append('%').append(Character.toUpperCase(Character.forDigit(c >> 4, 16)))
            .append(Character.toUpperCase(Character.forDigit(c & 0xf, 16)));
      }
    }
    return out.toString();
  }
}
