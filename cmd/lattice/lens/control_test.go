package lens

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/refractor/health/healthwire"
)

// startControlResponder stands up a responder on lattice.ctrl.refractor.> that
// records each request and replies with the canned response for the op named by
// the subject's last segment. It stands in for Refractor's control service: the
// surface under test is the CLI's request/decode/render path, and an op with no
// canned response answers with an error envelope, exactly as the real service
// does for an unknown lens.
func startControlResponder(t *testing.T, responses map[string]control.ControlResponse) (url string, seen *[]controlCall) {
	t.Helper()
	srv, nc := natsfixture.Server(t)
	url = srv.ClientURL()

	calls := make([]controlCall, 0, 4)
	sub, err := nc.Subscribe("lattice.ctrl.refractor.>", func(m *nats.Msg) {
		parts := strings.Split(m.Subject, ".")
		op := parts[len(parts)-1]
		lensID := parts[len(parts)-2]
		var req control.ControlRequest
		_ = json.Unmarshal(m.Data, &req)
		calls = append(calls, controlCall{op: op, lensID: lensID, actor: m.Header.Get("Lattice-Actor"), req: req})

		resp, ok := responses[op]
		if !ok {
			resp = control.ControlResponse{Error: "lens not found: " + lensID}
		}
		body, _ := json.Marshal(resp)
		_ = m.Respond(body)
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	require.NoError(t, nc.Flush())
	return url, &calls
}

type controlCall struct {
	op     string
	lensID string
	actor  string
	req    control.ControlRequest
}

func runLensCmd(t *testing.T, cmd *cobra.Command, args []string) (string, error) {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	runErr := cmd.Execute()

	require.NoError(t, w.Close())
	os.Stdout = orig
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String(), runErr
}

func strptr(s string) *string { return &s }

// TestLensResume_SendsResumeAndStampsActor pins the command that exists so a
// structurally paused lens has an operable recovery path at a terminal: the
// right subject op, the actor stamped on the request, and a confirmation naming
// the lens.
func TestLensResume_SendsResumeAndStampsActor(t *testing.T) {
	url, calls := startControlResponder(t, map[string]control.ControlResponse{
		"resume": {Resume: &control.ResumeResult{Resumed: true}},
	})

	outputFmt, defaultActor := "", ""
	cmd := NewCommand(&url, &outputFmt, &defaultActor)

	out, err := runLensCmd(t, cmd, []string{"resume", "lens-abc", "--actor", "vtx.identity.op1"})
	require.NoError(t, err)
	assert.Contains(t, out, `lens "lens-abc" resumed`)

	require.Len(t, *calls, 1)
	assert.Equal(t, "resume", (*calls)[0].op)
	assert.Equal(t, "lens-abc", (*calls)[0].lensID)
	assert.Equal(t, "vtx.identity.op1", (*calls)[0].actor, "the operator identity must reach the control plane's authorizer")
}

// TestLensPause_NotesRestartPersistence keeps the CLI honest about what a pause
// costs: it outlives the process, so an operator who pauses and cycles must not
// expect the lens back.
func TestLensPause_NotesRestartPersistence(t *testing.T) {
	url, calls := startControlResponder(t, map[string]control.ControlResponse{
		"pause": {Pause: &control.PauseResult{Paused: true}},
	})

	outputFmt, defaultActor := "", ""
	cmd := NewCommand(&url, &outputFmt, &defaultActor)

	out, err := runLensCmd(t, cmd, []string{"pause", "lens-abc"})
	require.NoError(t, err)
	assert.Contains(t, out, `lens "lens-abc" paused`)
	assert.Contains(t, out, "persists across restart until resume")
	require.Len(t, *calls, 1)
	assert.Equal(t, "pause", (*calls)[0].op)
}

// TestLensRebuild_TruncateReachesTheRequest verifies the one flag that changes
// what a rebuild does to the target — a preserved target reconciles, a
// truncated one guarantees no stale row survives.
func TestLensRebuild_TruncateReachesTheRequest(t *testing.T) {
	url, calls := startControlResponder(t, map[string]control.ControlResponse{
		"rebuild": {Rebuild: &control.RebuildResult{Started: true}},
	})

	outputFmt, defaultActor := "", ""
	cmd := NewCommand(&url, &outputFmt, &defaultActor)

	out, err := runLensCmd(t, cmd, []string{"rebuild", "lens-abc", "--truncate"})
	require.NoError(t, err)
	assert.Contains(t, out, "target truncated first")
	require.Len(t, *calls, 1)
	assert.True(t, (*calls)[0].req.Truncate, "--truncate must reach the control request")
}

// TestLensHealth_RendersPauseCause is the point of the whole increment at the
// terminal: "why is this lens down" answered without a browser. pauseReason
// names only the tier; lastError names the column the projection failed on.
func TestLensHealth_RendersPauseCause(t *testing.T) {
	const cause = `ERROR: column "row_kind" of relation "read_identity_credential_bindings" does not exist (SQLSTATE 42703)`
	url, _ := startControlResponder(t, map[string]control.ControlResponse{
		"health": {Entry: &healthwire.Entry{
			RuleID:      "lens-abc",
			Status:      "paused",
			PauseReason: strptr(healthwire.PauseReasonStructural),
			LastError:   strptr(cause),
		}},
	})

	outputFmt, defaultActor := "", ""
	cmd := NewCommand(&url, &outputFmt, &defaultActor)

	out, err := runLensCmd(t, cmd, []string{"health", "lens-abc"})
	require.NoError(t, err)
	assert.Contains(t, out, "structural")
	assert.Contains(t, out, cause, "the recorded cause is what the operator has to act on")
}

// TestLensResume_ControlErrorIsNotSilent guards the failure the operator most
// needs to see: a denied or unknown-lens resume must exit non-zero, never print
// a success line for a lens that is still down.
func TestLensResume_ControlErrorIsNotSilent(t *testing.T) {
	url, _ := startControlResponder(t, map[string]control.ControlResponse{})

	outputFmt, defaultActor := "", ""
	cmd := NewCommand(&url, &outputFmt, &defaultActor)

	out, err := runLensCmd(t, cmd, []string{"resume", "lens-missing"})
	require.Error(t, err)
	assert.NotContains(t, out, "resumed")
}
