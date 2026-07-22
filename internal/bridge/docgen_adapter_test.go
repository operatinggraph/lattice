package bridge_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bridge"
	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// The docGen reference vendor adapter proof: it renders the executed-lease text
// from the event's resolved doc fields, ObjectPuts the bytes into the object
// store under the deterministic application-derived store name, and returns the
// document-pointer set as its Detail. The byte-plane write is the external
// side-effect the idempotencyKey dedups (SideEffects == 1 under a double
// Execute); a render with missing required inputs is a terminal OutcomeFailed
// with NO byte write.

const docGenTestBucket = "core-objects"

// startDocGenStore runs an embedded JetStream server with the core-objects
// object store provisioned and returns a substrate conn over it.
func startDocGenStore(t *testing.T) *substrate.Conn {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := natstest.RunServer(opts)
	t.Cleanup(s.Shutdown)
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = conn.JetStream().CreateObjectStore(ctx, jetstream.ObjectStoreConfig{Bucket: docGenTestBucket})
	require.NoError(t, err)
	return conn
}

// docGenRawParams builds the external.docGen event params the instanceOp emits:
// {family, leaseAppKey, doc:{…}}. doc is free-form so tests can drop fields.
func docGenRawParams(t *testing.T, leaseAppKey string, doc map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"family":      "docGen",
		"leaseAppKey": leaseAppKey,
		"doc":         doc,
	})
	require.NoError(t, err)
	return raw
}

// fullDoc is a fully-populated resolved document field set.
func fullDoc() map[string]any {
	return map[string]any{
		"tenantName":           "Alice Smith",
		"applicant":            "vtx.identity.AAliceIdentity123456",
		"unitKey":              "vtx.unit.UUnitNanoID123456789",
		"unitAddress":          "123 Loft St",
		"unitCity":             "San Francisco",
		"unitRegion":           "CA",
		"unitRent":             2400,
		"unitCurrency":         "USD",
		"unitBedrooms":         2,
		"unitBathrooms":        1.5,
		"unitLeaseTermMonths":  12,
		"unitAvailableFrom":    "2026-08-01T00:00:00Z",
		"termsMoveInDate":      "2026-08-01",
		"termsLeaseTermMonths": 12,
		"termsRequestedRent":   2300,
		"signedAt":             "2026-07-01T12:00:00Z",
	}
}

// readStoredDoc fetches the rendered bytes back from the object store.
func readStoredDoc(t *testing.T, conn *substrate.Conn, storeName string) string {
	t.Helper()
	rc, _, err := conn.ObjectGet(context.Background(), docGenTestBucket, storeName)
	require.NoError(t, err)
	defer rc.Close()
	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	return string(b)
}

// TestFakeDocGen_RendersStoresAndReturnsPointer: the happy path — a Resolved
// completed Dispatch whose Detail is the JSON pointer set; the bytes land in
// the store under the deterministic leaseapp-derived name and render the
// resolved fields.
func TestFakeDocGen_RendersStoresAndReturnsPointer(t *testing.T) {
	conn := startDocGenStore(t)
	a := bridge.NewFakeDocGen(conn, docGenTestBucket, 1<<20)

	leaseAppKey := "vtx.leaseapp.LLeaseAppNanoID12345"
	d, err := a.Execute(context.Background(), bridge.Request{
		IdempotencyKey: "docgen-happy-1",
		Subject:        "docgen-happy-1",
		RawParams:      docGenRawParams(t, leaseAppKey, fullDoc()),
	})
	require.NoError(t, err)
	require.Equal(t, bridge.Resolved, d.Disposition, "a synchronous render must resolve inline")
	require.Equal(t, bridge.OutcomeCompleted, d.Result.Status)

	var ptr struct {
		Digest      string `json:"digest"`
		Size        uint64 `json:"size"`
		ContentType string `json:"contentType"`
		StoreName   string `json:"storeName"`
		Filename    string `json:"filename"`
	}
	require.NoError(t, json.Unmarshal([]byte(d.Result.Detail), &ptr),
		"a completed Detail must be the JSON document-pointer object")
	require.NotEmpty(t, ptr.Digest, "the pointer carries the NATS content digest")
	require.NotZero(t, ptr.Size)
	require.Equal(t, "text/plain; charset=utf-8", ptr.ContentType)
	require.Equal(t, substrate.DeriveNanoID("loftspace:lease-doc:store:", leaseAppKey), ptr.StoreName,
		"the store name is deterministic per application (a re-render overwrites, never orphans)")
	require.Equal(t, "signed-lease-leaseapp.LLeaseAp.txt", ptr.Filename)

	content := readStoredDoc(t, conn, ptr.StoreName)
	require.EqualValues(t, ptr.Size, len(content), "the pointer size matches the stored bytes")
	require.Contains(t, content, "RESIDENTIAL LEASE AGREEMENT")
	require.Contains(t, content, "Alice Smith", "the tenant renders by name")
	require.Contains(t, content, "vtx.identity.AAliceIdentity123456", "the tenant id line accompanies a named tenant")
	require.Contains(t, content, "123 Loft St, San Francisco, CA", "the premises join address, city, region")
	require.Contains(t, content, "$2400 (applicant offered 2300)", "the rent notes a differing applicant offer")
	require.Contains(t, content, "12 months", "the lease term renders in months")
	require.Contains(t, content, "2026-08-01", "the move-in date prefers the requested terms")
	require.Contains(t, content, "2026-07-01T12:00:00Z", "the signature date stamps the execution block")
	require.Contains(t, content, leaseAppKey, "the application key is on record")
}

