package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/appsession"
	"github.com/operatinggraph/lattice/internal/processor"
)

// withSession returns r carrying identityID as its resolved session — what
// RequireSession installs before any handler runs, so a handler test can
// exercise the handler alone. viaCookie is true: these tests cover a real
// signed-in session, the only kind the credential surfaces serve.
func withSession(r *http.Request, identityID string) *http.Request {
	return r.WithContext(appsession.WithSession(r.Context(), identityID, true))
}

// withBootSession is the boot-env fallback: an identity resolved from the
// process's own env, proven by no cookie at all.
func withBootSession(r *http.Request, identityID string) *http.Request {
	return r.WithContext(appsession.WithSession(r.Context(), identityID, false))
}

// TestCredentialSurfaces_RefuseTheBootFallbackSession — credentialBinding is
// a SENSITIVE aspect, so every credential surface requires a caller who
// PROVED which identity they are. The boot-env fallback proves nothing (it
// hands the process's identity to whoever connects), so an off-loopback boot
// deployment must not expose, or let anyone mutate, its bound credentials.
func TestCredentialSurfaces_RefuseTheBootFallbackSession(t *testing.T) {
	const id = "tenantnano0123456789"
	srv := &server{logger: slog.Default(), devSigner: testDevSigner(t)}

	w := httptest.NewRecorder()
	srv.handleCredentials(w, withBootSession(httptest.NewRequest(http.MethodGet, "/api/credentials", nil), id))
	require.Equal(t, http.StatusForbidden, w.Code)

	w = httptest.NewRecorder()
	srv.handleCredentialsLink(w, withBootSession(httptest.NewRequest(http.MethodPost, "/api/credentials/link", nil), id))
	require.Equal(t, http.StatusForbidden, w.Code)

	w = httptest.NewRecorder()
	srv.handleCredentialsUnlink(w, withBootSession(
		httptest.NewRequest(http.MethodPost, "/api/credentials/unlink", strings.NewReader(`{"credentialActorKey":"vtx.identity.x"}`)), id))
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestHandleCredentials_RequiresSession(t *testing.T) {
	srv := &server{logger: slog.Default()}
	w := httptest.NewRecorder()
	srv.handleCredentials(w, httptest.NewRequest(http.MethodGet, "/api/credentials", nil))
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestHandleCredentials_ReportsUnconfiguredReadModel(t *testing.T) {
	srv := &server{logger: slog.Default()}
	w := httptest.NewRecorder()
	r := withSession(httptest.NewRequest(http.MethodGet, "/api/credentials", nil), "tenantnano0123456789")
	srv.handleCredentials(w, r)
	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Contains(t, w.Body.String(), "FACET_PG_DSN")
}

func TestCredentialBindingData_Entries(t *testing.T) {
	// The N-credential array wins when present.
	d := credentialBindingData{
		ActorKey:    "vtx.identity.first0000000000000",
		BoundAt:     "2026-07-01T00:00:00Z",
		Credentials: []credentialEntry{{ActorKey: "vtx.identity.a1", BoundAt: "t1"}, {ActorKey: "vtx.identity.a2", BoundAt: "t2"}},
	}
	require.Len(t, d.entries(), 2)

	// A pre-Fire-2 record (no array) folds its singular fields into one entry
	// — mirrors the Starlark script's own read-side fallback.
	legacy := credentialBindingData{ActorKey: "vtx.identity.only0", BoundAt: "t0"}
	require.Equal(t, []credentialEntry{{ActorKey: "vtx.identity.only0", BoundAt: "t0"}}, legacy.entries())

	// An identity that never claimed projects nothing rather than a
	// phantom empty-key entry.
	require.Nil(t, credentialBindingData{}.entries())
}

func TestHandleCredentialsLink_RequiresSessionAndDevSigner(t *testing.T) {
	// No dev signer: the whole surface is disabled, same fail-closed default
	// as /api/claim.
	srv := &server{logger: slog.Default()}
	w := httptest.NewRecorder()
	r := withSession(httptest.NewRequest(http.MethodPost, "/api/credentials/link", nil), "tenantnano0123456789")
	srv.handleCredentialsLink(w, r)
	require.Equal(t, http.StatusNotFound, w.Code)

	// Signer present but no session: still refused.
	srv = &server{logger: slog.Default(), devSigner: testDevSigner(t)}
	w = httptest.NewRecorder()
	srv.handleCredentialsLink(w, httptest.NewRequest(http.MethodPost, "/api/credentials/link", nil))
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestHandleCredentialsLink_RunsInitiateThenCompleteAsDistinctActors is the
// load-bearing one: the link ceremony's whole security shape is that the two
// ops are submitted by DIFFERENT credentials — Initiate as U itself
// (scope=self), Complete as a brand-new throwaway A2 proving the secret
// (the raw-credential carve-out). Submitting both as U would be a silent
// downgrade that still "works" against a permissive fake.
func TestHandleCredentialsLink_RunsInitiateThenCompleteAsDistinctActors(t *testing.T) {
	type submitted struct {
		auth string
		env  struct {
			OperationType string                 `json:"operationType"`
			Class         string                 `json:"class"`
			Payload       json.RawMessage        `json:"payload"`
			Reads         []string               `json:"reads"`
			OptionalReads []string               `json:"optionalReads"`
			AuthContext   *processor.AuthContext `json:"authContext"`
		}
	}
	var calls []submitted
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var s submitted
		s.auth = r.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&s.env))
		calls = append(calls, s)
		_ = json.NewEncoder(w).Encode(processor.OperationReply{Status: processor.ReplyStatusAccepted})
	}))
	defer gw.Close()

	const uID = "tenantnano0123456789"
	srv := &server{logger: slog.Default(), devSigner: testDevSigner(t), gatewayURL: gw.URL}
	w := httptest.NewRecorder()
	r := withSession(httptest.NewRequest(http.MethodPost, "/api/credentials/link", nil), uID)
	srv.handleCredentialsLink(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, calls, 2)

	initiate, complete := calls[0], calls[1]
	uKey := "vtx.identity." + uID

	require.Equal(t, "InitiateCredentialLink", initiate.env.OperationType)
	require.Equal(t, "identity", initiate.env.Class)
	require.Equal(t, uKey, initiate.env.AuthContext.Target, "Initiate must be submitted as U itself (scope=self)")
	require.Contains(t, initiate.env.Reads, uKey)
	require.Contains(t, initiate.env.Reads, uKey+".state")

	require.Equal(t, "CompleteCredentialLink", complete.env.OperationType)
	require.NotEqual(t, uKey, complete.env.AuthContext.Target,
		"Complete must be submitted as the NEW throwaway credential A2, never as U")
	require.True(t, strings.HasPrefix(complete.env.AuthContext.Target, "vtx.identity."))
	require.NotEqual(t, initiate.auth, complete.auth, "the two ops must ride different bearer credentials")

	// The plaintext secret goes only to Complete; only its hash was ever
	// armed by Initiate — Lattice never holds the plaintext (design §3.2).
	var initiatePayload struct {
		LinkKeyHash string `json:"linkKeyHash"`
	}
	require.NoError(t, json.Unmarshal(initiate.env.Payload, &initiatePayload))
	require.Len(t, initiatePayload.LinkKeyHash, 64, "linkKeyHash must be a 64-char hex sha256")
	require.Equal(t, strings.ToLower(initiatePayload.LinkKeyHash), initiatePayload.LinkKeyHash)

	var completePayload struct {
		TargetIdentityKey string `json:"targetIdentityKey"`
		LinkKey           string `json:"linkKey"`
	}
	require.NoError(t, json.Unmarshal(complete.env.Payload, &completePayload))
	require.Equal(t, uKey, completePayload.TargetIdentityKey)
	require.NotEmpty(t, completePayload.LinkKey)
	require.NotEqual(t, initiatePayload.LinkKeyHash, completePayload.LinkKey,
		"the armed hash and the proved plaintext must not be the same value")

	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, complete.env.AuthContext.Target, resp["linkedCredentialKey"])
}

