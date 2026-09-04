package loom

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// newLoomStateStoreForTransition provisions loom-state the way a transition
// batch needs it and newLoomStateStore does not: the AtomicBatch write requires
// AllowAtomicPublish on the underlying KV_<bucket> stream, exactly as
// bootstrap's primordial seeder sets it for the real bucket
// (internal/bootstrap/primordial.go's enableAtomicPublish). LimitMarkerTTL is
// the other half of what the batch needs: it is what allows per-message TTLs
// on the bucket, and every removal a transition writes carries one, so on a
// plain bucket the batch is rejected outright rather than merely leaving a
// permanent marker behind.
func newLoomStateStoreForTransition(ctx context.Context, t *testing.T) *stateStore {
	t.Helper()
	conn := newLoomConn(t)
	const bucket = "loom-state"
	js := conn.JetStream()
	_, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket, LimitMarkerTTL: time.Second})
	require.NoError(t, err)

	stream, err := js.Stream(ctx, "KV_"+bucket)
	require.NoError(t, err)
	info, err := stream.Info(ctx)
	require.NoError(t, err)
	cfg := info.Config
	cfg.AllowAtomicPublish = true
	_, err = js.UpdateStream(ctx, cfg)
	require.NoError(t, err)

	return newStateStore(conn, bucket)
}

// TestSystemOpReads_ResolveAgainstSubject pins the template grammar
// submitSystemOp renders: the bare `subject` token is the instance subject's
// root vertex, and `subject.<aspect>` is its 4-segment aspect key (Contract #1)
// — the same key shape userTaskOptionalReads builds for `.availability`. A
// step that declares nothing must render nil, not an empty slice, so the
// event-only lifecycle ops keep the exact read-free outbox shape they have
// always had.
func TestSystemOpReads_ResolveAgainstSubject(t *testing.T) {
	t.Parallel()
	const subjectKey = "vtx.identity.BBsubjectHJKMNPQRSTV"

	for _, tc := range []struct {
		name      string
		templates []string
		want      []string
	}{
		{"nil declares nothing", nil, nil},
		{"empty declares nothing", []string{}, nil},
		{"bare subject is the root key", []string{subjectToken}, []string{subjectKey}},
		{
			"subject.<aspect> is the 4-segment aspect key",
			[]string{"subject.piiKey"},
			[]string{subjectKey + ".piiKey"},
		},
		{
			"order is preserved across a mixed set",
			[]string{subjectToken, "subject.piiKey", "subject.erasureRequested"},
			[]string{subjectKey, subjectKey + ".piiKey", subjectKey + ".erasureRequested"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := systemOpReads(tc.templates, subjectKey)
			require.Equal(t, tc.want, got)
			if tc.want == nil {
				require.Nil(t, got, "a step declaring no reads must render nil, not an empty slice")
			}
		})
	}
}

