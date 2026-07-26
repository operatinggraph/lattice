package main

import (
	"strings"
	"testing"
)

// The Edge fleet logic tier (F19): gap classification, Interest Set phrasing,
// identity grouping, and the headline/retention/caveat lines — asserted against
// the shipped embedded asset via the goja harness.
//
// The recurring assertion: a gap the platform CANNOT determine must read as
// unknown, never as a clean "current". A device that never attached has no
// comparable position; saying "within retention window" there would tell an
// operator the device is caught up when the truth is that nothing was measured.

func edgeDeviceJS(over map[string]any) map[string]any {
	d := map[string]any{
		"identityId":  "AAAAAAAAAAAAAAAAAAAA",
		"identityKey": "vtx.identity.AAAAAAAAAAAAAAAAAAAA",
		"deviceId":    "phone",
		"gapped":      nil,
		"subscribed":  false,
	}
	for k, v := range over {
		d[k] = v
	}
	return d
}

func TestGapVerdict(t *testing.T) {
	vm := logicVM(t, "edge.js")

	// A nil/absent gapped field is UNKNOWN — the load-bearing rule.
	for _, d := range []any{
		edgeDeviceJS(nil),
		edgeDeviceJS(map[string]any{"gapped": nil, "subscribed": true, "ackFloor": int64(900)}),
		map[string]any{},
		nil,
	} {
		got, ok := call(t, vm, "gapVerdict", d).(map[string]any)
		if !ok {
			t.Fatalf("gapVerdict(%v) did not return an object", d)
		}
		if got["state"] != "unknown" {
			t.Errorf("gapVerdict(%v).state = %v, want unknown", d, got["state"])
		}
	}

	gapped := call(t, vm, "gapVerdict", edgeDeviceJS(map[string]any{
		"gapped": true, "behindBy": int64(100), "subscribed": true,
	})).(map[string]any)
	if gapped["state"] != "gapped" {
		t.Errorf("state = %v, want gapped", gapped["state"])
	}
	if numVal(t, gapped["behindBy"]) != 100 {
		t.Errorf("behindBy = %v, want 100", gapped["behindBy"])
	}

	current := call(t, vm, "gapVerdict", edgeDeviceJS(map[string]any{
		"gapped": false, "subscribed": true,
	})).(map[string]any)
	if current["state"] != "current" {
		t.Errorf("state = %v, want current", current["state"])
	}
	if numVal(t, current["behindBy"]) != 0 {
		t.Errorf("a current device is behind by 0, got %v", current["behindBy"])
	}
}

// An unknown verdict must name WHY it is unknown — bare "unknown" reads as a
// bug rather than a fact about the platform.
func TestGapLabelExplainsUnknown(t *testing.T) {
	vm := logicVM(t, "edge.js")

	unreadable := call(t, vm, "gapLabel", edgeDeviceJS(map[string]any{"subscribed": true}), false).(string)
	if !strings.Contains(unreadable, "unreadable") {
		t.Errorf("gapLabel with no stream = %q, want it to name the unreadable stream", unreadable)
	}

	never := call(t, vm, "gapLabel", edgeDeviceJS(nil), true).(string)
	if !strings.Contains(never, "never attached") {
		t.Errorf("gapLabel for a never-attached device = %q, want it to say so", never)
	}

	gapped := call(t, vm, "gapLabel", edgeDeviceJS(map[string]any{
		"gapped": true, "behindBy": int64(1), "subscribed": true,
	}), true).(string)
	if !strings.Contains(gapped, "1 message aged out") {
		t.Errorf("gapLabel singular = %q, want singular phrasing", gapped)
	}

	current := call(t, vm, "gapLabel", edgeDeviceJS(map[string]any{
		"gapped": false, "subscribed": true,
	}), true).(string)
	if !strings.Contains(current, "within retention") {
		t.Errorf("gapLabel current = %q", current)
	}
}

