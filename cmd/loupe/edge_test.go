package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// docSet builds the readDoc callback computeEdgeFleet takes, from a plain map.
// A key absent from the map reads as "gone" (raced a deregister).
func docSet(docs map[string]interestDoc) func(string) interestRead {
	return func(k string) interestRead {
		d, ok := docs[k]
		if !ok {
			return interestRead{}
		}
		return interestRead{Doc: d, Found: true, Parsed: true}
	}
}

// consumerSet builds the readConsumer callback from durable-name → state. A
// durable absent from the map reads as "no such consumer", never as a failed
// lookup — the two are distinct outcomes and the tests that care say so.
func consumerSet(cs map[string]consumerState) func(string, string) consumerLookup {
	return func(identityID, deviceID string) consumerLookup {
		c, ok := cs[edgeSyncDurable(identityID, deviceID)]
		if !ok {
			return consumerLookup{}
		}
		return consumerLookup{State: c, Found: true}
	}
}

func TestSplitInterestKey(t *testing.T) {
	id, dev, ok := splitInterestKey("AAAAAAAAAAAAAAAAAAAA.phone-1")
	require.True(t, ok)
	assert.Equal(t, "AAAAAAAAAAAAAAAAAAAA", id)
	assert.Equal(t, "phone-1", dev)

	// The device half may itself contain dots — only the identity half is a
	// subject token, so the split is on the FIRST dot, not the last.
	id, dev, ok = splitInterestKey("AAAAAAAAAAAAAAAAAAAA.chrome.macos.1")
	require.True(t, ok)
	assert.Equal(t, "AAAAAAAAAAAAAAAAAAAA", id)
	assert.Equal(t, "chrome.macos.1", dev)

	for _, bad := range []string{"", "nodot", ".leading", "trailing."} {
		_, _, ok := splitInterestKey(bad)
		assert.False(t, ok, "key %q must not split", bad)
	}
}

// The durable name must match internal/edge/sync's construction by value, or
// every device looks unattached and the whole fleet reads as unmeasurable.
func TestEdgeSyncDurableMatchesProducer(t *testing.T) {
	assert.Equal(t, "edge-sync-MQsmTTAgNkngkdEjQz9L-BHrdHRUWXPkLiukEvK9e",
		edgeSyncDurable("MQsmTTAgNkngkdEjQz9L", "BHrdHRUWXPkLiukEvK9e"))
}

// A device whose durable has consumed past the stream's retention floor is
// healthy; one the floor has overtaken is gapped, and BehindBy counts the
// messages actually lost.
func TestComputeEdgeFleet_GapFromAckFloor(t *testing.T) {
	const idA, idB = "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB"
	keys := []string{idA + ".phone", idB + ".laptop"}
	docs := map[string]interestDoc{
		idA + ".phone":  {Types: []string{"lease"}, RegisteredAt: "2026-07-19T00:00:00Z"},
		idB + ".laptop": {RegisteredAt: "2026-07-19T00:00:00Z"},
	}
	cons := map[string]consumerState{
		edgeSyncDurable(idA, "phone"):  {AckFloor: 400, Pending: 12}, // behind the floor
		edgeSyncDurable(idB, "laptop"): {AckFloor: 900, Pending: 0},  // inside the window
	}

	devices, gapped, unsubscribed, _ := computeEdgeFleet(keys, docSet(docs), consumerSet(cons), retentionWindow{FirstSeq: 500, LastSeq: 900, Known: true})
	require.Len(t, devices, 2)
	assert.Equal(t, 1, gapped)
	assert.Equal(t, 0, unsubscribed)

	// Gapped sorts first regardless of identity ordering.
	g := devices[0]
	assert.Equal(t, idA, g.IdentityID)
	assert.Equal(t, "vtx.identity."+idA, g.IdentityKey)
	require.NotNil(t, g.Gapped)
	assert.True(t, *g.Gapped)
	// Lost messages are (400, 500) exclusive — 401..499, i.e. 99, not 100.
	assert.Equal(t, uint64(99), g.BehindBy)
	assert.True(t, g.Subscribed)
	assert.Equal(t, uint64(12), g.Pending)
	assert.False(t, g.Unfiltered, "a declared type is a filter")

	ok := devices[1]
	require.NotNil(t, ok.Gapped)
	assert.False(t, *ok.Gapped)
	assert.Zero(t, ok.BehindBy)
	assert.True(t, ok.Unfiltered, "no types and no anchors admits everything")
}

