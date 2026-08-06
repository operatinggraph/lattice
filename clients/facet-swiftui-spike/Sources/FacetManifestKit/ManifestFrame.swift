import Foundation

/// Mirrors `cmd/facet/feed.go`'s `frame` struct field-for-field, except
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
    public let outbox: OutboxEntry?
    public let reason: String
    public let connected: Bool

    /// The `manifest.*` namespace this frame's key belongs to — the same
    /// split `edge-manifest/lenses.go`'s lenses project (`manifest.me` /
    /// `.svc.` / `.op.` / `.task.` / `.inst.` / `.ent.`), used here to route
    /// a frame to the matching SwiftUI section. A non-manifest frame
    /// (outbox/ready/revoked/connectivity) has an empty key and resolves to
    /// `.other`.
    public enum Section: String {
        case identity, service, opMeta, task, instance, entity, other
    }

    public var section: Section {
        if key == "manifest.me" { return .identity }
        if key.hasPrefix("manifest.svc.") { return .service }
        if key.hasPrefix("manifest.op.") { return .opMeta }
        if key.hasPrefix("manifest.task.") { return .task }
        if key.hasPrefix("manifest.inst.") { return .instance }
        if key.hasPrefix("manifest.ent.") { return .entity }
        return .other
    }

    private struct Body: Decodable {
        let key: String?
        let deleted: Bool?
        let pending: Bool?
        let data: JSONValue?
        let revision: UInt64?
        let outbox: OutboxEntry?
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
        self.outbox = body.outbox
        self.reason = body.reason ?? ""
        self.connected = body.connected ?? false
    }
}

/// Mirrors `cmd/facet/feed.go`'s `outboxEntry` — the write-lifecycle fields
/// this spike's UI actually surfaces (state machine + error), not the
/// re-hydration fields (`Payload`/`Reads`/`OptionalReads`/`AuthContext`/
/// `CreatedAt`) the Go doc names for reopening a form pre-filled, which no
/// SwiftUI view here does yet.
public struct OutboxEntry: Equatable, Decodable {
    public let requestID: String
    public let operationType: String
    public let state: String // queued|submitting|confirmed|rejected
    public let errorCode: String?
    public let errorMessage: String?

    enum CodingKeys: String, CodingKey {
        case requestID = "requestId"
        case operationType, state, errorCode, errorMessage
    }
}
