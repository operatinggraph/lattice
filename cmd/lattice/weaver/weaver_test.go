package weaver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/testutil"
	internalweaver "github.com/operatinggraph/lattice/internal/weaver"
	"github.com/operatinggraph/lattice/internal/weaver/control"
)

// fakeEngine satisfies the control package's unexported engineControl
// interface structurally, mirroring internal/weaver/control's own test
// fake. Lets this package's tests drive a real weaver-control NATS
// responder without a *weaver.Engine.
type fakeEngine struct {
	targets []internalweaver.TargetSummary
	errOn   map[string]error
	// resetDeleted is the window count ResetConfidence reports on success.
	resetDeleted int
	// resetPrevious is the dispatch-count ResetRetryBudget reports on success;
	// budgetArgs records the per-gap arguments it received, which the control
	// subject cannot carry and which therefore have to survive the request body.
	resetPrevious int
	budgetArgs    []string
	// replayQueued is the row count ReplayTarget reports on success.
	replayQueued int
}

func (f *fakeEngine) ListTargets(_ context.Context) ([]internalweaver.TargetSummary, error) {
	return f.targets, nil
}

func (f *fakeEngine) Disable(_ context.Context, targetID string) error {
	return f.errOn["disable:"+targetID]
}

func (f *fakeEngine) Enable(_ context.Context, targetID string) error {
	return f.errOn["enable:"+targetID]
}

func (f *fakeEngine) Revoke(_ context.Context, targetID string) error {
	return f.errOn["revoke:"+targetID]
}

func (f *fakeEngine) ResetConfidence(_ context.Context, targetID string) (int, error) {
	if err := f.errOn["resetConfidence:"+targetID]; err != nil {
		return 0, err
	}
	return f.resetDeleted, nil
}

func (f *fakeEngine) ResetRetryBudget(_ context.Context, targetID, entityID, gapColumn string) (int, error) {
	f.budgetArgs = append(f.budgetArgs, targetID+"/"+entityID+"/"+gapColumn)
	if err := f.errOn["resetBudget:"+targetID]; err != nil {
		return 0, err
	}
	return f.resetPrevious, nil
}

func (f *fakeEngine) ReplayTarget(_ context.Context, targetID string) (int, error) {
	if err := f.errOn["replayTarget:"+targetID]; err != nil {
		return 0, err
	}
	return f.replayQueued, nil
}

