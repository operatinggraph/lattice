import SwiftUI
import FacetManifestKit

/// Second-renderer spike (edge-showcase-app-design.md §7 Fire 5): a native
/// SwiftUI client hydrating from the exact same `manifest.*` feed the PWA
/// renders, over `cmd/facet`'s already-shipped browser-facing HTTP+SSE
/// surface — no NATS/crypto client of its own, no changes to the Go host.
/// Environment: FACET_BASE_URL (default http://127.0.0.1:7810, matching
/// `cmd/facet/main.go`'s defaultHTTPAddr) and FACET_IDENTITY_ID (a 20-char
/// NanoID from `make seed-showcase`'s tenant set — required, since
/// `up-facet` runs with FACET_DEV_AUTH=1 and no boot identity).
@main
struct FacetSwiftUISpikeApp: App {
    @StateObject private var store = ManifestStore()

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environmentObject(store)
                .task { await connect() }
        }
    }

    private func connect() async {
        let env = ProcessInfo.processInfo.environment
        guard let baseURL = URL(string: env["FACET_BASE_URL"] ?? "http://127.0.0.1:7810") else {
            store.statusMessage = "Invalid FACET_BASE_URL"
            return
        }
        guard let identityID = env["FACET_IDENTITY_ID"], !identityID.isEmpty else {
            store.statusMessage = "Set FACET_IDENTITY_ID to a seeded tenant's identity id"
            return
        }
        let client = FeedClient(baseURL: baseURL)
        do {
            try await client.devLogin(identityID: identityID)
        } catch {
            store.statusMessage = "Login failed: \(error)"
            return
        }
        do {
            for try await frame in client.stream() {
                store.apply(frame)
            }
        } catch {
            store.statusMessage = "Feed error: \(error)"
        }
    }
}
