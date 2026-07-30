package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// edgeDevice is one registered Personal Lens device — a row of the Refractor's
// personal-lens-interest bucket, keyed "<identityId>.<deviceId>"
// (personal-secure-lens-design.md §3.3), joined against that device's own
// durable consumer on the SYNC stream.
//
// The row carries two numbers that look comparable and are NOT. AckFloor is a
// SYNC stream sequence — the device's own delivery position, and the only
// thing the retention floor can be compared against. RevisionCursor is the
// Refractor pipeline's LastAppliedSeq at the device's last hydration
// (pipeline.Hydrate returns Progress().LastAppliedSeq) — a position in the
// Core-KV change stream, an entirely different sequence space. It is displayed
// as a hydration checkpoint and never enters a gap verdict; comparing it to a
// SYNC sequence would manufacture gaps out of unrelated counters.
//
// Gapped is deliberately a *bool: nil means "cannot be determined", never
// "fine". Headroom is a *uint64 for the same reason — a device sitting exactly
// on the retention floor has a headroom of 0, the tightest reading there is, so
// it must not share an encoding with "not measured".
type edgeDevice struct {
	Key            string   `json:"key"`
	IdentityKey    string   `json:"identityKey"`
	IdentityID     string   `json:"identityId"`
	DeviceID       string   `json:"deviceId"`
	Types          []string `json:"types,omitempty"`
	Anchors        []string `json:"anchors,omitempty"`
	Unfiltered     bool     `json:"unfiltered"`
	RegisteredAt   string   `json:"registeredAt,omitempty"`
	RevisionCursor uint64   `json:"revisionCursor,omitempty"`
	Subscribed     bool     `json:"subscribed"`
	AckFloor       uint64   `json:"ackFloor,omitempty"`
	Delivered      uint64   `json:"delivered,omitempty"`
	Pending        uint64   `json:"pending,omitempty"`
	Gapped         *bool    `json:"gapped"`
	BehindBy       uint64   `json:"behindBy,omitempty"`
	Headroom       *uint64  `json:"headroom"`
	Unreadable     bool     `json:"unreadable,omitempty"`
	LastAckAt      string   `json:"lastAckAt,omitempty"`
	ConsumerMadeAt string   `json:"consumerCreatedAt,omitempty"`
	AttachUnknown  bool     `json:"attachmentUnknown,omitempty"`
	Malformed      bool     `json:"malformed,omitempty"`
}

// edgeStream describes the retention window every gap verdict is measured
// against. StreamKnown gates all of it: when false the console reports "gap
// check unavailable" rather than an all-clear — an absent measurement is not a
// zero.
//
// Now is the server's clock, sent so the browser's time arithmetic (how long
// before the floor advances, how long since a device last acked) is anchored to
// the clock the sequences were actually read on rather than to the visitor's,
// which is the same determinism rule /api/tasks applies to task expiry.
type edgeStream struct {
	Stream        string `json:"stream,omitempty"`
	StreamKnown   bool   `json:"streamKnown"`
	FirstSeq      uint64 `json:"firstSeq,omitempty"`
	LastSeq       uint64 `json:"lastSeq,omitempty"`
	FirstTime     string `json:"firstTime,omitempty"`
	LastTime      string `json:"lastTime,omitempty"`
	MaxAgeSeconds int64  `json:"maxAgeSeconds,omitempty"`
	Now           string `json:"now"`
}

// edgeFleet is the GET /api/edge/fleet reply.
type edgeFleet struct {
	edgeStream
	Devices      []edgeDevice `json:"devices"`
	Identities   int          `json:"identities"`
	Count        int          `json:"count"`
	Gapped       int          `json:"gapped"`
	Unknown      int          `json:"unknown"`
	Unsubscribed int          `json:"unsubscribed"`
	Notes        []string     `json:"notes,omitempty"`
}

// edgeDeviceDetail is the GET /api/edge/device?key= reply: one device against
// the same retention window the fleet measures, plus the identity's other
// registrations. The sibling list is what separates a device fault from an
// identity-wide one — one gapped phone next to a current laptop is a device
// that stopped attaching; a whole identity gapped together is its lens.
type edgeDeviceDetail struct {
	edgeStream
	Device   edgeDevice   `json:"device"`
	Siblings []edgeDevice `json:"siblings"`
	Notes    []string     `json:"notes,omitempty"`
}

