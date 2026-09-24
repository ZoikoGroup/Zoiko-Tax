/**
 * ZoikoTax Java SDK, usable from Kotlin.
 *
 * <p>The types in {@code com.zoikotax.sdk.model} are generated from the
 * contract and are not edited; {@code mvn verify} regenerates them and fails
 * the build on any drift, which is the same mechanism ADR-0010 §2.1 applies to
 * the generated server. What is hand-written is this package: a thin typed
 * layer over {@code java.net.http}, whose only runtime dependencies are Jackson
 * for JSON and the Jakarta nullability annotations.
 *
 * <p>Three decisions are worth stating, because each is a thing an SDK usually
 * does that this one deliberately does not.
 *
 * <p><b>An error is a value, not an exception.</b> Every method returns a
 * {@link com.zoikotax.sdk.Result} rather than throwing on a 4xx. A
 * {@link com.zoikotax.sdk.ZoikoTaxError} carries the Problem Details document
 * whole, and the field to branch on is {@code reasonCode} — a closed,
 * registered vocabulary that means the same thing in an error, in a decision
 * and in evidence (ADR-0016 §2.4). Nothing here matches on a title or a
 * message, and neither should a caller: both may be reworded without notice.
 *
 * <p><b>Nothing retries by itself.</b> {@code retryable} says whether retrying
 * unchanged could succeed, and the caller decides. An SDK that retried on its
 * own would, on the endpoints this surface is about to grow, submit a
 * transaction twice — and the thing that makes that safe is an
 * {@code Idempotency-Key} the caller chose (ADR-0013), not a backoff this
 * library picked.
 *
 * <p><b>No token handling.</b> The session is an opaque {@code HttpOnly}
 * cookie (ADR-0020). There is nothing to store, nothing to refresh and nothing
 * to leak, so this SDK has no credential store — each client keeps the cookie
 * in its own {@link java.net.CookieManager}, as a browser would, and gets out
 * of the way.
 */
package com.zoikotax.sdk;
