package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFeedPublishRevoked_IsStickyAndReplayed proves the §4.4 sign-out signal
// survives the case that actually matters: the drain loop discovers the dead
// credential on its own schedule, which is almost never while a browser
// happens to be listening. A browser connecting afterwards must still be
// told, so the reason is sticky and replayed to every later subscriber.
func TestFeedPublishRevoked_IsStickyAndReplayed(t *testing.T) {
	fd := newFeed()
	_, ok := fd.revoked()
	require.False(t, ok)

	sub := fd.subscribe()
	fd.publishRevoked("Your session is no longer valid.")

	select {
	case fr := <-sub:
		require.Equal(t, "revoked", fr.Kind)
		require.Equal(t, "Your session is no longer valid.", fr.Reason)
	default:
		t.Fatal("expected a revoked frame on the live subscriber")
	}

	reason, ok := fd.revoked()
	require.True(t, ok)
	require.Equal(t, "Your session is no longer valid.", reason)
}

// TestFeedPublishRevoked_Idempotent — the drain loop re-hits the same
// rejection every tick while intents stay queued; only the first publishes,
// so a revoked session doesn't spray a frame every 5s forever.
func TestFeedPublishRevoked_Idempotent(t *testing.T) {
	fd := newFeed()
	sub := fd.subscribe()

	fd.publishRevoked("first")
	fd.publishRevoked("second")
	fd.publishRevoked("third")

	require.Len(t, sub, 1, "only the first revocation should publish a frame")
	fr := <-sub
	require.Equal(t, "first", fr.Reason)

	reason, ok := fd.revoked()
	require.True(t, ok)
	require.Equal(t, "first", reason, "the sticky reason must not be overwritten by later ticks")
}

// TestRevokedFrame_SerializesReason guards the wire contract the browser's
// EventSource listener reads (app.js's "revoked" handler).
func TestRevokedFrame_SerializesReason(t *testing.T) {
	b, err := json.Marshal(frame{Kind: "revoked", Reason: "gone"})
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))
	require.Equal(t, "gone", got["reason"])
	// Kind rides the SSE `event:` line, never the JSON body.
	require.NotContains(t, got, "Kind")
}

// TestEngineManager_PurgeDeletesTheLocalMirror is design §4.4's "on
// confirmed revocation/sign-out the local mirror is purged" at the only
// level that means anything: the bbolt file on disk. Clearing the browser's
// render buffer is not a purge — writeSSE replays the whole snapshot on the
// next connect — so this is the assertion that keeps that distinction
// honest.
func TestEngineManager_PurgeDeletesTheLocalMirror(t *testing.T) {
	dir := t.TempDir()
	m := &engineManager{
		entries: make(map[string]*engineEntry),
		deps:    engineManagerDeps{engineConfig: engineConfig{StoreDir: dir}},
	}

	id := testNanoID(t)
	mirror := filepath.Join(dir, id+".db")
	require.NoError(t, os.WriteFile(mirror, []byte("local mirror contents"), 0o600))

	require.NoError(t, m.Purge(id))
	_, err := os.Stat(mirror)
	require.True(t, os.IsNotExist(err), "the identity's local mirror must be gone after a purge")
}

// TestEngineManager_PurgeIsCleanWhenNothingToPurge — a purge must never fail
// the sign-out it is part of. An already-reaped engine (mirror still on
// disk, no map entry) and a never-existed identity both purge cleanly.
func TestEngineManager_PurgeIsCleanWhenNothingToPurge(t *testing.T) {
	dir := t.TempDir()
	m := &engineManager{
		entries: make(map[string]*engineEntry),
		deps:    engineManagerDeps{engineConfig: engineConfig{StoreDir: dir}},
	}
	require.NoError(t, m.Purge(testNanoID(t)))
}

// TestEngineManager_PurgeRefusesPathTraversal — identityID is spliced into a
// filename and handed to os.Remove, and filepath.Join CLEANS rather than
// neutralizes "..", so an unvalidated id escapes StoreDir entirely
// (filepath.Join("/store", "../../../../etc/x.db") == "/etc/x.db"). The
// login path already refuses a non-NanoID subject; this pins the SINK
// refusing on its own, so no future caller can reintroduce the traversal.
func TestEngineManager_PurgeRefusesPathTraversal(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside.db")
	require.NoError(t, os.WriteFile(outside, []byte("must survive"), 0o600))

	storeDir := filepath.Join(dir, "store")
	require.NoError(t, os.MkdirAll(storeDir, 0o755))
	m := &engineManager{
		entries: make(map[string]*engineEntry),
		deps:    engineManagerDeps{engineConfig: engineConfig{StoreDir: storeDir}},
	}

	for _, evil := range []string{"../outside", "../../etc/passwd", "", "not-a-nanoid", "/absolute/path"} {
		require.Error(t, m.Purge(evil), "id=%q must be refused", evil)
	}
	_, err := os.Stat(outside)
	require.NoError(t, err, "a file outside StoreDir must never be deleted by a purge")
}