// Gapped means data was actually lost — at least one message strictly between
// the device's ack floor and the oldest sequence still retained.
//
// This deliberately diverges from the platform's syncgap predicate
// (`cursor < firstSeq`), which also fires at the boundary where nothing was
// lost. That conservatism is correct for a device deciding whether to
// re-hydrate (cost: one redundant hydrate) and wrong for an operator's triage
// metric: the SYNC stream is MaxAge-limited, so a stack idle past the window
// ages to empty and reports firstSeq = lastSeq+1, at which point every
// caught-up device would satisfy the platform predicate and the whole fleet
// would render red with nothing wrong. These cases pin that divergence.
func TestComputeEdgeFleet_GappedMeansDataActuallyLost(t *testing.T) {
	const id = "AAAAAAAAAAAAAAAAAAAA"
	run := func(ackFloor, firstSeq uint64) edgeDevice {
		devices, _, _, _ := computeEdgeFleet(
			[]string{id + ".phone"},
			docSet(map[string]interestDoc{id + ".phone": {}}),
			consumerSet(map[string]consumerState{edgeSyncDurable(id, "phone"): {AckFloor: ackFloor}}),
			retentionWindow{FirstSeq: firstSeq, LastSeq: firstSeq + 400, Known: true})
		require.Len(t, devices, 1)
		return devices[0]
	}
	notGapped := func(t *testing.T, d edgeDevice, why string) {
		t.Helper()
		require.NotNil(t, d.Gapped, why)
		assert.False(t, *d.Gapped, why)
		assert.Zero(t, d.BehindBy)
	}

	// Fully caught up: the floor has not passed the device at all.
	notGapped(t, run(500, 500), "ack floor at the retention floor lost nothing")

	// The boundary: oldest retained is exactly the next message the device
	// wants. Nothing in between, so nothing lost — this is the case that would
	// otherwise turn an idle stack's entire fleet red.
	notGapped(t, run(499, 500), "ack floor one below the retention floor lost nothing")

	// An attached device that never acked, on a stream retaining from seq 1:
	// message 1 is still there, so it has missed nothing yet.
	notGapped(t, run(0, 1), "nothing has aged out of a stream retaining from 1")

	// An empty stream leaves nothing to be behind.
	notGapped(t, run(0, 0), "an empty stream cannot have discarded anything")

	// One message strictly between (seq 499) actually aged out.
	lostOne := run(498, 500)
	require.NotNil(t, lostOne.Gapped)
	assert.True(t, *lostOne.Gapped)
	assert.Equal(t, uint64(1), lostOne.BehindBy)

	// A never-acked device on a stream that has already discarded seq 1.
	lostFromZero := run(0, 2)
	require.NotNil(t, lostFromZero.Gapped)
	assert.True(t, *lostFromZero.Gapped)
	assert.Equal(t, uint64(1), lostFromZero.BehindBy)
}

// The unknown counter must account for every device whose gap state was not
// determined, so the headline can refuse to print an all-clear it did not earn.
func TestComputeEdgeFleet_UnknownCounted(t *testing.T) {
	const id = "AAAAAAAAAAAAAAAAAAAA"
	keys := []string{id + ".attached", id + ".never"}
	docs := map[string]interestDoc{id + ".attached": {}, id + ".never": {}}
	cons := map[string]consumerState{edgeSyncDurable(id, "attached"): {AckFloor: 900}}

	_, _, _, unknown := computeEdgeFleet(keys, docSet(docs), consumerSet(cons), retentionWindow{FirstSeq: 500, LastSeq: 900, Known: true})
	assert.Equal(t, 1, unknown, "only the unattached device is unmeasured")

	// No readable stream ⇒ nothing is measurable at all.
	_, _, _, allUnknown := computeEdgeFleet(keys, docSet(docs), consumerSet(cons), retentionWindow{FirstSeq: 0, LastSeq: 0, Known: false})
	assert.Equal(t, 2, allUnknown)
}

