import XCTest
@testable import FacetManifestKit

/// Fixtures are the REAL shipped op-meta JSON (copied from each package's
/// `opmetas.go`, not invented), asserting `DescriptorForm` against the
/// actual vocabulary the platform emits — `inputSchema` arrives as a
/// JSON-encoded STRING (`OpMetaSpec.InputSchema` is a Go `string` field,
/// per `internal/pkgmgr/definition.go`), so fixtures encode it that way
/// too, exercising `DescriptorForm`'s `maybeParseJSON` path for real.
final class DescriptorFormTests: XCTestCase {
    private func opRow(
        operationType: String, inputSchema: String, fieldDescriptions: [String: JSONValue] = [:],
        dispatchAuthContext: String? = nil, dispatchTargetField: String? = nil,
        dispatchContextParams: [String: JSONValue]? = nil, dispatchReads: [String]? = nil
    ) -> JSONValue {
        var fields: [String: JSONValue] = [
            "operationType": .string(operationType),
            "inputSchema": .string(inputSchema),
            "fieldDescriptions": .object(fieldDescriptions),
        ]
        if let dispatchAuthContext { fields["dispatchAuthContext"] = .string(dispatchAuthContext) }
        if let dispatchTargetField { fields["dispatchTargetField"] = .string(dispatchTargetField) }
        if let dispatchContextParams { fields["dispatchContextParams"] = .object(dispatchContextParams) }
        if let dispatchReads { fields["dispatchReads"] = .array(dispatchReads.map(JSONValue.string)) }
        return .object(fields)
    }

    // MARK: cafe-domain OpenTab (packages/cafe-domain/opmetas.go) — single
    // required free-text field, no targetField/contextParams, first real
    // use of Dispatch.Reads (this fire's package fix).

    func testOpenTabFields() {
        let op = opRow(
            operationType: "OpenTab",
            inputSchema: #"{"type":"object","properties":{"leaseAppKey":{"type":"string","description":"vtx.leaseapp.<NanoID> of your own lease application."}},"required":["leaseAppKey"]}"#,
            fieldDescriptions: ["leaseAppKey": .string("Your own lease application.")],
            dispatchAuthContext: "self", dispatchReads: ["{payload.leaseAppKey}"]
        )
        let fields = DescriptorForm.fields(for: op)
        XCTAssertEqual(fields.map(\.name), ["leaseAppKey"])
        XCTAssertEqual(fields[0].kind, .text)
        XCTAssertTrue(fields[0].required)
        XCTAssertEqual(fields[0].help, "Your own lease application.")
    }

    func testOpenTabSubmissionDerivesReadsFromDispatchReadsTemplate() {
        let op = opRow(
            operationType: "OpenTab",
            inputSchema: #"{"type":"object","properties":{"leaseAppKey":{"type":"string"}},"required":["leaseAppKey"]}"#,
            dispatchAuthContext: "self", dispatchReads: ["{payload.leaseAppKey}"]
        )
        let ctx = DescriptorContext(actorIdentityKey: "vtx.identity.RESIDENT0000000001")
        let submission = DescriptorForm.buildSubmission(
            op: op, fieldValues: ["leaseAppKey": "vtx.leaseapp.LEASE00000000000001"], ctx: ctx)

        XCTAssertEqual(submission.payload["leaseAppKey"]?.stringValue, "vtx.leaseapp.LEASE00000000000001")
        XCTAssertEqual(submission.reads, ["vtx.leaseapp.LEASE00000000000001", "vtx.identity.RESIDENT0000000001"])
        XCTAssertEqual(submission.authContext, .object(["target": .string("vtx.identity.RESIDENT0000000001")]))
        XCTAssertEqual(submission.touchedKey, "vtx.identity.RESIDENT0000000001")
    }

    // MARK: service-domain RequestService (packages/service-domain/ddls.go)
    // — the platform's first service-path consumer op: its one property is
    // entirely auto-filled via dispatch.targetField, so the form has zero
    // visible fields.

    func testRequestServiceHasNoVisibleFields() {
        let op = opRow(
            operationType: "RequestService",
            inputSchema: #"{"type":"object","properties":{"service":{"type":"string"}},"required":["service"]}"#,
            dispatchAuthContext: "service", dispatchTargetField: "service"
        )
        XCTAssertEqual(DescriptorForm.fields(for: op), [])
    }

