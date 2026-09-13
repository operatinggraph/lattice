//go:build leaseshortwindow

package leaseconvergence_test

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
	privacybase "github.com/operatinggraph/lattice/packages/privacy-base"
)

// classEgressProbeScript is the fixture instanceOp: the externalTask shape
// lease-signing's CreateLeaseServiceInstance has (actor-guarded to Loom's relay
// actor, resolve_subject_params over the step's templated params, an
// external.<adapter> event off the op's own transactional outbox), with the
// subject pinned to a leaseapp instead of an identity so a CLASS-custodied
// aspect (.profile, custodied on lease-signing's underwritingRecord retention
// class) can be templated at the top level of the params.
//
// No shipped op can do that: CreateLeaseServiceInstance pins its subject to an
// identity and CreateLeaseDocInstance assembles its document params itself with
// no resolve_subject_params, so the live chain for a retention-class holder has
// no consumer in the shipped corpus for this test to drive.
//
// `nest` selects the second mode: the params resolve at the top level exactly as
// in mode one, and the resolved map — markers and all — is then emitted one
// level down under that key, which is the shape a pattern that templated a
// sensitive aspect into a nested document field would produce.
const classEgressProbeScript = `
` + orchestrationbase.ResolveSubjectParamsHelper + `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def required_bare_handle(p, name):
    v = required_string(p, name)
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "" or parts[2] == "":
        fail("InvalidArgument: " + name + ": empty type or id segment; got " + key)
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "EmitClassEgressProbe":
        # actor-guard first, exactly as the shipped instanceOp has it: the
        # subject is payload-named and its resolved params ride to a vendor, so
        # the submitter set is the one engine that dispatches externalTask steps.
        if op.actor != primordialActor["loom"]:
            fail("AuthDenied: EmitClassEgressProbe is restricted to Loom's relay actor; got " + op.actor)

        handle = required_bare_handle(p, "instanceKey")
        subject_key = required_string(p, "subjectKey")
        parts_of(subject_key, "subjectKey", "leaseapp")
        if not vertex_alive(state, subject_key):
            fail("UnknownSubject: " + subject_key)

        raw_params = p.params if hasattr(p, "params") and p.params != None else {}
        resolved = resolve_subject_params(raw_params, subject_key)
        resolved["family"] = "backgroundCheck"

        event_params = resolved
        if hasattr(p, "nest") and p.nest != None and p.nest != "":
            event_params = {p.nest: resolved}

        probe_key = "vtx.classegressprobe." + handle
        event_data = {
            "instanceKey":    handle,
            "adapter":        "backgroundCheck",
            "replyOp":        "RecordLeaseServiceOutcome",
            "dispatchOp":     "RecordServiceDispatch",
            "externalRef":    handle,
            "idempotencyKey": handle,
            "params":         event_params,
        }
        return {"mutations": [make_vtx(probe_key, "classEgressProbe", {})],
                "events": [{"class": "external.backgroundCheck", "data": event_data}],
                "response": {"primaryKey": probe_key}}

    fail("UnsupportedOperation: " + ot)
`