// interestRead is the outcome of reading one registration, which — like the
// consumer lookup — has more answers than "got it / didn't". A document that
// could not be FETCHED and one that was fetched and turned out to be corrupt
// are different facts about the device, and the panel says different things
// about them; collapsing both into "malformed" tells an operator their
// registration is damaged when a request merely timed out.
type interestRead struct {
	Doc    interestDoc
	Found  bool
	Parsed bool
	Failed bool
}

// interestDoc is the stored per-device Interest Set document — the wire shape
// internal/refractor/personalinterest writes.
type interestDoc struct {
	Types          []string `json:"types"`
	Anchors        []string `json:"anchors"`
	RegisteredAt   string   `json:"registeredAt"`
	RevisionCursor uint64   `json:"revisionCursor"`
}

// consumerState is the slice of a device's SYNC durable the fleet view joins.
//
// LastAckAt is JetStream's `ack_floor.last_active`, which the server fills from
// an in-memory activity clock stamped on each ack (nats-server 2.14
// server/consumer.go: `o.lat = time.Now()`), NOT from the acked message's own
// publish time. Two consequences the UI has to honour: it answers "when did
// this device last ack", which is the only liveness-adjacent signal this
// roster can carry at all — and it is process-local state that
// `resetLocalStartingSeq` zeroes, so its ABSENCE means "no ack observed since
// this consumer's state was last reset (a server restart included)", never
// "this device has never acked".
type consumerState struct {
	AckFloor  uint64
	Delivered uint64
	Pending   uint64
	LastAckAt string
	MadeAt    string
}

// consumerLookup is the OUTCOME of asking for a device's durable, which has
// three answers and not two. Found means the durable was read. Failed means the
// question itself broke — a timeout or a connection blip — and the difference
// matters: reporting a failed read as "this device never attached" states a
// fact about the device from a measurement that never happened, which is the
// same class of lie as a fabricated all-clear.
type consumerLookup struct {
	State  consumerState
	Found  bool
	Failed bool
}

// retentionWindow is the SYNC stream's retained sequence span. Known is the
// gate: with no readable stream no device has a comparable position, so every
// verdict downstream reads unknown rather than clean.
type retentionWindow struct {
	FirstSeq uint64
	LastSeq  uint64
	Known    bool
}

// empty reports whether the stream currently retains nothing. JetStream says so
// two ways — a drained stream reports firstSeq = lastSeq+1, a brand-new one
// reports 0/0 — and both must read as "no messages retained" rather than as a
// window a device could sit inside.
func (w retentionWindow) empty() bool { return w.LastSeq < w.FirstSeq || w.LastSeq == 0 }

// splitInterestKey splits a personal-lens-interest key into its identity and
// device halves, on the FIRST dot: the device id may contain dots, the identity
// id may not.
//
// That asymmetry holds because a control-plane ActorVerifier rebinds the
// request's identityId to the verified actor's bare NanoID before the
// registration is written. With no verifier configured — the dev/e2e posture
// the Refractor deliberately preserves — the id is self-asserted and only
// checked non-empty, so a dotted id would mis-split here and attribute the row
// to a truncated identity. The row is still rendered rather than dropped: an
// operator seeing an odd identity on a dev stack is better served than one
// seeing a silently short roster.
func splitInterestKey(key string) (identityID, deviceID string, ok bool) {
	id, dev, found := strings.Cut(key, ".")
	if !found || id == "" || dev == "" {
		return "", "", false
	}
	return id, dev, true
}

// edgeSyncDurable builds the durable consumer name internal/edge/sync gives a
// device's SYNC subscription. Must match sync.go's construction by value.
func edgeSyncDurable(identityID, deviceID string) string {
	return "edge-sync-" + identityID + "-" + deviceID
}

// deviceTriageTier orders the roster for triage rather than for census:
// 0 = gapped (data already lost), 1 = unmeasured, 2 = measured and current.
//
// Unmeasured sorting ABOVE current is deliberate and is the same rule the
// headline follows: a device whose gap state nobody could determine is an open
// question, and a console that sinks it below every answered row buries the
// rows an operator most needs to chase.
func deviceTriageTier(d edgeDevice) int {
	if d.Gapped == nil {
		return 1
	}
	if *d.Gapped {
		return 0
	}
	return 2
}