// TestHandleCredentialsLink_StopsWhenInitiateRejected proves the ceremony
// fails closed: a rejected Initiate must not go on to submit Complete.
func TestHandleCredentialsLink_StopsWhenInitiateRejected(t *testing.T) {
	var calls int
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(processor.OperationReply{
			Status: processor.ReplyStatusRejected,
			Error:  &processor.ReplyError{Code: processor.ErrCodeAuthDenied, Message: "denied"},
		})
	}))
	defer gw.Close()

	srv := &server{logger: slog.Default(), devSigner: testDevSigner(t), gatewayURL: gw.URL}
	w := httptest.NewRecorder()
	r := withSession(httptest.NewRequest(http.MethodPost, "/api/credentials/link", nil), "tenantnano0123456789")
	srv.handleCredentialsLink(w, r)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.Equal(t, 1, calls, "a rejected Initiate must not proceed to Complete")
}

func TestHandleCredentialsUnlink_SubmitsAsSelf(t *testing.T) {
	var gotEnv struct {
		OperationType string                 `json:"operationType"`
		Class         string                 `json:"class"`
		Payload       json.RawMessage        `json:"payload"`
		Reads         []string               `json:"reads"`
		OptionalReads []string               `json:"optionalReads"`
		AuthContext   *processor.AuthContext `json:"authContext"`
	}
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotEnv))
		_ = json.NewEncoder(w).Encode(processor.OperationReply{Status: processor.ReplyStatusAccepted})
	}))
	defer gw.Close()

	const uID = "tenantnano0123456789"
	srv := &server{logger: slog.Default(), devSigner: testDevSigner(t), gatewayURL: gw.URL}
	w := httptest.NewRecorder()
	r := withSession(
		httptest.NewRequest(http.MethodPost, "/api/credentials/unlink",
			strings.NewReader(`{"credentialActorKey":"vtx.identity.oldcred000000000000"}`)),
		uID)
	srv.handleCredentialsUnlink(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "UnlinkCredential", gotEnv.OperationType)
	require.Equal(t, "identity", gotEnv.Class)
	require.Equal(t, "vtx.identity."+uID, gotEnv.AuthContext.Target)

	var payload struct {
		CredentialActorKey string `json:"credentialActorKey"`
	}
	require.NoError(t, json.Unmarshal(gotEnv.Payload, &payload))
	require.Equal(t, "vtx.identity.oldcred000000000000", payload.CredentialActorKey)
	require.Contains(t, gotEnv.OptionalReads, "vtx.identity."+uID+".credentialBinding")
}

