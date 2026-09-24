package com.zoikotax.sdk;

import jakarta.annotation.Nonnull;
import jakarta.annotation.Nullable;
import java.io.IOException;
import java.net.CookieManager;
import java.net.CookiePolicy;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.TreeMap;

/**
 * How a request reaches a cell.
 *
 * <p>This is the seam a test drives the client through without a network, and
 * the one a server-side caller uses to substitute an instrumented client. It is
 * a single method rather than {@link HttpClient} itself because {@code
 * HttpClient} is an abstract class with a dozen methods a stub would have to
 * fake; this is a lambda.
 *
 * <p>An implementation throws for a request that produced no response at all.
 * Any response, whatever its status, is returned: deciding what a 4xx means is
 * the client's job, not the transport's.
 */
@FunctionalInterface
public interface Transport {

  /**
   * Send one request and return its response.
   *
   * @throws IOException if no response was received
   * @throws InterruptedException if the calling thread was interrupted while waiting
   */
  @Nonnull
  Response send(@Nonnull Request request) throws IOException, InterruptedException;

  /**
   * A request as the client built it.
   *
   * @param method the HTTP method
   * @param uri the absolute URI, path parameters already encoded
   * @param headers the request headers
   * @param body the JSON body, or null for a request without one
   * @param timeout the per-request timeout, or null for none
   */
  record Request(
      @Nonnull String method,
      @Nonnull URI uri,
      @Nonnull Map<String, String> headers,
      @Nullable String body,
      @Nullable Duration timeout) {

    public Request {
      Objects.requireNonNull(method, "method");
      Objects.requireNonNull(uri, "uri");
      headers = Collections.unmodifiableMap(new LinkedHashMap<>(headers));
    }

    /** A header by name, ignoring case as HTTP does, or null if absent. */
    @Nullable
    public String header(@Nonnull String name) {
      for (Map.Entry<String, String> e : headers.entrySet()) {
        if (e.getKey().equalsIgnoreCase(name)) {
          return e.getValue();
        }
      }
      return null;
    }
  }

  /**
   * A response as received.
   *
   * @param status the HTTP status
   * @param headers the response headers; names are compared ignoring case
   * @param body the body decoded as UTF-8, empty when there was none
   */
  record Response(
      int status, @Nonnull Map<String, List<String>> headers, @Nonnull String body) {

    public Response {
      TreeMap<String, List<String>> folded = new TreeMap<>(String.CASE_INSENSITIVE_ORDER);
      headers.forEach((k, v) -> folded.put(k, List.copyOf(v)));
      headers = Collections.unmodifiableSortedMap(folded);
      body = body == null ? "" : body;
    }

    /** A response with a body and a single-valued header map, for tests and simple transports. */
    @Nonnull
    public static Response of(int status, @Nonnull Map<String, String> headers, @Nullable String body) {
      TreeMap<String, List<String>> multi = new TreeMap<>(String.CASE_INSENSITIVE_ORDER);
      headers.forEach((k, v) -> multi.put(k, List.of(v)));
      return new Response(status, multi, body == null ? "" : body);
    }

    /** The first value of a header, ignoring case, or null if absent. */
    @Nullable
    public String header(@Nonnull String name) {
      List<String> values = headers.get(name);
      return values == null || values.isEmpty() ? null : values.get(0);
    }
  }

  /**
   * The default transport: a new {@link HttpClient} with its own cookie jar.
   *
   * <p>The session is an opaque {@code HttpOnly} cookie (ADR-0020). A browser
   * keeps it for you; a JVM does not, and without a cookie handler every call
   * after sign-in is {@code UNAUTHENTICATED} for a reason that looks like a
   * server problem. The jar is per transport — so per client — because a
   * cookie jar shared across clients is a session shared across cells.
   *
   * <p>Redirects are not followed. A redirect to another origin would move a
   * tenant's request, cookie included, out of the cell the client was built
   * for, and a cell is a residency boundary.
   */
  @Nonnull
  static Transport newDefault() {
    HttpClient client =
        HttpClient.newBuilder()
            .cookieHandler(new CookieManager(null, CookiePolicy.ACCEPT_ORIGINAL_SERVER))
            .followRedirects(HttpClient.Redirect.NEVER)
            .build();
    return of(client);
  }

  /**
   * A transport over a caller-supplied {@link HttpClient}.
   *
   * <p>The client must have a cookie handler, or the session will not
   * survive past sign-in. Share one only between ZoikoTax clients for the same
   * cell.
   */
  @Nonnull
  static Transport of(@Nonnull HttpClient client) {
    Objects.requireNonNull(client, "client");
    return request -> {
      HttpRequest.BodyPublisher publisher =
          request.body() == null
              ? HttpRequest.BodyPublishers.noBody()
              : HttpRequest.BodyPublishers.ofString(request.body(), StandardCharsets.UTF_8);
      HttpRequest.Builder builder =
          HttpRequest.newBuilder(request.uri()).method(request.method(), publisher);
      request.headers().forEach(builder::header);
      if (request.timeout() != null) {
        builder.timeout(request.timeout());
      }
      HttpResponse<String> response =
          client.send(builder.build(), HttpResponse.BodyHandlers.ofString(StandardCharsets.UTF_8));
      return new Response(response.statusCode(), response.headers().map(), response.body());
    };
  }
}