// classEgressFixturePackage is the §10 "test-package fixture": one vertexType
// DDL whose single op emits an external.backgroundCheck event whose params were
// resolved against a leaseapp subject. It declares no lens, no weaver target and
// no pattern, so installing it after lease-signing leaves the harness's lens
// activation and every other convergence test untouched.
func classEgressFixturePackage() pkgmgr.Definition {
	return pkgmgr.Definition{
		Name:        "class-egress-fixture",
		Version:     "0.0.1",
		Description: "Test-only externalTask instanceOp over a leaseapp subject, for the retention-class egress live-chain proof.",
		DDLs: []pkgmgr.DDLSpec{{
			CanonicalName:     "classEgressProbe",
			Class:             "meta.ddl.vertexType",
			PermittedCommands: []string{"EmitClassEgressProbe"},
			Description: "Test fixture vertex type (class-egress-fixture). EmitClassEgressProbe{instanceKey, subjectKey, params, nest?} " +
				"mints vtx.classegressprobe.<handle> (root data {} — D5) and emits the external.backgroundCheck event off its own " +
				"transactional outbox, with params resolved by orchestration-base's resolve_subject_params against the leaseapp " +
				"subjectKey. Restricted to Loom's relay actor. `nest`, when set, emits the resolved map one level down under that key.",
			Script: classEgressProbeScript,
			InputSchema: `{"type":"object","properties":` +
				`{"instanceKey":{"type":"string","description":"The bare instance handle; the op prepends vtx.classegressprobe."},` +
				`"subjectKey":{"type":"string","description":"vtx.leaseapp.<NanoID> the templated params resolve against."},` +
				`"params":{"type":"object","description":"Opaque adapter params; a subject.<aspect>.data.<field> string resolves against subjectKey."},` +
				`"nest":{"type":"string","description":"When non-empty, the resolved params are emitted nested one level under this key."}},` +
				`"required":["instanceKey","subjectKey"]}`,
			OutputSchema: `{"type":"object","properties":{"primaryKey":{"type":"string","description":"vtx.classegressprobe.<handle>."}}}`,
			FieldDescription: map[string]string{
				"instanceKey": "Bare instance handle — also the event's externalRef and idempotencyKey.",
				"subjectKey":  "Full vtx.leaseapp.<NanoID> key the subject.* param templates resolve against.",
				"params":      "Opaque adapter params passed to resolve_subject_params.",
				"nest":        "Optional key to nest the resolved params under, producing a marker below the top level of params.",
			},
			Examples: []pkgmgr.ExampleSpec{{
				Name: "EmitClassEgressProbe — template a class-custodied aspect at the top level",
				Payload: map[string]any{
					"instanceKey": "<bareHandle>",
					"subjectKey":  "vtx.leaseapp.<NanoID>",
					"params":      map[string]any{"employer": "subject.profile.data.employerName"},
				},
				ExpectedOutcome: "Mints vtx.classegressprobe.<handle> and emits external.backgroundCheck with params.employer " +
					"carrying the $sensitiveRef marker the Processor hydrated for the retention-class-custodied .profile aspect.",
			}},
		}},
	}
}

// withExtraPackages installs additional package definitions after the real
// chain — here the test-only fixture below.
func withExtraPackages(defs ...pkgmgr.Definition) harnessOpt {
	return func(hc *harnessConfig) { hc.extraPackages = append(hc.extraPackages, defs...) }
}

// seedClassCustodiedProfile writes the leaseapp's `.profile` aspect — sensitive,
// custodied on lease-signing's underwritingRecord RETENTION CLASS rather than on
// the applicant's identity — with a known employerName, and returns it. Step 6.5
// mints the class holder's `.piiKey` lazily in this same batch, which is what
// gives the retentionClassKeyEnvelope lens a row to project.
func (h *harness) seedClassCustodiedProfile(appKey, employerName string) string {
	h.t.Helper()
	reply := h.submitOp("SetApplicantProfile", "leaseapp", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"leaseAppKey": appKey, "unit": h.lastUnitKey, "annualIncome": 96000,
		"employmentStatus": "employed", "employerName": employerName,
	}, &processor.ContextHint{Reads: []string{appKey}})
	require.Equalf(h.t, processor.ReplyStatusAccepted, reply.Status, "SetApplicantProfile: %+v", reply.Error)
	return employerName
}

// submitClassEgressProbe submits the fixture op as Loom's relay actor with the
// read declaration Loom's own inferExternalTaskReads would produce for this step
// (Reads = the subject; EgressReads = the templated sensitive aspect key), so
// the Processor hydrates `.profile` as a `$sensitiveRef` marker rather than
// decrypting it into the script.
func (h *harness) submitClassEgressProbe(handle, appKey string, payload map[string]any) *processor.OperationReply {
	h.t.Helper()
	full := map[string]any{"instanceKey": handle, "subjectKey": appKey}
	for k, v := range payload {
		full[k] = v
	}
	return h.submitOp("EmitClassEgressProbe", "classEgressProbe", "default", bootstrap.LoomIdentityKey, full,
		&processor.ContextHint{Reads: []string{appKey}, EgressReads: []string{appKey + ".profile"}})
}

// externalEventBodyFor returns the raw body of the durable
// events.external.<adapter> message whose payload.instanceKey is instanceKey,
// or nil when the stream holds none. The subject also carries whatever the
// harness's own onboarding flow dispatches, so the body is selected by the
// probe's unique handle rather than by recency.
func (h *harness) externalEventBodyFor(adapter, instanceKey string) []byte {
	h.t.Helper()
	cons, err := h.conn.JetStream().OrderedConsumer(h.ctx, bootstrap.CoreEventsStreamName,
		jetstream.OrderedConsumerConfig{
			FilterSubjects: []string{"events.external." + adapter},
			DeliverPolicy:  jetstream.DeliverAllPolicy,
		})
	if err != nil {
		return nil
	}
	for {
		msg, err := cons.Next(jetstream.FetchMaxWait(500 * time.Millisecond))
		if err != nil {
			return nil
		}
		var ev struct {
			Payload struct {
				InstanceKey string `json:"instanceKey"`
			} `json:"payload"`
		}
		if json.Unmarshal(msg.Data(), &ev) == nil && ev.Payload.InstanceKey == instanceKey {
			return msg.Data()
		}
	}
}

