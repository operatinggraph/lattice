import Foundation

/// Incremental Server-Sent-Events line parser, decoupled from networking so
/// it is unit-testable with plain strings. Mirrors the wire format
/// `cmd/facet/feed.go`'s `writeFrame` emits: an `event: <kind>` line, a
/// `data: <json>` line, then a blank line terminating the frame. A `: ping`
/// comment line (the 20s keepalive) carries no `event:`/`data:` prefix and
/// is ignored, same as a browser `EventSource` ignores it.
public final class SSEDecoder {
    private var pendingEvent: String?
    private var pendingData: String?

    public init() {}

    /// Feed one line (no trailing newline). Returns a decoded frame when
    /// this line was the blank terminator of a complete event, `nil`
    /// otherwise (including a malformed body — dropped rather than thrown,
    /// mirroring `dispatchFrame`'s "unmodelled frame kind — ignore" posture
    /// for forward compatibility with a newer host).
    public func feed(line: String) -> ManifestFrame? {
        if line.isEmpty {
            defer { pendingEvent = nil; pendingData = nil }
            guard let kind = pendingEvent, let dataStr = pendingData,
                  let dataBytes = dataStr.data(using: .utf8) else {
                return nil
            }
            return try? ManifestFrame(kind: kind, jsonBody: dataBytes)
        }
        if line.hasPrefix("event: ") {
            pendingEvent = String(line.dropFirst("event: ".count))
        } else if line.hasPrefix("data: ") {
            pendingData = String(line.dropFirst("data: ".count))
        }
        // Any other line (e.g. ": ping" comments) is ignored.
        return nil
    }
}
