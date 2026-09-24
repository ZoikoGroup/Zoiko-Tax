package com.zoikotax.sdk;

import jakarta.annotation.Nonnull;
import jakarta.annotation.Nullable;
import java.util.Objects;

/**
 * The result of a call: the value, or the reason there is none.
 *
 * <p>Every client method returns one of these rather than throwing on a 4xx,
 * because a refused request is an ordinary outcome of calling a tax API and
 * the caller should have to look at it. Sealed, so it can be matched
 * exhaustively:
 *
 * <pre>{@code
 * if (result instanceof Result.Err<Capabilities> err
 *     && err.error() instanceof ZoikoTaxError e) {
 *   log.warn("{} {}", e.getReasonCode(), e.getRequestId());
 * }
 * }</pre>
 *
 * <p>In Kotlin, import it explicitly ({@code import com.zoikotax.sdk.Result});
 * an explicit import takes precedence over the standard library's
 * {@code kotlin.Result}.
 *
 * @param <T> the value's type; {@link Void} for an operation with no body
 */
public sealed interface Result<T> permits Result.Ok, Result.Err {

  /** Whether this is a value rather than a failure. */
  boolean isOk();

  /**
   * The value, or throw the failure.
   *
   * <p>For callers that prefer exceptions, and for tests. It is here rather
   * than a client option, so that the client has one behaviour and the choice
   * is visible at the call site.
   *
   * <pre>{@code
   * Capabilities capabilities = client.getCapabilities().unwrap();
   * }</pre>
   *
   * @throws ZoikoTaxError if the service answered with a Problem
   * @throws ZoikoTaxTransportError if the service was not reached
   */
  T unwrap();

  /** The value, or null if this is a failure (or an operation with no body). */
  @Nullable
  T valueOrNull();

  /** The failure, or null if this is a value. */
  @Nullable
  ZoikoTaxFailure errorOrNull();

  /** A value. For an operation with no response body, {@code value} is null. */
  record Ok<T>(@Nullable T value) implements Result<T> {
    @Override
    public boolean isOk() {
      return true;
    }

    @Override
    public T unwrap() {
      return value;
    }

    @Override
    @Nullable
    public T valueOrNull() {
      return value;
    }

    @Override
    @Nullable
    public ZoikoTaxFailure errorOrNull() {
      return null;
    }
  }

  /** A failure: {@link ZoikoTaxError} or {@link ZoikoTaxTransportError}. */
  record Err<T>(@Nonnull ZoikoTaxFailure error) implements Result<T> {
    public Err {
      Objects.requireNonNull(error, "error");
    }

    @Override
    public boolean isOk() {
      return false;
    }

    @Override
    public T unwrap() {
      throw error;
    }

    @Override
    @Nullable
    public T valueOrNull() {
      return null;
    }

    @Override
    @Nonnull
    public ZoikoTaxFailure errorOrNull() {
      return error;
    }
  }
}