// TestPatternValidate_DeclaredReads is the pattern-load gate on the declared
// read-sets. Two properties matter and they fail in opposite directions:
//
//   - A systemOp's entries must be subject-relative, because the subject is the
//     only key a shared pattern definition can name — a literal key would pin one
//     instance's data into a definition every instance runs.
//   - A userTask/externalTask may not declare one at all. Their read-sets are
//     DERIVED by the engine (userTaskReads / inferExternalTaskReads), so a
//     declaration would be silently ignored — and an author who believes a key is
//     hydrated writes a script that reads it. Failing the pattern load is louder
//     than a HydrationMiss at run time.
func TestPatternValidate_DeclaredReads(t *testing.T) {
	t.Parallel()

	systemOp := func(reads, optionalReads []string) Step {
		return Step{Kind: StepKindSystemOp, Operation: "ShredIdentityKey", Reads: reads, OptionalReads: optionalReads}
	}

	for _, tc := range []struct {
		name    string
		step    Step
		wantErr string
	}{
		{"systemOp: bare subject", systemOp([]string{"subject"}, nil), ""},
		{"systemOp: subject aspect", systemOp([]string{"subject.piiKey"}, nil), ""},
		{"systemOp: both sets", systemOp([]string{"subject"}, []string{"subject.piiKey"}), ""},
		{
			"systemOp: a literal key is not a template",
			systemOp([]string{"vtx.identity.BBsubjectHJKMNPQRSTV"}, nil),
			"subject-relative templates",
		},
		{
			"systemOp: a foreign template root is not the subject",
			systemOp([]string{"actor.piiKey"}, nil),
			"subject-relative templates",
		},
		{
			"systemOp: a trailing dot names no aspect",
			systemOp([]string{"subject."}, nil),
			"not a Contract #1 aspect localName",
		},
		{
			"systemOp: an aspect localName is a single segment",
			systemOp([]string{"subject.piiKey.data"}, nil),
			"not a Contract #1 aspect localName",
		},
		// A rendered key is fetched with a NATS KV GET, whose charset is narrower
		// than "any string without a dot". An entry admitted here but rejected
		// there fails as ErrInvalidKey — not the absence branch, but a hard
		// hydration error every redelivery reproduces, so the step wedges and the
		// instance rides its deadline to a failed terminal. Each of these installs
		// clean and runs dark unless the localName check catches it.
		{
			"systemOp: an aspect with a space is not a localName",
			systemOp([]string{"subject.pii key"}, nil),
			"not a Contract #1 aspect localName",
		},
		{
			"systemOp: an aspect with a NATS wildcard is not a localName",
			systemOp([]string{"subject.*"}, nil),
			"not a Contract #1 aspect localName",
		},
		{
			"systemOp: an aspect with a NATS full wildcard is not a localName",
			systemOp([]string{"subject.>"}, nil),
			"not a Contract #1 aspect localName",
		},
		{
			"systemOp: an aspect with a slash is not a localName",
			systemOp([]string{"subject.pii/key"}, nil),
			"not a Contract #1 aspect localName",
		},
		{
			"systemOp: a non-ASCII aspect is not a localName",
			systemOp([]string{"subject.piiKé"}, nil),
			"not a Contract #1 aspect localName",
		},
		{
			"systemOp: a newline in an aspect is not a localName",
			systemOp([]string{"subject.pii\nkey"}, nil),
			"not a Contract #1 aspect localName",
		},
		{
			"systemOp: optionalReads is validated too",
			systemOp(nil, []string{"vtx.identity.BBsubjectHJKMNPQRSTV"}),
			"subject-relative templates",
		},
		{
			"userTask may not declare reads",
			Step{Kind: StepKindUserTask, Operation: "SignLease", Reads: []string{"subject"}},
			"reads is a systemOp-only field",
		},
		{
			"userTask may not declare optionalReads",
			Step{Kind: StepKindUserTask, Operation: "SignLease", OptionalReads: []string{"subject"}},
			"optionalReads is a systemOp-only field",
		},
		{
			"externalTask may not declare reads",
			Step{
				Kind: StepKindExternalTask, Adapter: "esign", InstanceOp: "CreateEnvelope",
				ReplyOp: "ResolveEnvelope", Reads: []string{"subject"},
			},
			"reads is a systemOp-only field",
		},
		{
			"externalTask may not declare optionalReads",
			Step{
				Kind: StepKindExternalTask, Adapter: "esign", InstanceOp: "CreateEnvelope",
				ReplyOp: "ResolveEnvelope", OptionalReads: []string{"subject"},
			},
			"optionalReads is a systemOp-only field",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &Pattern{PatternID: "identityErasure", SubjectType: "identity", Steps: []Step{tc.step}}
			err := p.validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestPatternStep_ReadsRoundTripFromSpecBody proves the wire half: the `reads`/
// `optionalReads`/`enumerations` keys pkgmgr's loomPatternSpecBody writes into
// the meta.loomPattern spec aspect deserialize onto Step. The two sides are
// edited in lockstep by hand, so the json tags are the only thing joining them
// — a renamed tag would leave a pattern that installs cleanly and then runs
// read-free, or walk-declaration-free, with nothing red.
func TestPatternStep_ReadsRoundTripFromSpecBody(t *testing.T) {
	t.Parallel()

	const specBody = `{
	  "patternId": "identityErasure",
	  "subjectType": "identity",
	  "steps": [
	    {"kind": "systemOp", "operation": "ShredIdentityKey",
	     "reads": ["subject"], "optionalReads": ["subject.piiKey"]},
	    {"kind": "systemOp", "operation": "UnbindIdentityCredentials",
	     "enumerations": [{"hub": "subject", "relation": "boundTo", "direction": "in"}]},
	    {"kind": "systemOp", "operation": "PurgeIdentityDedupFootprint"}
	  ]
	}`

	var p Pattern
	require.NoError(t, json.Unmarshal([]byte(specBody), &p))
	require.NoError(t, p.validate())

	require.Equal(t, []string{"subject"}, p.Steps[0].Reads)
	require.Equal(t, []string{"subject.piiKey"}, p.Steps[0].OptionalReads)

	// `enumerations` is the same join and the same hazard: pkgmgr's
	// enumerationBodies writes the three inner keys as string literals, and
	// Enumeration's tags are the only thing binding them. A drift on either
	// side installs cleanly, decodes to nil, and publishes a walk-free envelope
	// for every dispatch — no error anywhere.
	require.Equal(t,
		[]Enumeration{{Hub: "subject", Relation: "boundTo", Direction: "in"}},
		p.Steps[1].Enumerations)

	require.Nil(t, p.Steps[2].Reads, "a step declaring nothing round-trips as nil")
	require.Nil(t, p.Steps[2].OptionalReads)
	require.Nil(t, p.Steps[2].Enumerations)

	// And the omitempty half: a read-free step must not gain the keys on the way
	// back out, or every shipped read-free pattern's spec body changes shape.
	out, err := json.Marshal(p.Steps[2])
	require.NoError(t, err)
	require.NotContains(t, string(out), "reads")
	require.NotContains(t, string(out), "enumerations")
}

// TestHandleTrigger_SubjectKeyMustBeAVertexKey guards the precondition the
// declared-read grammar's confinement rests on. `subject` and
// `subject.<aspect>` are only confined to the instance's own vertex if
// subjectKey names a vertex; a caller passing a deeper key would make `subject`
// name an aspect and `subject.<aspect>` name something below it, which is a
// namespace escape rather than a template. subjectKey arrives in a
// caller-supplied trigger payload, so it is bound here, where the input enters.
//
// The trigger is dropped with an Ack in every rejected case: a malformed key is
// not fixable by redelivery, so a Nak would only spin.
func TestHandleTrigger_SubjectKeyMustBeAVertexKey(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	state := newLoomStateStoreForTransition(ctx, t)
	e := &Engine{
		cfg:    Config{Lane: "core", ActorKey: "identity.system.loom", StepTimeout: time.Minute},
		logger: logger,
		state:  state,
		// An empty source loads no pattern, which is what makes the positive
		// vector legible: a subjectKey that CLEARS the shape guard falls through
		// to the pattern lookup and Naks for redelivery, a different decision from
		// the guard's own Ack-and-drop.
		source: newPatternSource(state.conn, "core-kv", "test", logger),
		ctx:    ctx,
	}

	for _, tc := range []struct {
		name       string
		subjectKey string
		rejected   bool
	}{
		{"an aspect key is a namespace escape", "vtx.identity.BBsubjectHJKMNPQRSTV.piiKey", true},
		{"a link key is not a vertex", "lnk.identity.BBsubjectHJKMNPQRSTV.boundTo.identity.BBotherHJKMNPQRSTUVW", true},
		{"a bare id is not a key", "BBsubjectHJKMNPQRSTV", true},
		{"a two-segment key is not a vertex", "vtx.identity", true},
		{"a non-vtx prefix is not a vertex", "obj.identity.BBsubjectHJKMNPQRSTV", true},
		{"an empty type segment is not a vertex", "vtx..BBsubjectHJKMNPQRSTV", true},
		{"an empty id segment is not a vertex", "vtx.identity.", true},
		// The positive vector: without it, every rejection above could be passing
		// because the handler drops the trigger for some unrelated reason.
		{"a well-formed vertex key is accepted", "vtx.identity.BBsubjectHJKMNPQRSTV", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"payload": map[string]any{
				"instanceId": "BBinstanceHJKMNPQRST",
				"patternRef": "vtx.meta.BBpatternHJKMNPQRSTU",
				"subjectKey": tc.subjectKey,
			}})
			require.NoError(t, err)

			got := e.handleTrigger(ctx, substrate.Message{Body: body})
			if tc.rejected {
				require.Equal(t, substrate.Ack, got,
					"a malformed subjectKey is dropped, never Nak'd into a redelivery spin — redelivery cannot fix it")
			} else {
				require.Equal(t, substrate.Nak, got,
					"a well-formed subjectKey clears the guard and reaches the pattern lookup")
			}

			// The guard runs before any state is written, so no rejected trigger
			// leaves an instance behind.
			inst, err := state.getInstance(ctx, "BBinstanceHJKMNPQRST")
			require.NoError(t, err)
			require.Nil(t, inst, "no instance is created either way in this test")
		})
	}
}

