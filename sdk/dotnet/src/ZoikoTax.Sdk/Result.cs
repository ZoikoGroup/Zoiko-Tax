using System.Diagnostics.CodeAnalysis;

namespace ZoikoTax.Sdk;

/// <summary>
/// The result of a call that returns a value: the value, or the reason there
/// is none.
/// </summary>
/// <remarks>
/// A failed call is a value rather than an exception, because a 4xx is an
/// ordinary outcome a caller is expected to branch on — by
/// <see cref="ZoikoTaxError.ReasonCode"/>, never by message. <see cref="Unwrap"/>
/// is there for callers who prefer exceptions; it is a method on the result
/// rather than a client option, so the client has one behaviour and the choice
/// is visible at the call site.
/// </remarks>
/// <typeparam name="T">The value a successful call produces.</typeparam>
public sealed class Result<T>
{
    internal Result(T? data, ZoikoTaxException? error)
    {
        Data = data;
        Error = error;
    }

    /// <summary>Whether the call succeeded. When true, <see cref="Data"/> is set; when false, <see cref="Error"/> is.</summary>
    [MemberNotNullWhen(true, nameof(Data))]
    [MemberNotNullWhen(false, nameof(Error))]
    public bool IsOk => Error is null;

    /// <summary>The value, when the call succeeded.</summary>
    public T? Data { get; }

    /// <summary>
    /// Why the call failed: a <see cref="ZoikoTaxError"/> when the service
    /// answered with a Problem, a <see cref="ZoikoTaxTransportError"/> when it
    /// did not.
    /// </summary>
    public ZoikoTaxException? Error { get; }

    /// <summary>The value, or the error thrown.</summary>
    /// <example><code>var capabilities = (await client.GetCapabilitiesAsync()).Unwrap();</code></example>
    /// <exception cref="ZoikoTaxError">The service answered with a Problem.</exception>
    /// <exception cref="ZoikoTaxTransportError">The service did not answer.</exception>
    public T Unwrap() => IsOk ? Data : throw Error;
}

/// <summary>
/// The result of a call that returns nothing on success (a <c>204</c>): success,
/// or the reason it failed.
/// </summary>
/// <remarks>
/// A separate type from <see cref="Result{T}"/> rather than a
/// <c>Result&lt;Unit&gt;</c>, so that a caller never has to read a
/// <c>Data</c> that means nothing.
/// </remarks>
public sealed class Result
{
    private static readonly Result Success = new(null);

    private Result(ZoikoTaxException? error)
    {
        Error = error;
    }

    /// <summary>Whether the call succeeded. When false, <see cref="Error"/> is set.</summary>
    [MemberNotNullWhen(false, nameof(Error))]
    public bool IsOk => Error is null;

    /// <summary>
    /// Why the call failed: a <see cref="ZoikoTaxError"/> when the service
    /// answered with a Problem, a <see cref="ZoikoTaxTransportError"/> when it
    /// did not.
    /// </summary>
    public ZoikoTaxException? Error { get; }

    /// <summary>A successful result.</summary>
    public static Result Ok() => Success;

    /// <summary>A failed result.</summary>
    public static Result Fail(ZoikoTaxException error)
    {
        System.ArgumentNullException.ThrowIfNull(error);
        return new(error);
    }

    // The factories for Result<T> live here rather than on it, so a caller
    // writes Result.Ok(value) and the type argument is inferred — which is
    // also what a test double for this client needs.

    /// <summary>A successful result carrying <paramref name="data"/>.</summary>
    public static Result<T> Ok<T>(T data) => new(data, null);

    /// <summary>A failed result for a call that would have returned a <typeparamref name="T"/>.</summary>
    public static Result<T> Fail<T>(ZoikoTaxException error)
    {
        System.ArgumentNullException.ThrowIfNull(error);
        return new(default, error);
    }

    /// <summary>Returns on success, throws the error on failure.</summary>
    /// <exception cref="ZoikoTaxError">The service answered with a Problem.</exception>
    /// <exception cref="ZoikoTaxTransportError">The service did not answer.</exception>
    public void Unwrap()
    {
        if (!IsOk)
        {
            throw Error;
        }
    }
}
