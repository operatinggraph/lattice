package bootstrap_test

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// newProvisionSeeder returns a Seeder over a fresh embedded NATS, with the
// bootstrap identifier table loaded.
func newProvisionSeeder(t *testing.T, nc *nats.Conn) *bootstrap.Seeder {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	_, err := bootstrap.LoadOrGenerate(t.TempDir() + "/lattice.bootstrap.json")
	require.NoError(t, err)
	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	return seeder
}

// streamConfig reads the live config of a JetStream stream.
func streamConfig(t *testing.T, ctx context.Context, js jetstream.JetStream, name string) jetstream.StreamConfig {
	t.Helper()
	stream, err := js.Stream(ctx, name)
	require.NoError(t, err, "stream %q must exist", name)
	info, err := stream.Info(ctx)
	require.NoError(t, err)
	return info.Config
}

// TestProvisionBuckets_ReprovisionIssuesNoKVStreamWrite pins the guarantee the
// whole read-first posture exists for: a boot against an already-provisioned
// deployment sends the server no stream CREATE and no stream UPDATE for any
// registry bucket.
//
// It is asserted on the JetStream API requests themselves rather than on the
// config afterwards, because the config afterwards is not where the damage is.
// The stream config nats.go derives from a KeyValueConfig carries no
// AllowAtomicPublish, so a CreateOrUpdateKeyValue against an existing core-kv
// or loom-state CLEARS the flag and only a following UpdateStream puts it back:
// between those two round trips every Conn.AtomicBatch against the bucket — each
// Processor commit, each Loom transition — is refused with "atomic publish is
// disabled". A write that is never issued has no such window, and that is what
// this observes.
//
// The subscriptions see the requests because NATS delivers a copy of every
// published message to all matching subscribers, the server's own JetStream API
// subscription included; the AccountInfo round trip after ProvisionBuckets is
// the ordering barrier that makes the drain deterministic.
func TestProvisionBuckets_ReprovisionIssuesNoKVStreamWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("requires embedded NATS")
	}
	nc := startBootstrapNATS(t)
	seeder := newProvisionSeeder(t, nc)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	require.NoError(t, seeder.ProvisionBuckets(ctx), "first ProvisionBuckets must not error")

	creates, err := nc.SubscribeSync("$JS.API.STREAM.CREATE.>")
	require.NoError(t, err)
	updates, err := nc.SubscribeSync("$JS.API.STREAM.UPDATE.>")
	require.NoError(t, err)
	require.NoError(t, nc.Flush())

	require.NoError(t, seeder.ProvisionBuckets(ctx), "re-run ProvisionBuckets must not error")

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	_, err = js.AccountInfo(ctx) // barrier: the reply cannot outrun the copies already queued to this connection
	require.NoError(t, err)

	kvWrites := map[string][]string{}
	for verb, sub := range map[string]*nats.Subscription{"CREATE": creates, "UPDATE": updates} {
		for {
			msg, err := sub.NextMsg(10 * time.Millisecond)
			if err != nil {
				break
			}
			// Only the KV buckets are in scope: the core streams and the
			// core-objects Object Store are provisioned by their own
			// CreateOrUpdate calls, which carry their full config.
			if stream := msg.Subject[strings.LastIndex(msg.Subject, ".")+1:]; strings.HasPrefix(stream, "KV_") {
				kvWrites[stream] = append(kvWrites[stream], verb)
			}
		}
	}
	require.Empty(t, kvWrites,
		"a re-provision of unchanged buckets must write no KV stream config; each write listed here reopens the "+
			"AllowAtomicPublish window on core-kv and loom-state")

	for _, bucket := range []string{bootstrap.CoreKVBucket, bootstrap.LoomStateBucket} {
		require.True(t, streamConfig(t, ctx, js, "KV_"+bucket).AllowAtomicPublish,
			"KV_%s must still allow atomic publish after a re-provision", bucket)
	}
}