// TestHandleCredentialsUnlink_TargetsOnlyTheSession proves the handler never
// lets a caller name WHICH identity to unlink from: the target is always the
// session's own key, so a caller cannot strip another identity's credential
// by passing someone else's id.
func TestHandleCredentialsUnlink_TargetsOnlyTheSession(t *testing.T) {
	var gotEnv struct {
		AuthContext *processor.AuthContext `json:"authContext"`
	}
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotEnv))
		_ = json.NewEncoder(w).Encode(processor.OperationReply{Status: processor.ReplyStatusAccepted})
	}))
	defer gw.Close()

	srv := &server{logger: slog.Default(), devSigner: testDevSigner(t), gatewayURL: gw.URL}
	w := httptest.NewRecorder()
	// The body names a victim identity; the session is someone else.
	r := withSession(
		httptest.NewRequest(http.MethodPost, "/api/credentials/unlink",
			strings.NewReader(`{"credentialActorKey":"vtx.identity.victimcred000000000","identityId":"victimnano0123456789"}`)),
		"tenantnano0123456789")
	srv.handleCredentialsUnlink(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "vtx.identity.tenantnano0123456789", gotEnv.AuthContext.Target,
		"the unlink target must come from the session, never from the request body")
}

func TestHandleCredentialsUnlink_RequiresCredentialActorKey(t *testing.T) {
	srv := &server{logger: slog.Default(), devSigner: testDevSigner(t)}
	w := httptest.NewRecorder()
	r := withSession(httptest.NewRequest(http.MethodPost, "/api/credentials/unlink", strings.NewReader(`{}`)), "tenantnano0123456789")
	srv.handleCredentialsUnlink(w, r)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestMintLinkSecret_IsRandomAndHashMatches(t *testing.T) {
	p1, h1, err := mintLinkSecret()
	require.NoError(t, err)
	p2, _, err := mintLinkSecret()
	require.NoError(t, err)
	require.NotEqual(t, p1, p2, "every link secret must be freshly random")
	require.Len(t, h1, 64)
}
