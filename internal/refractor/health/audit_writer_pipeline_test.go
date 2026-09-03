package health_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/health"
)

// TestAuditWriter_PipelinedEntryIsNotStoredUntilFlush is the discriminating
// assertion for the pipelined audit path: an entry written through a pipeline
// is OUTSTANDING when the call returns and becomes stored only at the flush.
// A WriteAuditPipelined that ignored its pipeline and published synchronously
// would leave nothing pending and pass every content assertion, which is why
// the pending count — not the entry's presence — is what pins the mechanism.
func TestAuditWriter_PipelinedEntryIsNotStoredUntilFlush(t *testing.T) {
	env := startAuditServer(t)
	ctx := context.Background()
	require.NoError(t, health.EnsureAuditStream(ctx, env.conn))

	const ruleID = "rule-pipelined-audit"
	aw := health.NewAuditWriter(env.conn, ruleID)

	pipe := aw.NewPublishPipeline()
	require.Equal(t, 0, pipe.Pending(), "a fresh pipeline holds nothing")

	row := map[string]any{"name": "Alice", "score": float64(42)}
	require.NoError(t, aw.WriteAuditPipelined(ctx, pipe, "entity-pipelined", "upsert", row))
	require.Equal(t, 1, pipe.Pending(),
		"a pipelined audit entry must be outstanding when the call returns — a synchronous publish here means the pipeline is being ignored")

	require.NoError(t, pipe.Flush(ctx))
	assert.Equal(t, 0, pipe.Pending(), "the flush is what awaits it")

	entry := readAuditMsg(t, env.js, ruleID)
	assert.Equal(t, "entity-pipelined", entry.EntityID)
	assert.Equal(t, "upsert", entry.Operation)
	assert.Len(t, entry.OutputRowHash, 64,
		"a pipelined entry carries the same body a synchronous one does")
}

// TestAuditWriter_NilPipelineIsTheSynchronousPath pins the fallback the
// retry-queue replay takes: with no pipeline the entry is stored by the time
// the call returns, exactly as WriteAudit has always behaved.
func TestAuditWriter_NilPipelineIsTheSynchronousPath(t *testing.T) {
	env := startAuditServer(t)
	ctx := context.Background()
	require.NoError(t, health.EnsureAuditStream(ctx, env.conn))

	const ruleID = "rule-nil-pipeline"
	aw := health.NewAuditWriter(env.conn, ruleID)

	require.NoError(t, aw.WriteAuditPipelined(ctx, nil, "entity-sync", "delete", nil))
	assert.Equal(t, 0, env.conn.PublishAsyncPending(),
		"a nil pipeline must take the synchronous path, leaving no ack outstanding")

	entry := readAuditMsg(t, env.js, ruleID)
	assert.Equal(t, "entity-sync", entry.EntityID)
	assert.Equal(t, "delete", entry.Operation)
}