// TestProvisionBuckets_AtomicBatchSurvivesConcurrentReprovision drives the real
// writer against the window: a Conn.AtomicBatch loop on core-kv and loom-state
// runs while ProvisionBuckets re-provisions underneath it, exactly as a booting
// binary does against a live deployment. Every batch must commit — a bucket
// whose AllowAtomicPublish is being cleared and restored refuses the batches
// that land in between.
func TestProvisionBuckets_AtomicBatchSurvivesConcurrentReprovision(t *testing.T) {
	if testing.Short() {
		t.Skip("requires embedded NATS")
	}
	nc := startBootstrapNATS(t)
	seeder := newProvisionSeeder(t, nc)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	require.NoError(t, seeder.ProvisionBuckets(ctx))

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	const reprovisions = 20
	writerCtx, stopWriters := context.WithCancel(ctx)
	defer stopWriters()

	var (
		mu       sync.Mutex
		failures []error
		commits  int
	)
	var writers sync.WaitGroup
	for _, bucket := range []string{bootstrap.CoreKVBucket, bootstrap.LoomStateBucket} {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for i := 0; writerCtx.Err() == nil; i++ {
				_, err := conn.AtomicBatch(writerCtx, []substrate.BatchOp{{
					Bucket: bucket,
					Key:    "provisionprobe.batch",
					Value:  []byte(`{"probe":true}`),
				}})
				mu.Lock()
				switch {
				case err == nil:
					commits++
				case writerCtx.Err() != nil: // the batch raced the shutdown, not the re-provision
				default:
					failures = append(failures, err)
				}
				mu.Unlock()
			}
		}()
	}

	for i := 0; i < reprovisions; i++ {
		require.NoError(t, seeder.ProvisionBuckets(ctx), "re-provision %d must not error", i)
	}
	stopWriters()
	writers.Wait()

	mu.Lock()
	defer mu.Unlock()
	require.Empty(t, failures,
		"every atomic batch must commit across a re-provision; a rejection here is the AllowAtomicPublish window")
	require.NotZero(t, commits, "the writers must actually have committed batches for this to prove anything")
}

// TestProvisionBuckets_ConvergesADivergentBucket proves the read-first posture
// skips only what already agrees with the registry: a bucket whose live marker
// lifetime and description differ from its row is still brought to the row's
// values, and the atomic-publish flag the update clears is restored in the same
// pass.
func TestProvisionBuckets_ConvergesADivergentBucket(t *testing.T) {
	if testing.Short() {
		t.Skip("requires embedded NATS")
	}
	nc := startBootstrapNATS(t)
	seeder := newProvisionSeeder(t, nc)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	// loom-state below its row's hour and under a stale description; weaver-state
	// above its row's floor. The two divergences bracket the registry value, so
	// neither direction can be frozen unnoticed.
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         bootstrap.LoomStateBucket,
		Description:    "stale description",
		LimitMarkerTTL: bootstrap.MinMarkerTTL,
	})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         bootstrap.WeaverStateBucket,
		LimitMarkerTTL: bootstrap.LoomStateMarkerTTL,
	})
	require.NoError(t, err)

	require.NoError(t, seeder.ProvisionBuckets(ctx))

	rows := map[string]bootstrap.PlatformBucket{}
	for _, b := range bootstrap.PlatformBuckets() {
		rows[b.Name] = b
	}

	loom := streamConfig(t, ctx, js, "KV_"+bootstrap.LoomStateBucket)
	require.Equal(t, rows[bootstrap.LoomStateBucket].MarkerTTL, loom.SubjectDeleteMarkerTTL,
		"a divergent marker lifetime must be converged to the registry row, not frozen by the skip")
	require.Equal(t, rows[bootstrap.LoomStateBucket].Description, loom.Description,
		"a divergent description must be converged to the registry row")
	require.True(t, loom.AllowAtomicPublish,
		"the update that converges loom-state clears AllowAtomicPublish, so the same pass must put it back")

	weaver := streamConfig(t, ctx, js, "KV_"+bootstrap.WeaverStateBucket)
	require.Equal(t, rows[bootstrap.WeaverStateBucket].MarkerTTL, weaver.SubjectDeleteMarkerTTL,
		"a marker lifetime longer than the registry row's must be converged down to it")
}