// The load-bearing honesty rule: an unanswerable gap question must read as
// unknown (nil), never as a clean false.
func TestComputeEdgeFleet_UnknownIsNeverFalse(t *testing.T) {
	const id = "AAAAAAAAAAAAAAAAAAAA"

	t.Run("stream unreadable", func(t *testing.T) {
		docs := map[string]interestDoc{id + ".phone": {RevisionCursor: 900}}
		cons := map[string]consumerState{edgeSyncDurable(id, "phone"): {AckFloor: 900}}
		devices, gapped, unsubscribed, _ := computeEdgeFleet([]string{id + ".phone"}, docSet(docs), consumerSet(cons), retentionWindow{FirstSeq: 0, LastSeq: 0, Known: false})
		require.Len(t, devices, 1)
		assert.Nil(t, devices[0].Gapped, "no readable stream ⇒ no verdict")
		assert.Zero(t, gapped)
		// Attachment is read THROUGH the stream, so with no stream it is
		// unknown rather than absent — counting it as unattached would assert
		// something never measured.
		assert.Zero(t, unsubscribed, "attachment is unmeasurable without the stream")
	})

	t.Run("registered but never attached", func(t *testing.T) {
		docs := map[string]interestDoc{id + ".phone": {RegisteredAt: "2026-07-19T00:00:00Z"}}
		devices, gapped, unsubscribed, _ := computeEdgeFleet([]string{id + ".phone"}, docSet(docs), consumerSet(nil), retentionWindow{FirstSeq: 500, LastSeq: 900, Known: true})
		require.Len(t, devices, 1)
		assert.Nil(t, devices[0].Gapped, "no durable ⇒ no comparable position")
		assert.False(t, devices[0].Subscribed)
		assert.Zero(t, gapped)
		assert.Equal(t, 1, unsubscribed)
	})
}

// revisionCursor is the Refractor pipeline's LastAppliedSeq — a position in the
// Core-KV change stream, NOT a SYNC sequence. Comparing it to the SYNC
// retention floor would manufacture gaps out of unrelated counters, so it must
// never produce a verdict however far apart the two numbers are.
func TestComputeEdgeFleet_RevisionCursorNeverProducesVerdict(t *testing.T) {
	const id = "AAAAAAAAAAAAAAAAAAAA"
	docs := map[string]interestDoc{id + ".phone": {RevisionCursor: 2487}}
	// 2487 sits far below a realistic SYNC floor of 8355 — the exact shape that
	// would read as "gapped by 5867" if the two sequence spaces were conflated.
	devices, gapped, _, _ := computeEdgeFleet([]string{id + ".phone"}, docSet(docs), consumerSet(nil), retentionWindow{FirstSeq: 8355, LastSeq: 8755, Known: true})
	require.Len(t, devices, 1)
	assert.Nil(t, devices[0].Gapped, "a hydration checkpoint is not a stream position")
	assert.Zero(t, devices[0].BehindBy)
	assert.Zero(t, gapped)
	// It is still carried for display.
	assert.Equal(t, uint64(2487), devices[0].RevisionCursor)
}

