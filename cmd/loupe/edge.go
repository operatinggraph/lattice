package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

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
// "fine".
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
	Pending        uint64   `json:"pending,omitempty"`
	Gapped         *bool    `json:"gapped"`
	BehindBy       uint64   `json:"behindBy,omitempty"`
	Malformed      bool     `json:"malformed,omitempty"`
}

// edgeFleet is the GET /api/edge/fleet reply. StreamKnown gates every gap
// verdict: when false the console reports "gap check unavailable" rather than
// an all-clear — an absent measurement is not a zero.
type edgeFleet struct {
	Devices      []edgeDevice `json:"devices"`
	Identities   int          `json:"identities"`
	Count        int          `json:"count"`
	Gapped       int          `json:"gapped"`
	Unknown      int          `json:"unknown"`
	Unsubscribed int          `json:"unsubscribed"`
	Stream       string       `json:"stream,omitempty"`
	StreamKnown  bool         `json:"streamKnown"`
	FirstSeq     uint64       `json:"firstSeq,omitempty"`
	LastSeq      uint64       `json:"lastSeq,omitempty"`
	Notes        []string     `json:"notes,omitempty"`
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
type consumerState struct {
	AckFloor uint64
	Pending  uint64
}

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

// computeEdgeFleet joins registered devices against their SYNC durables and the
// stream's retention floor to derive each device's gap state. Pure — the handler
// supplies the already-read docs, so every branch is unit-testable without a
// substrate.
//
// A device is gapped when the stream has discarded messages the device had not
// consumed: deltas that aged out of the retention window, so a warm resume
// would silently miss them (edge-syncgap-control-rpc-design.md §3.2). Gapped
// devices sort first — this view is triage, not a census.
//
// unknown counts devices whose gap state could not be determined at all. It is
// reported separately and never folded into the healthy remainder, so the
// headline cannot read as an all-clear when nothing was measured.
func computeEdgeFleet(
	keys []string,
	readDoc func(string) (interestDoc, bool, bool),
	readConsumer func(identityID, deviceID string) (consumerState, bool),
	firstSeq uint64,
	streamKnown bool,
) (devices []edgeDevice, gapped, unsubscribed, unknown int) {
	devices = make([]edgeDevice, 0, len(keys))
	for _, k := range keys {
		identityID, deviceID, ok := splitInterestKey(k)
		if !ok {
			// A key that does not split is not a device registration; listing it
			// unattributed would invent an identity.
			continue
		}
		doc, found, parsed := readDoc(k)
		if !found {
			// Deregistered between the list and the get — drop it rather than
			// fail the whole page (the handleVaultShreds posture).
			continue
		}
		d := edgeDevice{
			Key:         k,
			IdentityID:  identityID,
			IdentityKey: "vtx.identity." + identityID,
			DeviceID:    deviceID,
			Malformed:   !parsed,
		}
		if parsed {
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
			if cs, has := readConsumer(identityID, deviceID); has {
				d.Subscribed = true
				d.AckFloor = cs.AckFloor
				d.Pending = cs.Pending
			}
		}
		// Attachment is only knowable through the stream — with no readable
		// stream every device's durable is unqueryable, so "not attached" is
		// unknown rather than false, and nothing is counted.
		if streamKnown && !d.Subscribed {
			unsubscribed++
		}
		// Only the device's own durable answers the gap question: its ack floor
		// is a SYNC sequence, so the retention floor is commensurable with it.
		// A device with no durable has no comparable position at all — that is
		// unanswerable, not healthy.
		if streamKnown && d.Subscribed {
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
			isGapped := firstSeq > d.AckFloor+1
			d.Gapped = &isGapped
			if isGapped {
				d.BehindBy = firstSeq - d.AckFloor - 1
				gapped++
			}
		} else {
			unknown++
		}
		devices = append(devices, d)
	}
	sort.Slice(devices, func(i, j int) bool {
		gi := devices[i].Gapped != nil && *devices[i].Gapped
		gj := devices[j].Gapped != nil && *devices[j].Gapped
		if gi != gj {
			return gi
		}
		if devices[i].IdentityID != devices[j].IdentityID {
			return devices[i].IdentityID < devices[j].IdentityID
		}
		return devices[i].DeviceID < devices[j].DeviceID
	})
	return devices, gapped, unsubscribed, unknown
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

	var firstSeq, lastSeq uint64
	streamKnown := false
	if stream != "" {
		st, serr := conn.JetStream().Stream(ctx, stream)
		if serr != nil {
			notes = append(notes, "Could not read stream "+stream+" ("+serr.Error()+"); gap state is unknown for every device.")
		} else {
			state := st.CachedInfo().State
			firstSeq, lastSeq, streamKnown = state.FirstSeq, state.LastSeq, true
		}
	}

	// A read fault must not silently shorten the roster: a missing row with a
	// confident count reads as "this device is not registered", which is the
	// same class of lie as a fabricated all-clear. Only a genuine absence drops.
	var readFaults int
	readDoc := func(k string) (interestDoc, bool, bool) {
		entry, gerr := conn.KVGet(ctx, bootstrap.PersonalLensInterestKV, k)
		switch {
		case errors.Is(gerr, substrate.ErrKeyNotFound):
			// Deregistered between the list and the get — genuinely gone.
			return interestDoc{}, false, false
		case gerr != nil:
			// Transient fault: the key still names a real registration, so keep
			// the row and mark it unreadable rather than under-reporting.
			readFaults++
			return interestDoc{}, true, false
		}
		var doc interestDoc
		if json.Unmarshal(entry.Value, &doc) != nil {
			return interestDoc{}, true, false
		}
		return doc, true, true
	}

	// A consumer lookup that fails for any reason other than "no such durable"
	// is a failed measurement, not evidence the device never attached. Both
	// currently render the same way (gap unknown), but the fault is counted so
	// the page can say a read broke rather than implying a quiet fleet.
	var consumerFaults int
	readConsumer := func(identityID, deviceID string) (consumerState, bool) {
		if !streamKnown {
			return consumerState{}, false
		}
		cons, cerr := conn.JetStream().Consumer(ctx, stream, edgeSyncDurable(identityID, deviceID))
		if cerr != nil {
			if !errors.Is(cerr, jetstream.ErrConsumerNotFound) {
				consumerFaults++
			}
			return consumerState{}, false
		}
		info := cons.CachedInfo()
		return consumerState{AckFloor: info.AckFloor.Stream, Pending: info.NumPending}, true
	}

	devices, gapped, unsubscribed, unknown := computeEdgeFleet(keys, readDoc, readConsumer, firstSeq, streamKnown)
	if consumerFaults > 0 {
		notes = append(notes, strconv.Itoa(consumerFaults)+" consumer lookup(s) failed; those devices show as gap-unknown rather than as unattached.")
	}
	if readFaults > 0 {
		notes = append(notes, strconv.Itoa(readFaults)+" registration document(s) could not be read; those rows show as unreadable rather than being omitted.")
	}
	identities := map[string]bool{}
	for _, d := range devices {
		identities[d.IdentityID] = true
	}
	s.writeJSON(w, http.StatusOK, edgeFleet{
		Devices:      devices,
		Identities:   len(identities),
		Count:        len(devices),
		Gapped:       gapped,
		Unknown:      unknown,
		Unsubscribed: unsubscribed,
		Stream:       stream,
		StreamKnown:  streamKnown,
		FirstSeq:     firstSeq,
		LastSeq:      lastSeq,
		Notes:        notes,
	})
}
