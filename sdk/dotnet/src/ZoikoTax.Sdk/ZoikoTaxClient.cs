// ZoikoTax .NET SDK.
//
// The model types in Generated/Models.g.cs are generated from the contract and
// are not edited; scripts/check.sh fails the build on any drift, which is the
// same mechanism ADR-0010 §2.1 applies to the generated server. What is
// hand-written is this file and its few neighbours: a thin layer over
// HttpClient, with no runtime dependencies beyond the shared framework.
//
// Three decisions are worth stating, because each is a thing an SDK usually
// does that this one deliberately does not.
//
// An error is a value, not an exception. Every method returns a Result rather
// than throwing on a 4xx. A ZoikoTaxError carries the Problem Details document
// whole, and the field to branch on is ReasonCode — a closed, registered
// vocabulary that means the same thing in an error, in a decision and in
// evidence (ADR-0016 §2.4). Nothing here matches on a title or a message, and
// neither should a caller: both may be reworded without notice.
//
// Nothing retries by itself. Retryable says whether retrying unchanged could
// succeed, and the caller decides. An SDK that retried on its own would, on the
// endpoints this surface is about to grow, submit a transaction twice — and the
// thing that makes that safe is an Idempotency-Key the caller chose
// (ADR-0013), not a backoff this library picked. For the same reason the
// client installs no resilience handler, and one should not be put in front of
// it.
//
// No token handling. The session is an opaque HttpOnly cookie (ADR-0020).
// There is nothing to store, nothing to refresh and nothing to leak, so this
// SDK has no credential store — the default handler keeps a CookieContainer,
// which does for a .NET process what `credentials: "include"` does for a
// browser, and the client gets out of the way.

using System;
using System.Globalization;
using System.Net;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

namespace ZoikoTax.Sdk;

/// <summary>
/// A client for one cell.
/// </summary>
/// <remarks>
/// <para>
/// One client per cell, because a cell is a residency boundary: data never
/// leaves its region, and a client that transparently failed over to another
/// cell would be moving a tenant's data across one.
/// </para>
/// <para>
/// Every method returns a <see cref="Result{T}"/> or a <see cref="Result"/>, and
/// none throws on a response the service sends. What does throw is a
/// programming error (a <c>null</c> argument) and cancellation through the
/// caller's own <see cref="CancellationToken"/>, which surfaces as
/// <see cref="OperationCanceledException"/> as it does everywhere else in .NET.
/// </para>
/// <para>Thread-safe: one instance may serve concurrent calls.</para>
/// </remarks>
public sealed class ZoikoTaxClient : IDisposable
{
    // Problem Details first, because an error is the response whose shape
    // this client most needs to be sure of.
    private const string Accept = "application/problem+json, application/json";

    private readonly string _baseUrl;
    private readonly HttpClient _http;
    private readonly bool _ownsHttp;
    private readonly (string Name, string Value)[] _headers;
    private readonly TimeSpan? _timeout;

    /// <summary>Creates a client for the cell at <paramref name="baseUrl"/>, with the default handler.</summary>
    /// <param name="baseUrl">The cell's base URL, such as <c>https://eu-west-1.zoikotax.com</c>.</param>
    public ZoikoTaxClient(Uri baseUrl)
        : this(new ZoikoTaxClientOptions { BaseUrl = baseUrl })
    {
    }

    /// <summary>Creates a client for a cell.</summary>
    /// <exception cref="ArgumentException">The options do not describe a usable client.</exception>
    public ZoikoTaxClient(ZoikoTaxClientOptions options)
    {
        ArgumentNullException.ThrowIfNull(options);

        var baseUrl = options.BaseUrl;
        if (baseUrl is null)
        {
            throw new ArgumentException("ZoikoTaxClient: BaseUrl is required", nameof(options));
        }

        if (!baseUrl.IsAbsoluteUri || (baseUrl.Scheme != Uri.UriSchemeHttps && baseUrl.Scheme != Uri.UriSchemeHttp))
        {
            throw new ArgumentException($"ZoikoTaxClient: BaseUrl must be an absolute http(s) URL, not '{baseUrl}'", nameof(options));
        }

        if (baseUrl.Query.Length > 0 || baseUrl.Fragment.Length > 0)
        {
            throw new ArgumentException("ZoikoTaxClient: BaseUrl must not carry a query or a fragment", nameof(options));
        }

        if (options.HttpClient is not null && options.Handler is not null)
        {
            throw new ArgumentException("ZoikoTaxClient: set HttpClient or Handler, not both", nameof(options));
        }

        if (options.Cookies is not null && (options.HttpClient is not null || options.Handler is not null))
        {
            // Silently ignoring it would be worse: the caller would believe a
            // session was shared that is not.
            throw new ArgumentException("ZoikoTaxClient: Cookies applies to the default handler only; a supplied HttpClient or Handler keeps its own", nameof(options));
        }

        _baseUrl = baseUrl.AbsoluteUri.TrimEnd('/');
        _timeout = options.Timeout;
        _headers = ValidateHeaders(options);

        if (options.HttpClient is not null)
        {
            _http = options.HttpClient;
            _ownsHttp = false;
        }
        else if (options.Handler is not null)
        {
            _http = new HttpClient(options.Handler, disposeHandler: false);
            _ownsHttp = true;
        }
        else
        {
            Cookies = options.Cookies ?? new CookieContainer();
            var handler = new SocketsHttpHandler
            {
                // The session is an HttpOnly cookie. Without a jar every call
                // after sign-in is UNAUTHENTICATED, for a reason that looks
                // like a server problem.
                UseCookies = true,
                CookieContainer = Cookies,
                // A long-lived client otherwise holds a connection to an
                // address DNS has since moved away from.
                PooledConnectionLifetime = TimeSpan.FromMinutes(5),
            };
            _http = new HttpClient(handler, disposeHandler: true);
            _ownsHttp = true;
        }
    }