// Rows that cannot be attributed to an identity are dropped, not rendered
// unattributed; a row whose doc raced a deregister drops; a row whose doc is
// unreadable survives, flagged, so a read fault cannot silently shorten the
// roster.
func TestComputeEdgeFleet_MalformedAndRacedRows(t *testing.T) {
	const id = "AAAAAAAAAAAAAAAAAAAA"
	keys := []string{"nodot", id + ".gone", id + ".broken"}
	readDoc := func(k string) interestRead {
		switch k {
		case id + ".gone":
			return interestRead{} // deregistered mid-page
		case id + ".broken":
			return interestRead{Found: true} // present, and its document will not parse
		}
		return interestRead{}
	}
	devices, _, _, _ := computeEdgeFleet(keys, readDoc, consumerSet(nil), retentionWindow{FirstSeq: 500, LastSeq: 900, Known: true})
	require.Len(t, devices, 1, "only the unreadable-but-present row survives")
	assert.Equal(t, id+".broken", devices[0].Key)
	assert.True(t, devices[0].Malformed)
	assert.Nil(t, devices[0].Gapped)
	// An unreadable doc must not be reported as an unfiltered (widest)
	// subscription — that would be a security-relevant claim from no evidence.
	assert.False(t, devices[0].Unfiltered)
}

// The SYNC stream is discovered from the installed personal lens specs, never
// assumed — and an ambiguous or absent discovery yields a note, not a guess.
func TestPersonalSyncStream(t *testing.T) {
	spec := func(m map[string]lensSpecInfo) func(string) lensSpecInfo {
		return func(id string) lensSpecInfo { return m[id] }
	}
	// Health keys: bare NanoIDs are lens reporters; "health.*" keys are not.
	lensA, lensB := "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB"

	t.Run("one personal lens", func(t *testing.T) {
		got, note := personalSyncStream([]string{lensA, "health.refractor.r1"}, spec(map[string]lensSpecInfo{
			lensA: {TargetType: "nats_subject", Personal: true, Stream: "SYNC"},
		}))
		assert.Equal(t, "SYNC", got)
		assert.Empty(t, note)
	})

	t.Run("non-personal nats_subject lens is not the personal stream", func(t *testing.T) {
		got, note := personalSyncStream([]string{lensA}, spec(map[string]lensSpecInfo{
			lensA: {TargetType: "nats_subject", Personal: false, Stream: "BROADCAST"},
		}))
		assert.Empty(t, got)
		assert.Contains(t, note, "No Personal Lens")
	})

	t.Run("several personal lenses sharing one stream", func(t *testing.T) {
		got, note := personalSyncStream([]string{lensA, lensB}, spec(map[string]lensSpecInfo{
			lensA: {TargetType: "nats_subject", Personal: true, Stream: "SYNC"},
			lensB: {TargetType: "nats_subject", Personal: true, Stream: "SYNC"},
		}))
		assert.Equal(t, "SYNC", got)
		assert.Empty(t, note)
	})

	t.Run("ambiguous streams refuse a verdict", func(t *testing.T) {
		got, note := personalSyncStream([]string{lensA, lensB}, spec(map[string]lensSpecInfo{
			lensA: {TargetType: "nats_subject", Personal: true, Stream: "SYNC"},
			lensB: {TargetType: "nats_subject", Personal: true, Stream: "SYNC2"},
		}))
		assert.Empty(t, got, "ambiguity must not resolve to an arbitrary stream")
		assert.Contains(t, note, "more than one stream")
	})
}

// Headroom must survive the round trip as a POINTER: 0 is the tightest real
// reading there is (the device sits exactly on the floor), so it cannot share
// an encoding with "not measured".
func TestComputeEdgeFleet_HeadroomZeroIsNotAbsent(t *testing.T) {
	const id = "AAAAAAAAAAAAAAAAAAAA"
	run := func(cs map[string]consumerState, win retentionWindow) edgeDevice {
		devices, _, _, _ := computeEdgeFleet(
			[]string{id + ".phone"},
			docSet(map[string]interestDoc{id + ".phone": {}}),
			consumerSet(cs), win)
		require.Len(t, devices, 1)
		return devices[0]
	}
	win := retentionWindow{FirstSeq: 500, LastSeq: 900, Known: true}

	onFloor := run(map[string]consumerState{edgeSyncDurable(id, "phone"): {AckFloor: 499}}, win)
	require.NotNil(t, onFloor.Headroom, "a device on the floor was measured, so headroom is 0 and not absent")
	assert.Equal(t, uint64(0), *onFloor.Headroom)

	inside := run(map[string]consumerState{edgeSyncDurable(id, "phone"): {AckFloor: 600}}, win)
	require.NotNil(t, inside.Headroom)
	assert.Equal(t, uint64(101), *inside.Headroom, "500..600 inclusive is 101 retained below the device")

	// Unmeasured: no durable at all. Nothing to compare, so nothing is claimed.
	unmeasured := run(nil, win)
	assert.Nil(t, unmeasured.Headroom, "an unmeasured device must not report a headroom")
	assert.Nil(t, unmeasured.Gapped)

	// Gapped: the damage is BehindBy; a headroom alongside it would be a second,
	// contradictory reading of the same position.
	gapped := run(map[string]consumerState{edgeSyncDurable(id, "phone"): {AckFloor: 100}}, win)
	require.NotNil(t, gapped.Gapped)
	assert.True(t, *gapped.Gapped)
	assert.Nil(t, gapped.Headroom)
}