// computeEdgeFleet joins registered devices against their SYNC durables and the
// stream's retention floor to derive each device's gap state. Pure — the handler
// supplies the already-read docs, so every branch is unit-testable without a
// substrate.
//
// A device is gapped when the stream has discarded messages the device had not
// consumed: deltas that aged out of the retention window, so a warm resume
// would silently miss them (edge-syncgap-control-rpc-design.md §3.2). The
// output is ordered worst-first (deviceTriageTier, then by damage within the
// tier) — this view is triage, not a census.
//
// unknown counts devices whose gap state could not be determined at all. It is
// reported separately and never folded into the healthy remainder, so the
// headline cannot read as an all-clear when nothing was measured.
func computeEdgeFleet(
	keys []string,
	readDoc func(string) interestRead,
	readConsumer func(identityID, deviceID string) consumerLookup,
	win retentionWindow,
) (devices []edgeDevice, gapped, unsubscribed, unknown int) {
	devices = make([]edgeDevice, 0, len(keys))
	for _, k := range keys {
		identityID, deviceID, ok := splitInterestKey(k)
		if !ok {
			// A key that does not split is not a device registration; listing it
			// unattributed would invent an identity.
			continue
		}
		reg := readDoc(k)
		if !reg.Found {
			// Deregistered between the list and the get — drop it rather than
			// fail the whole page (the handleVaultShreds posture).
			continue
		}
		d := edgeDevice{
			Key:         k,
			IdentityID:  identityID,
			IdentityKey: "vtx.identity." + identityID,
			DeviceID:    deviceID,
			Malformed:   !reg.Parsed && !reg.Failed,
			Unreadable:  reg.Failed,
		}
		if reg.Parsed {
			doc := reg.Doc
			d.Types = doc.Types
			d.Anchors = doc.Anchors
			d.RegisteredAt = doc.RegisteredAt
			d.RevisionCursor = doc.RevisionCursor
			// "Absence is never a denial" (personalinterest's own rule): an
			// empty filter admits everything authorized — a wider subscription,
			// not a narrower one.
			d.Unfiltered = len(doc.Types) == 0 && len(doc.Anchors) == 0
		}
		if readConsumer != nil {
			look := readConsumer(identityID, deviceID)
			d.AttachUnknown = look.Failed
			if look.Found {
				d.Subscribed = true
				d.AckFloor = look.State.AckFloor
				d.Delivered = look.State.Delivered
				d.Pending = look.State.Pending
				d.LastAckAt = look.State.LastAckAt
				d.ConsumerMadeAt = look.State.MadeAt
			}
		}
		// Attachment is only knowable through the stream — with no readable
		// stream every device's durable is unqueryable, so "not attached" is
		// unknown rather than false, and nothing is counted. A lookup that
		// FAILED is the same: it is not evidence the device never attached, so
		// it does not join the unattached count either.
		if win.Known && !d.Subscribed && !d.AttachUnknown {
			unsubscribed++
		}
		// Only the device's own durable answers the gap question: its ack floor
		// is a SYNC sequence, so the retention floor is commensurable with it.
		// A device with no durable has no comparable position at all — that is
		// unanswerable, not healthy.
		if win.Known && d.Subscribed {
			// The messages actually lost are (ackFloor, firstSeq) exclusive:
			// firstSeq is still retained and ackFloor was already consumed. So a
			// device is gapped only once at least one message falls strictly
			// between them.
			//
			// This is a DELIBERATE divergence from the platform's own syncgap
			// predicate (`cursor < firstSeq`), which also fires when the device
			// sits exactly one below the floor and nothing was lost. That
			// conservatism is right where it lives — the cost to a device is one
			// redundant re-hydrate. It is wrong here, because this number is an
			// operator's triage metric: the SYNC stream is MaxAge-limited, so a
			// stack idle past the retention window ages to empty and reports
			// firstSeq = lastSeq+1, at which point EVERY fully-caught-up device
			// satisfies the platform predicate and the whole fleet would render
			// red with nothing wrong. The console reports data actually lost.
			isGapped := win.FirstSeq > d.AckFloor+1
			d.Gapped = &isGapped
			if isGapped {
				d.BehindBy = win.FirstSeq - d.AckFloor - 1
				gapped++
			} else if h, ok := retentionHeadroom(d.AckFloor, win); ok {
				d.Headroom = &h
			}
		} else {
			unknown++
		}
		devices = append(devices, d)
	}
	sort.Slice(devices, func(i, j int) bool {
		a, b := devices[i], devices[j]
		if ta, tb := deviceTriageTier(a), deviceTriageTier(b); ta != tb {
			return ta < tb
		}
		// Within the gapped tier, most data lost first. Within the current
		// tier, tightest headroom first — the device closest to becoming the
		// next gapped row.
		if a.BehindBy != b.BehindBy {
			return a.BehindBy > b.BehindBy
		}
		if a.Headroom != nil && b.Headroom != nil && *a.Headroom != *b.Headroom {
			return *a.Headroom < *b.Headroom
		}
		if a.IdentityID != b.IdentityID {
			return a.IdentityID < b.IdentityID
		}
		return a.DeviceID < b.DeviceID
	})
	return devices, gapped, unsubscribed, unknown
}