    /// <summary>
    /// The cookie jar holding the session, when this client uses its default
    /// handler; <c>null</c> when an <see cref="HttpClient"/> or handler was
    /// supplied, which keeps its own.
    /// </summary>
    public CookieContainer? Cookies { get; }

    // --- discovery ----------------------------------------------------------

    /// <summary>Report this deployment's effective capability.</summary>
    /// <remarks>
    /// Call it before treating any figure as authoritative:
    /// <see cref="Capabilities.Authoritative"/> is false until A4, and a
    /// deployment that reports false produces advisory figures that must not
    /// be filed.
    /// </remarks>
    public Task<Result<Capabilities>> GetCapabilitiesAsync(CancellationToken cancellationToken = default)
        => SendAsync<Capabilities>(HttpMethod.Get, "/v1/capabilities", null, cancellationToken);

    // --- authentication -----------------------------------------------------

    /// <summary>Open a session. The response carries no token; the cookie is the session.</summary>
    public Task<Result<Session>> SignInAsync(SignInRequest body, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(body);
        return SendAsync<Session>(HttpMethod.Post, "/v1/auth/sign-in", body, cancellationToken);
    }

    /// <summary>End the current session.</summary>
    public Task<Result> SignOutAsync(CancellationToken cancellationToken = default)
        => SendAsync(HttpMethod.Post, "/v1/auth/sign-out", null, cancellationToken);

    /// <summary>Who am I, and until when.</summary>
    public Task<Result<Session>> GetSessionAsync(CancellationToken cancellationToken = default)
        => SendAsync<Session>(HttpMethod.Get, "/v1/auth/session", null, cancellationToken);

    /// <summary>Change the current subject's password.</summary>
    /// <remarks>
    /// Every session is revoked, including this one. Expect the next call to
    /// fail with <c>UNAUTHENTICATED</c>, and send the user to sign in.
    /// </remarks>
    public Task<Result> ChangePasswordAsync(ChangePasswordRequest body, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(body);
        return SendAsync(HttpMethod.Post, "/v1/auth/password", body, cancellationToken);
    }

    // --- administration -----------------------------------------------------

    /// <summary>The authenticated subject's tenant.</summary>
    public Task<Result<Tenant>> GetTenantAsync(CancellationToken cancellationToken = default)
        => SendAsync<Tenant>(HttpMethod.Get, "/v1/admin/tenant", null, cancellationToken);

    /// <summary>List the tenant's users.</summary>
    /// <param name="limit">The most to return. Omitted, the server's default applies; the server caps it either way.</param>
    /// <param name="cancellationToken">Cancels the request.</param>
    public Task<Result<UserList>> ListUsersAsync(int? limit = null, CancellationToken cancellationToken = default)
        => SendAsync<UserList>(HttpMethod.Get, "/v1/admin/users" + Query(limit), null, cancellationToken);

    /// <summary>Create a user. Omitting the password creates an <c>INVITED</c> user.</summary>
    public Task<Result<User>> CreateUserAsync(CreateUserRequest body, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(body);
        return SendAsync<User>(HttpMethod.Post, "/v1/admin/users", body, cancellationToken);
    }

    /// <summary>Enable or disable a user. Disabling revokes their sessions.</summary>
    public Task<Result> SetUserStatusAsync(string userId, UserStatus status, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(userId);
        return SendAsync(
            HttpMethod.Post,
            $"/v1/admin/users/{Segment(userId)}/status",
            new SetUserStatusRequest { Status = status },
            cancellationToken);
    }

