using System;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace ZoikoTax.Sdk;

/// <summary>How the SDK's types meet JSON. One set of options, for both directions.</summary>
internal static class Wire
{
    /// <summary>
    /// The serializer options every request and response goes through.
    /// </summary>
    /// <remarks>
    /// <para>
    /// Absent optional members are omitted rather than sent as <c>null</c>: the
    /// contract's request schemas say what may be present, and a <c>null</c>
    /// where a member is optional is a value the server would have to decide
    /// the meaning of.
    /// </para>
    /// <para>
    /// Enums are written by name, and the generated names are the wire values
    /// (<c>ADMIN</c>, <c>INVITED</c>). Integers are refused: an enum number is
    /// not in the contract. A test checks every generated member's name
    /// against its declared wire value, because <c>System.Text.Json</c> on
    /// .NET 8 ignores <c>[EnumMember]</c> and would otherwise send a renamed
    /// member's C# name without complaint.
    /// </para>
    /// <para>
    /// Unknown members in a response are ignored, which is the default and is
    /// deliberate: <c>/v1</c> grows by addition (ADR-0010 §2.6), and a client
    /// that rejected an added field would break on a change the contract
    /// promises is compatible.
    /// </para>
    /// </remarks>
    public static readonly JsonSerializerOptions Options = new()
    {
        DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull,
        Converters = { new JsonStringEnumConverter(namingPolicy: null, allowIntegerValues: false) },
    };

    /// <summary>The wire form of an enum member, as it appears in a path segment.</summary>
    public static string Name<TEnum>(TEnum value)
        where TEnum : struct, Enum
    {
        return JsonSerializer.SerializeToElement(value, Options).GetString()
            ?? throw new InvalidOperationException($"{typeof(TEnum).Name}.{value} has no wire form");
    }
}
