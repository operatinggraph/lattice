import Foundation
import FacetManifestKit

/// Reduces the live `ManifestFrame` stream into per-section dictionaries a
/// SwiftUI view can render, the same last-write-wins-per-key reducer shape
/// `app.js`'s manifest handler uses on the PWA side — the point of this
/// spike is that this reducer, not any browser-specific API, is the only
/// renderer-neutral thing about the manifest.
@MainActor
public final class ManifestStore: ObservableObject {
    @Published public private(set) var me: JSONValue?
    @Published public private(set) var services: [JSONValue] = []
    @Published public private(set) var ops: [JSONValue] = []
    @Published public private(set) var tasks: [JSONValue] = []
    @Published public private(set) var instances: [JSONValue] = []
    @Published public var connected: Bool = false
    @Published public var statusMessage: String = "Connecting…"

    private var servicesByKey: [String: JSONValue] = [:]
    private var opsByKey: [String: JSONValue] = [:]
    private var tasksByKey: [String: JSONValue] = [:]
    private var instancesByKey: [String: JSONValue] = [:]

    public init() {}

    public func apply(_ frame: ManifestFrame) {
        switch frame.kind {
        case "connectivity":
            connected = frame.connected
            return
        case "ready":
            statusMessage = "Live"
            return
        case "revoked":
            statusMessage = "Revoked: \(frame.reason)"
            return
        case "manifest":
            break
        default:
            return // outbox and any future frame kind: out of this spike's scope
        }

        switch frame.section {
        case .identity:
            me = frame.deleted ? nil : frame.data
        case .service:
            apply(frame, to: &servicesByKey)
            services = sortedByKey(servicesByKey)
        case .opMeta:
            apply(frame, to: &opsByKey)
            ops = sortedByKey(opsByKey)
        case .task:
            apply(frame, to: &tasksByKey)
            tasks = sortedByKey(tasksByKey)
        case .instance:
            apply(frame, to: &instancesByKey)
            instances = sortedByKey(instancesByKey)
        case .other:
            break
        }
    }

    private func apply(_ frame: ManifestFrame, to dict: inout [String: JSONValue]) {
        if frame.deleted {
            dict.removeValue(forKey: frame.key)
        } else if let data = frame.data {
            dict[frame.key] = data
        }
    }

    private func sortedByKey(_ dict: [String: JSONValue]) -> [JSONValue] {
        dict.keys.sorted().compactMap { dict[$0] }
    }
}