// retentionHeadroom counts only what the stream actually holds — a drained
// stream, a brand-new one, and an ack floor past the last sequence must all
// yield 0 rather than a window-sized or wrapped-around count.
func TestRetentionHeadroomClamps(t *testing.T) {
	cases := []struct {
		name     string
		ackFloor uint64
		win      retentionWindow
		want     uint64
		wantOK   bool
	}{
		// A stream retaining nothing gives no headroom READING at all. Answering
		// 0 would render every fully-caught-up device as the tightest row on the
		// fleet the moment an idle stack ages its stream to empty — the same
		// fleet-wide false red the gap predicate is written to avoid.
		{"drained stream reports firstSeq = lastSeq+1", 900, retentionWindow{FirstSeq: 901, LastSeq: 900, Known: true}, 0, false},
		{"brand-new stream reports 0/0", 0, retentionWindow{FirstSeq: 0, LastSeq: 0, Known: true}, 0, false},
		{"below the floor holds nothing", 400, retentionWindow{FirstSeq: 500, LastSeq: 900, Known: true}, 0, true},
		{"exactly on the floor holds one", 500, retentionWindow{FirstSeq: 500, LastSeq: 900, Known: true}, 1, true},
		{"at the head holds the whole window", 900, retentionWindow{FirstSeq: 500, LastSeq: 900, Known: true}, 401, true},
		{"past the head clamps to the window", 1200, retentionWindow{FirstSeq: 500, LastSeq: 900, Known: true}, 401, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := retentionHeadroom(c.ackFloor, c.win)
			assert.Equal(t, c.wantOK, ok)
			assert.Equal(t, c.want, got)
		})
	}
}

// An empty stream must leave every still-current device UNMEASURED rather than
// reporting a headroom of 0, which the console renders as "on the retention
// floor" in error styling. Nothing is retained, so nothing can be lost.
func TestComputeEdgeFleet_EmptyStreamLeavesHeadroomUnmeasured(t *testing.T) {
	const id = "AAAAAAAAAAAAAAAAAAAA"
	for _, win := range []retentionWindow{
		{FirstSeq: 901, LastSeq: 900, Known: true}, // drained by MaxAge
		{FirstSeq: 0, LastSeq: 0, Known: true},     // brand new
	} {
		devices, gapped, _, _ := computeEdgeFleet(
			[]string{id + ".phone"},
			docSet(map[string]interestDoc{id + ".phone": {}}),
			consumerSet(map[string]consumerState{edgeSyncDurable(id, "phone"): {AckFloor: 900}}),
			win)
		require.Len(t, devices, 1)
		assert.Zero(t, gapped, "an empty stream has discarded nothing this device wanted")
		require.NotNil(t, devices[0].Gapped)
		assert.False(t, *devices[0].Gapped)
		assert.Nil(t, devices[0].Headroom,
			"a stream holding nothing gives no headroom reading; 0 would render as the tightest row on the fleet")
	}
}