// bridgeReplyWatch records the terminal replyOp the bridge posts for ONE
// externalRef. It keys on the handle rather than on the bridge's per-adapter
// health issue, which the next successful dispatch of the same adapter clears —
// the harness's own onboarding flow dispatches background checks of its own.
type bridgeReplyWatch struct {
	mu     sync.Mutex
	status string
	detail string
}

func (w *bridgeReplyWatch) outcome() (status, detail string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status, w.detail
}

// watchBridgeReply subscribes to the bridge's op lane and captures the
// RecordLeaseServiceOutcome envelope carrying externalRef. The Processor refuses
// that op here (the probe mints no service instance for it to find), which is
// downstream of what this witnesses: the bridge publishes it and Acks, so the
// envelope is the bridge's own statement of the terminal outcome it reached.
// Start it BEFORE the op that produces the event.
func (h *harness) watchBridgeReply(externalRef string) *bridgeReplyWatch {
	h.t.Helper()
	w := &bridgeReplyWatch{}
	sub, err := h.conn.NATS().Subscribe("ops.system", func(msg *nats.Msg) {
		var env struct {
			OperationType string `json:"operationType"`
			Payload       struct {
				ExternalRef string `json:"externalRef"`
				Status      string `json:"status"`
				Result      string `json:"result"`
			} `json:"payload"`
		}
		if json.Unmarshal(msg.Data, &env) != nil {
			return
		}
		if env.OperationType != "RecordLeaseServiceOutcome" || env.Payload.ExternalRef != externalRef {
			return
		}
		w.mu.Lock()
		w.status, w.detail = env.Payload.Status, env.Payload.Result
		w.mu.Unlock()
	})
	require.NoError(h.t, err)
	h.t.Cleanup(func() { _ = sub.Unsubscribe() })
	return w
}

// classEgressHarness boots the standard all-engines harness with the
// retentionClassKeyEnvelope lens projected (the bridge's envelope read model for
// a retention-class holder) and the fixture package installed after the real
// chain.
func classEgressHarness(t *testing.T) *harness {
	t.Helper()
	return newHarness(t,
		withExtraLenses("retentionClassKeyEnvelope"),
		withExtraPackages(classEgressFixturePackage()))
}

