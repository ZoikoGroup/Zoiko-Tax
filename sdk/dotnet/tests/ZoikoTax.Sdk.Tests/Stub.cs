using System;
using System.Collections.Generic;
using System.Net;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

namespace ZoikoTax.Sdk.Tests;

/// <summary>What the stub was asked, captured when it was asked.</summary>
internal sealed record Call(HttpMethod Method, Uri Uri, IReadOnlyDictionary<string, string> Headers, string? ContentType, string? Body);

/// <summary>A handler that answers every request the same way, and records what it was asked.</summary>
internal sealed class StubHandler : HttpMessageHandler
{
    private readonly Func<HttpRequestMessage, CancellationToken, Task<HttpResponseMessage>> _answer;

    public StubHandler(Func<HttpResponseMessage> response)
        : this((_, _) => Task.FromResult(response()))
    {
    }

    public StubHandler(Func<HttpRequestMessage, CancellationToken, Task<HttpResponseMessage>> answer)
    {
        _answer = answer;
    }

    public List<Call> Calls { get; } = new();

    protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
    {
        var body = request.Content is null ? null : await request.Content.ReadAsStringAsync(cancellationToken);
        var headers = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
        foreach (var (name, values) in request.Headers)
        {
            headers[name] = string.Join(", ", values);
        }

        Calls.Add(new Call(request.Method, request.RequestUri!, headers, request.Content?.Headers.ContentType?.ToString(), body));
        return await _answer(request, cancellationToken);
    }
}

internal static class Stub
{
    public const string BaseUrl = "https://eu-west-1.zoikotax.com";

    public static (ZoikoTaxClient Client, StubHandler Handler) Client(Func<HttpResponseMessage> response, string baseUrl = BaseUrl)
    {
        var handler = new StubHandler(response);
        return (new ZoikoTaxClient(new ZoikoTaxClientOptions { BaseUrl = new Uri(baseUrl), Handler = handler }), handler);
    }

    public static HttpResponseMessage Json(HttpStatusCode status, object body, IDictionary<string, string>? headers = null)
    {
        var response = new HttpResponseMessage(status)
        {
            Content = new StringContent(JsonSerializer.Serialize(body), Encoding.UTF8),
        };
        response.Content.Headers.ContentType = new MediaTypeHeaderValue((int)status >= 400 ? "application/problem+json" : "application/json");
        foreach (var (name, value) in headers ?? new Dictionary<string, string>())
        {
            response.Headers.TryAddWithoutValidation(name, value);
        }

        return response;
    }

    public static HttpResponseMessage NoContent() => new(HttpStatusCode.NoContent);

    public static Dictionary<string, object?> Problem(params (string Key, object? Value)[] overrides)
    {
        var problem = new Dictionary<string, object?>
        {
            ["type"] = "https://errors.zoikotax.com/v1/forbidden",
            ["title"] = "Forbidden",
            ["status"] = 403,
            ["detail"] = "The authenticated subject does not hold a role permitting this action.",
            ["instance"] = "/v1/admin/users",
            ["ztx_reason_code"] = "FORBIDDEN",
            ["ztx_request_id"] = "01JBQ0S9C3X8Q1H6M2KX5R7F4K",
            ["ztx_retryable"] = false,
        };
        foreach (var (key, value) in overrides)
        {
            problem[key] = value;
        }

        return problem;
    }
}