// A consumer lookup that BROKE is not evidence the device never attached: it
// must not join the unattached count, and it carries its own flag so the view
// can say the measurement failed instead of asserting an absence.
func TestComputeEdgeFleet_FailedConsumerLookupIsNotAnAbsence(t *testing.T) {
	const id = "AAAAAAAAAAAAAAAAAAAA"
	keys := []string{id + ".broke", id + ".absent"}
	docs := map[string]interestDoc{id + ".broke": {}, id + ".absent": {}}
	readConsumer := func(identityID, deviceID string) consumerLookup {
		if deviceID == "broke" {
			return consumerLookup{Failed: true}
		}
		return consumerLookup{}
	}

	devices, _, unsubscribed, unknown := computeEdgeFleet(keys, docSet(docs), readConsumer,
		retentionWindow{FirstSeq: 500, LastSeq: 900, Known: true})
	require.Len(t, devices, 2)
	byID := map[string]edgeDevice{}
	for _, d := range devices {
		byID[d.DeviceID] = d
	}
	assert.True(t, byID["broke"].AttachUnknown, "a failed lookup must say so")
	assert.False(t, byID["broke"].Subscribed)
	assert.False(t, byID["absent"].AttachUnknown, "a genuine ErrConsumerNotFound is an absence, not a fault")
	assert.Equal(t, 1, unsubscribed, "only the genuinely-absent durable counts as unattached")
	assert.Equal(t, 2, unknown, "neither device has a comparable position, so both are gap-unknown")
}

// A registration whose READ broke is not a corrupt document. Reporting it as
// malformed tells an operator their registration is damaged when a request
// merely timed out.
func TestComputeEdgeFleet_FailedReadIsNotAMalformedDocument(t *testing.T) {
	const id = "AAAAAAAAAAAAAAAAAAAA"
	keys := []string{id + ".broke", id + ".corrupt", id + ".gone"}
	readDoc := func(k string) interestRead {
		switch k {
		case id + ".broke":
			return interestRead{Found: true, Failed: true}
		case id + ".corrupt":
			return interestRead{Found: true}
		default:
			return interestRead{} // deregistered mid-read
		}
	}

	devices, _, _, _ := computeEdgeFleet(keys, readDoc, consumerSet(nil),
		retentionWindow{FirstSeq: 500, LastSeq: 900, Known: true})
	require.Len(t, devices, 2, "a genuinely absent registration drops; the other two stay")
	byID := map[string]edgeDevice{}
	for _, d := range devices {
		byID[d.DeviceID] = d
	}
	assert.True(t, byID["broke"].Unreadable)
	assert.False(t, byID["broke"].Malformed, "a failed read must not be blamed on the document")
	assert.True(t, byID["corrupt"].Malformed)
	assert.False(t, byID["corrupt"].Unreadable)
	// Neither may assert a filter: an unparsed document says nothing about the
	// subscription's width.
	assert.False(t, byID["broke"].Unfiltered)
	assert.False(t, byID["corrupt"].Unfiltered)
}

// The panel's roster is narrowed to one identity, and the target is matched on
// the full registration KEY: two identities may name a device the same thing.
func TestIdentityKeysAndSelectDeviceAndSiblings(t *testing.T) {
	const idA, idB = "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB"
	keys := []string{idA + ".phone", idB + ".phone", idA + ".laptop", "nodot", idB + ".tablet"}

	kin := identityKeys(keys, idA)
	assert.Equal(t, []string{idA + ".phone", idA + ".laptop"}, kin,
		"only this identity's registrations, and a key that does not split is not one")
	assert.Empty(t, identityKeys(keys, "CCCCCCCCCCCCCCCCCCCC"))

	devices := []edgeDevice{
		{Key: idA + ".phone", DeviceID: "phone"},
		{Key: idA + ".laptop", DeviceID: "laptop"},
	}
	target, siblings := selectDeviceAndSiblings(devices, idA+".laptop")
	require.NotNil(t, target)
	assert.Equal(t, "laptop", target.DeviceID)
	require.Len(t, siblings, 1)
	assert.Equal(t, "phone", siblings[0].DeviceID)

	// A device id that matches but whose key does not must NOT be selected.
	missing, all := selectDeviceAndSiblings(devices, idB+".phone")
	assert.Nil(t, missing, "matching is on the key, not the device id")
	assert.Len(t, all, 2)
}

