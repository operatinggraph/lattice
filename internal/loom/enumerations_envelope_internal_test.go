package loom

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestSystemOpEnumerations_ReachEnvelope proves the DELIVERING line, not the
// rule: a step's declared kv.Links walks must survive the transition batch as
// an outbox record AND arrive on the op envelope the relay publishes, with each
// hub resolved against this instance's subject. A declaration that parses into
// Step, persists, and never reaches the wire is a declaration nobody can read.
//
// It walks the real path in both halves — submitSystemOp writes the record,
// then the relay's own handler publishes it — and reads the envelope off
// ops.<lane>, so no in-process assertion stands in for the publish.
func TestSystemOpEnumerations_ReachEnvelope(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const subjectKey = "vtx.identity.BBsubjectHJKMNPQRSTV"
	state := newLoomStateStoreForTransition(ctx, t)
	js := state.conn.JetStream()
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{Name: "core-operations", Subjects: []string{"ops.>"}})
	require.NoError(t, err)
	ops, err := state.conn.NATS().SubscribeSync("ops.core")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ops.Unsubscribe() })

	e := &Engine{
		cfg:    Config{Lane: "core", ActorKey: "identity.system.loom", StepTimeout: time.Minute},
		logger: testRelayLogger(),
		state:  state,
		relay:  newRelay(state.conn, state.bucket, testRelayLogger()),
	}
	pattern := &Pattern{
		PatternID:   "identityErasure",
		SubjectType: "identity",
		MetaKey:     "vtx.meta.BBpatternHJKMNPQRSTU",
	}

	for _, tc := range []struct {
		name       string
		instanceID string
		step       Step
		want       []Enumeration
		// wantReadFree marks the step that declares walks and NOTHING else, so
		// the published contextHint can only exist because of the walks.
		wantReadFree bool
	}{
		{
			name:       "declared walks resolve and reach the envelope",
			instanceID: "inst-walks",
			step: Step{
				Kind: StepKindSystemOp, Operation: "UnbindIdentityCredentials",
				Reads: []string{"subject"},
				Enumerations: []Enumeration{
					{Hub: subjectToken, Relation: "boundTo", Direction: "in"},
					{Hub: subjectToken, Relation: "boundTo", Direction: "out"},
				},
			},
			want: []Enumeration{
				{Hub: subjectKey, Relation: "boundTo", Direction: "in"},
				{Hub: subjectKey, Relation: "boundTo", Direction: "out"},
			},
		},
		{
			name:       "a step declaring no walks stays walk-free",
			instanceID: "inst-no-walks",
			step:       Step{Kind: StepKindSystemOp, Operation: "ShredIdentityKey", Reads: []string{"subject"}},
			want:       nil,
		},
		{
			// The case that exercises the relay's ContextHint guard on the
			// enumerations disjunct ALONE. Every other step in this table, and
			// every step shipped in the corpus, also declares Reads — so with
			// only those, the guard would attach a contextHint for the reads
			// and carry the walks along for free, and reverting the disjunct
			// would change nothing anywhere. An op that walks links without
			// hydrating any key is the shape that proves the disjunct is load
			// bearing: without it the envelope carries NO contextHint at all
			// and the declaration is dropped on the floor.
			name:       "walks alone attach the contextHint",
			instanceID: "inst-walks-only",
			step: Step{
				Kind: StepKindSystemOp, Operation: "SweepWithoutHydrating",
				Enumerations: []Enumeration{
					{Hub: subjectToken, Relation: "duplicateOf", Direction: "out"},
				},
			},
			want: []Enumeration{
				{Hub: subjectKey, Relation: "duplicateOf", Direction: "out"},
			},
			wantReadFree: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst := &Instance{
				InstanceID: tc.instanceID,
				PatternRef: pattern.MetaKey,
				SubjectKey: subjectKey,
				Status:     StatusRunning,
			}
			require.NoError(t, e.submitSystemOp(ctx, inst, pattern, tc.step, ""))

			key := outboxKey(inst.PendingToken)
			entry, err := state.conn.KVGet(ctx, state.bucket, key)
			require.NoError(t, err, "the transition batch must have written the outbox record")
			var rec outboxRecord
			require.NoError(t, json.Unmarshal(entry.Value, &rec))
			require.Equal(t, tc.want, rec.Enumerations, "the persisted record carries the resolved walks")

			// The relay is what publishes; drive its real handler over the
			// record the batch just wrote, addressed by the KV subject it
			// consumes.
			dec, err := e.relay.handle(ctx, substrate.Message{
				Subject: "$KV." + state.bucket + "." + key,
				Body:    entry.Value,
			})
			require.NoError(t, err)
			require.Equal(t, substrate.Ack, dec)

			msg, err := ops.NextMsg(5 * time.Second)
			require.NoError(t, err, "the relay must have published to ops.core")
			var env struct {
				ContextHint *struct {
					Enumerations []Enumeration `json:"enumerations"`
				} `json:"contextHint"`
			}
			require.NoError(t, json.Unmarshal(msg.Data, &env))
			require.NotNil(t, env.ContextHint, "the step declares a contextHint's worth of context")
			require.Equal(t, tc.want, env.ContextHint.Enumerations)

			var raw map[string]any
			require.NoError(t, json.Unmarshal(msg.Data, &raw))
			hint, _ := raw["contextHint"].(map[string]any)

			if tc.want == nil {
				_, present := hint["enumerations"]
				require.False(t, present, "a walk-free step must publish no enumerations key at all")
			}
			if tc.wantReadFree {
				require.NotContains(t, hint, "reads",
					"this step declares no reads — the contextHint on the wire is the enumerations disjunct's doing, nothing else's")
				require.NotContains(t, hint, "optionalReads")
				require.Contains(t, hint, "enumerations")
			}
		})
	}
}

