import Foundation

/// Resolves a `manifest.op` row's `inputSchema`/`dispatch.*` fields into a
/// submittable form — the Swift-side mirror of `cmd/facet/web/app.js`'s
/// `renderDescriptorForm`/`renderField`/`submitDescriptorForm`
/// (facet-app-ux.md §3.6): a client renders + submits any op with zero
/// hardcoded per-operation knowledge, same as the read side's `JSONValue`
/// posture. Narrowed to the field kinds every shipped op-meta actually uses
/// today — free-text string and string enum (`OpenTab`'s `leaseAppKey`,
/// `SetAppointmentStatus`'s fixed `status` enum) — not the PWA's fuller
/// vocabulary (date/money/entity-ref/boolean/textarea), which no op-meta
/// exercises yet; extending `DescriptorFieldKind` when one does is cheap,
/// building it unused today is not (no op-meta ships those types).
public enum DescriptorFieldKind: Equatable {
    case text
    case enumOptions([String])
}

public struct DescriptorField: Identifiable, Equatable {
    public var id: String { name }
    public let name: String
    public let title: String
    public let help: String?
    public let required: Bool
    public let kind: DescriptorFieldKind
}

/// The submission-time context a descriptor form is opened with — mirrors
/// `app.js`'s `ctx` (`serviceKey`/`scopedTo`/`taskKey`, set by whichever
/// manifest row the form was opened from) plus the signed-in actor's own
/// identity key, which `app.js` reads off `me()` at submit time instead of
/// threading through `ctx`.
public struct DescriptorContext {
    public let actorIdentityKey: String?
    public let serviceKey: String?
    public let scopedTo: String?
    public let taskKey: String?
    /// The me-row's declared `selfAnchors`, keyed by Contract #1 vertex type
    /// — what `{me.<type>}` resolves against. A type appears only when the
    /// identity holds exactly one vertex of it; the caller drops an ambiguous
    /// type rather than passing a guess (`app.js`'s `selfAnchorKey`).
    public let selfAnchors: [String: String]

    public init(actorIdentityKey: String?, serviceKey: String? = nil, scopedTo: String? = nil, taskKey: String? = nil, selfAnchors: [String: String] = [:]) {
        self.actorIdentityKey = actorIdentityKey
        self.serviceKey = serviceKey
        self.scopedTo = scopedTo
        self.taskKey = taskKey
        self.selfAnchors = selfAnchors
    }
}

public struct DescriptorSubmission: Equatable {
    public let payload: JSONValue
    public let reads: [String]
    public let authContext: JSONValue?
    public let touchedKey: String?
}

public enum DescriptorForm {
    /// The visible form fields for one `manifest.op` row: `inputSchema`'s
    /// properties, minus whatever `dispatch.contextParams` auto-fills
    /// (hidden — the widget vocabulary: those fields are auto-filled and
    /// never shown) and minus `dispatch.targetField` (auto-filled from the
    /// context the op was invoked from, never user-entered — mirrors
    /// `app.js`'s `fieldNames` filter).
    ///
    /// Field order is alphabetical, not the source JSON's key order:
    /// `JSONValue.object` decodes into a Swift `Dictionary`, which does not
    /// preserve insertion order the way `app.js`'s `Object.keys` does — a
    /// real, harmless divergence from the PWA renderer's field ordering,
    /// not a bug to chase.
    public static func fields(for op: JSONValue) -> [DescriptorField] {
        guard case let .object(props)? = maybeParseJSON(op["inputSchema"])?["properties"] else { return [] }
        let requiredSet = Set((maybeParseJSON(op["inputSchema"])?["required"]?.arrayValue ?? []).compactMap { $0.stringValue })
        var fieldDescs: [String: JSONValue] = [:]
        if case let .object(d)? = maybeParseJSON(op["fieldDescriptions"]) { fieldDescs = d }
        var contextParamKeys: Set<String> = []
        if case let .object(d)? = maybeParseJSON(op["dispatchContextParams"]) { contextParamKeys = Set(d.keys) }
        let targetField = op["dispatchTargetField"]?.stringValue

        return props.keys.filter { !contextParamKeys.contains($0) && $0 != targetField }.sorted().map { name in
            let propSchema = props[name] ?? .object([:])
            let title = propSchema["title"]?.stringValue ?? name
            let kind: DescriptorFieldKind
            if let enumVals = propSchema["enum"]?.arrayValue, !enumVals.isEmpty {
                kind = .enumOptions(enumVals.compactMap { $0.stringValue })
            } else {
                kind = .text
            }
            return DescriptorField(name: name, title: title, help: fieldDescs[name]?.stringValue, required: requiredSet.contains(name), kind: kind)
        }
    }

