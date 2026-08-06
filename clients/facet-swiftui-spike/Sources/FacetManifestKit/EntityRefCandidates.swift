import Foundation

/// One `manifest.ent.*` row, resolved to what an `x-entityRef` picker needs
/// to show and submit — mirrors `app.js`'s `entityRefCandidates` return
/// shape (`{key, label}`).
public struct EntityRefCandidate: Equatable, Identifiable {
    public let key: String
    public let label: String
    public var id: String { key }

    public init(key: String, label: String) {
        self.key = key
        self.label = label
    }
}

/// Answers what an `x-entityRef: "<type>"` field may hold, from the
/// `manifest.ent.*` rows the client already holds for that type — the
/// Swift-side mirror of `app.js`'s `entityRefCandidates`. A row with no
/// `title` falls back to its bare `entityKey`, same as `app.js`'s
/// `prettify(r.data.entityKey)` fallback simplified to the raw key (no
/// title-casing on this side).
public func entityRefCandidates(type: String, entities: [JSONValue]) -> [EntityRefCandidate] {
    entities.compactMap { row in
        guard row["entityType"]?.stringValue == type, let key = row["entityKey"]?.stringValue else { return nil }
        return EntityRefCandidate(key: key, label: row["title"]?.stringValue ?? key)
    }
}
