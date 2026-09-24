using System.Collections.Generic;
using System.Collections.ObjectModel;
using System.Text.Json.Serialization;

// The three list responses. The contract declares each inline in its
// operation rather than as a named schema, and NSwag names inline schemas
// Response, Response2 and Response3 by position — names that would silently
// change meaning if an operation were added above them. So generate.sh
// excludes them and they are declared here, by name, over the generated item
// types. This is the same thing the TypeScript SDK does when it writes
// `{ users: User[] }` in a method signature.
//
// They are the only hand-written shapes in the SDK. If the contract gives
// these responses named schemas, delete this file and drop the exclusion.

namespace ZoikoTax.Sdk;

/// <summary>The tenant's users, as <c>GET /v1/admin/users</c> returns them.</summary>
public sealed class UserList
{
    /// <summary>The users.</summary>
    [JsonPropertyName("users")]
    public ICollection<User> Users { get; set; } = new Collection<User>();
}

/// <summary>The tenant's sessions, as <c>GET /v1/admin/sessions</c> returns them.</summary>
public sealed class SessionList
{
    /// <summary>The sessions. <see cref="SessionSummary.Current"/> marks the caller's own.</summary>
    [JsonPropertyName("sessions")]
    public ICollection<SessionSummary> Sessions { get; set; } = new Collection<SessionSummary>();
}

/// <summary>The tenant's audit trail, as <c>GET /v1/admin/audit</c> returns it.</summary>
public sealed class AuditRecordList
{
    /// <summary>The records, most recent first.</summary>
    [JsonPropertyName("records")]
    public ICollection<AuditRecord> Records { get; set; } = new Collection<AuditRecord>();
}
