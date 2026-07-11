//go:build leaseshortwindow

package leaseconvergence_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/processor"
)

// lastExternalEventBody returns the raw body of the most recent
// events.external.<adapter> message on the durable core-events stream (nil if
// none yet) — the durable-plane witness: whatever this holds is what a
// replayed/redelivered consumer would see, forever.
func (h *harness) lastExternalEventBody(adapter string) []byte {
	h.t.Helper()
	stream, err := h.conn.JetStream().Stream(h.ctx, bootstrap.CoreEventsStreamName)
	if err != nil {
		return nil
	}
	msg, err := stream.GetLastMsgForSubject(h.ctx, "events.external."+adapter)
	if err != nil || msg == nil {
		return nil
	}
	return msg.Data
}

// TestLeaseConvergence_SensitiveParamEgress_PlaintextAtAdapter_CiphertextOnEventStream
// is the live consumer sensitive-param-egress-design.md §7 names: the
// backgroundCheck pattern's name/dob subject.*.data.value templates (both
// identity-domain sensitive aspects) resolve through Loom's egressReads split,
// the Processor's ref-marker hydration, and the bridge's egress unwrap — the
// vendor (FakeBackgroundCheck) receives real plaintext, while the durable
// events.external.backgroundCheck message that rode the transactional outbox
// carries only `$sensitiveRef` values, never plaintext PII, anywhere in the
// stream any redelivery or replay would observe.
func TestLeaseConvergence_SensitiveParamEgress_PlaintextAtAdapter_CiphertextOnEventStream(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping the all-engines lease convergence e2e in -short mode")
	}
	h := newHarness(t)
	appKey, appID, applicantKey := h.seedApplicant()
	applicantID := applicantKey[len("vtx.identity."):]

	h.driveApplicantSteps(appKey, applicantKey)
	h.decideLandlord(appKey, "approved")
	h.drainUntilConverged(appID, 15*time.Second)

	handle := h.bgcheckHandle(applicantID)
	require.NotEmpty(t, handle, "a backgroundCheck service instance must have been minted for the applicant")

	// The last-mile vendor view: real plaintext, not a ref.
	params := h.bgFake.LastParams(handle)
	require.NotNil(t, params, "FakeBackgroundCheck must have been called")
	require.Equal(t, "Lease Applicant", params["name"], "the vendor must receive the applicant's real plaintext name")
	require.Equal(t, "1990-01-01", params["dob"], "the vendor must receive the applicant's real plaintext dob")
	require.Equal(t, "backgroundCheck", params["family"], "the literal family param must pass through unchanged")

	// The durable plane: only refs, never plaintext, anywhere the body could be
	// replayed from (a redelivery, a DR restore, an operator KV browse).
	body := h.lastExternalEventBody("backgroundCheck")
	require.NotEmpty(t, body, "the external.backgroundCheck event must have been published to the durable core-events stream")
	bodyStr := string(body)
	require.Contains(t, bodyStr, `"$sensitiveRef"`, "the durable event must carry the sensitive-ref marker for the templated PII fields")
	require.NotContains(t, bodyStr, "Lease Applicant", "the applicant's plaintext name must never reach the durable event stream")
	require.NotContains(t, bodyStr, "1990-01-01", "the applicant's plaintext dob must never reach the durable event stream")

	var ev struct {
		Payload struct {
			Params map[string]json.RawMessage `json:"params"`
		} `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(body, &ev))
	require.Contains(t, string(ev.Payload.Params["name"]), "$sensitiveRef", "params.name must ride as a sensitive-ref, not plaintext")
	require.Contains(t, string(ev.Payload.Params["dob"]), "$sensitiveRef", "params.dob must ride as a sensitive-ref, not plaintext")
	var family string
	require.NoError(t, json.Unmarshal(ev.Payload.Params["family"], &family))
	require.Equal(t, "backgroundCheck", family)
}

// TestLeaseConvergence_SensitiveParamEgress_ShredThenReplay_NeverDecrypts is
// the shred-durability arm at the live-pattern level: once the applicant's
// bgcheck has completed (the plaintext already reached the vendor once — a
// legitimate pre-shred call), a subsequent ShredIdentityKey commit followed by
// a REPLAYED external.backgroundCheck event (mirroring a redelivery/DR
// restore) must NEVER decrypt again — the bridge's unwrap refuses (the
// permanent shredded-key failure path), so no second plaintext reaches
// FakeBackgroundCheck. The bridge-unit-level shred + shred-then-restart
// mechanism proofs (internal/bridge's egress_test.go) cover the mechanism
// itself; this is the live-pattern replay proof the design's §7 e2e names.
func TestLeaseConvergence_SensitiveParamEgress_ShredThenReplay_NeverDecrypts(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping the all-engines lease convergence e2e in -short mode")
	}
	h := newHarness(t)
	appKey, appID, applicantKey := h.seedApplicant()
	applicantID := applicantKey[len("vtx.identity."):]

	h.driveApplicantSteps(appKey, applicantKey)
	h.decideLandlord(appKey, "approved")
	h.drainUntilConverged(appID, 15*time.Second)

	handle := h.bgcheckHandle(applicantID)
	require.NotEmpty(t, handle)
	callsBeforeShred := h.bgFake.SideEffects(handle)
	require.Equal(t, 1, callsBeforeShred, "exactly one legitimate pre-shred vendor call")

	// Shred the applicant's key (privacy-base's real op, through the real
	// Processor — the same commit path production uses).
	shredReply := h.submitOp("ShredIdentityKey", "shredIdentityKey", "default", bootstrap.BootstrapIdentityKey,
		map[string]any{"identityKey": applicantKey}, &processor.ContextHint{Reads: []string{applicantKey}})
	require.Equalf(t, processor.ReplyStatusAccepted, shredReply.Status, "ShredIdentityKey: %+v", shredReply.Error)

	// Replay the SAME external.backgroundCheck event (the durable body still
	// carries the pre-shred $sensitiveRef — the ciphertext is permanently at
	// rest regardless of the shred). A redelivered/replayed event must not
	// re-call the vendor with decrypted plaintext.
	body := h.lastExternalEventBody("backgroundCheck")
	require.NotEmpty(t, body)
	_, err := h.conn.JetStream().Publish(h.ctx, "events.external.backgroundCheck", body)
	require.NoError(t, err)

	// Give the bridge's redelivery-consumer a beat to (fail to) process the
	// replay, then assert no NEW side-effect landed — the shred gate held.
	time.Sleep(2 * time.Second)
	require.Equal(t, callsBeforeShred, h.bgFake.SideEffects(handle),
		"a shred must permanently block further vendor calls for this identity — no post-shred plaintext reached the adapter")
}
