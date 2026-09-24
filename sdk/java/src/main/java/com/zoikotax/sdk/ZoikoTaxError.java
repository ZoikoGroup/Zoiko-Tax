package com.zoikotax.sdk;

import com.fasterxml.jackson.databind.JsonNode;
import com.zoikotax.sdk.model.Problem;
import jakarta.annotation.Nonnull;
import jakarta.annotation.Nullable;
import java.time.Duration;

/**
 * A failed request: the service answered, with a Problem Details document
 * (RFC 9457, ADR-0016 §2.5).
 *
 * <p>The field to branch on is {@link #getReasonCode()} — a closed, registered
 * vocabulary that means the same thing in an error, in a decision and in
 * evidence (ADR-0016 §2.4). Never match on the title, the detail or
 * {@link #getMessage()}; all three may be reworded without notice.
 *
 * <p>It carries the Problem document whole, twice: as the generated
 * {@link Problem} model, and as the JSON tree it was read from. The tree is
 * there because {@code /v1} grows by addition (ADR-0010 §2.6) — an extension
 * this SDK release predates is not in the model, and a caller who needs it can
 * still reach it.
 */
public final class ZoikoTaxError extends ZoikoTaxFailure {

  private static final long serialVersionUID = 1L;

  private final transient Problem problem;
  private final transient JsonNode problemDocument;
  private final String reasonCode;
  private final int status;
  private final boolean retryable;
  private final @Nullable String requestId;
  private final @Nullable String field;
  private final @Nullable Duration retryAfter;

  ZoikoTaxError(
      @Nonnull Problem problem, @Nonnull JsonNode problemDocument, @Nullable Duration retryAfter) {
    super(problem.getDetail() != null ? problem.getDetail() : String.valueOf(problem.getTitle()), null);
    this.problem = problem;
    this.problemDocument = problemDocument;
    this.reasonCode = problem.getZtxReasonCode();
    this.status = problem.getStatus();
    this.retryable = Boolean.TRUE.equals(problem.getZtxRetryable());
    this.requestId = problem.getZtxRequestId();
    this.field = problem.getZtxField();
    this.retryAfter = retryAfter;
  }

  /**
   * The registered reason code. This is the field to branch on.
   *
   * <p>A {@code String} rather than an enum, deliberately: the register grows
   * by addition, and an enum would make an SDK release a prerequisite for
   * every new code — so an integration would break on an additive change,
   * which is the one thing additive-only versioning promises will not happen.
   */
  @Nonnull
  public String getReasonCode() {
    return reasonCode;
  }

  /** The HTTP status, for logging and for the cases where it is the clearer signal. */
  public int getStatus() {
    return status;
  }

  /**
   * Whether retrying this request unchanged could succeed. Only a transient
   * failure is retryable; everything else fails identically. Nothing in this
   * SDK acts on it: the caller decides.
   */
  public boolean isRetryable() {
    return retryable;
  }

  /** This request's identifier. Quote it in a support request. */
  @Nullable
  public String getRequestId() {
    return requestId;
  }

  /** The contract field at fault, for a validation failure. A name, never a value. */
  @Nullable
  public String getField() {
    return field;
  }

  /**
   * How long to wait before retrying, where the server said, from the
   * {@code Retry-After} header. Only the delta-seconds form is read; the
   * contract declares no other.
   */
  @Nullable
  public Duration getRetryAfter() {
    return retryAfter;
  }

  /** The Problem Details document, as the generated model. */
  @Nonnull
  public Problem getProblem() {
    return problem;
  }

  /**
   * The Problem Details document as received, including any extension this
   * SDK release does not model.
   */
  @Nonnull
  public JsonNode getProblemDocument() {
    return problemDocument;
  }
}
