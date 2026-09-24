using System;
using System.Collections.Generic;
using System.Net;
using System.Net.Http;

namespace ZoikoTax.Sdk;

/// <summary>How to reach a cell.</summary>
public sealed class ZoikoTaxClientOptions
{
    /// <summary>
    /// The cell's base URL, such as <c>https://eu-west-1.zoikotax.com</c>. A
    /// trailing slash is tolerated. Required.
    /// </summary>
    public Uri? BaseUrl { get; set; }

    /// <summary>
    /// An <see cref="HttpClient"/> to send through. The client does not
    /// dispose it, and ignores its <see cref="HttpClient.BaseAddress"/>. It
    /// must keep cookies — the session is one — which a client from
    /// <c>IHttpClientFactory</c> does not do by default.
    /// </summary>
    /// <remarks>Set this or <see cref="Handler"/>, not both.</remarks>
    public HttpClient? HttpClient { get; set; }

    /// <summary>
    /// A handler to send through. Supplying it is how a test drives this client
    /// without a network, and how a server-side caller substitutes an
    /// instrumented pipeline. The client does not dispose it. It must keep
    /// cookies, for the same reason as <see cref="HttpClient"/>.
    /// </summary>
    /// <remarks>Set this or <see cref="HttpClient"/>, not both.</remarks>
    public HttpMessageHandler? Handler { get; set; }

    /// <summary>
    /// The cookie jar for the default handler. Unset, the client makes its own.
    /// Supplying one lets two clients for the same cell share a session, or a
    /// caller persist one; it is refused alongside <see cref="HttpClient"/> or
    /// <see cref="Handler"/>, which bring their own.
    /// </summary>
    public CookieContainer? Cookies { get; set; }

    /// <summary>
    /// Headers added to every request. Useful for a correlation header a
    /// platform requires; it is not where a credential goes, because there is
    /// no credential.
    /// </summary>
    public IDictionary<string, string> Headers { get; } = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);

    /// <summary>
    /// Per-request timeout. Unset means no timeout of this client's own, and
    /// the <see cref="System.Net.Http.HttpClient.Timeout"/> of whatever client
    /// sends the request applies. A timeout is a
    /// <see cref="ZoikoTaxTransportError"/>, not an exception.
    /// </summary>
    public TimeSpan? Timeout { get; set; }
}
