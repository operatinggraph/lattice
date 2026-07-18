import SwiftUI
import FacetManifestKit

/// The real descriptor-form UI (facet-app-ux.md §3.6): renders
/// `DescriptorForm.fields(for:)` for one tapped catalog row and submits via
/// `ManifestStore.submitDescriptorForm` — the SwiftUI-side sheet mirroring
/// `app.js`'s `openDescriptorForm`/`renderDescriptorForm` modal. Replaces
/// the Fire 5 Inc 2 spike's blind empty-payload "Enqueue" button
/// (`ContentView`'s prior `catalogSection`) with a form that actually
/// resolves the op's `inputSchema` into typed-in fields.
struct DescriptorFormSheet: View {
    @EnvironmentObject var store: ManifestStore
    @Environment(\.dismiss) private var dismiss
    let op: JSONValue

    @State private var values: [String: String] = [:]
    @State private var submitting = false

    private var fields: [DescriptorField] { DescriptorForm.fields(for: op) }

    var body: some View {
        NavigationStack {
            Form {
                if let description = op["description"]?.stringValue {
                    Text(description).foregroundStyle(.secondary)
                }
                if fields.isEmpty {
                    Text("No fields to fill in — every input is auto-filled from context.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                ForEach(fields) { field in
                    Section {
                        fieldInput(field)
                        if let help = field.help {
                            Text(help).font(.caption).foregroundStyle(.secondary)
                        }
                    } header: {
                        Text(field.title + (field.required ? " *" : ""))
                    }
                }
            }
            .navigationTitle(op["title"]?.stringValue ?? "Submit")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(op["submitLabel"]?.stringValue ?? "Submit") { submit() }
                        .disabled(submitting || !requiredFieldsFilled)
                }
            }
        }
    }

    private var requiredFieldsFilled: Bool {
        fields.filter(\.required).allSatisfy { !(values[$0.name] ?? "").isEmpty }
    }

    @ViewBuilder
    private func fieldInput(_ field: DescriptorField) -> some View {
        switch field.kind {
        case .text:
            TextField(field.title, text: binding(for: field.name))
        case .enumOptions(let options):
            Picker(field.title, selection: binding(for: field.name)) {
                Text("Choose…").tag("")
                ForEach(options, id: \.self) { Text($0).tag($0) }
            }
        }
    }

    private func binding(for name: String) -> Binding<String> {
        Binding(get: { values[name] ?? "" }, set: { values[name] = $0 })
    }

    private func submit() {
        submitting = true
        let ctx = DescriptorContext(actorIdentityKey: store.me?["identityKey"]?.stringValue)
        Task {
            await store.submitDescriptorForm(op: op, fieldValues: values, ctx: ctx)
            submitting = false
            dismiss()
        }
    }
}
