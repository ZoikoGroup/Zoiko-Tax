package com.zoikotax.sdk;

import jakarta.annotation.Nullable;

/**
 * Why a call produced no value: the service said no ({@link ZoikoTaxError}),
 * or the service was not reached ({@link ZoikoTaxTransportError}).
 *
 * <p>Sealed, so a {@code switch} or a Kotlin {@code when} over the two can be
 * exhaustive. It is an exception so that a stack trace survives and so that
 * {@link Result#unwrap()} can throw it as it is; the client itself never
 * throws one. It is unchecked because a checked exception would make every
 * {@code unwrap()} call site in Java carry a {@code throws} clause for a choice
 * the caller already made by calling {@code unwrap()}.
 */
public abstract sealed class ZoikoTaxFailure extends RuntimeException
    permits ZoikoTaxError, ZoikoTaxTransportError {

  private static final long serialVersionUID = 1L;

  ZoikoTaxFailure(String message, @Nullable Throwable cause) {
    super(message, cause);
  }
}
