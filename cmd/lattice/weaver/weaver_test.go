package weaver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/testutil"
	internalweaver "github.com/asolgan/lattice/internal/weaver"
	"github.com/asolgan/lattice/internal/weaver/control"
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

// startWeaverControlTest starts an embedded NATS server with a
// weaver-control responder backed by eng, and returns its NATS URL.
func startWeaverControlTest(t *testing.T, eng *fakeEngine) string {
	t.Helper()
	url := testutil.StartEmbeddedNATS(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	conn, err := connectRaw(t, url)
	require.NoError(t, err)

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

	conn, err := connectRaw(t, url)
	require.NoError(t, err)

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

// connectRaw opens a plain *nats.Conn for the control service under test.
func connectRaw(t *testing.T, url string) (*nats.Conn, error) {
	t.Helper()
	return nats.Connect(url)
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
