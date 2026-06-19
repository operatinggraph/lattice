package loom

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

	internalloom "github.com/asolgan/lattice/internal/loom"
	"github.com/asolgan/lattice/internal/loom/control"
	"github.com/asolgan/lattice/internal/testutil"
)

// fakeEngine satisfies the control package's unexported engineControl interface
// structurally, letting this package's tests drive a real loom-control NATS
// responder without a *loom.Engine.
type fakeEngine struct {
	instances []internalloom.InstanceSummary
	consumers []internalloom.ConsumerStatus
	detail    map[string]internalloom.InstanceDetail
	errOn     map[string]error
	pauseNote string // advisory note PauseConsumer returns on success
}

func (f *fakeEngine) ListInstances(_ context.Context) ([]internalloom.InstanceSummary, error) {
	return f.instances, f.errOn["list"]
}

func (f *fakeEngine) ListConsumers(_ context.Context) ([]internalloom.ConsumerStatus, error) {
	return f.consumers, f.errOn["consumers"]
}

func (f *fakeEngine) InspectInstance(_ context.Context, instanceID string) (internalloom.InstanceDetail, error) {
	if err := f.errOn["inspect:"+instanceID]; err != nil {
		return internalloom.InstanceDetail{}, err
	}
	return f.detail[instanceID], nil
}

func (f *fakeEngine) PauseConsumer(_ context.Context, name string) (string, error) {
	if err := f.errOn["pause:"+name]; err != nil {
		return "", err
	}
	return f.pauseNote, nil
}

func (f *fakeEngine) ResumeConsumer(_ context.Context, name string) error {
	return f.errOn["resume:"+name]
}