// "Absence is never a denial" — an empty Interest Set is a WIDER subscription,
// not a narrower one. Phrasing it as "no interests" would invert its meaning.
func TestInterestSummaryEmptyMeansEverything(t *testing.T) {
	vm := logicVM(t, "edge.js")

	for _, d := range []any{
		edgeDeviceJS(nil),
		edgeDeviceJS(map[string]any{"types": []any{}, "anchors": []any{}}),
		nil,
	} {
		got := call(t, vm, "interestSummary", d).(string)
		if !strings.Contains(got, "unfiltered") || !strings.Contains(got, "everything") {
			t.Errorf("interestSummary(%v) = %q, want it to read as unfiltered/everything", d, got)
		}
	}

	filtered := call(t, vm, "interestSummary", edgeDeviceJS(map[string]any{
		"types": []any{"lease", "task"},
	})).(string)
	if !strings.Contains(filtered, "2 types") || !strings.Contains(filtered, "lease, task") {
		t.Errorf("interestSummary filtered = %q", filtered)
	}

	both := call(t, vm, "interestSummary", edgeDeviceJS(map[string]any{
		"types": []any{"lease"}, "anchors": []any{"vtx.location.x"},
	})).(string)
	if !strings.Contains(both, "1 type") || !strings.Contains(both, "1 anchor") {
		t.Errorf("interestSummary both = %q", both)
	}
}

// Grouping preserves the server's gapped-first ordering: an identity with a
// gapped device must not sink below a healthy one just because it sorts later.
func TestGroupByIdentityPreservesOrder(t *testing.T) {
	vm := logicVM(t, "edge.js")

	devices := []any{
		edgeDeviceJS(map[string]any{"identityId": "ZZZ", "identityKey": "vtx.identity.ZZZ", "deviceId": "a", "gapped": true}),
		edgeDeviceJS(map[string]any{"identityId": "AAA", "identityKey": "vtx.identity.AAA", "deviceId": "b", "gapped": false}),
		edgeDeviceJS(map[string]any{"identityId": "ZZZ", "identityKey": "vtx.identity.ZZZ", "deviceId": "c", "gapped": false}),
	}
	groups, ok := call(t, vm, "groupByIdentity", devices).([]any)
	if !ok {
		t.Fatal("groupByIdentity did not return an array")
	}
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(groups))
	}
	first := groups[0].(map[string]any)
	if first["identityId"] != "ZZZ" {
		t.Errorf("first group = %v, want the gapped identity ZZZ first", first["identityId"])
	}
	if numVal(t, first["gapped"]) != 1 {
		t.Errorf("ZZZ gapped count = %v, want 1", first["gapped"])
	}
	if got := len(first["devices"].([]any)); got != 2 {
		t.Errorf("ZZZ device count = %d, want 2", got)
	}
	if numVal(t, groups[1].(map[string]any)["gapped"]) != 0 {
		t.Error("AAA must report 0 gapped")
	}
}

// The headline must not fold unmeasured devices into a healthy count.
func TestFleetHeadline(t *testing.T) {
	vm := logicVM(t, "edge.js")

	empty := call(t, vm, "fleetHeadline", map[string]any{"count": int64(0)}).(string)
	if !strings.Contains(empty, "No devices") {
		t.Errorf("empty headline = %q", empty)
	}

	// No readable stream ⇒ the headline says the gap state is unknown for ALL,
	// and never prints a "0 gapped" all-clear.
	noStream := call(t, vm, "fleetHeadline", map[string]any{
		"count": int64(3), "identities": int64(2), "streamKnown": false, "gapped": int64(0),
	}).(string)
	if !strings.Contains(noStream, "unknown") {
		t.Errorf("no-stream headline = %q, want it to report unknown", noStream)
	}
	if strings.Contains(noStream, "0 gapped") {
		t.Errorf("no-stream headline = %q, must not claim an all-clear", noStream)
	}

	known := call(t, vm, "fleetHeadline", map[string]any{
		"count": int64(3), "identities": int64(1), "streamKnown": true,
		"gapped": int64(1), "unsubscribed": int64(1), "stream": "SYNC",
	}).(string)
	for _, want := range []string{"3 devices", "1 identity", "1 gapped", "not attached"} {
		if !strings.Contains(known, want) {
			t.Errorf("headline %q missing %q", known, want)
		}
	}
}

