// The SDK as a Kotlin caller writes it.
//
// This file is compiled by kotlinc on every build, so the build fails if the
// Java API stops reading well from Kotlin. The `when` expressions have no
// `else`: they compile only while Result and ZoikoTaxFailure stay sealed. The
// getters are annotated jakarta.annotation.Nullable / Nonnull, which kotlinc
// reads as real nullability — `error.requestId.length` does not compile; it
// has to be `error.requestId?.length`.

package com.zoikotax.sdk

// Explicit, so it wins over the standard library's kotlin.Result.
import com.zoikotax.sdk.Result
import com.zoikotax.sdk.model.Capabilities
import com.zoikotax.sdk.model.Role
import java.time.Duration
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertNull
import org.junit.jupiter.api.Test

class KotlinUsageTest {

  private fun respond(status: Int, body: String, headers: Map<String, String> = emptyMap()) =
      ZoikoTaxClient(
          ClientOptions.of("https://eu-west-1.zoikotax.com")
              .withTransport { Transport.Response.of(status, headers, body) })

  /** Exhaustive over both sealed hierarchies, with no `else`. */
  private fun <T> describe(result: Result<T>): String =
      when (result) {
        is Result.Ok -> "ok"
        is Result.Err ->
            when (val error = result.error) {
              is ZoikoTaxError -> "${error.reasonCode} retryable=${error.isRetryable}"
              is ZoikoTaxTransportError -> "transport ${error.status}"
            }
      }

  @Test
  fun anErrorIsAValueAKotlinCallerCanMatchOn() {
    val client =
        respond(
            503,
            """{"type":"https://errors.zoikotax.com/v1/database-unavailable","title":"Database unavailable",
               "status":503,"ztx_reason_code":"DATABASE_UNAVAILABLE","ztx_retryable":true}""",
            mapOf("Retry-After" to "2"))

    val result = client.grantRole("ztu_01JBQ0S9C3X8Q1H6M2KX5R7F4C", Role.AUDITOR)

    assertEquals("DATABASE_UNAVAILABLE retryable=true", describe(result))
    val error = result.errorOrNull() as ZoikoTaxError
    // Nullable in Kotlin because it is annotated nullable in Java.
    val requestId: String? = error.requestId
    assertNull(requestId)
    assertEquals(Duration.ofSeconds(2), error.retryAfter)
  }

  @Test
  fun aSuccessReadsAsProperties() {
    val client =
        respond(
            200,
            """{"cell":"eu-west-1","region":"eu-west","environment":"production",
               "trains":{"app":"0.4.0","content":"2026.09.1","ai":"0.1.0","adapter":"0.2.0",
                         "infra":"0.3.0","schema":"0.1.0","migration":"6"},
               "canonProfile":"canon/v1","authoritative":false,"reasonCodes":["FORBIDDEN"]}""")

    val capabilities: Capabilities = client.getCapabilities().unwrap()

    assertEquals("eu-west-1", capabilities.cell)
    assertEquals(false, capabilities.authoritative)
    assertEquals("6", capabilities.trains.migration)
  }

  @Test
  fun aProxyPageIsATransportError() {
    val result = respond(502, "<html>502 Bad Gateway</html>").getSession()

    assertEquals("transport 502", describe(result))
  }
}