// startLoomControlTest starts an embedded NATS server with a loom-control
// responder backed by eng, and returns its NATS URL.
func startLoomControlTest(t *testing.T, eng *fakeEngine) string {
	t.Helper()
	url := testutil.StartEmbeddedNATS(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	conn, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	svc := control.NewService(eng, nil, testutil.TestLogger())
	require.NoError(t, svc.StartNATSListener(ctx, conn))

	return url
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

func newFakeEngine() *fakeEngine {
	return &fakeEngine{detail: map[string]internalloom.InstanceDetail{}, errOn: map[string]error{}}
}

func TestLoomList_HappyPath_JSON(t *testing.T) {
	eng := newFakeEngine()
	eng.instances = []internalloom.InstanceSummary{
		{InstanceID: "i1", PatternRef: "vtx.meta.p1", SubjectKey: "vtx.widget.w1", Cursor: 0, Status: "running"},
		{InstanceID: "i2", PatternRef: "vtx.meta.p2", SubjectKey: "vtx.widget.w2", Cursor: 2, Status: "complete"},
	}
	url := startLoomControlTest(t, eng)

	natsURL := url
	outputFmt := "json"
	cmd := NewCommand(&natsURL, &outputFmt)

	out, err := runCmd(t, cmd, []string{"list"})
	require.NoError(t, err)
	assert.Contains(t, out, "i1")
	assert.Contains(t, out, "i2")
	assert.Contains(t, out, "running")
	assert.Contains(t, out, "complete")
}

func TestLoomList_Empty_Table(t *testing.T) {
	eng := newFakeEngine()
	url := startLoomControlTest(t, eng)

	natsURL := url
	outputFmt := ""
	cmd := NewCommand(&natsURL, &outputFmt)

	out, err := runCmd(t, cmd, []string{"list"})
	require.NoError(t, err)
	assert.Contains(t, out, "no instances")
}

func TestLoomConsumers_HappyPath_Table(t *testing.T) {
	eng := newFakeEngine()
	eng.consumers = []internalloom.ConsumerStatus{
		{Name: "loom-trigger", State: "running"},
		{Name: "loom-widget", State: "pausedManual"},
	}
	url := startLoomControlTest(t, eng)

	natsURL := url
	outputFmt := ""
	cmd := NewCommand(&natsURL, &outputFmt)

	out, err := runCmd(t, cmd, []string{"consumers"})
	require.NoError(t, err)
	assert.Contains(t, out, "loom-trigger")
	assert.Contains(t, out, "loom-widget")
	assert.Contains(t, out, "pausedManual")
}

func TestLoomInspect_Running_Table(t *testing.T) {
	eng := newFakeEngine()
	eng.detail["i1"] = internalloom.InstanceDetail{
		Instance:    internalloom.InstanceSummary{InstanceID: "i1", Status: "running", Cursor: 1, PatternRef: "vtx.meta.p1"},
		CurrentStep: &internalloom.Step{Kind: "userTask", Operation: "ApproveThing"},
		Terminal:    false,
	}
	url := startLoomControlTest(t, eng)

	natsURL := url
	outputFmt := ""
	cmd := NewCommand(&natsURL, &outputFmt)

	out, err := runCmd(t, cmd, []string{"inspect", "i1"})
	require.NoError(t, err)
	assert.Contains(t, out, "i1")
	assert.Contains(t, out, "running")
	assert.Contains(t, out, "userTask")
	assert.Contains(t, out, "ApproveThing")
}

func TestLoomInspect_Terminal_Table(t *testing.T) {
	eng := newFakeEngine()
	eng.detail["done1"] = internalloom.InstanceDetail{
		Instance:    internalloom.InstanceSummary{InstanceID: "done1", Status: "complete", Cursor: 2},
		CurrentStep: nil,
		Terminal:    true,
	}
	url := startLoomControlTest(t, eng)

	natsURL := url
	outputFmt := ""
	cmd := NewCommand(&natsURL, &outputFmt)

	out, err := runCmd(t, cmd, []string{"inspect", "done1"})
	require.NoError(t, err)
	assert.Contains(t, out, "terminal:    true")
	assert.Contains(t, out, "currentStep: (none)")
}

func TestLoomPause_HappyPath_Table(t *testing.T) {
	eng := newFakeEngine()
	// The engine composes the advisory note (persist-across-restart + the
	// per-domain stall warning); the CLI renders whatever it returns.
	eng.pauseNote = "manual pause persists across restart until resume; in-flight instances awaiting this domain will stall until resume"
	url := startLoomControlTest(t, eng)

	natsURL := url
	outputFmt := ""
	cmd := NewCommand(&natsURL, &outputFmt)

	out, err := runCmd(t, cmd, []string{"pause", "loom-widget"})
	require.NoError(t, err)
	assert.Contains(t, out, `consumer "loom-widget" paused`)
	// C7: the CLI pause output notes the across-restart persistence.
	assert.Contains(t, out, "persists across restart until resume")
	// The per-domain stall warning is surfaced to the operator too.
	assert.Contains(t, out, "stall until resume")
}

func TestLoomResume_HappyPath_Table(t *testing.T) {
	eng := newFakeEngine()
	url := startLoomControlTest(t, eng)

	natsURL := url
	outputFmt := ""
	cmd := NewCommand(&natsURL, &outputFmt)

	out, err := runCmd(t, cmd, []string{"resume", "loom-widget"})
	require.NoError(t, err)
	assert.Contains(t, out, `consumer "loom-widget" resumed`)
}

func TestLoomPause_NotManaged_JSON(t *testing.T) {
	eng := newFakeEngine()
	eng.errOn["pause:ghost"] = errors.New(`loom: consumer not managed: "ghost"`)
	url := startLoomControlTest(t, eng)

	natsURL := url
	outputFmt := "json"
	cmd := NewCommand(&natsURL, &outputFmt)

	out, err := runCmd(t, cmd, []string{"pause", "ghost"})
	require.Error(t, err)
	assert.Contains(t, out, "ghost")
	assert.Contains(t, out, `"ok":false`)
}

func TestLoomPause_DottedName_Rejected(t *testing.T) {
	eng := newFakeEngine()
	url := startLoomControlTest(t, eng)

	natsURL := url
	outputFmt := ""
	cmd := NewCommand(&natsURL, &outputFmt)

	// A dotted name is rejected client-side before any request (it would build a
	// subject no endpoint matches).
	_, err := runCmd(t, cmd, []string{"pause", "loom.widget"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain '.'")
}

func TestLoomInspect_TypedError_JSON(t *testing.T) {
	eng := newFakeEngine()
	eng.errOn["inspect:nopin"] = errors.New("pattern pin missing for live instance (pin is written atomically with the instance)")
	url := startLoomControlTest(t, eng)

	natsURL := url
	outputFmt := "json"
	cmd := NewCommand(&natsURL, &outputFmt)

	out, err := runCmd(t, cmd, []string{"inspect", "nopin"})
	require.Error(t, err)
	assert.Contains(t, out, `"ok":false`)
	assert.Contains(t, out, "pattern pin missing")
}
