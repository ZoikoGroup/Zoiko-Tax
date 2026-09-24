// SDK behaviour, against a stub HttpMessageHandler.
//
// These test the decisions in the client's own header comment — that an error
// is a value, that nothing retries by itself, that a non-Problem error body is
// not attributed to the service. They are not contract tests: what the
// *server* does is asserted in Go, against the same contract file
// (backend/internal/transport/http/contract_test.go).
//
// The first eleven are the TypeScript SDK's cases (sdk/typescript/test/
// client.test.js), in the same order and under the same names, so the two
// SDKs can be read side by side. The rest cover what is particular to .NET.

using System;
using System.Collections.Generic;
using System.Linq;
using System.Net;
using System.Net.Http;
using System.Net.Sockets;
using System.Reflection;
using System.Runtime.Serialization;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using Xunit;

namespace ZoikoTax.Sdk.Tests;

public sealed class ClientTests
{
    [Fact(DisplayName = "a success returns the body")]
    public async Task ASuccessReturnsTheBody()
    {
        var (client, stub) = Stub.Client(() => Stub.Json(HttpStatusCode.OK, new { cell = "eu-west-1", authoritative = false }), "https://eu-west-1.zoikotax.com/");

        var result = await client.GetCapabilitiesAsync();

        Assert.True(result.IsOk);
        Assert.Equal("eu-west-1", result.Data.Cell);
        Assert.False(result.Data.Authoritative);
        Assert.Equal("https://eu-west-1.zoikotax.com/v1/capabilities", stub.Calls[0].Uri.AbsoluteUri);
        Assert.Equal(HttpMethod.Get, stub.Calls[0].Method);
        Assert.Contains("application/problem+json", stub.Calls[0].Headers["Accept"], StringComparison.Ordinal);

        // The session is an HttpOnly cookie; without a jar every call is
        // UNAUTHENTICATED for a reason that looks like a server problem. A
        // supplied handler keeps its own, so this is asserted on the default
        // one — and end to end in TheDefaultHandlerKeepsTheSessionCookie.
        using var defaults = new ZoikoTaxClient(new Uri(Stub.BaseUrl));
        Assert.NotNull(defaults.Cookies);
    }

    [Fact(DisplayName = "an error is a value carrying the reason code, not an exception")]
    public async Task AnErrorIsAValue()
    {
        var (client, _) = Stub.Client(() => Stub.Json(HttpStatusCode.Forbidden, Stub.Problem()));

        var result = await client.ListUsersAsync();

        Assert.False(result.IsOk);
        var error = Assert.IsType<ZoikoTaxError>(result.Error);
        Assert.Equal("FORBIDDEN", error.ReasonCode);
        Assert.Equal(403, error.Status);
        Assert.False(error.Retryable);
        Assert.Equal("01JBQ0S9C3X8Q1H6M2KX5R7F4K", error.RequestId);
        Assert.Equal("The authenticated subject does not hold a role permitting this action.", error.Message);
        // The whole document survives, so a caller needing a field this SDK
        // release predates can still reach it.
        Assert.Equal(new Uri("https://errors.zoikotax.com/v1/forbidden"), error.Problem.Type);
        Assert.Equal("https://errors.zoikotax.com/v1/forbidden", error.RawProblem.GetProperty("type").GetString());
    }

    [Fact(DisplayName = "retryable and Retry-After are reported, and nothing is retried")]
    public async Task RetryableAndRetryAfterAreReportedAndNothingIsRetried()
    {
        var (client, stub) = Stub.Client(() => Stub.Json(
            HttpStatusCode.ServiceUnavailable,
            Stub.Problem(("status", 503), ("ztx_reason_code", "DATABASE_UNAVAILABLE"), ("ztx_retryable", true)),
            new Dictionary<string, string> { ["Retry-After"] = "2" }));

        var result = await client.GetTenantAsync();

        Assert.False(result.IsOk);
        var error = Assert.IsType<ZoikoTaxError>(result.Error);
        Assert.True(error.Retryable);
        Assert.Equal(TimeSpan.FromSeconds(2), error.RetryAfter);
        // One attempt. Retrying is the caller's decision, because on the
        // endpoints this surface is about to grow, an automatic retry submits a
        // transaction twice.
        Assert.Single(stub.Calls);
    }

