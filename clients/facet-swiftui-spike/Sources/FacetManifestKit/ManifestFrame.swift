import Foundation

/// Mirrors `cmd/facet/feed.go`'s `frame` struct field-for-field, except
/// `Outbox` (write-lifecycle UI, out of this spike's scope — the spike is
/// read-path renderer-neutrality, not a second write surface) and
/// `Kind`, which the Go side tags `json:"-"` because it rides the SSE
/// `event:` line rather than the body; this side sets it the same way,
/// from the event name the frame arrived on (see `SSEDecoder`).
public struct ManifestFrame: Equatable {
    public let kind: String
    public let key: String
    public let deleted: Bool
    public let pending: Bool
    public let data: JSONValue?
    public let revision: UInt64
    public let reason: String
    public let connected: Bool

    /// The `manifest.*` namespace this frame's key belongs to — the same
    /// five-way split `edge-manifest/lenses.go`'s five lenses project
    /// (`manifest.me` / `.svc.` / `.op.` / `.task.` / `.inst.`), used here
    /// to route a frame to the matching SwiftUI section. A non-manifest
    /// frame (outbox/ready/revoked/connectivity) has an empty key and
    /// resolves to `.other`.
    public enum Section: String {
        case identity, service, opMeta, task, instance, other
    }

    public var section: Section {
        if key == "manifest.me" { return .identity }
        if key.hasPrefix("manifest.svc.") { return .service }
        if key.hasPrefix("manifest.op.") { return .opMeta }
        if key.hasPrefix("manifest.task.") { return .task }
        if key.hasPrefix("manifest.inst.") { return .instance }
        return .other
    }

    private struct Body: Decodable {
        let key: String?
        let deleted: Bool?
        let pending: Bool?
        let data: JSONValue?
        let revision: UInt64?
        let reason: String?
        let connected: Bool?
    }

    /// Decodes one SSE frame's `data:` JSON body, paired with the `event:`
    /// name that named its kind on the wire.
    public init(kind: String, jsonBody: Data) throws {
        let body = try JSONDecoder().decode(Body.self, from: jsonBody)
        self.kind = kind
        self.key = body.key ?? ""
        self.deleted = body.deleted ?? false
        self.pending = body.pending ?? false
        self.data = body.data
        self.revision = body.revision ?? 0
        self.reason = body.reason ?? ""
        self.connected = body.connected ?? false
    }
}