func TestRetentionAndStaleLines(t *testing.T) {
	vm := logicVM(t, "edge.js")

	// No stream ⇒ no retention claim at all, rather than a 0–0 window.
	if got := call(t, vm, "retentionLine", map[string]any{"streamKnown": false}).(string); got != "" {
		t.Errorf("retentionLine without a stream = %q, want empty", got)
	}
	line := call(t, vm, "retentionLine", map[string]any{
		"streamKnown": true, "stream": "SYNC", "firstSeq": int64(500), "lastSeq": int64(600),
	}).(string)
	for _, want := range []string{"SYNC", "500", "600", "101 messages"} {
		if !strings.Contains(line, want) {
			t.Errorf("retentionLine %q missing %q", line, want)
		}
	}

	// The standing caveat only appears when there is a roster to caveat, and it
	// must state that this is registration, not liveness.
	if got := call(t, vm, "staleWarning", map[string]any{"count": int64(0)}).(string); got != "" {
		t.Errorf("staleWarning on an empty fleet = %q, want empty", got)
	}
	warn := call(t, vm, "staleWarning", map[string]any{"count": int64(2)}).(string)
	for _, want := range []string{"never expire", "not who is connected"} {
		if !strings.Contains(warn, want) {
			t.Errorf("staleWarning %q missing %q", warn, want)
		}
	}
}

// A red chip saying "0 messages aged out" reads as a contradiction, so the
// retention boundary names itself instead.
func TestGapLabelRetentionBoundary(t *testing.T) {
	vm := logicVM(t, "edge.js")
	got := call(t, vm, "gapLabel", edgeDeviceJS(map[string]any{
		"gapped": true, "behindBy": int64(0), "subscribed": true,
	}), true).(string)
	if !strings.Contains(got, "retention boundary") {
		t.Errorf("boundary label = %q, want it to name the boundary", got)
	}
	if strings.Contains(got, "0 message") {
		t.Errorf("boundary label = %q, must not claim 0 messages aged out", got)
	}
}

// An unreadable registration document must not be reported as the WIDEST
// possible subscription — that is a security-relevant claim from no evidence.
func TestInterestSummaryMalformedIsNotUnfiltered(t *testing.T) {
	vm := logicVM(t, "edge.js")
	got := call(t, vm, "interestSummary", edgeDeviceJS(map[string]any{"malformed": true})).(string)
	if strings.Contains(got, "unfiltered") || strings.Contains(got, "everything") {
		t.Errorf("malformed interest = %q, must not assert an unfiltered subscription", got)
	}
	if !strings.Contains(got, "unknown") {
		t.Errorf("malformed interest = %q, want it to read as unknown", got)
	}
}

// The headline must never print a gapped count that reads as an all-clear when
// devices went unmeasured.
func TestFleetHeadlineSurfacesUnknown(t *testing.T) {
	vm := logicVM(t, "edge.js")

	// Stream readable, but no device had a durable: every row is unmeasured, so
	// "0 gapped" would be an unearned all-clear.
	allUnknown := call(t, vm, "fleetHeadline", map[string]any{
		"count": int64(5), "identities": int64(2), "streamKnown": true,
		"gapped": int64(0), "unknown": int64(5),
	}).(string)
	if strings.Contains(allUnknown, "0 gapped") {
		t.Errorf("headline = %q, must not claim an all-clear when nothing was measured", allUnknown)
	}
	if !strings.Contains(allUnknown, "unknown for all") {
		t.Errorf("headline = %q, want it to report the fleet as unmeasured", allUnknown)
	}

	// Partially measured: the unmeasured remainder rides alongside the count.
	partial := call(t, vm, "fleetHeadline", map[string]any{
		"count": int64(5), "identities": int64(2), "streamKnown": true,
		"gapped": int64(1), "unknown": int64(2),
	}).(string)
	for _, want := range []string{"1 gapped", "2 unknown"} {
		if !strings.Contains(partial, want) {
			t.Errorf("headline %q missing %q", partial, want)
		}
	}
}

// An empty "gapped only" list is not an all-clear when rows were hidden because
// their gap state could not be determined.
func TestFilterEmptyMessage(t *testing.T) {
	vm := logicVM(t, "edge.js")

	clean := call(t, vm, "filterEmptyMessage", []any{
		edgeDeviceJS(map[string]any{"gapped": false}),
	}).(string)
	if clean != "(no gapped devices)" {
		t.Errorf("all-measured empty filter = %q", clean)
	}

	hidden := call(t, vm, "filterEmptyMessage", []any{
		edgeDeviceJS(map[string]any{"gapped": false}),
		edgeDeviceJS(nil),
		edgeDeviceJS(nil),
	}).(string)
	if !strings.Contains(hidden, "not an all-clear") || !strings.Contains(hidden, "2 device") {
		t.Errorf("filter with hidden unknowns = %q, want the unknown count stated", hidden)
	}
}