    [Fact(DisplayName = "a 204 yields no body rather than a parse failure")]
    public async Task A204YieldsNoBody()
    {
        var (client, stub) = Stub.Client(Stub.NoContent);

        var result = await client.RevokeRoleAsync("ztu_01JBQ0S9C3X8Q1H6M2KX5R7F4C", Role.AUDITOR);

        Assert.True(result.IsOk);
        Assert.Null(result.Error);
        Assert.Equal(HttpMethod.Delete, stub.Calls[0].Method);
        Assert.EndsWith("/v1/admin/users/ztu_01JBQ0S9C3X8Q1H6M2KX5R7F4C/roles/AUDITOR", stub.Calls[0].Uri.AbsoluteUri, StringComparison.Ordinal);
    }

    [Fact(DisplayName = "a non-Problem error body is not attributed to the service")]
    public async Task ANonProblemErrorBodyIsNotAttributedToTheService()
    {
        // What a load balancer, a proxy or a WAF returns. Reporting it as a
        // ZoikoTaxError would give it a reason code nobody registered.
        var (client, _) = Stub.Client(() => new HttpResponseMessage(HttpStatusCode.BadGateway)
        {
            Content = new StringContent("<html>502 Bad Gateway</html>", Encoding.UTF8, "text/html"),
        });

        var result = await client.GetSessionAsync();

        Assert.False(result.IsOk);
        var error = Assert.IsType<ZoikoTaxTransportError>(result.Error);
        Assert.IsNotType<ZoikoTaxError>(result.Error);
        Assert.Equal(502, error.Status);
    }

    [Fact(DisplayName = "a request that never reaches the service is a transport error")]
    public async Task ARequestThatNeverReachesTheServiceIsATransportError()
    {
        var handler = new StubHandler((_, _) => throw new HttpRequestException("Name or service not known (eu-west-1.zoikotax.com:443)"));
        using var client = new ZoikoTaxClient(new ZoikoTaxClientOptions { BaseUrl = new Uri(Stub.BaseUrl), Handler = handler });

        var result = await client.GetCapabilitiesAsync();

        Assert.False(result.IsOk);
        var error = Assert.IsType<ZoikoTaxTransportError>(result.Error);
        Assert.IsType<HttpRequestException>(error.InnerException);
        Assert.Null(error.Status);
    }

    [Fact(DisplayName = "path parameters are encoded")]
    public async Task PathParametersAreEncoded()
    {
        var (client, stub) = Stub.Client(Stub.NoContent);

        await client.RevokeSessionAsync("zts_01/../../admin");

        Assert.EndsWith("/v1/admin/sessions/zts_01%2F..%2F..%2Fadmin", stub.Calls[0].Uri.AbsoluteUri, StringComparison.Ordinal);
    }

    [Fact(DisplayName = "a limit becomes a query parameter and an absent one does not")]
    public async Task ALimitBecomesAQueryParameter()
    {
        var (client, stub) = Stub.Client(() => Stub.Json(HttpStatusCode.OK, new { users = Array.Empty<object>() }));

        await client.ListUsersAsync(limit: 50);
        await client.ListUsersAsync();

        Assert.EndsWith("/v1/admin/users?limit=50", stub.Calls[0].Uri.AbsoluteUri, StringComparison.Ordinal);
        Assert.EndsWith("/v1/admin/users", stub.Calls[1].Uri.AbsoluteUri, StringComparison.Ordinal);
    }