// retentionHeadroom counts the retained messages sitting at or below a device's
// ack floor: how far the floor can advance before it starts discarding deltas
// this device has not consumed. Zero is a real reading — the device is ON the
// floor, the last position before a discard starts costing it — and is why the
// caller stores it behind a pointer.
//
// A stream retaining NOTHING has no such number at all, and ok is false there.
// Returning 0 would read as "on the floor" and render every fully-caught-up
// device as the tightest row on the fleet, which is precisely the fleet-wide
// false red the gap predicate above goes out of its way to avoid: a stack idle
// past the retention window ages the stream to empty, and at that moment no
// device has lost anything.
func retentionHeadroom(ackFloor uint64, win retentionWindow) (uint64, bool) {
	if win.empty() {
		return 0, false
	}
	if ackFloor < win.FirstSeq {
		return 0, true
	}
	top := ackFloor
	if top > win.LastSeq {
		// A durable's ack floor can only exceed the stream's last sequence when
		// the two reads straddled a purge; clamping keeps the count inside the
		// window rather than reporting headroom the stream does not hold.
		top = win.LastSeq
	}
	return top - win.FirstSeq + 1, true
}

// personalSyncStream discovers the stream the Personal Lens delivers over by
// reading it off the installed lens specs rather than assuming a "SYNC" literal
// — the same reason cmd/refractor takes it from the rule's own Into.Stream: a
// deployment whose personal lens targets a differently-named stream would
// otherwise be gap-checked against the wrong one, which can yield a false
// all-clear.
func personalSyncStream(keys []string, resolveSpec func(string) lensSpecInfo) (stream, note string) {
	streams := make([]string, 0, 2)
	seen := map[string]bool{}
	for _, k := range keys {
		if _, kind := classifyHealthKey(k); kind != kindLens {
			continue
		}
		spec := resolveSpec(k)
		if spec.TargetType != "nats_subject" || !spec.Personal || spec.Stream == "" {
			continue
		}
		if !seen[spec.Stream] {
			seen[spec.Stream] = true
			streams = append(streams, spec.Stream)
		}
	}
	sort.Strings(streams)
	switch len(streams) {
	case 0:
		return "", "No Personal Lens is currently reporting, so there is no SYNC stream to gap-check against."
	case 1:
		return streams[0], ""
	default:
		return "", "Personal Lenses target more than one stream (" + strings.Join(streams, ", ") +
			"); a single fleet-wide gap verdict would be ambiguous."
	}
}

// edgeReaders is everything both Edge handlers need before they can classify a
// device: the discovered stream and its window, the per-key registration read,
// and the per-device durable read. Sharing it is what keeps the fleet roster
// and one device's panel from drifting into two different gap verdicts for the
// same device.
type edgeReaders struct {
	win          retentionWindow
	info         edgeStream
	notes        []string
	readDoc      func(string) interestRead
	readConsumer func(identityID, deviceID string) consumerLookup
	faults       func() []string
}