// TestSubmitSystemOp_OutboxCarriesResolvedReads is the end-to-end assertion for
// the arm that hands buildOutbox the step's resolved read-sets: the
// outbox record submitSystemOp persists must carry the step's declared reads,
// resolved against THIS instance's subject. It reads the record back out of
// loom-state rather than inspecting the value in-process, because the outbox
// write is what the relay submits — an in-process assertion would not prove the
// read-set survived the transition batch.
func TestSubmitSystemOp_OutboxCarriesResolvedReads(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const subjectKey = "vtx.identity.BBsubjectHJKMNPQRSTV"
	state := newLoomStateStoreForTransition(ctx, t)
	e := &Engine{
		cfg:    Config{Lane: "core", ActorKey: "identity.system.loom", StepTimeout: time.Minute},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		state:  state,
	}
	pattern := &Pattern{
		PatternID:   "identityErasure",
		SubjectType: "identity",
		MetaKey:     "vtx.meta.BBpatternHJKMNPQRSTU",
	}

	for _, tc := range []struct {
		name              string
		instanceID        string
		step              Step
		wantReads         []string
		wantOptionalReads []string
	}{
		{
			name:       "declared reads resolve against the instance subject",
			instanceID: "inst-declared",
			step: Step{
				Kind: StepKindSystemOp, Operation: "ShredIdentityKey",
				Reads: []string{"subject"}, OptionalReads: []string{"subject.piiKey"},
			},
			wantReads:         []string{subjectKey},
			wantOptionalReads: []string{subjectKey + ".piiKey"},
		},
		{
			name:       "a step declaring nothing stays read-free",
			instanceID: "inst-read-free",
			step:       Step{Kind: StepKindSystemOp, Operation: "CompletePattern"},
			wantReads:  nil, wantOptionalReads: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst := &Instance{
				InstanceID: tc.instanceID,
				PatternRef: pattern.MetaKey,
				SubjectKey: subjectKey,
				Status:     StatusRunning,
			}
			require.NoError(t, e.submitSystemOp(ctx, inst, pattern, tc.step, "", tokenCreateOnly))

			entry, err := state.conn.KVGet(ctx, state.bucket, outboxKey(inst.PendingToken))
			require.NoError(t, err, "the transition batch must have written the outbox record")
			var rec outboxRecord
			require.NoError(t, json.Unmarshal(entry.Value, &rec))

			require.Equal(t, tc.step.Operation, rec.Operation)
			require.Equal(t, tc.wantReads, rec.Reads)
			require.Equal(t, tc.wantOptionalReads, rec.OptionalReads)
			require.Nil(t, rec.EgressReads, "a systemOp declares no external-plane egress")
		})
	}
}