// TestFakeDocGen_RenderDegradesWithoutOptionalFields: an application missing
// the optional fields still renders — by bare applicant key, with the listing
// fallbacks, and without blank-valued lines.
func TestFakeDocGen_RenderDegradesWithoutOptionalFields(t *testing.T) {
	conn := startDocGenStore(t)
	a := bridge.NewFakeDocGen(conn, docGenTestBucket, 1<<20)

	leaseAppKey := "vtx.leaseapp.MMinimalLease1234567"
	d, err := a.Execute(context.Background(), bridge.Request{
		IdempotencyKey: "docgen-minimal-1",
		RawParams: docGenRawParams(t, leaseAppKey, map[string]any{
			"applicant": "vtx.identity.BBareKeyApplicant123",
			"signedAt":  "2026-07-02T09:00:00Z",
		}),
	})
	require.NoError(t, err)
	require.Equal(t, bridge.OutcomeCompleted, d.Result.Status)

	var ptr struct {
		StoreName string `json:"storeName"`
	}
	require.NoError(t, json.Unmarshal([]byte(d.Result.Detail), &ptr))
	content := readStoredDoc(t, conn, ptr.StoreName)
	require.Contains(t, content, "vtx.identity.BBareKeyApplicant123", "an unnamed applicant renders by bare key")
	require.NotContains(t, content, "Tenant ID", "no separate id line when the tenant IS the bare key")
	require.NotContains(t, content, "Premises", "absent fields emit no blank lines")
	require.NotContains(t, content, "Monthly rent")
}

// TestFakeDocGen_IdempotentPerKey: a repeat Execute with the same
// idempotencyKey returns the first Result verbatim and performs NO second
// byte-plane side-effect.
func TestFakeDocGen_IdempotentPerKey(t *testing.T) {
	conn := startDocGenStore(t)
	a := bridge.NewFakeDocGen(conn, docGenTestBucket, 1<<20)

	leaseAppKey := "vtx.leaseapp.IIdemLeaseApp1234567"
	req := bridge.Request{
		IdempotencyKey: "docgen-idem-1",
		RawParams:      docGenRawParams(t, leaseAppKey, fullDoc()),
	}
	first, err := a.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, bridge.OutcomeCompleted, first.Result.Status)
	require.Equal(t, 1, a.SideEffects("docgen-idem-1"))

	second, err := a.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, first.Result, second.Result, "a repeat key returns the first call's Result verbatim")
	require.Equal(t, 1, a.SideEffects("docgen-idem-1"), "no second byte-plane side-effect for a repeat key")
}

// TestFakeDocGen_MissingRequiredInputs_TerminalFailed: no leaseAppKey / no
// signedAt is a definitive business rejection (Resolved OutcomeFailed with the
// reason in Detail, err == nil) and writes nothing to the store.
func TestFakeDocGen_MissingRequiredInputs_TerminalFailed(t *testing.T) {
	conn := startDocGenStore(t)
	a := bridge.NewFakeDocGen(conn, docGenTestBucket, 1<<20)

	t.Run("no leaseAppKey", func(t *testing.T) {
		raw, err := json.Marshal(map[string]any{"family": "docGen", "doc": fullDoc()})
		require.NoError(t, err)
		d, err := a.Execute(context.Background(), bridge.Request{IdempotencyKey: "docgen-nokey-1", RawParams: raw})
		require.NoError(t, err, "a render rejection is a business verdict, not an error")
		require.Equal(t, bridge.Resolved, d.Disposition)
		require.Equal(t, bridge.OutcomeFailed, d.Result.Status)
		require.Contains(t, d.Result.Detail, "leaseAppKey")
		require.Equal(t, 0, a.SideEffects("docgen-nokey-1"), "a failed render performs no byte-plane write")
	})

	t.Run("no signedAt", func(t *testing.T) {
		leaseAppKey := "vtx.leaseapp.UUnsignedLease123456"
		doc := fullDoc()
		delete(doc, "signedAt")
		d, err := a.Execute(context.Background(), bridge.Request{
			IdempotencyKey: "docgen-unsigned-1",
			RawParams:      docGenRawParams(t, leaseAppKey, doc),
		})
		require.NoError(t, err)
		require.Equal(t, bridge.OutcomeFailed, d.Result.Status)
		require.Contains(t, d.Result.Detail, "signedAt")
		require.Equal(t, 0, a.SideEffects("docgen-unsigned-1"))
		_, _, err = conn.ObjectGet(context.Background(), docGenTestBucket,
			substrate.DeriveNanoID("loftspace:lease-doc:store:", leaseAppKey))
		require.Error(t, err, "no bytes may land for a failed render")
	})

	t.Run("failed verdict memoizes", func(t *testing.T) {
		d, err := a.Execute(context.Background(), bridge.Request{IdempotencyKey: "docgen-nokey-1", RawParams: nil})
		require.NoError(t, err)
		require.Equal(t, bridge.OutcomeFailed, d.Result.Status,
			"a repeat key returns the memoized terminal verdict regardless of the replayed params")
	})
}