func (s *server) edgeReaders(ctx context.Context, conn *substrate.Conn) edgeReaders {
	var notes []string
	var stream, note string
	if healthKeys, _, _, resolveSpec, herr := s.healthReaders(ctx, conn); herr != nil {
		note = "Lens roster unavailable (" + herr.Error() + "), so the SYNC stream could not be identified."
	} else {
		stream, note = personalSyncStream(healthKeys, resolveSpec)
	}
	if note != "" {
		notes = append(notes, note)
	}

	out := edgeReaders{notes: notes}
	out.info.Stream = stream
	out.info.Now = time.Now().UTC().Format(time.RFC3339)
	if stream != "" {
		st, serr := conn.JetStream().Stream(ctx, stream)
		if serr != nil {
			out.notes = append(out.notes,
				"Could not read stream "+stream+" ("+serr.Error()+"); gap state is unknown for every device.")
		} else {
			ci := st.CachedInfo()
			out.win = retentionWindow{FirstSeq: ci.State.FirstSeq, LastSeq: ci.State.LastSeq, Known: true}
			out.info.StreamKnown = true
			out.info.FirstSeq, out.info.LastSeq = ci.State.FirstSeq, ci.State.LastSeq
			if !out.win.empty() {
				out.info.FirstTime = ci.State.FirstTime.UTC().Format(time.RFC3339)
				out.info.LastTime = ci.State.LastTime.UTC().Format(time.RFC3339)
			}
			// MaxAge is what makes the floor advance on a clock rather than on
			// pressure. Zero means the stream is not age-limited at all, and the
			// console must then decline to predict when the floor moves instead
			// of inventing a deadline. A sub-second age is rounded UP rather
			// than truncated to that zero: reporting "no age limit" for a stream
			// that expires everything within the second is the one lie this
			// field can tell.
			if age := ci.Config.MaxAge; age > 0 && age < time.Second {
				out.info.MaxAgeSeconds = 1
			} else {
				out.info.MaxAgeSeconds = int64(age / time.Second)
			}
		}
	}

	// A read fault must not silently shorten the roster: a missing row with a
	// confident count reads as "this device is not registered", which is the
	// same class of lie as a fabricated all-clear. Only a genuine absence drops.
	var readFaults, consumerFaults int
	out.readDoc = func(k string) interestRead {
		entry, gerr := conn.KVGet(ctx, bootstrap.PersonalLensInterestKV, k)
		switch {
		case errors.Is(gerr, substrate.ErrKeyNotFound):
			// Deregistered between the list and the get — genuinely gone.
			return interestRead{}
		case gerr != nil:
			// Transient fault: the key still names a real registration, so keep
			// the row and mark the READ as broken rather than under-reporting
			// the roster or blaming the document.
			readFaults++
			return interestRead{Found: true, Failed: true}
		}
		var doc interestDoc
		if json.Unmarshal(entry.Value, &doc) != nil {
			return interestRead{Found: true}
		}
		return interestRead{Doc: doc, Found: true, Parsed: true}
	}

	// A consumer lookup that fails for any reason other than "no such durable"
	// is a failed measurement, not evidence the device never attached. Both
	// currently render the same way (gap unknown), but the fault is counted so
	// the page can say a read broke rather than implying a quiet fleet.
	out.readConsumer = func(identityID, deviceID string) consumerLookup {
		if !out.win.Known {
			return consumerLookup{}
		}
		cons, cerr := conn.JetStream().Consumer(ctx, stream, edgeSyncDurable(identityID, deviceID))
		if cerr != nil {
			if errors.Is(cerr, jetstream.ErrConsumerNotFound) {
				return consumerLookup{}
			}
			consumerFaults++
			return consumerLookup{Failed: true}
		}
		info := cons.CachedInfo()
		cs := consumerState{
			AckFloor:  info.AckFloor.Stream,
			Delivered: info.Delivered.Stream,
			Pending:   info.NumPending,
			MadeAt:    info.Created.UTC().Format(time.RFC3339),
		}
		if info.AckFloor.Last != nil {
			cs.LastAckAt = info.AckFloor.Last.UTC().Format(time.RFC3339)
		}
		return consumerLookup{State: cs, Found: true}
	}

	out.faults = func() []string {
		var f []string
		if consumerFaults > 0 {
			f = append(f, strconv.Itoa(consumerFaults)+
				" consumer lookup(s) failed; those devices show their attachment as unmeasured rather than as absent.")
		}
		if readFaults > 0 {
			f = append(f, strconv.Itoa(readFaults)+
				" registration document(s) could not be read; those rows show as unreadable rather than being omitted.")
		}
		return f
	}
	return out
}

// handleEdgeFleet implements GET /api/edge/fleet: the Personal Lens / Edge
// Lattice subscriber roster. The Interest Set bucket is Refractor-owned
// operational state (not Core KV, not a lens target) — Loupe reads it with the
// ordinary KVListKeys/KVGet the inspector already uses for the Gateway's
// revocation set and the Vault's shred ledger.
//
// This is a REGISTRATION roster, not a liveness view, and the UI says so: edge
// nodes structurally cannot self-report health (their per-identity permission
// set admits only their own sync subject and control RPCs — publishing to
// health-kv would be a permissions violation, not a missing grant), and nothing
// garbage-collects a registration, so a device that vanished without a clean
// deregister keeps its row forever.
func (s *server) handleEdgeFleet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusBadRequest, "GET required")
		return
	}
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	keys, err := conn.KVListKeys(ctx, bootstrap.PersonalLensInterestKV)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list "+bootstrap.PersonalLensInterestKV+": "+err.Error())
		return
	}

	rd := s.edgeReaders(ctx, conn)
	devices, gapped, unsubscribed, unknown := computeEdgeFleet(keys, rd.readDoc, rd.readConsumer, rd.win)
	notes := append(rd.notes, rd.faults()...)
	identities := map[string]bool{}
	for _, d := range devices {
		identities[d.IdentityID] = true
	}
	s.writeJSON(w, http.StatusOK, edgeFleet{
		edgeStream:   rd.info,
		Devices:      devices,
		Identities:   len(identities),
		Count:        len(devices),
		Gapped:       gapped,
		Unknown:      unknown,
		Unsubscribed: unsubscribed,
		Notes:        notes,
	})
}