    [Fact(DisplayName = "a request body is sent as JSON with the right content type")]
    public async Task ARequestBodyIsSentAsJson()
    {
        var (client, stub) = Stub.Client(() => Stub.Json(HttpStatusCode.OK, SessionBody()));

        var result = await client.SignInAsync(new SignInRequest
        {
            Tenant = "acme",
            Email = "admin@acme.example",
            Password = "correct horse battery staple",
        });

        Assert.True(result.IsOk);
        Assert.Equal("application/json", stub.Calls[0].ContentType);
        Assert.Equal(
            new Dictionary<string, string>
            {
                ["tenant"] = "acme",
                ["email"] = "admin@acme.example",
                ["password"] = "correct horse battery staple",
            },
            JsonSerializer.Deserialize<Dictionary<string, string>>(stub.Calls[0].Body!));
    }

    [Fact(DisplayName = "unwrap throws the error for callers who prefer exceptions")]
    public async Task UnwrapThrowsTheError()
    {
        var (client, _) = Stub.Client(() => Stub.Json(HttpStatusCode.Forbidden, Stub.Problem()));

        var result = await client.ListUsersAsync();

        var error = Assert.Throws<ZoikoTaxError>(() => result.Unwrap());
        Assert.Equal("FORBIDDEN", error.ReasonCode);
        Assert.NotNull(error.StackTrace);
    }

    [Fact(DisplayName = "a client with no base url is refused at construction")]
    public void AClientWithNoBaseUrlIsRefused()
    {
        Assert.Throws<ArgumentException>(() => new ZoikoTaxClient(new ZoikoTaxClientOptions()));
        Assert.Throws<ArgumentException>(() => new ZoikoTaxClient(new Uri("/relative", UriKind.Relative)));
    }

    // --- .NET-specific ------------------------------------------------------

    [Fact(DisplayName = "the default handler keeps the session cookie between calls")]
    public async Task TheDefaultHandlerKeepsTheSessionCookie()
    {
        // A real listener rather than a stub, because what is under test is the
        // default handler's cookie jar, which a stub handler would replace.
        var port = FreePort();
        using var listener = new HttpListener();
        listener.Prefixes.Add($"http://127.0.0.1:{port}/");
        listener.Start();

        string? cookieOnSecondCall = null;
        var server = Task.Run(async () =>
        {
            var signIn = await listener.GetContextAsync();
            signIn.Response.StatusCode = 200;
            signIn.Response.ContentType = "application/json";
            signIn.Response.AddHeader("Set-Cookie", "ztx_session=opaque-secret; Path=/; HttpOnly; SameSite=Strict");
            var bytes = JsonSerializer.SerializeToUtf8Bytes(SessionBody());
            await signIn.Response.OutputStream.WriteAsync(bytes);
            signIn.Response.Close();

            var session = await listener.GetContextAsync();
            cookieOnSecondCall = session.Request.Headers["Cookie"];
            session.Response.StatusCode = 200;
            session.Response.ContentType = "application/json";
            await session.Response.OutputStream.WriteAsync(bytes);
            session.Response.Close();
        });

        using var client = new ZoikoTaxClient(new Uri($"http://127.0.0.1:{port}"));
        var signedIn = await client.SignInAsync(new SignInRequest { Tenant = "acme", Email = "admin@acme.example", Password = "correct horse battery staple" });
        var current = await client.GetSessionAsync();
        await server.WaitAsync(TimeSpan.FromSeconds(10));

        Assert.True(signedIn.IsOk, signedIn.Error?.Message);
        Assert.True(current.IsOk, current.Error?.Message);
        Assert.Equal("ztx_session=opaque-secret", cookieOnSecondCall);
    }

    [Fact(DisplayName = "an optional member left unset is omitted, not sent as null")]
    public async Task AnOptionalMemberLeftUnsetIsOmitted()
    {
        var (client, stub) = Stub.Client(() => Stub.Json(HttpStatusCode.Created, UserBody()));

        var result = await client.CreateUserAsync(new CreateUserRequest
        {
            Email = "auditor@acme.example",
            DisplayName = "Katherine Johnson",
            Roles = new List<Role> { Role.AUDITOR },
        });

        Assert.True(result.IsOk);
        Assert.Equal(UserStatus.INVITED, result.Data.Status);
        Assert.Equal(new[] { Role.AUDITOR }, result.Data.Roles);
        // A timestamp stays the exact string the server sent (ADR-0011 §2.1 P2).
        Assert.Equal("2026-09-23T12:00:00.000000Z", result.Data.CreatedAt);

        using var sent = JsonDocument.Parse(stub.Calls[0].Body!);
        Assert.False(sent.RootElement.TryGetProperty("password", out _));
        Assert.Equal("AUDITOR", sent.RootElement.GetProperty("roles")[0].GetString());
    }