// TestFakeDocGen_RentFormats: the rent line's currency/offer formatting across
// the resolved-field combinations, proven through the rendered artifact.
func TestFakeDocGen_RentFormats(t *testing.T) {
	conn := startDocGenStore(t)
	a := bridge.NewFakeDocGen(conn, docGenTestBucket, 1<<20)

	cases := []struct {
		name string
		doc  map[string]any
		want string
	}{
		{"usd no offer", map[string]any{"unitRent": 2400, "unitCurrency": "USD"}, "Monthly rent:   $2400\n"},
		{"blank currency", map[string]any{"unitRent": 2400}, "Monthly rent:   $2400\n"},
		{"non-usd", map[string]any{"unitRent": 1500, "unitCurrency": "EUR"}, "Monthly rent:   1500 EUR\n"},
		{"offer differs", map[string]any{"unitRent": 2400, "unitCurrency": "USD", "termsRequestedRent": 2300}, "applicant offered 2300"},
		{"no listing rent, has offer", map[string]any{"termsRequestedRent": 2200}, "2200 (applicant offer)"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaseAppKey := fmt.Sprintf("vtx.leaseapp.RRentCase%011d", i)
			doc := tc.doc
			doc["signedAt"] = "2026-07-01T00:00:00Z"
			d, err := a.Execute(context.Background(), bridge.Request{
				IdempotencyKey: "docgen-rent-" + tc.name,
				RawParams:      docGenRawParams(t, leaseAppKey, doc),
			})
			require.NoError(t, err)
			require.Equal(t, bridge.OutcomeCompleted, d.Result.Status)
			var ptr struct {
				StoreName string `json:"storeName"`
			}
			require.NoError(t, json.Unmarshal([]byte(d.Result.Detail), &ptr))
			require.Contains(t, readStoredDoc(t, conn, ptr.StoreName), tc.want)
		})
	}
}

// TestFakeDocGen_DeterministicRender: the same inputs render byte-identical
// artifacts across distinct calls (distinct idempotency keys), the basis for
// the digest-stable, idempotent attach.
func TestFakeDocGen_DeterministicRender(t *testing.T) {
	conn := startDocGenStore(t)
	a := bridge.NewFakeDocGen(conn, docGenTestBucket, 1<<20)

	leaseAppKey := "vtx.leaseapp.DDeterministic123456"
	run := func(key string) string {
		d, err := a.Execute(context.Background(), bridge.Request{
			IdempotencyKey: key,
			RawParams:      docGenRawParams(t, leaseAppKey, fullDoc()),
		})
		require.NoError(t, err)
		require.Equal(t, bridge.OutcomeCompleted, d.Result.Status)
		var ptr struct {
			Digest string `json:"digest"`
		}
		require.NoError(t, json.Unmarshal([]byte(d.Result.Detail), &ptr))
		return ptr.Digest
	}
	require.Equal(t, run("docgen-det-1"), run("docgen-det-2"),
		"identical inputs must yield the identical digest (a fresh claim re-render overwrites the same content)")
}

// TestFakeDocGen_PollUnsupported: this adapter is synchronous (Execute never
// returns Pending), so Poll must surface a clear error rather than silently
// resolving a ref it never issued.
func TestFakeDocGen_PollUnsupported(t *testing.T) {
	conn := startDocGenStore(t)
	a := bridge.NewFakeDocGen(conn, docGenTestBucket, 1<<20)
	_, err := a.Poll(context.Background(), "some-ref")
	require.Error(t, err, "Poll: want an error for a synchronous adapter")
}