// TestLeaseConvergence_ClassEgress_TopLevelTemplate_PlaintextAtAdapter is the
// live-chain proof retention-class-egress-envelope-design.md §10 names, in the
// consumer's shape rather than the background check's: an aspect whose DEK is
// custodied on a RETENTION CLASS (not on any identity) is templated at the top
// level of an externalTask step's params, and rides the whole chain — Loom's
// egressReads split, the Processor's ref-marker mint over a class holder, the
// retentionClassKeyEnvelope lens's row, the bridge's per-holder-kind envelope
// lookup and ref-verified decrypt — to reach the vendor as real plaintext, while
// the durable event body a replay or DR restore would observe carries only the
// `$sensitiveRef` marker, keyed to the class holder.
func TestLeaseConvergence_ClassEgress_TopLevelTemplate_PlaintextAtAdapter(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping the all-engines lease convergence e2e in -short mode")
	}
	h := classEgressHarness(t)
	appKey, _, _ := h.seedApplicant()
	employer := h.seedClassCustodiedProfile(appKey, "Retained Employer Co")

	handle := mustNanoID(t)
	reply := h.submitClassEgressProbe(handle, appKey, map[string]any{
		"params": map[string]any{"employer": "subject.profile.data.employerName"},
	})
	require.Equalf(t, processor.ReplyStatusAccepted, reply.Status, "EmitClassEgressProbe: %+v", reply.Error)

	// The last-mile vendor view: the class-custodied field arrives decrypted.
	var params map[string]string
	require.Eventuallyf(t, func() bool {
		params = h.bgFake.LastParams(handle)
		return params != nil
	}, 30*time.Second, 200*time.Millisecond,
		"the bridge must dispatch the probe's external.backgroundCheck event to FakeBackgroundCheck")
	require.Equal(t, employer, params["employer"],
		"the vendor must receive the retention-class-custodied employerName as plaintext")
	require.Equal(t, "backgroundCheck", params["family"], "the literal family param must pass through unchanged")

	// The durable plane: a ref, never the plaintext, and the ref names the
	// CLASS holder — the whole point of the second envelope source.
	body := h.externalEventBodyFor("backgroundCheck", handle)
	require.NotEmpty(t, body, "the probe's external.backgroundCheck event must be on the durable core-events stream")
	require.Contains(t, string(body), `"$sensitiveRef"`, "the durable event must carry the sensitive-ref marker")
	require.NotContains(t, string(body), employer, "the retained employer name must never reach the durable event stream")

	var ev struct {
		Payload struct {
			Params struct {
				Employer struct {
					Ref struct {
						Ciphertext struct {
							KeyID string `json:"keyId"`
						} `json:"ciphertext"`
					} `json:"$sensitiveRef"`
				} `json:"employer"`
			} `json:"params"`
		} `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(body, &ev))
	holderKey := ev.Payload.Params.Employer.Ref.Ciphertext.KeyID
	require.Truef(t, len(holderKey) > len("vtx.retentionclass.") && holderKey[:len("vtx.retentionclass.")] == "vtx.retentionclass.",
		"the marker's key holder must be the retention class, not an identity; got %q", holderKey)

	// The row the bridge just read: the new lens projected the class holder's
	// envelope into its OWN bucket, beside the identity lens's.
	entry, err := h.conn.KVGet(h.ctx, privacybase.RetentionKeyEnvelopeBucket, holderKey)
	require.NoErrorf(t, err, "retentionClassKeyEnvelope must project a row for %s", holderKey)
	require.NotEmpty(t, entry.Value)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &envelope))
	require.Equal(t, holderKey, envelope["key"])
	require.NotEmpty(t, envelope["wrappedDEK"], "the projected envelope must carry the wrapped DEK the bridge decrypts with")
}

// TestLeaseConvergence_ClassEgress_NestedTemplate_NeverReachesVendor is the
// §3.5(b) arm at the live-pattern level: the same class-custodied aspect,
// resolved the same way, but emitted one level below the top of `params`. The
// unwrap substitutes markers at the top level only; a marker any deeper is a
// permanent refusal rather than a pass-through, because nothing substitutes it
// and it would otherwise ride to the vendor as a retained record's ciphertext +
// MAC inside RawParams. So the whole shape is one typed terminal outcome: the
// bridge posts a failed replyOp naming the param and the depth, and the vendor
// is never called at all across the observation window.
func TestLeaseConvergence_ClassEgress_NestedTemplate_NeverReachesVendor(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping the all-engines lease convergence e2e in -short mode")
	}
	h := classEgressHarness(t)
	appKey, _, _ := h.seedApplicant()
	employer := h.seedClassCustodiedProfile(appKey, "Retained Employer Co")

	handle := mustNanoID(t)
	watch := h.watchBridgeReply(handle)
	// The probe emits {"doc": {"employer": <marker>, "family": "backgroundCheck"}}
	// — the marker one level below where the unwrap serves it.
	reply := h.submitClassEgressProbe(handle, appKey, map[string]any{
		"params": map[string]any{"employer": "subject.profile.data.employerName"},
		"nest":   "doc",
	})
	require.Equalf(t, processor.ReplyStatusAccepted, reply.Status, "EmitClassEgressProbe(nested): %+v", reply.Error)

	// The bridge's own witness that it SAW this event and reached a TERMINAL
	// failed outcome on it — without it the never-called assertion below could
	// pass vacuously on an event that was simply never delivered.
	var status, detail string
	require.Eventuallyf(t, func() bool {
		status, detail = watch.outcome()
		return status != ""
	}, 30*time.Second, 200*time.Millisecond,
		"the bridge must post a terminal replyOp for the nested-marker probe")
	require.Equal(t, "failed", status, "a nested marker must converge to a failed outcome, never park")
	require.Contains(t, detail, "doc", "the failure must name the param the nested marker was found under")
	require.Contains(t, detail, "depth", "the failure must name the depth the unwrap does not serve")

	// And the property itself: no plaintext, no ciphertext, no call at all.
	require.Neverf(t, func() bool { return h.bgFake.SideEffects(handle) > 0 },
		3*time.Second, 150*time.Millisecond,
		"a marker nested below the top level of params must never reach the vendor")
	require.Nil(t, h.bgFake.LastParams(handle), "the vendor must never have been called for the nested probe")
	body := h.externalEventBodyFor("backgroundCheck", handle)
	require.NotEmpty(t, body, "the probe's event must still be durably on the stream — the refusal is at the bridge, not the outbox")
	require.NotContains(t, string(body), employer, "the retained employer name must never reach the durable event stream")
}