// An empty stream must not render as an inverted or zero-width range.
func TestRetentionLineEmptyStream(t *testing.T) {
	vm := logicVM(t, "edge.js")
	for _, f := range []map[string]any{
		{"streamKnown": true, "stream": "SYNC", "firstSeq": int64(101), "lastSeq": int64(100)},
		{"streamKnown": true, "stream": "SYNC", "firstSeq": int64(0), "lastSeq": int64(0)},
	} {
		got := call(t, vm, "retentionLine", f).(string)
		if !strings.Contains(got, "holds no messages") {
			t.Errorf("retentionLine(%v) = %q, want it to read as empty", f, got)
		}
		if strings.Contains(got, "–") {
			t.Errorf("retentionLine(%v) = %q, must not print a range", f, got)
		}
	}
}

// The hydration checkpoint is a Refractor pipeline sequence, not a SYNC
// position — the label must say so, or it reads as a second, contradictory
// sync cursor next to the ack floor.
func TestHydrationNoteNamesItsSequenceSpace(t *testing.T) {
	vm := logicVM(t, "edge.js")
	if got := call(t, vm, "hydrationNote", edgeDeviceJS(nil)); got != "" {
		t.Errorf("hydrationNote with no cursor = %v, want empty", got)
	}
	got := call(t, vm, "hydrationNote", edgeDeviceJS(map[string]any{"revisionCursor": int64(2487)})).(string)
	if !strings.Contains(got, "pipeline seq") || !strings.Contains(got, "2487") {
		t.Errorf("hydrationNote = %q, want it to name the pipeline sequence space", got)
	}
}

// The retention window's own clock. Every fixture below feeds the SERVER's
// `now`, because that is the clock the sequences were read on — a browser
// clock running fast would otherwise show a device as having headroom past the
// point it has any.
func edgeFleetJS(over map[string]any) map[string]any {
	f := map[string]any{
		"streamKnown":   true,
		"stream":        "SYNC",
		"firstSeq":      int64(500),
		"lastSeq":       int64(900),
		"firstTime":     "2026-07-25T12:00:00Z",
		"lastTime":      "2026-07-25T12:10:00Z",
		"maxAgeSeconds": int64(3600),
		"now":           "2026-07-25T12:10:00Z",
	}
	for k, v := range over {
		f[k] = v
	}
	return f
}

// A stream with no age limit has no deadline to report, and the view must SAY
// so: silence next to a headroom count reads as "and it will hold".
func TestFloorClockRefusesToPredictWithoutAnAgeLimit(t *testing.T) {
	vm := logicVM(t, "edge.js")

	noAge := edgeFleetJS(map[string]any{"maxAgeSeconds": int64(0)})
	if got := call(t, vm, "floorClock", noAge); got != nil {
		t.Errorf("floorClock without a max age = %v, want null", got)
	}
	line := call(t, vm, "floorClockLine", noAge).(string)
	if !strings.Contains(line, "no age limit") || !strings.Contains(line, "no deadline") {
		t.Errorf("floorClockLine = %q, want an explicit refusal to predict", line)
	}

	// Nothing to say at all when there is no readable stream.
	if got := call(t, vm, "floorClockLine", map[string]any{"streamKnown": false}).(string); got != "" {
		t.Errorf("floorClockLine without a stream = %q, want empty", got)
	}

	// With an age limit the clock is exact: the oldest retained message dies at
	// its own publish time plus the max age.
	left := numVal(t, call(t, vm, "floorClock", edgeFleetJS(nil)))
	if left != 3000 { // 3600s max age, 600s elapsed since firstTime
		t.Errorf("floorClock = %v, want 3000", left)
	}
	// Already past due reads as imminent, never as negative.
	overdue := numVal(t, call(t, vm, "floorClock", edgeFleetJS(map[string]any{
		"now": "2026-07-25T14:00:00Z",
	})))
	if overdue != 0 {
		t.Errorf("an overdue floor = %v, want 0", overdue)
	}
}

