import SwiftUI
import FacetManifestKit

/// The renderer-neutrality proof: every row here is read straight off the
/// same `manifest.*` frames `cmd/facet/web/app.js` renders as HTML — this
/// view supplies none of the presentation text itself, only SwiftUI list
/// chrome. A service/op template that ships with `.presentation` data
/// appears here with zero app change, same claim as the PWA renderer.
struct ContentView: View {
    @EnvironmentObject var store: ManifestStore

    var body: some View {
        NavigationStack {
            List {
                Section("Me") {
                    if let me = store.me {
                        Text(me["displayName"]?.stringValue ?? "(unnamed)")
                            .font(.headline)
                        Text(me["claimed"]?.boolValue == true ? "Claimed" : "Unclaimed")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    } else {
                        Text("Not hydrated yet").foregroundStyle(.secondary)
                    }
                }
                manifestSection("Services", rows: store.services, title: "name", subtitle: "description")
                manifestSection("Catalog", rows: store.ops, title: "title", subtitle: "description")
                manifestSection("Tasks", rows: store.tasks, title: "operationType", subtitle: nil)
                manifestSection("My Instances", rows: store.instances, title: "templateName", subtitle: "status")
            }
            .navigationTitle("Facet (SwiftUI spike)")
            .toolbar {
                ToolbarItem(placement: .automatic) {
                    Label(store.connected ? "Connected" : "Disconnected",
                          systemImage: store.connected ? "wifi" : "wifi.slash")
                        .foregroundStyle(store.connected ? .green : .red)
                }
            }
            .overlay {
                if store.services.isEmpty && store.ops.isEmpty && store.me == nil {
                    VStack(spacing: 8) {
                        ProgressView()
                        Text(store.statusMessage).foregroundStyle(.secondary)
                    }
                }
            }
        }
    }

    @ViewBuilder
    private func manifestSection(_ title: String, rows: [JSONValue], title titleField: String, subtitle subtitleField: String?) -> some View {
        if !rows.isEmpty {
            Section("\(title) (\(rows.count))") {
                ForEach(Array(rows.enumerated()), id: \.offset) { _, row in
                    VStack(alignment: .leading) {
                        Text(row[titleField]?.stringValue ?? "(untitled)")
                        if let subtitleField, let subtitle = row[subtitleField]?.stringValue {
                            Text(subtitle).font(.caption).foregroundStyle(.secondary)
                        }
                    }
                }
            }
        }
    }
}