    /// Assembles the Contract #2 envelope pieces for one submission —
    /// `payload`/`reads`/`authContext`/`touchedKey` — from the visible
    /// fields' typed-in values plus the op's dispatch recipe, mirroring
    /// `app.js`'s `submitDescriptorForm` step for step: user fields first,
    /// then `contextParams` substitution, then `targetField`, then reads
    /// (`dispatch.reads` templates, the resolved `targetField` value, and
    /// the actor's own key — all unconditionally, since under-reading fails
    /// the request and over-reading is harmless).
    public static func buildSubmission(op: JSONValue, fieldValues: [String: String], ctx: DescriptorContext) -> DescriptorSubmission {
        let fieldNames = fields(for: op).map(\.name)
        var payload: [String: JSONValue] = [:]
        for name in fieldNames {
            if let v = fieldValues[name], !v.isEmpty {
                payload[name] = .string(v)
            }
        }

        var contextParams: [String: String] = [:]
        if case let .object(d)? = maybeParseJSON(op["dispatchContextParams"]) {
            for (k, v) in d { if let s = v.stringValue { contextParams[k] = s } }
        }
        for (field, template) in contextParams {
            payload[field] = .string(substituteTemplate(template, ctx: ctx, payload: payload))
        }

        let authKind = op["dispatchAuthContext"]?.stringValue
        let targetField = op["dispatchTargetField"]?.stringValue
        if let targetField {
            payload[targetField] = .string(targetFieldValue(authKind, ctx: ctx) ?? "")
        }

        var reads: [String] = []
        if case let .array(templates)? = op["dispatchReads"] {
            for t in templates {
                if let s = t.stringValue {
                    reads.append(substituteTemplate(s, ctx: ctx, payload: payload))
                }
            }
        }
        if let targetField, let v = payload[targetField]?.stringValue, !v.isEmpty, !reads.contains(v) {
            reads.append(v)
        }
        if let selfKey = ctx.actorIdentityKey, !reads.contains(selfKey) {
            reads.append(selfKey)
        }

        return DescriptorSubmission(
            payload: .object(payload),
            reads: reads,
            authContext: buildAuthContext(authKind, ctx: ctx),
            touchedKey: resolveTouchedKey(authKind: authKind, ctx: ctx)
        )
    }

    private static func buildAuthContext(_ kind: String?, ctx: DescriptorContext) -> JSONValue? {
        switch kind {
        case "self": return .object(["target": .string(ctx.actorIdentityKey ?? "")])
        case "service": return .object(["service": .string(ctx.serviceKey ?? "")])
        case "task": return .object(["task": .string(ctx.taskKey ?? ""), "target": .string(ctx.scopedTo ?? "")])
        default: return nil
        }
    }

    private static func targetFieldValue(_ kind: String?, ctx: DescriptorContext) -> String? {
        switch kind {
        case "self": return ctx.actorIdentityKey
        case "service": return ctx.serviceKey
        case "task": return ctx.scopedTo
        default: return nil
        }
    }

    /// Mirrors `app.js`'s `resolveTouchedKey` (design R3): a task's own key
    /// takes precedence (it should disappear from the Tasks list on
    /// confirm), else the actor's own key for a self-scoped op, else none —
    /// a create op has no predictable target.
    private static func resolveTouchedKey(authKind: String?, ctx: DescriptorContext) -> String? {
        if let taskKey = ctx.taskKey { return taskKey }
        if authKind == "self" { return ctx.actorIdentityKey }
        return nil
    }

    /// Mirrors `app.js`'s `substituteTemplate`: replaces each `{expr}` token
    /// with `{actor}`/`{service}`/`{scopedTo}`/`{me.<type>}`/`{payload.<field>}`,
    /// left-to-right, single pass — an unrecognized token is left verbatim.
    private static func substituteTemplate(_ str: String, ctx: DescriptorContext, payload: [String: JSONValue]) -> String {
        var result = ""
        var i = str.startIndex
        while i < str.endIndex {
            if str[i] == "{", let close = str[i...].firstIndex(of: "}") {
                let expr = String(str[str.index(after: i)..<close])
                if let resolved = resolveTemplateExpr(expr, ctx: ctx, payload: payload) {
                    result += resolved
                } else {
                    result += "{\(expr)}"
                }
                i = str.index(after: close)
            } else {
                result.append(str[i])
                i = str.index(after: i)
            }
        }
        return result
    }

    private static func resolveTemplateExpr(_ expr: String, ctx: DescriptorContext, payload: [String: JSONValue]) -> String? {
        if expr == "actor" { return ctx.actorIdentityKey ?? "" }
        if expr == "service" { return ctx.serviceKey ?? "" }
        if expr == "scopedTo" { return ctx.scopedTo ?? "" }
        // `{me.<type>}` — the submitting identity's own vertex of that type.
        // Resolving to "" rather than leaving the token verbatim is the point:
        // a literal "{me.leaseapp}" reaching the Processor as a vertex key is
        // strictly worse than an empty one that fails the op outright.
        if expr.hasPrefix("me.") {
            return ctx.selfAnchors[String(expr.dropFirst("me.".count))] ?? ""
        }
        if expr.hasPrefix("payload.") {
            let field = String(expr.dropFirst("payload.".count))
            return payload[field]?.stringValue ?? ""
        }
        return nil
    }

    /// `manifest.op` rows carry `inputSchema`/`fieldDescriptions`/
    /// `dispatchContextParams` either as a real JSON object or as a
    /// JSON-encoded string, per aspect (`JSONValue.swift`'s doc comment) —
    /// mirrors `app.js`'s `maybeParseJSON`. `dispatchReads` is never
    /// double-encoded this way (it's a flat array column straight off the
    /// lens projection), so callers read it directly, not through this.
    private static func maybeParseJSON(_ v: JSONValue?) -> JSONValue? {
        guard let v else { return nil }
        if case let .string(s) = v {
            guard let data = s.data(using: .utf8) else { return nil }
            return try? JSONDecoder().decode(JSONValue.self, from: data)
        }
        return v
    }
}
