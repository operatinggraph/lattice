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
    /// Per-field visible query text for `.entityRef` fields — kept separate
    /// from `values`, which holds the submitted entity KEY once a candidate
    /// is picked (mirrors `app.js`'s hidden-vs-visible input split: what a
    /// visitor sees is not what the op receives).
    @State private var entityRefQuery: [String: String] = [:]

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
        case .boolean:
            Toggle(field.title, isOn: boolBinding(for: field.name))
        case .money:
            TextField(field.title, text: binding(for: field.name))
                #if canImport(UIKit)
                .keyboardType(.decimalPad)
                #endif
        case .date:
            DatePicker(field.title, selection: dateBinding(for: field.name, formatter: Self.dateOnlyFormatter), displayedComponents: .date)
        case .dateTime:
            DatePicker(field.title, selection: dateBinding(for: field.name, formatter: Self.dateTimeFormatter), displayedComponents: [.date, .hourAndMinute])
        case .entityRef(let type):
            entityRefInput(field, type: type)
        }
    }

    /// Search-and-pick control for an `.entityRef` field — mirrors
    /// `app.js`'s `entityRefCandidates`/`renderEntityRefResults`: the
    /// visible text field is a query over `ManifestStore.entityRefCandidates`,
    /// typing a character invalidates a prior pick (`values[field.name]`
    /// clears, same as `app.js`'s `onGlobalInput`), and the candidate list
    /// (capped at 6, same cap as `app.js`) only shows while nothing is
    /// picked yet.
    @ViewBuilder
    private func entityRefInput(_ field: DescriptorField, type: String) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            TextField("Search \(type)…", text: entityRefQueryBinding(for: field.name))
                #if canImport(UIKit)
                .autocapitalization(.none)
                #endif
                .disableAutocorrection(true)
            if (values[field.name] ?? "").isEmpty {
                let all = store.entityRefCandidates(type: type)
                let query = entityRefQuery[field.name] ?? ""
                let matches = query.isEmpty ? all : all.filter { $0.label.localizedCaseInsensitiveContains(query) }
                if matches.isEmpty {
                    Text(all.isEmpty ? "Nothing available to pick yet." : "Nothing matches that.")
                        .font(.caption).foregroundStyle(.secondary)
                } else {
                    ForEach(matches.prefix(6)) { candidate in
                        Button(candidate.label) {
                            values[field.name] = candidate.key
                            entityRefQuery[field.name] = candidate.label
                        }
                        .font(.caption)
                    }
                }
            }
        }
    }

    private func entityRefQueryBinding(for name: String) -> Binding<String> {
        Binding(
            get: { entityRefQuery[name] ?? "" },
            set: { newValue in
                entityRefQuery[name] = newValue
                values[name] = ""
            }
        )
    }

    private func binding(for name: String) -> Binding<String> {
        Binding(get: { values[name] ?? "" }, set: { values[name] = $0 })
    }

    private func boolBinding(for name: String) -> Binding<Bool> {
        Binding(get: { values[name] == "true" }, set: { values[name] = $0 ? "true" : "false" })
    }

    /// Round-trips through the SAME string form `DescriptorForm.buildSubmission`
    /// expects for `.date`/`.dateTime` (plain-date "yyyy-MM-dd" / RFC3339,
    /// passed through unchanged) — `DatePicker` deals natively in `Date`
    /// (an absolute instant), so unlike `app.js`'s raw `datetime-local` text
    /// input, no local→UTC conversion step is needed on this side; the
    /// formatter alone is the wire format.
    private func dateBinding(for name: String, formatter: DateFormatter) -> Binding<Date> {
        Binding(
            get: { values[name].flatMap(formatter.date(from:)) ?? Date() },
            set: { values[name] = formatter.string(from: $0) }
        )
    }

    private static let dateOnlyFormatter: DateFormatter = {
        let f = DateFormatter()
        f.dateFormat = "yyyy-MM-dd"
        f.timeZone = TimeZone(identifier: "UTC")
        f.calendar = Calendar(identifier: .gregorian)
        return f
    }()

    private static let dateTimeFormatter: DateFormatter = {
        let f = DateFormatter()
        f.dateFormat = "yyyy-MM-dd'T'HH:mm:ss'Z'"
        f.timeZone = TimeZone(identifier: "UTC")
        f.calendar = Calendar(identifier: .gregorian)
        return f
    }()

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
