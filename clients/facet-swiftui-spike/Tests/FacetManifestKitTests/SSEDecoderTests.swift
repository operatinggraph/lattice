import XCTest
@testable import FacetManifestKit

final class SSEDecoderTests: XCTestCase {
    func testDecodesOneManifestFrame() {
        let decoder = SSEDecoder()
        var frames: [ManifestFrame] = []
        let lines = [
            "event: manifest",
            #"data: {"key":"manifest.svc.abc","data":{"name":"Riverside Café","description":"Coffee & pastries"}}"#,
            "",
        ]
        for line in lines {
            if let frame = decoder.feed(line: line) {
                frames.append(frame)
            }
        }
        XCTAssertEqual(frames.count, 1)
        let frame = frames[0]
        XCTAssertEqual(frame.kind, "manifest")
        XCTAssertEqual(frame.key, "manifest.svc.abc")
        XCTAssertEqual(frame.section, .service)
        XCTAssertEqual(frame.data?["name"]?.stringValue, "Riverside Café")
        XCTAssertEqual(frame.data?["description"]?.stringValue, "Coffee & pastries")
    }

    func testDecodesMultipleFramesAndIgnoresPingComments() {
        let decoder = SSEDecoder()
        var frames: [ManifestFrame] = []
        let lines = [
            "event: connectivity",
            #"data: {"connected":true}"#,
            "",
            ": ping",
            "event: manifest",
            #"data: {"key":"manifest.me","data":{"displayName":"Ada","claimed":true}}"#,
            "",
        ]
        for line in lines {
            if let frame = decoder.feed(line: line) {
                frames.append(frame)
            }
        }
        XCTAssertEqual(frames.count, 2)
        XCTAssertEqual(frames[0].kind, "connectivity")
        XCTAssertEqual(frames[0].connected, true)
        XCTAssertEqual(frames[1].section, .identity)
        XCTAssertEqual(frames[1].data?["displayName"]?.stringValue, "Ada")
        XCTAssertEqual(frames[1].data?["claimed"]?.boolValue, true)
    }

    func testDeletedFrameHasNoData() {
        let decoder = SSEDecoder()
        var frames: [ManifestFrame] = []
        let lines = [
            "event: manifest",
            #"data: {"key":"manifest.task.xyz","deleted":true}"#,
            "",
        ]
        for line in lines {
            if let frame = decoder.feed(line: line) {
                frames.append(frame)
            }
        }
        XCTAssertEqual(frames.count, 1)
        XCTAssertTrue(frames[0].deleted)
        XCTAssertEqual(frames[0].section, .task)
        XCTAssertNil(frames[0].data)
    }

    func testMalformedBodyIsDroppedNotThrown() {
        let decoder = SSEDecoder()
        let lines = [
            "event: manifest",
            "data: not-json",
            "",
        ]
        var frames: [ManifestFrame] = []
        for line in lines {
            if let frame = decoder.feed(line: line) {
                frames.append(frame)
            }
        }
        XCTAssertTrue(frames.isEmpty)
    }
}
