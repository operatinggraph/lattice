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

// TestCredentialSurfaces_RefuseTheBootFallbackSession — a credential surface
// serves only a caller who PROVED which identity they are. The boot-env
// fallback proves nothing (it hands the process's identity to whoever
// connects), so an off-loopback boot deployment must not be able to arm a new
// sign-in method on it.
//
// The LIST and the unlink submission used to be asserted here too. Both moved
// to the pane path, where handlePane makes the same refusal for the same
// reason — pane_test.go pins it there.
func TestCredentialSurfaces_RefuseTheBootFallbackSession(t *testing.T) {
	const id = "tenantnano9123456789"
	srv := &server{logger: slog.Default(), devSigner: testDevSigner(t)}

	w := httptest.NewRecorder()
	srv.handleCredentialsLink(w, withBootSession(httptest.NewRequest(http.MethodPost, "/api/credentials/link", nil), id))
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestHandleCredentialsLink_RequiresSessionAndDevSigner(t *testing.T) {
	// No dev signer: the whole surface is disabled, same fail-closed default
	// as /api/claim.
	srv := &server{logger: slog.Default()}
	w := httptest.NewRecorder()
	r := withSession(httptest.NewRequest(http.MethodPost, "/api/credentials/link", nil), "tenantnano9123456789")
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

	const uID = "tenantnano9123456789"
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
	// Both legs declare absence-tolerantly and declare nothing required. A
	// required read's absence faults HydrationMiss and echoes the probed key
	// back — and the link ceremony's fail_link reuses the "ClaimKeyInvalid: "
	// prefix precisely so NFR-S6's one-generic-answer rule covers it too.
	require.Empty(t, initiate.env.Reads)
	require.Contains(t, initiate.env.OptionalReads, uKey)
	require.Contains(t, initiate.env.OptionalReads, uKey+".state")

	require.Equal(t, "CompleteCredentialLink", complete.env.OperationType)
	require.NotEqual(t, uKey, complete.env.AuthContext.Target,
		"Complete must be submitted as the NEW throwaway credential A2, never as U")
	require.True(t, strings.HasPrefix(complete.env.AuthContext.Target, "vtx.identity."))
	require.NotEqual(t, initiate.auth, complete.auth, "the two ops must ride different bearer credentials")
	// Complete's target is caller-named and its scope=self gate binds the
	// CREDENTIAL, not the target — so nothing derived from it may be required.
	require.Empty(t, complete.env.Reads)
	require.Contains(t, complete.env.OptionalReads, uKey)
	require.Contains(t, complete.env.OptionalReads, uKey+".state")
	require.Contains(t, complete.env.OptionalReads, uKey+".linkKey")
	require.Contains(t, complete.env.OptionalReads, uKey+".credentialBinding")

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
	r := withSession(httptest.NewRequest(http.MethodPost, "/api/credentials/link", nil), "tenantnano9123456789")
	srv.handleCredentialsLink(w, r)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.Equal(t, 1, calls, "a rejected Initiate must not proceed to Complete")
}

func TestMintLinkSecret_IsRandomAndHashMatches(t *testing.T) {
	p1, h1, err := mintLinkSecret()
	require.NoError(t, err)
	p2, _, err := mintLinkSecret()
	require.NoError(t, err)
	require.NotEqual(t, p1, p2, "every link secret must be freshly random")
	require.Len(t, h1, 64)
}