    /// <summary>Grant a role. Granting one the user already holds is not an error.</summary>
    public Task<Result> GrantRoleAsync(string userId, Role role, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(userId);
        return SendAsync(
            HttpMethod.Post,
            $"/v1/admin/users/{Segment(userId)}/roles",
            new RoleRequest { Role = role },
            cancellationToken);
    }

    /// <summary>Revoke a role. Revoking the last <c>ADMIN</c> is refused.</summary>
    public Task<Result> RevokeRoleAsync(string userId, Role role, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(userId);
        return SendAsync(
            HttpMethod.Delete,
            $"/v1/admin/users/{Segment(userId)}/roles/{Segment(Wire.Name(role))}",
            null,
            cancellationToken);
    }

    /// <summary>List the tenant's sessions. <see cref="SessionSummary.Current"/> marks the caller's own.</summary>
    /// <param name="limit">The most to return. Omitted, the server's default applies; the server caps it either way.</param>
    /// <param name="cancellationToken">Cancels the request.</param>
    public Task<Result<SessionList>> ListSessionsAsync(int? limit = null, CancellationToken cancellationToken = default)
        => SendAsync<SessionList>(HttpMethod.Get, "/v1/admin/sessions" + Query(limit), null, cancellationToken);

    /// <summary>Revoke a session. It is marked revoked, never deleted.</summary>
    public Task<Result> RevokeSessionAsync(string sessionId, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(sessionId);
        return SendAsync(HttpMethod.Delete, $"/v1/admin/sessions/{Segment(sessionId)}", null, cancellationToken);
    }

    /// <summary>Read the tenant's audit trail, most recent first.</summary>
    /// <param name="limit">The most to return. Omitted, the server's default applies; the server caps it either way.</param>
    /// <param name="cancellationToken">Cancels the request.</param>
    public Task<Result<AuditRecordList>> ListAuditAsync(int? limit = null, CancellationToken cancellationToken = default)
        => SendAsync<AuditRecordList>(HttpMethod.Get, "/v1/admin/audit" + Query(limit), null, cancellationToken);

    /// <summary>Releases the client's own <see cref="HttpClient"/>. A supplied one is left alone.</summary>
    public void Dispose()
    {
        if (_ownsHttp)
        {
            _http.Dispose();
        }
    }

    // --- transport ----------------------------------------------------------

    private async Task<Result<T>> SendAsync<T>(HttpMethod method, string path, object? body, CancellationToken cancellationToken)
        where T : class
    {
        var exchange = await ExchangeAsync(method, path, body, cancellationToken).ConfigureAwait(false);
        if (exchange.Error is not null)
        {
            return Result.Fail<T>(exchange.Error);
        }

        if (exchange.Body is not JsonElement json)
        {
            return Result.Fail<T>(new ZoikoTaxTransportError(
                $"{method} {path} returned {exchange.Status} with no body where the contract declares one",
                exchange.Status));
        }

        T? data;
        try
        {
            data = json.Deserialize<T>(Wire.Options);
        }
        catch (JsonException e)
        {
            return Result.Fail<T>(new ZoikoTaxTransportError(
                $"{method} {path} returned {exchange.Status} with a body this SDK release cannot read as {typeof(T).Name}",
                exchange.Status,
                e));
        }

        return data is null
            ? Result.Fail<T>(new ZoikoTaxTransportError($"{method} {path} returned {exchange.Status} with a null body", exchange.Status))
            : Result.Ok(data);
    }

    private async Task<Result> SendAsync(HttpMethod method, string path, object? body, CancellationToken cancellationToken)
    {
        var exchange = await ExchangeAsync(method, path, body, cancellationToken).ConfigureAwait(false);
        return exchange.Error is null ? Result.Ok() : Result.Fail(exchange.Error);
    }

