package com.zoikotax.sdk;

import jakarta.annotation.Nonnull;
import jakarta.annotation.Nullable;
import java.time.Duration;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.Objects;

/**
 * How to reach a cell.
 *
 * <p>A record with {@code with…} methods rather than a builder, so that it is
 * one expression in Java and in Kotlin alike:
 *
 * <pre>{@code
 * ClientOptions.of("https://eu-west-1.zoikotax.com").withTimeout(Duration.ofSeconds(10))
 * }</pre>
 *
 * @param baseUrl the cell's base URL, such as {@code https://eu-west-1.zoikotax.com}.
 *     A trailing slash is tolerated.
 * @param transport how requests are sent. Null means {@link Transport#newDefault()},
 *     created once per client so that each client has its own cookie jar. Supplying
 *     one is how a test drives the client without a network, and how a server-side
 *     caller substitutes an instrumented client.
 * @param headers headers added to every request. Useful for a correlation header a
 *     platform requires; it is not where a credential goes, because there is no
 *     credential.
 * @param timeout per-request timeout. Null means no client-side timeout, and the
 *     transport's default applies.
 */
public record ClientOptions(
    @Nonnull String baseUrl,
    @Nullable Transport transport,
    @Nonnull Map<String, String> headers,
    @Nullable Duration timeout) {

  public ClientOptions {
    Objects.requireNonNull(baseUrl, "baseUrl");
    headers =
        headers == null
            ? Map.of()
            : Collections.unmodifiableMap(new LinkedHashMap<>(headers));
  }

  /** Options for a cell, with the default transport, no extra headers and no timeout. */
  @Nonnull
  public static ClientOptions of(@Nonnull String baseUrl) {
    return new ClientOptions(baseUrl, null, Map.of(), null);
  }

  /** These options with a different transport. */
  @Nonnull
  public ClientOptions withTransport(@Nullable Transport transport) {
    return new ClientOptions(baseUrl, transport, headers, timeout);
  }

  /** These options with a different set of extra headers. */
  @Nonnull
  public ClientOptions withHeaders(@Nonnull Map<String, String> headers) {
    return new ClientOptions(baseUrl, transport, headers, timeout);
  }

  /** These options with a different per-request timeout. */
  @Nonnull
  public ClientOptions withTimeout(@Nullable Duration timeout) {
    return new ClientOptions(baseUrl, transport, headers, timeout);
  }
}