// TestPatternValidate_DeclaredEnumerations is the engine-side gate on a step's
// declared walks, and the counterpart of pkgmgr's
// TestValidateEnumerations_RejectsWhatTheProcessorWould. The two validators are
// kept in lockstep so an install can never admit a pattern this would reject at
// CDC load, leaving the pattern dark.
//
// Three properties, each failing in its own direction: the hub is subject-
// relative (the subject is the only key a shared definition can name); the
// relation/direction pair is what the Processor's envelope parse accepts (a
// declaration it refuses is terminal on every redelivery); and only a systemOp
// may declare one at all, because a userTask's and an externalTask's op are
// engine-chosen.
func TestPatternValidate_DeclaredEnumerations(t *testing.T) {
	t.Parallel()

	sweep := func(ens ...Enumeration) Step {
		return Step{Kind: StepKindSystemOp, Operation: "UnbindIdentityCredentials", Enumerations: ens}
	}

	for _, tc := range []struct {
		name    string
		step    Step
		wantErr string
	}{
		{"a subject-relative hub is admitted", sweep(Enumeration{Hub: subjectToken, Relation: "boundTo", Direction: "in"}), ""},
		{"an aspect hub is admitted", sweep(Enumeration{Hub: "subject.piiKey", Relation: "indexes", Direction: "out"}), ""},
		{
			"a literal hub is not a template",
			sweep(Enumeration{Hub: "vtx.identity.BBsubjectHJKMNPQRSTV", Relation: "boundTo", Direction: "in"}),
			"subject-relative templates",
		},
		{
			"an aspect hub outside the localName charset",
			sweep(Enumeration{Hub: "subject.pii key", Relation: "boundTo", Direction: "in"}),
			"not a Contract #1 aspect localName",
		},
		{"an empty relation names no walk", sweep(Enumeration{Hub: subjectToken, Direction: "in"}), "requires a relation"},
		{
			"a direction outside out|in is rejected",
			sweep(Enumeration{Hub: subjectToken, Relation: "boundTo", Direction: "both"}),
			"direction must be",
		},
		{
			"a userTask may not declare walks",
			Step{Kind: StepKindUserTask, Operation: "SignLease",
				Enumerations: []Enumeration{{Hub: subjectToken, Relation: "boundTo", Direction: "in"}}},
			"enumerations is a systemOp-only field",
		},
		{
			"an externalTask may not declare walks",
			Step{
				Kind: StepKindExternalTask, Adapter: "esign", InstanceOp: "CreateEnvelope", ReplyOp: "ResolveEnvelope",
				Enumerations: []Enumeration{{Hub: subjectToken, Relation: "boundTo", Direction: "in"}},
			},
			"enumerations is a systemOp-only field",
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
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}