// The time half of a headroom is an ESTIMATE riding an even-spacing assumption,
// so it must decline rather than guess whenever the window cannot support the
// interpolation — and a device sitting on the floor must agree with the floor's
// own exact clock.
func TestTimeHeadroomDeclinesWhenItCannotBeDerived(t *testing.T) {
	vm := logicVM(t, "edge.js")

	onFloor := edgeDeviceJS(map[string]any{
		"gapped": false, "subscribed": true, "ackFloor": int64(500), "headroom": int64(1),
	})
	got := numVal(t, call(t, vm, "timeHeadroom", onFloor, edgeFleetJS(nil)))
	floor := numVal(t, call(t, vm, "floorClock", edgeFleetJS(nil)))
	if got != floor {
		t.Errorf("a device ON the floor has %v left, but the floor's own clock says %v — these must agree", got, floor)
	}

	// Halfway up the window is halfway later in time, so it outlives the floor.
	mid := edgeDeviceJS(map[string]any{
		"gapped": false, "subscribed": true, "ackFloor": int64(700), "headroom": int64(201),
	})
	midLeft := numVal(t, call(t, vm, "timeHeadroom", mid, edgeFleetJS(nil)))
	if midLeft <= floor {
		t.Errorf("a device further up the stream (%v) must outlive the floor (%v)", midLeft, floor)
	}

	// Every input that cannot support the estimate yields null, not a number.
	for name, args := range map[string][2]any{
		"unmeasured device":    {edgeDeviceJS(map[string]any{"subscribed": true}), edgeFleetJS(nil)},
		"no age limit":         {onFloor, edgeFleetJS(map[string]any{"maxAgeSeconds": int64(0)})},
		"no stream":            {onFloor, map[string]any{"streamKnown": false}},
		"no window timestamps": {onFloor, edgeFleetJS(map[string]any{"firstTime": "", "lastTime": ""})},
		"seq above window":     {edgeDeviceJS(map[string]any{"gapped": false, "subscribed": true, "ackFloor": int64(9999), "headroom": int64(401)}), edgeFleetJS(nil)},
	} {
		if got := call(t, vm, "timeHeadroom", args[0], args[1]); got != nil {
			t.Errorf("timeHeadroom with %s = %v, want null", name, got)
		}
	}

	// A one-message window is not a failure to interpolate — that message's
	// publish time is known outright, so the answer stays exact.
	single := edgeFleetJS(map[string]any{"lastSeq": int64(500), "lastTime": "2026-07-25T12:00:00Z"})
	if got := numVal(t, call(t, vm, "timeHeadroom", onFloor, single)); got != 3000 {
		t.Errorf("timeHeadroom on a one-message window = %v, want the exact 3000", got)
	}

	// A device BELOW the floor (headroom 0) is the tightest row on the fleet and
	// the one that most needs a deadline. Its next loss is the floor message
	// itself, whose death is EXACT — so it gets the floor's own clock, not a
	// refusal and not an interpolation.
	below := edgeDeviceJS(map[string]any{
		"gapped": false, "subscribed": true, "ackFloor": int64(10), "headroom": int64(0),
	})
	if got := numVal(t, call(t, vm, "timeHeadroom", below, edgeFleetJS(nil))); got != floor {
		t.Errorf("a device below the floor got %v, want the floor's own exact clock %v", got, floor)
	}

	// Clock skew (Loupe behind the stream's own timestamps) must never report
	// more headroom than the retention period itself grants.
	skewed := edgeFleetJS(map[string]any{"now": "2026-07-25T11:00:00Z"})
	if got := numVal(t, call(t, vm, "timeHeadroom", mid, skewed)); got > 3600 {
		t.Errorf("skewed clock yielded %v, want it capped at the 3600s retention period", got)
	}
	if got := numVal(t, call(t, vm, "floorClock", skewed)); got > 3600 {
		t.Errorf("skewed floorClock = %v, want it capped at the retention period", got)
	}
}