    [Fact(DisplayName = "enums reach the wire as the contract spells them")]
    public void EnumsReachTheWireAsTheContractSpellsThem()
    {
        // System.Text.Json on .NET 8 writes an enum by its C# name and ignores
        // [EnumMember]. The generator happens to use the wire value as the
        // name, which is the only reason the client's name-based converter is
        // right; this is what notices if it ever stops. The bodies themselves
        // are asserted in AGrantAndAStatusChangeSendTheContractsBodies.
        foreach (var type in new[] { typeof(Role), typeof(UserStatus), typeof(TenantStatus) })
        {
            foreach (var field in type.GetFields(BindingFlags.Public | BindingFlags.Static))
            {
                Assert.Equal(field.GetCustomAttribute<EnumMemberAttribute>()!.Value, field.Name);
            }
        }
    }

    [Fact(DisplayName = "a grant and a status change send the contract's bodies")]
    public async Task AGrantAndAStatusChangeSendTheContractsBodies()
    {
        var (client, stub) = Stub.Client(Stub.NoContent);

        Assert.True((await client.GrantRoleAsync("ztu_01JBQ0S9C3X8Q1H6M2KX5R7F4C", Role.AUDITOR)).IsOk);
        Assert.True((await client.SetUserStatusAsync("ztu_01JBQ0S9C3X8Q1H6M2KX5R7F4C", UserStatus.DISABLED)).IsOk);

        Assert.Equal("{\"role\":\"AUDITOR\"}", stub.Calls[0].Body);
        Assert.EndsWith("/v1/admin/users/ztu_01JBQ0S9C3X8Q1H6M2KX5R7F4C/roles", stub.Calls[0].Uri.AbsoluteUri, StringComparison.Ordinal);
        Assert.Equal("{\"status\":\"DISABLED\"}", stub.Calls[1].Body);
        Assert.EndsWith("/v1/admin/users/ztu_01JBQ0S9C3X8Q1H6M2KX5R7F4C/status", stub.Calls[1].Uri.AbsoluteUri, StringComparison.Ordinal);
    }

    [Fact(DisplayName = "a Problem extension this release does not model survives in RawProblem")]
    public async Task AnUnmodelledExtensionSurvives()
    {
        var (client, _) = Stub.Client(() => Stub.Json(HttpStatusCode.Forbidden, Stub.Problem(("ztx_future_extension", "kept"))));

        var result = await client.GetTenantAsync();

        var error = Assert.IsType<ZoikoTaxError>(result.Error);
        Assert.Equal("kept", error.RawProblem.GetProperty("ztx_future_extension").GetString());
    }

    [Fact(DisplayName = "a success whose body is not JSON is a transport error, not a crash")]
    public async Task ASuccessWhoseBodyIsNotJsonIsATransportError()
    {
        var (client, _) = Stub.Client(() => new HttpResponseMessage(HttpStatusCode.OK)
        {
            Content = new StringContent("<html>captive portal</html>", Encoding.UTF8, "text/html"),
        });

        var result = await client.GetCapabilitiesAsync();

        var error = Assert.IsType<ZoikoTaxTransportError>(result.Error);
        Assert.Equal(200, error.Status);
        Assert.IsAssignableFrom<JsonException>(error.InnerException);
    }

