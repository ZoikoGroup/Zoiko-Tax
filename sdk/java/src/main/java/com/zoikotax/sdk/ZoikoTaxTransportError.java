package com.zoikotax.sdk;

import jakarta.annotation.Nullable;

/**
 * A failure that produced no Problem document at all: a DNS failure, a TLS
 * failure, a timeout, an interrupted thread, a proxy that returned HTML.
 *
 * <p>It is a separate type because the caller's options differ. A
 * {@link ZoikoTaxError} is the service answering; this is not reaching it, and
 * the reason is outside anything the contract describes. Reporting a load
 * balancer's 502 as a {@code ZoikoTaxError} would attribute it to the service
 * and give it a reason code nobody registered.
 */
public final class ZoikoTaxTransportError extends ZoikoTaxFailure {

  private static final long serialVersionUID = 1L;

  private final @Nullable Integer status;

  ZoikoTaxTransportError(String message, @Nullable Throwable cause, @Nullable Integer status) {
    super(message, cause);
    this.status = status;
  }

  /** The HTTP status, where there was a response at all. */
  @Nullable
  public Integer getStatus() {
    return status;
  }
}