// The roster is ordered for TRIAGE: most data lost first, then the devices
// nobody could measure, then the still-current ones with the least room left.
//
// The unmeasured tier sitting above the current one is the deliberate half: a
// device whose gap state could not be determined is an open question, and
// sinking it below every answered row buries exactly the rows to chase.
func TestComputeEdgeFleet_OrdersWorstFirst(t *testing.T) {
	const id = "AAAAAAAAAAAAAAAAAAAA"
	names := []string{"lost-a-little", "lost-a-lot", "unmeasured", "roomy", "tight"}
	keys := make([]string, 0, len(names))
	docs := map[string]interestDoc{}
	for _, n := range names {
		keys = append(keys, id+"."+n)
		docs[id+"."+n] = interestDoc{}
	}
	cons := map[string]consumerState{
		edgeSyncDurable(id, "lost-a-little"): {AckFloor: 480}, // 19 lost
		edgeSyncDurable(id, "lost-a-lot"):    {AckFloor: 100}, // 399 lost
		edgeSyncDurable(id, "roomy"):         {AckFloor: 890},
		edgeSyncDurable(id, "tight"):         {AckFloor: 505},
		// "unmeasured" has no durable at all.
	}

	devices, _, _, _ := computeEdgeFleet(keys, docSet(docs), consumerSet(cons),
		retentionWindow{FirstSeq: 500, LastSeq: 900, Known: true})
	got := make([]string, 0, len(devices))
	for _, d := range devices {
		got = append(got, d.DeviceID)
	}
	assert.Equal(t, []string{"lost-a-lot", "lost-a-little", "unmeasured", "tight", "roomy"}, got)
}

func TestHandleEdgeDevice_Validation(t *testing.T) {
	mux := testServer()

	cases := []struct {
		method, path string
		want         int
	}{
		// Key validation runs BEFORE requireConn (testServer has a nil conn), so
		// a malformed request answers 400 rather than a misleading 502.
		{"GET", "/api/edge/device", http.StatusBadRequest},
		{"GET", "/api/edge/device?key=", http.StatusBadRequest},
		{"GET", "/api/edge/device?key=nodot", http.StatusBadRequest},
		{"GET", "/api/edge/device?key=.leading", http.StatusBadRequest},
		{"GET", "/api/edge/device?key=trailing.", http.StatusBadRequest},
		{"POST", "/api/edge/device?key=AAAAAAAAAAAAAAAAAAAA.phone", http.StatusBadRequest},
		// A well-formed key with no NATS gets the honest upstream answer.
		{"GET", "/api/edge/device?key=AAAAAAAAAAAAAAAAAAAA.phone", http.StatusBadGateway},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.want {
			t.Errorf("%s %s: status = %d, want %d", c.method, c.path, rec.Code, c.want)
		}
	}
}

func TestHandleEdgeHydrateRequest_Validation(t *testing.T) {
	mux := testServer()

	cases := []struct {
		method, path string
		want         int
	}{
		// Method + key validation run BEFORE requireConn (testServer has a nil
		// conn), so a malformed request answers 400 rather than a misleading 502.
		{"GET", "/api/edge/hydrate?key=AAAAAAAAAAAAAAAAAAAA.phone", http.StatusBadRequest},
		{"POST", "/api/edge/hydrate", http.StatusBadRequest},
		{"POST", "/api/edge/hydrate?key=", http.StatusBadRequest},
		{"POST", "/api/edge/hydrate?key=nodot", http.StatusBadRequest},
		{"POST", "/api/edge/hydrate?key=.leading", http.StatusBadRequest},
		{"POST", "/api/edge/hydrate?key=trailing.", http.StatusBadRequest},
		// A well-formed key with no NATS gets the honest upstream answer.
		{"POST", "/api/edge/hydrate?key=AAAAAAAAAAAAAAAAAAAA.phone", http.StatusBadGateway},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.want {
			t.Errorf("%s %s: status = %d, want %d", c.method, c.path, rec.Code, c.want)
		}
	}
}