// "0 messages of headroom" is a reading an operator skims past; being ON the
// floor is the last moment before data starts being lost, so it says that.
func TestHeadroomLabel(t *testing.T) {
	vm := logicVM(t, "edge.js")

	// An unmeasured device claims nothing at all.
	if got := call(t, vm, "headroomLabel", edgeDeviceJS(map[string]any{"subscribed": true}), edgeFleetJS(nil)).(string); got != "" {
		t.Errorf("headroomLabel for an unmeasured device = %q, want empty", got)
	}

	onFloor := call(t, vm, "headroomLabel", edgeDeviceJS(map[string]any{
		"gapped": false, "subscribed": true, "ackFloor": int64(500), "headroom": int64(0),
	}), edgeFleetJS(nil)).(string)
	if !strings.Contains(onFloor, "retention floor") {
		t.Errorf("zero headroom = %q, want it to name the floor", onFloor)
	}
	if strings.Contains(onFloor, "0 messages of headroom") {
		t.Errorf("zero headroom = %q, must not read as a quantity to skim past", onFloor)
	}

	// The measured half and the estimated half are distinguishable: the time is
	// marked "~", the message count is not.
	roomy := call(t, vm, "headroomLabel", edgeDeviceJS(map[string]any{
		"gapped": false, "subscribed": true, "ackFloor": int64(700), "headroom": int64(201),
	}), edgeFleetJS(nil)).(string)
	if !strings.Contains(roomy, "201 messages of headroom") {
		t.Errorf("headroomLabel = %q, want the exact message count", roomy)
	}
	if !strings.Contains(roomy, "~") || !strings.Contains(roomy, "observed message spacing") {
		t.Errorf("headroomLabel = %q, want the time half marked as an estimate", roomy)
	}

	// With no age limit the count stands alone rather than borrowing a deadline.
	noAge := call(t, vm, "headroomLabel", edgeDeviceJS(map[string]any{
		"gapped": false, "subscribed": true, "ackFloor": int64(700), "headroom": int64(201),
	}), edgeFleetJS(map[string]any{"maxAgeSeconds": int64(0)})).(string)
	if strings.Contains(noAge, "~") {
		t.Errorf("headroomLabel = %q, must not estimate a time the stream does not have", noAge)
	}
}

// An absent ack timestamp is NOT evidence the device never acked: JetStream
// keeps `ack_floor.last_active` in process-local consumer state that a server
// restart zeroes. Reading it as "never" would condemn a healthy device.
func TestLastAckLabelDoesNotClaimNever(t *testing.T) {
	vm := logicVM(t, "edge.js")

	if got := call(t, vm, "lastAckLabel", edgeDeviceJS(nil), edgeFleetJS(nil)).(string); got != "" {
		t.Errorf("an unattached device has no ack activity to report, got %q", got)
	}

	absent := call(t, vm, "lastAckLabel", edgeDeviceJS(map[string]any{"subscribed": true}), edgeFleetJS(nil)).(string)
	if !strings.Contains(absent, "reset") {
		t.Errorf("absent ack time = %q, want it to name the consumer-state reset", absent)
	}
	if strings.Contains(absent, "never") {
		t.Errorf("absent ack time = %q, must not claim the device never acked", absent)
	}

	recent := call(t, vm, "lastAckLabel", edgeDeviceJS(map[string]any{
		"subscribed": true, "lastAckAt": "2026-07-25T12:05:00Z",
	}), edgeFleetJS(nil)).(string)
	if !strings.Contains(recent, "5m ago") {
		t.Errorf("lastAckLabel = %q, want it measured against the server's now", recent)
	}
}