// startWeaverControlTest starts an embedded NATS server with a
// weaver-control responder backed by eng, and returns its NATS URL.
func startWeaverControlTest(t *testing.T, eng *fakeEngine) string {
	t.Helper()
	url := testutil.StartEmbeddedNATS(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	conn := natsfixture.Connect(t, url)

	// Explicit allow-all stub: these tests exercise the CLI→wire mechanics, not
	// capability enforcement (a nil checker fails closed). Auth is covered by the
	// control package's own tests.
	svc := control.NewService(eng, control.NewStubCapabilityChecker(testutil.TestLogger()), testutil.TestLogger())
	require.NoError(t, svc.StartNATSListener(ctx, conn))
	require.NoError(t, conn.Flush())

	return url
}

// recordingCapability records the actor argument of the last Authorize call
// and always allows — used to prove the CLI's --actor flag (and its
// credential-file default) reach the wire as the Lattice-Actor header.
type recordingCapability struct{ last string }

func (r *recordingCapability) Authorize(_ context.Context, actor, _, _ string) error {
	r.last = actor
	return nil
}

// startWeaverControlTestWithCapability is startWeaverControlTest but wired to
// a caller-supplied CapabilityChecker instead of the plain helper's allow-all stub.
func startWeaverControlTestWithCapability(t *testing.T, eng *fakeEngine, cap control.CapabilityChecker) string {
	t.Helper()
	url := testutil.StartEmbeddedNATS(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	conn := natsfixture.Connect(t, url)

	svc := control.NewService(eng, cap, testutil.TestLogger())
	require.NoError(t, svc.StartNATSListener(ctx, conn))
	require.NoError(t, conn.Flush())

	return url
}

// TestWeaverDisable_ActorFlagReachesWire verifies --actor is stamped as the
// Lattice-Actor header on the control request (control-plane-capability-authz
// -design.md Fire 1a).
func TestWeaverDisable_ActorFlagReachesWire(t *testing.T) {
	eng := &fakeEngine{errOn: map[string]error{}}
	rec := &recordingCapability{}
	url := startWeaverControlTestWithCapability(t, eng, rec)

	natsURL := url
	outputFmt := ""
	actorKey := ""
	cmd := NewCommand(&natsURL, &outputFmt, &actorKey)

	_, err := runCmd(t, cmd, []string{"disable", "t1", "--actor", "vtx.identity.OPERATOR"})
	require.NoError(t, err)
	assert.Equal(t, "vtx.identity.OPERATOR", rec.last)
}

// TestWeaverList_DefaultActorFallsBackToCredentialFile verifies the
// credential-file default (op.NewCommand's third arg) is used when --actor is
// not passed.
func TestWeaverList_DefaultActorFallsBackToCredentialFile(t *testing.T) {
	eng := &fakeEngine{errOn: map[string]error{}}
	rec := &recordingCapability{}
	url := startWeaverControlTestWithCapability(t, eng, rec)

	natsURL := url
	outputFmt := ""
	actorKey := "vtx.identity.CREDFILE"
	cmd := NewCommand(&natsURL, &outputFmt, &actorKey)

	_, err := runCmd(t, cmd, []string{"list"})
	require.NoError(t, err)
	assert.Equal(t, "vtx.identity.CREDFILE", rec.last)
}

// runCmd executes cmd with args, capturing stdout. Returns stdout and the
// command error.
func runCmd(t *testing.T, cmd *cobra.Command, args []string) (string, error) {
	t.Helper()
	cmd.SetArgs(args)

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	cmdErr := cmd.Execute()

	require.NoError(t, w.Close())
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String(), cmdErr
}

func TestWeaverList_HappyPath(t *testing.T) {
	eng := &fakeEngine{
		targets: []internalweaver.TargetSummary{
			{TargetID: "t1", LensRef: "lens-1", Gaps: []string{"missing_a"}, State: "active"},
			{TargetID: "t2", LensRef: "lens-2", Gaps: []string{"missing_b"}, State: "disabled"},
		},
		errOn: map[string]error{},
	}
	url := startWeaverControlTest(t, eng)

	natsURL := url
	outputFmt := "json"
	actorKey := ""
	cmd := NewCommand(&natsURL, &outputFmt, &actorKey)

	out, err := runCmd(t, cmd, []string{"list"})
	require.NoError(t, err)
	assert.Contains(t, out, "t1")
	assert.Contains(t, out, "t2")
	assert.Contains(t, out, "active")
	assert.Contains(t, out, "disabled")
}

func TestWeaverList_Empty(t *testing.T) {
	eng := &fakeEngine{errOn: map[string]error{}}
	url := startWeaverControlTest(t, eng)

	natsURL := url
	outputFmt := ""
	actorKey := ""
	cmd := NewCommand(&natsURL, &outputFmt, &actorKey)

	out, err := runCmd(t, cmd, []string{"list"})
	require.NoError(t, err)
	assert.Contains(t, out, "no registered targets")
}

func TestWeaverDisable_HappyPath(t *testing.T) {
	eng := &fakeEngine{errOn: map[string]error{}}
	url := startWeaverControlTest(t, eng)

	natsURL := url
	outputFmt := ""
	actorKey := ""
	cmd := NewCommand(&natsURL, &outputFmt, &actorKey)

	out, err := runCmd(t, cmd, []string{"disable", "t1"})
	require.NoError(t, err)
	assert.Contains(t, out, `target "t1" disabled`)
}

func TestWeaverEnable_HappyPath(t *testing.T) {
	eng := &fakeEngine{errOn: map[string]error{}}
	url := startWeaverControlTest(t, eng)

	natsURL := url
	outputFmt := ""
	actorKey := ""
	cmd := NewCommand(&natsURL, &outputFmt, &actorKey)

	out, err := runCmd(t, cmd, []string{"enable", "t1"})
	require.NoError(t, err)
	assert.Contains(t, out, `target "t1" enabled`)
}

func TestWeaverRevoke_HappyPath(t *testing.T) {
	eng := &fakeEngine{errOn: map[string]error{}}
	url := startWeaverControlTest(t, eng)

	natsURL := url
	outputFmt := ""
	actorKey := ""
	cmd := NewCommand(&natsURL, &outputFmt, &actorKey)

	out, err := runCmd(t, cmd, []string{"revoke", "t1"})
	require.NoError(t, err)
	assert.Contains(t, out, `target "t1" revoked`)
}

func TestWeaverDisable_NotRegistered_JSON(t *testing.T) {
	eng := &fakeEngine{errOn: map[string]error{
		"disable:ghost": errors.New(`weaver: target "ghost" not registered`),
	}}
	url := startWeaverControlTest(t, eng)

	natsURL := url
	outputFmt := "json"
	actorKey := ""
	cmd := NewCommand(&natsURL, &outputFmt, &actorKey)

	out, err := runCmd(t, cmd, []string{"disable", "ghost"})
	require.Error(t, err)
	assert.Contains(t, out, "ghost")
	assert.Contains(t, out, `"ok":false`)
}

// TestWeaverResetConfidence_HappyPath verifies `reset-confidence <targetId>`
// reaches the resetConfidence endpoint and reports the engine's deleted-window
// count — the operator's confirmation the drain found the fossils.
func TestWeaverResetConfidence_HappyPath(t *testing.T) {
	eng := &fakeEngine{errOn: map[string]error{}, resetDeleted: 4}
	url := startWeaverControlTest(t, eng)

	natsURL := url
	outputFmt := ""
	actorKey := ""
	cmd := NewCommand(&natsURL, &outputFmt, &actorKey)

	out, err := runCmd(t, cmd, []string{"reset-confidence", "t1"})
	require.NoError(t, err)
	assert.Contains(t, out, `target "t1" confidence reset`)
	assert.Contains(t, out, "4 window(s) deleted")
}

// TestWeaverResetConfidence_JSONReportsCount pins the machine-readable shape
// operators and Loupe read.
func TestWeaverResetConfidence_JSONReportsCount(t *testing.T) {
	eng := &fakeEngine{errOn: map[string]error{}, resetDeleted: 2}
	url := startWeaverControlTest(t, eng)

	natsURL := url
	outputFmt := "json"
	actorKey := ""
	cmd := NewCommand(&natsURL, &outputFmt, &actorKey)

	out, err := runCmd(t, cmd, []string{"reset-confidence", "t1"})
	require.NoError(t, err)
	assert.Contains(t, out, `"ok":true`)
	assert.Contains(t, out, `"windowsDeleted":2`)
}

// TestWeaverResetConfidence_NotRegistered_JSON verifies an unregistered target
// fails loudly rather than printing a successful zero-window drain.
func TestWeaverResetConfidence_NotRegistered_JSON(t *testing.T) {
	eng := &fakeEngine{errOn: map[string]error{
		"resetConfidence:ghost": errors.New(`weaver: target "ghost" not registered`),
	}}
	url := startWeaverControlTest(t, eng)

	natsURL := url
	outputFmt := "json"
	actorKey := ""
	cmd := NewCommand(&natsURL, &outputFmt, &actorKey)

	out, err := runCmd(t, cmd, []string{"reset-confidence", "ghost"})
	require.Error(t, err)
	assert.Contains(t, out, "ghost")
	assert.Contains(t, out, `"ok":false`)
}

// TestWeaverResetBudget_HappyPath verifies `reset-budget <targetId> <entityId>
// <gapColumn>` reaches the resetBudget endpoint with all three arguments — two
// of which the control subject cannot carry — and reports what the park had
// spent, plus what happens next. A successful reset means "the next sweep pass
// will dispatch it", not "it has dispatched", and the line says so.
func TestWeaverResetBudget_HappyPath(t *testing.T) {
	eng := &fakeEngine{errOn: map[string]error{}, resetPrevious: 3}
	url := startWeaverControlTest(t, eng)

	natsURL := url
	outputFmt := ""
	actorKey := ""
	cmd := NewCommand(&natsURL, &outputFmt, &actorKey)

	const entityID = "entityAAAAAAAAAAAAAA"
	out, err := runCmd(t, cmd, []string{"reset-budget", "t1", entityID, "missing_x"})
	require.NoError(t, err)
	assert.Contains(t, out, "retry budget re-armed (was 3)")
	// The verb re-arms a budget; whether anything dispatches is the sweep's
	// call (the gap may be frozen, its row gone, its target unregistered), so
	// the line must not promise a dispatch it never checked.
	assert.Contains(t, out, "if it is still dispatchable")
	require.Equal(t, []string{"t1/" + entityID + "/missing_x"}, eng.budgetArgs)
}

// TestWeaverResetBudget_JSONReportsPreviousCount pins the machine-readable
// shape operators and Loupe read.
func TestWeaverResetBudget_JSONReportsPreviousCount(t *testing.T) {
	eng := &fakeEngine{errOn: map[string]error{}, resetPrevious: 2}
	url := startWeaverControlTest(t, eng)

	natsURL := url
	outputFmt := "json"
	actorKey := ""
	cmd := NewCommand(&natsURL, &outputFmt, &actorKey)

	out, err := runCmd(t, cmd, []string{"reset-budget", "t1", "entityAAAAAAAAAAAAAA", "missing_x"})
	require.NoError(t, err)
	assert.Contains(t, out, `"ok":true`)
	assert.Contains(t, out, `"previousCount":2`)
}

// TestWeaverResetBudget_RejectsMalformedArgumentsLocally verifies each argument
// shape is refused before a request is published. The weaver-state key this
// names is split positionally, so a dotted or non-NanoID argument would name a
// key nothing can parse — and the round trip would only report the same thing
// less clearly. The engine must not be reached at all.
func TestWeaverResetBudget_RejectsMalformedArgumentsLocally(t *testing.T) {
	for _, tc := range []struct{ name, targetID, entityID, gapColumn, want string }{
		{"dotted targetId", "a.b", "entityAAAAAAAAAAAAAA", "missing_x", "must not contain"},
		{"entityId is not a NanoID", "t1", "nope", "missing_x", "NanoID"},
		{"gapColumn is not a missing_ column", "t1", "entityAAAAAAAAAAAAAA", "violating", "missing_"},
		{"dotted gapColumn", "t1", "entityAAAAAAAAAAAAAA", "missing_a.b", "single token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng := &fakeEngine{errOn: map[string]error{}}
			url := startWeaverControlTest(t, eng)

			natsURL := url
			outputFmt := ""
			actorKey := ""
			cmd := NewCommand(&natsURL, &outputFmt, &actorKey)

			_, err := runCmd(t, cmd, []string{"reset-budget", tc.targetID, tc.entityID, tc.gapColumn})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Empty(t, eng.budgetArgs, "a locally-rejected argument must never reach the engine")
		})
	}
}

// TestWeaverResetBudget_NoBudget_JSON verifies the engine's refusal — a gap
// nothing has ever dispatched has no budget to reset — surfaces as a failed
// command rather than a cheerful zero.
func TestWeaverResetBudget_NoBudget_JSON(t *testing.T) {
	eng := &fakeEngine{errOn: map[string]error{
		"resetBudget:t1": errors.New(`weaver: no retry budget for target "t1" entity "entityAAAAAAAAAAAAAA" gap "missing_x" (nothing has dispatched it)`),
	}}
	url := startWeaverControlTest(t, eng)

	natsURL := url
	outputFmt := "json"
	actorKey := ""
	cmd := NewCommand(&natsURL, &outputFmt, &actorKey)

	out, err := runCmd(t, cmd, []string{"reset-budget", "t1", "entityAAAAAAAAAAAAAA", "missing_x"})
	require.Error(t, err)
	assert.Contains(t, out, "no retry budget")
	assert.Contains(t, out, `"ok":false`)
}

// TestWeaverReplayTarget_HappyPath verifies `replay-target <targetId>` reaches
// the replayTarget endpoint and reports the queued-row count the engine
// returned — how big a re-delivery burst the operator just ordered.
func TestWeaverReplayTarget_HappyPath(t *testing.T) {
	eng := &fakeEngine{errOn: map[string]error{}, replayQueued: 26}
	url := startWeaverControlTest(t, eng)

	natsURL := url
	outputFmt := ""
	actorKey := ""
	cmd := NewCommand(&natsURL, &outputFmt, &actorKey)

	out, err := runCmd(t, cmd, []string{"replay-target", "t1"})
	require.NoError(t, err)
	assert.Contains(t, out, `target "t1" replayed`)
	assert.Contains(t, out, "26 row(s) queued")
}

// TestWeaverReplayTarget_JSONReportsCount pins the machine-readable shape
// operators and scripts read.
func TestWeaverReplayTarget_JSONReportsCount(t *testing.T) {
	eng := &fakeEngine{errOn: map[string]error{}, replayQueued: 7}
	url := startWeaverControlTest(t, eng)

	natsURL := url
	outputFmt := "json"
	actorKey := ""
	cmd := NewCommand(&natsURL, &outputFmt, &actorKey)

	out, err := runCmd(t, cmd, []string{"replay-target", "t1"})
	require.NoError(t, err)
	assert.Contains(t, out, `"ok":true`)
	assert.Contains(t, out, `"rowsQueued":7`)
}

// TestWeaverReplayTarget_Refused_JSON verifies an engine refusal fails loudly
// rather than printing a successful zero-row replay. A replay reported as
// succeeding when nothing was re-delivered leaves the operator's diagnostic
// standing over a fact nothing re-derived, which is the whole reason the verb
// refuses instead of no-opping.
func TestWeaverReplayTarget_Refused_JSON(t *testing.T) {
	eng := &fakeEngine{errOn: map[string]error{
		"replayTarget:ghost": errors.New(`weaver: target "ghost" not registered`),
	}}
	url := startWeaverControlTest(t, eng)

	natsURL := url
	outputFmt := "json"
	actorKey := ""
	cmd := NewCommand(&natsURL, &outputFmt, &actorKey)

	out, err := runCmd(t, cmd, []string{"replay-target", "ghost"})
	require.Error(t, err)
	assert.Contains(t, out, "ghost")
	assert.Contains(t, out, `"ok":false`)
}

// TestWeaverReplayTarget_DottedTargetIDRejectedLocally verifies the local
// targetId shape check refuses a dotted id before a subject is built — the
// control endpoints subscribe a single-token wildcard, so a dotted id would
// otherwise hang to the client timeout with an opaque "no responders".
func TestWeaverReplayTarget_DottedTargetIDRejectedLocally(t *testing.T) {
	eng := &fakeEngine{errOn: map[string]error{}}
	url := startWeaverControlTest(t, eng)

	natsURL := url
	outputFmt := "json"
	actorKey := ""
	cmd := NewCommand(&natsURL, &outputFmt, &actorKey)

	out, err := runCmd(t, cmd, []string{"replay-target", "a.b"})
	require.Error(t, err)
	assert.Contains(t, out, `"ok":false`)
	assert.Contains(t, out, "single dot-free token")
}
