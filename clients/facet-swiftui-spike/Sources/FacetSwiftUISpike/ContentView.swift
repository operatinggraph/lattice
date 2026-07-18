import SwiftUI
import FacetManifestKit

/// The renderer-neutrality proof: every row here is read straight off the
/// same `manifest.*` frames `cmd/facet/web/app.js` renders as HTML — this
/// view supplies none of the presentation text itself, only SwiftUI list
/// chrome. A service/op template that ships with `.presentation` data
/// appears here with zero app change, same claim as the PWA renderer.
struct ContentView: View {
    @EnvironmentObject var store: ManifestStore
    @State private var selectedOp: SelectedOp?

    private struct SelectedOp: Identifiable {
        let id: String
        let op: JSONValue
    }

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
                catalogSection()
                manifestSection("Tasks", rows: store.tasks, title: "operationType", subtitle: nil)
                manifestSection("My Instances", rows: store.instances, title: "templateName", subtitle: "status")
                outboxSection()
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
            .sheet(item: $selectedOp) { selected in
                DescriptorFormSheet(op: selected.op).environmentObject(store)
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

    /// The write-path trigger: tapping a catalog row opens `DescriptorFormSheet`,
    /// which resolves the row's `inputSchema`/`dispatch` fields (`DescriptorForm`)
    /// into a real form before submitting — the Fire 5 Inc 3 successor to
    /// Inc 2's blind empty-payload "Enqueue" button.
    @ViewBuilder
    private func catalogSection() -> some View {
        if !store.ops.isEmpty {
            Section("Catalog (\(store.ops.count))") {
                ForEach(Array(store.ops.enumerated()), id: \.offset) { _, row in
                    Button {
                        let id = row["opMetaKey"]?.stringValue ?? row["operationType"]?.stringValue ?? UUID().uuidString
                        selectedOp = SelectedOp(id: id, op: row)
                    } label: {
                        VStack(alignment: .leading) {
                            Text(row["title"]?.stringValue ?? "(untitled)")
                            if let subtitle = row["description"]?.stringValue {
                                Text(subtitle).font(.caption).foregroundStyle(.secondary)
                            }
                        }
                    }
                    .buttonStyle(.plain)
                }
            }
        }
    }

    @ViewBuilder
    private func outboxSection() -> some View {
        if !store.outbox.isEmpty {
            Section("Outbox (\(store.outbox.count))") {
                ForEach(store.outbox, id: \.requestID) { entry in
                    VStack(alignment: .leading) {
                        Text(entry.operationType)
                        Text(entry.errorMessage ?? entry.state)
                            .font(.caption)
                            .foregroundStyle(entry.state == "rejected" ? .red : (entry.state == "confirmed" ? .green : .secondary))
                    }
                }
            }
        }
    }
}
