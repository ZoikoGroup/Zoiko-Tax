using System;
using System.Text.Json;

namespace ZoikoTax.Sdk;

/// <summary>
/// Either kind of failure a call can produce. It exists so that a caller who
/// unwraps can write one <c>catch</c>; a caller who branches should match the
/// two concrete types, because what can be done about each differs.
/// </summary>
public abstract class ZoikoTaxException : Exception
{
    /// <summary>Creates a failure with a message and, optionally, what caused it.</summary>
    protected ZoikoTaxException(string message, Exception? innerException = null)
        : base(message, innerException)
    {
    }
}

/// <summary>
/// A failed request: the service answered, and the answer was an RFC 9457
/// Problem Details document.
/// </summary>
/// <remarks>
/// It is an <see cref="Exception"/> so a stack trace survives <see cref="Result{T}.Unwrap"/>,
/// and it carries the Problem document whole so nothing a caller might need is
/// lost in translation. Branch on <see cref="ReasonCode"/>, never on the message:
/// the title and detail may be reworded without notice (ADR-0016 §2.4).
/// </remarks>
public sealed class ZoikoTaxError : ZoikoTaxException
{
    /// <summary>Creates an error from a Problem document as received.</summary>
    /// <param name="problem">The Problem document.</param>
    /// <param name="raw">
    /// The same document as JSON, including any member this SDK release does
    /// not model. Omitted, it is re-serialized from <paramref name="problem"/>.
    /// </param>
    /// <param name="retryAfter">The <c>Retry-After</c> delay, where the server sent one.</param>
    public ZoikoTaxError(Problem problem, JsonElement? raw = null, TimeSpan? retryAfter = null)
        : base(MessageFor(problem))
    {
        Problem = problem;
        RawProblem = raw ?? JsonSerializer.SerializeToElement(problem, Wire.Options);
        RetryAfter = retryAfter;
    }

    /// <summary>
    /// The registered reason code. This is the field to branch on.
    /// </summary>
    /// <remarks>
    /// Deliberately a <see cref="string"/> rather than an enum: the register
    /// grows by addition (ADR-0010 §2.6), and an enum would make an SDK release
    /// a prerequisite for every new code — so an integration would break on an
    /// additive change, which is the one thing additive-only versioning
    /// promises will not happen.
    /// </remarks>
    public string ReasonCode => Problem.Ztx_reason_code;

    /// <summary>The HTTP status, for logging and for the cases where it is the clearer signal.</summary>
    public int Status => Problem.Status;

    /// <summary>
    /// Whether retrying this request unchanged could succeed. Only a transient
    /// failure is retryable; everything else fails identically.
    /// </summary>
    public bool Retryable => Problem.Ztx_retryable;

    /// <summary>This request's identifier. Quote it in a support request.</summary>
    public string? RequestId => Problem.Ztx_request_id;

    /// <summary>The contract field at fault, for a validation failure.</summary>
    public string? Field => Problem.Ztx_field;

    /// <summary>
    /// How long to wait before retrying, where the server said. Only the
    /// delta-seconds form of <c>Retry-After</c> is read; an HTTP-date would make
    /// the answer depend on this machine's clock.
    /// </summary>
    public TimeSpan? RetryAfter { get; }

    /// <summary>The Problem Details document as received, in the modelled form.</summary>
    public Problem Problem { get; }

    /// <summary>
    /// The Problem Details document as received, as JSON. A caller that needs
    /// an extension member this SDK release predates reads it here; the
    /// contract makes additions the normal case (ADR-0010 §2.6).
    /// </summary>
    public JsonElement RawProblem { get; }

    private static string MessageFor(Problem problem)
    {
        ArgumentNullException.ThrowIfNull(problem);
        return string.IsNullOrEmpty(problem.Detail) ? problem.Title : problem.Detail;
    }
}

/// <summary>
/// A failure that produced no Problem document at all: a DNS failure, a TLS
/// failure, a timeout, a proxy that returned HTML, a body that is not JSON.
/// </summary>
/// <remarks>
/// It is a separate type because the caller's options differ. A
/// <see cref="ZoikoTaxError"/> is the service answering; this is not reaching
/// it, or something between the caller and the cell answering instead, and
/// the reason is outside anything the contract describes. The underlying
/// exception, where there was one, is <see cref="Exception.InnerException"/>.
/// </remarks>
public sealed class ZoikoTaxTransportError : ZoikoTaxException
{
    /// <summary>Creates a transport error.</summary>
    /// <param name="message">What failed, naming the method and path.</param>
    /// <param name="status">The HTTP status, where there was a response at all.</param>
    /// <param name="innerException">What caused it, where something did.</param>
    public ZoikoTaxTransportError(string message, int? status = null, Exception? innerException = null)
        : base(message, innerException)
    {
        Status = status;
    }

    /// <summary>The HTTP status, where there was a response at all.</summary>
    public int? Status { get; }
}