    [Fact(DisplayName = "a timeout is a transport error")]
    public async Task ATimeoutIsATransportError()
    {
        var handler = new StubHandler(async (_, token) =>
        {
            await Task.Delay(Timeout.Infinite, token);
            return Stub.NoContent();
        });
        using var client = new ZoikoTaxClient(new ZoikoTaxClientOptions
        {
            BaseUrl = new Uri(Stub.BaseUrl),
            Handler = handler,
            Timeout = TimeSpan.FromMilliseconds(50),
        });

        var result = await client.SignOutAsync();

        var error = Assert.IsType<ZoikoTaxTransportError>(result.Error);
        Assert.Contains("timed out after 50ms", error.Message, StringComparison.Ordinal);
    }

    [Fact(DisplayName = "cancellation by the caller is an OperationCanceledException, as everywhere in .NET")]
    public async Task CancellationByTheCallerThrows()
    {
        var handler = new StubHandler(async (_, token) =>
        {
            await Task.Delay(Timeout.Infinite, token);
            return Stub.NoContent();
        });
        using var client = new ZoikoTaxClient(new ZoikoTaxClientOptions { BaseUrl = new Uri(Stub.BaseUrl), Handler = handler });
        using var cancel = new CancellationTokenSource(TimeSpan.FromMilliseconds(50));

        await Assert.ThrowsAnyAsync<OperationCanceledException>(() => client.SignOutAsync(cancel.Token));
    }

    [Fact(DisplayName = "configured headers go on every request, and a content header is refused")]
    public async Task ConfiguredHeadersGoOnEveryRequest()
    {
        var handler = new StubHandler(Stub.NoContent);
        var options = new ZoikoTaxClientOptions { BaseUrl = new Uri(Stub.BaseUrl), Handler = handler };
        options.Headers["X-Correlation-Id"] = "abc-123";
        using var client = new ZoikoTaxClient(options);

        await client.SignOutAsync();

        Assert.Equal("abc-123", handler.Calls[0].Headers["X-Correlation-Id"]);

        var refused = new ZoikoTaxClientOptions { BaseUrl = new Uri(Stub.BaseUrl) };
        refused.Headers["Content-Type"] = "text/plain";
        Assert.Throws<ArgumentException>(() => new ZoikoTaxClient(refused));
    }

    [Fact(DisplayName = "every operation in the contract has a method")]
    public void EveryOperationHasAMethod()
    {
        var expected = new[]
        {
            "GetCapabilitiesAsync", "SignInAsync", "SignOutAsync", "GetSessionAsync", "ChangePasswordAsync",
            "GetTenantAsync", "ListUsersAsync", "CreateUserAsync", "SetUserStatusAsync", "GrantRoleAsync",
            "RevokeRoleAsync", "ListSessionsAsync", "RevokeSessionAsync", "ListAuditAsync",
        };
        var methods = typeof(ZoikoTaxClient).GetMethods(BindingFlags.Public | BindingFlags.Instance | BindingFlags.DeclaredOnly)
            .Where(m => m.Name.EndsWith("Async", StringComparison.Ordinal))
            .ToList();

        Assert.Equal(expected.OrderBy(n => n, StringComparer.Ordinal), methods.Select(m => m.Name).OrderBy(n => n, StringComparer.Ordinal));
        Assert.All(methods, m => Assert.Equal(typeof(CancellationToken), m.GetParameters()[^1].ParameterType));
    }

    private static object SessionBody() => new
    {
        tenant = new { id = "ztn_01JBQ0S9C3X8Q1H6M2KX5R7F4A", slug = "acme", displayName = "Acme Telecom", residencyRegion = "eu-west", status = "ACTIVE" },
        user = UserBody(),
        expiresAt = "2026-09-24T09:14:22.104000Z",
    };

    private static object UserBody() => new
    {
        id = "ztu_01JBQ0S9C3X8Q1H6M2KX5R7F4D",
        email = "auditor@acme.example",
        displayName = "Katherine Johnson",
        status = "INVITED",
        roles = new[] { "AUDITOR" },
        createdAt = "2026-09-23T12:00:00.000000Z",
    };

    private static int FreePort()
    {
        using var probe = new TcpListener(IPAddress.Loopback, 0);
        probe.Start();
        return ((IPEndPoint)probe.LocalEndpoint).Port;
    }
}