// The triage line names the head of each tier and must AGREE with the roster
// order beneath it — a summary pointing at a different row than the list sends
// an operator to the wrong device.
func TestTriageLine(t *testing.T) {
	vm := logicVM(t, "edge.js")

	devices := []any{
		edgeDeviceJS(map[string]any{"deviceId": "lost-a-lot", "gapped": true, "behindBy": int64(399), "subscribed": true}),
		edgeDeviceJS(map[string]any{"deviceId": "lost-a-little", "gapped": true, "behindBy": int64(19), "subscribed": true}),
		edgeDeviceJS(map[string]any{"deviceId": "unmeasured", "gapped": nil}),
		edgeDeviceJS(map[string]any{"deviceId": "tight", "gapped": false, "subscribed": true, "ackFloor": int64(505), "headroom": int64(6)}),
		edgeDeviceJS(map[string]any{"deviceId": "roomy", "gapped": false, "subscribed": true, "ackFloor": int64(890), "headroom": int64(391)}),
	}
	got := call(t, vm, "triageLine", edgeFleetJS(map[string]any{"devices": devices})).(string)
	for _, want := range []string{"lost-a-lot", "399", "tight", "6 message"} {
		if !strings.Contains(got, want) {
			t.Errorf("triageLine %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "roomy") || strings.Contains(got, "lost-a-little") {
		t.Errorf("triageLine %q must name only the head of each tier", got)
	}

	// Nothing to triage against without a readable stream.
	if got := call(t, vm, "triageLine", map[string]any{"streamKnown": false, "devices": devices}).(string); got != "" {
		t.Errorf("triageLine without a stream = %q, want empty", got)
	}
	if got := call(t, vm, "triageLine", edgeFleetJS(map[string]any{"devices": []any{}})).(string); got != "" {
		t.Errorf("triageLine on an empty fleet = %q, want empty", got)
	}
}

// An unreadable registration yields NO terms — an empty term list next to
// "unfiltered" phrasing would assert the widest possible subscription from a
// document nobody could parse.
func TestInterestTerms(t *testing.T) {
	vm := logicVM(t, "edge.js")

	if got := call(t, vm, "interestTerms", edgeDeviceJS(map[string]any{
		"malformed": true, "types": []any{"lease"},
	})).([]any); len(got) != 0 {
		t.Errorf("malformed registration yielded %d terms, want 0", len(got))
	}

	terms := call(t, vm, "interestTerms", edgeDeviceJS(map[string]any{
		"types": []any{"lease", "task"}, "anchors": []any{"vtx.location.x"},
	})).([]any)
	if len(terms) != 3 {
		t.Fatalf("got %d terms, want 3", len(terms))
	}
	last := terms[2].(map[string]any)
	if last["kind"] != "anchor" || last["value"] != "vtx.location.x" {
		t.Errorf("anchor term = %v, want the anchor key carried through for linking", last)
	}
}

// The sibling list exists to separate a device fault from an identity-wide one,
// so its phrasing must draw exactly that distinction — and must not draw it at
// all when none of the siblings were measurable.
func TestSiblingLine(t *testing.T) {
	vm := logicVM(t, "edge.js")

	alone := call(t, vm, "siblingLine", []any{}).(string)
	if !strings.Contains(alone, "no other registered device") {
		t.Errorf("siblingLine with no siblings = %q", alone)
	}

	deviceLocal := call(t, vm, "siblingLine", []any{
		edgeDeviceJS(map[string]any{"gapped": false}),
		edgeDeviceJS(map[string]any{"gapped": false}),
	}).(string)
	if !strings.Contains(deviceLocal, "device-local") {
		t.Errorf("all-current siblings = %q, want the device-local reading", deviceLocal)
	}

	identityWide := call(t, vm, "siblingLine", []any{
		edgeDeviceJS(map[string]any{"gapped": true}),
		edgeDeviceJS(map[string]any{"gapped": true}),
	}).(string)
	if !strings.Contains(identityWide, "identity's lens") {
		t.Errorf("all-gapped siblings = %q, want the identity-wide reading", identityWide)
	}

	// Unmeasurable siblings say nothing either way rather than picking a side.
	blind := call(t, vm, "siblingLine", []any{edgeDeviceJS(nil), edgeDeviceJS(nil)}).(string)
	if strings.Contains(blind, "device-local") || strings.Contains(blind, "identity's lens") {
		t.Errorf("unmeasurable siblings = %q, must not draw a conclusion", blind)
	}
	if !strings.Contains(blind, "none of them measurable") {
		t.Errorf("unmeasurable siblings = %q, want the absence stated", blind)
	}
}

func TestFormatSpan(t *testing.T) {
	vm := logicVM(t, "edge.js")
	for _, c := range []struct {
		in   int64
		want string
	}{{0, "0s"}, {-5, "0s"}, {42, "42s"}, {90, "1m"}, {3600, "1h"}, {86400 * 3, "3d"}} {
		if got := call(t, vm, "formatSpan", c.in).(string); got != c.want {
			t.Errorf("formatSpan(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A failed consumer lookup must never render as "never attached", and a failed
// registration READ must never be blamed on the document.
func TestUnknownIsAttributedToTheRightCause(t *testing.T) {
	vm := logicVM(t, "edge.js")

	broke := call(t, vm, "gapLabel", edgeDeviceJS(map[string]any{"attachmentUnknown": true}), true).(string)
	if !strings.Contains(broke, "lookup failed") {
		t.Errorf("gapLabel for a failed lookup = %q, want it to name the failed measurement", broke)
	}
	if strings.Contains(broke, "never attached") {
		t.Errorf("gapLabel = %q, must not assert an absence from a measurement that did not happen", broke)
	}

	unread := call(t, vm, "interestSummary", edgeDeviceJS(map[string]any{"unreadable": true})).(string)
	if !strings.Contains(unread, "could not be read") {
		t.Errorf("interestSummary for a failed read = %q, want it to name the read", unread)
	}
	if strings.Contains(unread, "unfiltered") || strings.Contains(unread, "document unreadable") {
		t.Errorf("interestSummary = %q, must neither assert a filter nor blame the document", unread)
	}
	if got := call(t, vm, "interestTerms", edgeDeviceJS(map[string]any{
		"unreadable": true, "types": []any{"lease"},
	})).([]any); len(got) != 0 {
		t.Errorf("an unread registration yielded %d terms, want 0", len(got))
	}
}

// The summary lines read the RENDERED list: under the "gapped only" filter a
// fleet-wide summary would name a device that is not on the page and cannot be
// clicked, which is worse than no summary at all.
func TestSummaryLinesFollowTheRenderedList(t *testing.T) {
	vm := logicVM(t, "edge.js")

	gapped := edgeDeviceJS(map[string]any{"deviceId": "phone", "gapped": true, "behindBy": int64(399), "subscribed": true})
	current := edgeDeviceJS(map[string]any{
		"deviceId": "laptop", "gapped": false, "subscribed": true, "ackFloor": int64(505), "headroom": int64(6),
	})
	fleet := edgeFleetJS(map[string]any{"devices": []any{gapped, current}})

	filtered := call(t, vm, "triageLine", fleet, []any{gapped}).(string)
	if strings.Contains(filtered, "laptop") {
		t.Errorf("triageLine over the filtered list = %q, must not name a hidden device", filtered)
	}
	if !strings.Contains(filtered, "phone") {
		t.Errorf("triageLine = %q, want the visible worst device named", filtered)
	}
	if spread := call(t, vm, "headroomSpread", fleet, []any{gapped}).(string); strings.Contains(spread, "laptop") ||
		strings.Contains(spread, "still-current device") {
		t.Errorf("headroomSpread over a gapped-only list = %q, want no still-current claim", spread)
	}
}

// The fleet summary is shaped by retention headroom, and it must never let the
// measured range read as covering devices that were not measured.
func TestHeadroomSpread(t *testing.T) {
	vm := logicVM(t, "edge.js")

	dev := func(id string, headroom any) map[string]any {
		d := map[string]any{"deviceId": id, "gapped": false, "subscribed": true, "ackFloor": int64(600)}
		if headroom != nil {
			d["headroom"] = headroom
		} else {
			d["gapped"] = nil
		}
		return edgeDeviceJS(d)
	}

	got := call(t, vm, "headroomSpread", edgeFleetJS(nil), []any{
		dev("a", int64(6)), dev("b", int64(401)), dev("c", int64(90)),
	}).(string)
	for _, want := range []string{"3 still-current devices", "between 6 and 401 messages"} {
		if !strings.Contains(got, want) {
			t.Errorf("headroomSpread %q missing %q", got, want)
		}
	}

	// One device reads as a single value, not a degenerate "between 6 and 6".
	one := call(t, vm, "headroomSpread", edgeFleetJS(nil), []any{dev("a", int64(6))}).(string)
	if !strings.Contains(one, "holding 6 messages") || strings.Contains(one, "between") {
		t.Errorf("single-device spread = %q", one)
	}

	// Unmeasured devices ride alongside the range rather than inside it, and a
	// gapped device is NOT counted as unmeasured — its damage is reported
	// separately, so folding it in here would double-count it.
	mixed := call(t, vm, "headroomSpread", edgeFleetJS(nil), []any{
		dev("a", int64(6)), dev("b", nil),
		edgeDeviceJS(map[string]any{"deviceId": "g", "gapped": true, "behindBy": int64(9), "subscribed": true}),
	}).(string)
	if !strings.Contains(mixed, "1 more unmeasured") {
		t.Errorf("mixed spread = %q, want exactly the one unmeasured device counted", mixed)
	}

	// Nothing measurable at all makes no range claim.
	none := call(t, vm, "headroomSpread", edgeFleetJS(nil), []any{dev("b", nil)}).(string)
	if strings.Contains(none, "between") || !strings.Contains(none, "No still-current device") {
		t.Errorf("all-unmeasured spread = %q", none)
	}
	if got := call(t, vm, "headroomSpread", map[string]any{"streamKnown": false}, []any{dev("a", int64(6))}).(string); got != "" {
		t.Errorf("headroomSpread without a stream = %q, want empty", got)
	}
}