    func testRequestServiceSubmissionAutoFillsTargetFieldAndReads() {
        let op = opRow(
            operationType: "RequestService",
            inputSchema: #"{"type":"object","properties":{"service":{"type":"string"}},"required":["service"]}"#,
            dispatchAuthContext: "service", dispatchTargetField: "service"
        )
        let ctx = DescriptorContext(actorIdentityKey: "vtx.identity.CONSUMER00000000001", serviceKey: "vtx.service.TEMPLATE000000001")
        let submission = DescriptorForm.buildSubmission(op: op, fieldValues: [:], ctx: ctx)

        XCTAssertEqual(submission.payload["service"]?.stringValue, "vtx.service.TEMPLATE000000001")
        XCTAssertEqual(submission.reads, ["vtx.service.TEMPLATE000000001", "vtx.identity.CONSUMER00000000001"])
        XCTAssertEqual(submission.authContext, .object(["service": .string("vtx.service.TEMPLATE000000001")]))
        // authKind "service" (not "self"/"task") and no taskKey: no touchedKey — a create op mints a
        // fresh instance with no predictable target (mirrors app.js's resolveTouchedKey doc comment).
        XCTAssertNil(submission.touchedKey)
    }

    // MARK: clinic-domain SetAppointmentStatus (packages/clinic-domain/opmetas.go)
    // — a fixed-choice enum field alongside a targetField, proving enum
    // rendering + the targetField exclusion work together.

    func testSetAppointmentStatusFieldsExcludeTargetFieldAndDetectEnum() {
        let op = opRow(
            operationType: "SetAppointmentStatus",
            inputSchema: #"{"type":"object","properties":{"appointmentKey":{"type":"string"},"status":{"type":"string","enum":["cancelled"],"default":"cancelled"},"note":{"type":"string"}},"required":["appointmentKey","status"]}"#,
            dispatchAuthContext: "self", dispatchTargetField: "appointmentKey"
        )
        let fields = DescriptorForm.fields(for: op)
        XCTAssertEqual(fields.map(\.name), ["note", "status"])
        let status = fields.first { $0.name == "status" }
        XCTAssertEqual(status?.kind, .enumOptions(["cancelled"]))
        XCTAssertTrue(status?.required ?? false)
        let note = fields.first { $0.name == "note" }
        XCTAssertEqual(note?.kind, .text)
        XCTAssertFalse(note?.required ?? true)
    }

    // MARK: wellness-domain CreateBooking (packages/wellness-domain/opmetas.go)
    // — the platform's first real use of dispatch.contextParams
    // ({"booker": "{actor}"}), a field that is not even an inputSchema
    // property at all.

    func testCreateBookingContextParamAddsUndeclaredPayloadField() {
        let op = opRow(
            operationType: "CreateBooking",
            inputSchema: #"{"type":"object","properties":{"session":{"type":"string"},"leaseAppKey":{"type":"string"}},"required":["session"]}"#,
            dispatchAuthContext: "self", dispatchTargetField: "session",
            dispatchContextParams: ["booker": .string("{actor}")]
        )
        // "booker" is not an inputSchema property, so it never shows up as a visible field either way.
        XCTAssertEqual(DescriptorForm.fields(for: op).map(\.name), ["leaseAppKey"])

        let ctx = DescriptorContext(actorIdentityKey: "vtx.identity.BOOKER000000000001")
        let submission = DescriptorForm.buildSubmission(op: op, fieldValues: [:], ctx: ctx)
        XCTAssertEqual(submission.payload["booker"]?.stringValue, "vtx.identity.BOOKER000000000001")
    }

    // MARK: cafe-domain VoidCharge (packages/cafe-domain/opmetas.go) — the
    // `Cents$` money convention (no shipped op-meta declares `x-format`, so
    // the name suffix IS the vocabulary's only money signal, mirroring
    // `app.js`'s `renderField`).

    func testVoidChargeDetectsMoneyAndConvertsDollarsToCents() {
        let op = opRow(
            operationType: "VoidCharge",
            inputSchema: #"{"type":"object","properties":{"tabKey":{"type":"string"},"amountCents":{"type":"integer","title":"Amount","minimum":1}},"required":["tabKey","amountCents"]}"#,
            dispatchAuthContext: "standing", dispatchTargetField: "tabKey"
        )
        let fields = DescriptorForm.fields(for: op)
        XCTAssertEqual(fields.map(\.name), ["amountCents"])
        XCTAssertEqual(fields[0].kind, .money)

        let submission = DescriptorForm.buildSubmission(op: op, fieldValues: ["amountCents": "4.50"], ctx: DescriptorContext(actorIdentityKey: nil))
        XCTAssertEqual(submission.payload["amountCents"], .number(450))
    }

