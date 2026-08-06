import XCTest
@testable import FacetManifestKit

/// Fixtures shaped like real `manifest.ent.*` rows (`edgeEntityProvidersTail`,
/// `packages/edge-manifest/lenses.go`), asserting `entityRefCandidates`
/// against the actual columns a shipped lens projects.
final class EntityRefCandidatesTests: XCTestCase {
    private func entityRow(key: String, type: String, title: String?) -> JSONValue {
        var fields: [String: JSONValue] = [
            "entityKey": .string(key),
            "entityType": .string(type),
        ]
        if let title { fields["title"] = .string(title) }
        return .object(fields)
    }

    func testFiltersByTypeAndUsesTitle() {
        let entities = [
            entityRow(key: "vtx.provider.p1", type: "provider", title: "Dr. Alex Rivera"),
            entityRow(key: "vtx.provider.p2", type: "provider", title: "Dr. Sam Lee"),
            entityRow(key: "vtx.booking.b1", type: "booking", title: "Vinyasa Flow"),
        ]
        let candidates = entityRefCandidates(type: "provider", entities: entities)
        XCTAssertEqual(candidates, [
            EntityRefCandidate(key: "vtx.provider.p1", label: "Dr. Alex Rivera"),
            EntityRefCandidate(key: "vtx.provider.p2", label: "Dr. Sam Lee"),
        ])
    }

    func testFallsBackToKeyWhenNoTitle() {
        let entities = [entityRow(key: "vtx.provider.p1", type: "provider", title: nil)]
        let candidates = entityRefCandidates(type: "provider", entities: entities)
        XCTAssertEqual(candidates, [EntityRefCandidate(key: "vtx.provider.p1", label: "vtx.provider.p1")])
    }

    func testNoMatchingTypeReturnsEmpty() {
        let entities = [entityRow(key: "vtx.booking.b1", type: "booking", title: "Vinyasa Flow")]
        XCTAssertEqual(entityRefCandidates(type: "provider", entities: entities), [])
    }

    func testRowMissingEntityKeyIsSkipped() {
        let entities: [JSONValue] = [.object(["entityType": .string("provider"), "title": .string("No key")])]
        XCTAssertEqual(entityRefCandidates(type: "provider", entities: entities), [])
    }
}