// identityKeys narrows a bucket listing to one identity's registrations. The
// panel needs the target and its siblings, never the whole fleet's durables —
// a per-device page that queried every device's consumer would cost the fleet
// view's whole read on every drill-in.
func identityKeys(keys []string, identityID string) []string {
	kin := make([]string, 0, 4)
	for _, k := range keys {
		if id, _, ok := splitInterestKey(k); ok && id == identityID {
			kin = append(kin, k)
		}
	}
	return kin
}

// selectDeviceAndSiblings splits a classified identity roster into the
// requested device and the rest. Matching is on the full registration KEY, not
// on the device id: two identities can name a device the same thing, and the
// key is what the caller asked for.
//
// Siblings are a fresh slice, so the returned pointer into the roster stays
// valid regardless of how the caller grows the sibling list.
func selectDeviceAndSiblings(devices []edgeDevice, key string) (*edgeDevice, []edgeDevice) {
	var target *edgeDevice
	siblings := make([]edgeDevice, 0, len(devices))
	for i := range devices {
		if devices[i].Key == key {
			target = &devices[i]
			continue
		}
		siblings = append(siblings, devices[i])
	}
	return target, siblings
}

// handleEdgeDevice implements GET /api/edge/device?key=<identityId>.<deviceId>:
// one device's registration and sync position, against the same retention
// window the fleet roster measures, plus the identity's other devices.
//
// The key rides a query parameter rather than a path segment because a NATS KV
// key admits "/" (jetstream's key grammar), so a device id containing one would
// split a path route into the wrong number of segments.
//
// Siblings are classified through the SAME computeEdgeFleet call as the target,
// so a device can never read one way here and another way on the roster.
func (s *server) handleEdgeDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusBadRequest, "GET required")
		return
	}
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	identityID, _, ok := splitInterestKey(key)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "expected ?key=<identityId>.<deviceId>")
		return
	}
	conn, connOK := s.requireConn(w)
	if !connOK {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	keys, err := conn.KVListKeys(ctx, bootstrap.PersonalLensInterestKV)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "list "+bootstrap.PersonalLensInterestKV+": "+err.Error())
		return
	}
	kin := identityKeys(keys, identityID)

	rd := s.edgeReaders(ctx, conn)
	devices, _, _, _ := computeEdgeFleet(kin, rd.readDoc, rd.readConsumer, rd.win)
	target, siblings := selectDeviceAndSiblings(devices, key)
	if target == nil {
		s.writeError(w, http.StatusNotFound, "device "+key+" is not registered")
		return
	}
	s.writeJSON(w, http.StatusOK, edgeDeviceDetail{
		edgeStream: rd.info,
		Device:     *target,
		Siblings:   siblings,
		Notes:      append(rd.notes, rd.faults()...),
	})
}

// handleEdgeHydrateRequest implements POST /api/edge/hydrate?key=<identityId>.
// <deviceId>: durably marks the device for hydration on its next SYNC attach
// (loupe-flows-edge-depth-ux.md §3.2) via the Refractor control plane's
// "requesthydration" op. The console cannot push to a device it cannot see —
// edge nodes cannot self-report and no connection state is observable — so
// this only records the request; the remedy still runs on the device's own
// next attach. The reply is forwarded raw, same as every other control
// mutation Loupe proxies (control.go).
func (s *server) handleEdgeHydrateRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusBadRequest, "POST required")
		return
	}
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	identityID, deviceID, ok := splitInterestKey(key)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "expected ?key=<identityId>.<deviceId>")
		return
	}
	conn, connOK := s.requireConn(w)
	if !connOK {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	body, err := json.Marshal(map[string]string{"identityId": identityID, "deviceId": deviceID})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	raw, err := s.controlRequestBody(ctx, conn, "lattice.ctrl.refractor.personal.requesthydration", body)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}