    // MARK: clinic-domain RecordEncounter (packages/clinic-domain/opmetas.go)
    // — boolean `followUpRequested` alongside plain-date `followUpDate`,
    // proving both new kinds render AND submit correctly from the same op.

    func testRecordEncounterDetectsBooleanAndDate() {
        let op = opRow(
            operationType: "RecordEncounter",
            inputSchema: #"{"type":"object","properties":{"appointmentKey":{"type":"string"},"summary":{"type":"string"},"followUpRequested":{"type":"boolean","title":"Follow-up needed"},"followUpDate":{"type":"string","format":"date","title":"Follow-up date"}},"required":["appointmentKey","summary"]}"#,
            dispatchAuthContext: "standing", dispatchTargetField: "appointmentKey"
        )
        let fields = DescriptorForm.fields(for: op)
        XCTAssertEqual(fields.map(\.name), ["followUpDate", "followUpRequested", "summary"])
        XCTAssertEqual(fields.first { $0.name == "followUpRequested" }?.kind, .boolean)
        XCTAssertEqual(fields.first { $0.name == "followUpDate" }?.kind, .date)

        // An untouched (never-toggled) boolean still submits explicit false, never absence.
        let untouched = DescriptorForm.buildSubmission(op: op, fieldValues: ["summary": "Routine visit"], ctx: DescriptorContext(actorIdentityKey: nil))
        XCTAssertEqual(untouched.payload["followUpRequested"], .bool(false))
        XCTAssertNil(untouched.payload["followUpDate"])

        let withFollowUp = DescriptorForm.buildSubmission(
            op: op, fieldValues: ["summary": "Routine visit", "followUpRequested": "true", "followUpDate": "2026-09-01"],
            ctx: DescriptorContext(actorIdentityKey: nil))
        XCTAssertEqual(withFollowUp.payload["followUpRequested"], .bool(true))
        XCTAssertEqual(withFollowUp.payload["followUpDate"], .string("2026-09-01"))
    }

    // MARK: clinic-domain RescheduleAppointment (packages/clinic-domain/opmetas.go)
    // — date-time `startsAt`/`endsAt` plus an `x-entityRef` `provider` field
    // that is NOT the dispatch target, proving entity-ref detection is
    // independent of the targetField exclusion.

    func testRescheduleAppointmentDetectsDateTimeAndEntityRef() {
        let op = opRow(
            operationType: "RescheduleAppointment",
            inputSchema: #"{"type":"object","properties":{"appointmentKey":{"type":"string"},"provider":{"type":"string","title":"Provider","x-entityRef":"provider"},"startsAt":{"type":"string","format":"date-time","title":"New start"},"endsAt":{"type":"string","format":"date-time","title":"New end"}},"required":["appointmentKey","provider","startsAt","endsAt"]}"#,
            dispatchAuthContext: "self", dispatchTargetField: "appointmentKey"
        )
        let fields = DescriptorForm.fields(for: op)
        XCTAssertEqual(fields.map(\.name), ["endsAt", "provider", "startsAt"])
        XCTAssertEqual(fields.first { $0.name == "provider" }?.kind, .entityRef(type: "provider"))
        XCTAssertEqual(fields.first { $0.name == "startsAt" }?.kind, .dateTime)
        XCTAssertEqual(fields.first { $0.name == "endsAt" }?.kind, .dateTime)

        let submission = DescriptorForm.buildSubmission(
            op: op,
            fieldValues: ["provider": "vtx.provider.PROVIDER000000001", "startsAt": "2026-09-01T14:00:00Z", "endsAt": "2026-09-01T14:30:00Z"],
            ctx: DescriptorContext(actorIdentityKey: "vtx.identity.PATIENT0000000001"))
        XCTAssertEqual(submission.payload["provider"]?.stringValue, "vtx.provider.PROVIDER000000001")
        XCTAssertEqual(submission.payload["startsAt"]?.stringValue, "2026-09-01T14:00:00Z")
        XCTAssertEqual(submission.payload["endsAt"]?.stringValue, "2026-09-01T14:30:00Z")
    }
}
