import Foundation

/// A JSON value of unknown shape. `manifest.*` rows vary per namespace
/// (`edge-manifest/lenses.go`'s RETURN clauses), the same way the Go host
/// carries `Data json.RawMessage` in `cmd/facet/feed.go`'s `frame` rather
/// than a per-namespace struct — this is that same posture on the Swift
/// side, decoded once and read field-by-field per section.
public enum JSONValue: Decodable, Equatable {
    case string(String)
    case number(Double)
    case bool(Bool)
    case object([String: JSONValue])
    case array([JSONValue])
    case null

    public init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        if container.decodeNil() {
            self = .null
        } else if let v = try? container.decode(Bool.self) {
            self = .bool(v)
        } else if let v = try? container.decode(Double.self) {
            self = .number(v)
        } else if let v = try? container.decode(String.self) {
            self = .string(v)
        } else if let v = try? container.decode([JSONValue].self) {
            self = .array(v)
        } else if let v = try? container.decode([String: JSONValue].self) {
            self = .object(v)
        } else {
            throw DecodingError.dataCorruptedError(in: container, debugDescription: "unsupported JSON value")
        }
    }

    /// Field access into an `.object`, `nil` for every other case (including
    /// a field that decoded as JSON `null`) — mirrors the renderer
    /// obligation `edge-manifest.md` names for degenerate collect() rows:
    /// treat a null/absent field as absence, not an error.
    public subscript(key: String) -> JSONValue? {
        if case let .object(dict) = self { return dict[key] }
        return nil
    }

    public var stringValue: String? {
        if case let .string(s) = self { return s }
        return nil
    }

    public var arrayValue: [JSONValue]? {
        if case let .array(a) = self { return a }
        return nil
    }

    public var boolValue: Bool? {
        if case let .bool(b) = self { return b }
        return nil
    }
}