    private async Task<Exchange> ExchangeAsync(HttpMethod method, string path, object? body, CancellationToken cancellationToken)
    {
        using var request = new HttpRequestMessage(method, new Uri(_baseUrl + path, UriKind.Absolute));
        request.Headers.TryAddWithoutValidation("Accept", Accept);
        foreach (var (name, value) in _headers)
        {
            request.Headers.Remove(name);
            request.Headers.TryAddWithoutValidation(name, value);
        }

        if (body is not null)
        {
            request.Content = new ByteArrayContent(JsonSerializer.SerializeToUtf8Bytes(body, body.GetType(), Wire.Options));
            request.Content.Headers.ContentType = new MediaTypeHeaderValue("application/json");
        }

        using var deadline = _timeout is null ? null : CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        deadline?.CancelAfter(_timeout!.Value);
        var token = deadline?.Token ?? cancellationToken;

        int status;
        byte[] bytes;
        TimeSpan? retryAfter;
        HttpResponseMessage? response = null;
        try
        {
            response = await _http.SendAsync(request, HttpCompletionOption.ResponseContentRead, token).ConfigureAwait(false);
            status = (int)response.StatusCode;
            retryAfter = response.Headers.RetryAfter?.Delta;
            bytes = await response.Content.ReadAsByteArrayAsync(token).ConfigureAwait(false);
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
        {
            // The caller asked for this. It is not a failure of the call, and
            // .NET's convention for it is the exception, not a value.
            throw;
        }
        catch (OperationCanceledException e)
        {
            var why = deadline is not null && deadline.IsCancellationRequested
                ? $"timed out after {_timeout!.Value.TotalMilliseconds.ToString(CultureInfo.InvariantCulture)}ms"
                : "timed out";
            return Exchange.Failed(new ZoikoTaxTransportError($"{method} {path} {why}", (int?)response?.StatusCode, e));
        }
        catch (HttpRequestException e)
        {
            return Exchange.Failed(new ZoikoTaxTransportError($"{method} {path} did not reach the service", (int?)response?.StatusCode, e));
        }
        finally
        {
            response?.Dispose();
        }

        if (status is 204 or 205)
        {
            return Exchange.Succeeded(status, null);
        }

        JsonElement? parsed = null;
        if (bytes.Length > 0)
        {
            try
            {
                using var document = JsonDocument.Parse(bytes);
                parsed = document.RootElement.Clone();
            }
            catch (JsonException e)
            {
                return Exchange.Failed(new ZoikoTaxTransportError(
                    $"{method} {path} returned {status} with a body that is not JSON", status, e));
            }
        }

        if (status is >= 200 and < 300)
        {
            return Exchange.Succeeded(status, parsed);
        }

        if (parsed is JsonElement candidate && IsProblem(candidate))
        {
            try
            {
                var problem = candidate.Deserialize<Problem>(Wire.Options);
                if (problem is not null)
                {
                    return Exchange.Failed(new ZoikoTaxError(problem, candidate, retryAfter));
                }
            }
            catch (JsonException)
            {
                // Falls through: something shaped almost like a Problem is not
                // one, and is reported as what it is.
            }
        }

        // A non-Problem error body is something between the client and the
        // cell: a load balancer, a proxy, a WAF. Reporting it as a
        // ZoikoTaxError would attribute it to the service and give it a reason
        // code nobody registered.
        return Exchange.Failed(new ZoikoTaxTransportError(
            $"{method} {path} returned {status} without a Problem Details body; something between this client and the cell answered",
            status));
    }

    /// <summary>
    /// Whether a body is a Problem. It checks the two members a caller acts on
    /// rather than validating the whole document: a stricter check would reject
    /// a Problem carrying an extension this SDK release predates, and ADR-0010
    /// §2.6 makes additions the normal case.
    /// </summary>
    private static bool IsProblem(JsonElement body)
        => body.ValueKind == JsonValueKind.Object
            && body.TryGetProperty("ztx_reason_code", out var code) && code.ValueKind == JsonValueKind.String
            && body.TryGetProperty("status", out var status) && status.ValueKind == JsonValueKind.Number;

    /// <summary>
    /// A path segment, escaped so that an identifier can only ever be one
    /// segment: <c>/</c> becomes <c>%2F</c>, so <c>zts_01/../../admin</c> cannot
    /// walk to another route.
    /// </summary>
    private static string Segment(string value) => Uri.EscapeDataString(value);

    private static string Query(int? limit)
        => limit is int value ? "?limit=" + value.ToString(CultureInfo.InvariantCulture) : "";

    private static (string Name, string Value)[] ValidateHeaders(ZoikoTaxClientOptions options)
    {
        var headers = new (string Name, string Value)[options.Headers.Count];
        using var probe = new HttpRequestMessage();
        var i = 0;
        foreach (var (name, value) in options.Headers)
        {
            // Refused here rather than dropped at send time: a content header
            // (Content-Type) cannot go on every request, and a header that
            // vanishes without a word is one a platform team will spend a day
            // looking for.
            if (!probe.Headers.TryAddWithoutValidation(name, value))
            {
                throw new ArgumentException($"ZoikoTaxClient: '{name}' cannot be a per-request header", nameof(options));
            }

            headers[i++] = (name, value);
        }

        return headers;
    }

    private readonly record struct Exchange(int Status, JsonElement? Body, ZoikoTaxException? Error)
    {
        public static Exchange Succeeded(int status, JsonElement? body) => new(status, body, null);

        public static Exchange Failed(ZoikoTaxException error) => new(0, null, error);
    }
}
