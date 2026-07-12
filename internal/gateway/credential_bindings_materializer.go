package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/gateway/credentialbinding"
	"github.com/asolgan/lattice/internal/substrate"
)

// credentialBindingsConsumerName is the durable name of the Gateway's own
// events.identity.> materializer for the claim-flow shared-seam amendment
// (gateway-claim-flow-identity-provisioning-design.md §11.0/§11.5 R1).
const credentialBindingsConsumerName = "gateway-credential-bindings"

// credentialBindingsFilterSubject also delivers sibling identity-domain
// events (e.g. identity.provisioned) — the handler ignores anything but a
// claim.
const credentialBindingsFilterSubject = "events.identity.>"

// credentialBindingsCatchUpTimeout mirrors revocationCatchUpTimeout: a slow
// drain is an infra hiccup the durable self-heals from, not a wiring
// failure.
const credentialBindingsCatchUpTimeout = 15 * time.Second

// credentialBindingsIssueKey is the Contract #5 issue key the materializer's
// pause state is surfaced under.
const credentialBindingsIssueKey = "credential-bindings-consumer"

// credentialBindingsPoisonIssueKey is the Contract #5 issue key a dropped,
// never-redelivered claim event is surfaced under.
const credentialBindingsPoisonIssueKey = "credential-bindings-poison-key"

// credentialBindingEventBody is the shape the outbox publishes for
// identity.claimed (packages/identity-domain/ddls.go's ClaimIdentity),
// identity.rebound (packages/identity-hygiene/ddls.go's MergeIdentity,
// multi-credential-identity-linking-design.md §3.3), and identity.unbound
// (packages/identity-domain/ddls.go's UnlinkCredential, design §8) — all
// three carry the same {identityKey, actorKey} shape; rebound's extra
// previousIdentityKey field is audit-only and unused by the fold.
type credentialBindingEventBody struct {
	EventType string `json:"eventType"`
	Payload   struct {
		IdentityKey string `json:"identityKey"`
		ActorKey    string `json:"actorKey"`
	} `json:"payload"`
}

// StartCredentialBindingsMaterializer opens the credential-bindings bucket,
// attaches the durable events.identity.> consumer that folds
// identity.claimed events into it, and blocks until the durable has drained
// every event committed before this call (cold-start correctness, mirroring
// StartRevocationMaterializer). Unlike the revocation kill-switch this backs
// an additive, best-effort resolution seam (ConfigureCredentialBindings): a
// caller may treat a returned error as non-fatal and run the Gateway without
// credential-binding resolution — every actor then simply acts as itself,
// exactly as before this mechanism existed.
func StartCredentialBindingsMaterializer(ctx context.Context, conn *substrate.Conn, hb *Heartbeater, logger *slog.Logger) (*substrate.ConsumerSupervisor, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := conn.KVStatus(ctx, credentialbinding.BucketName); err != nil {
		return nil, fmt.Errorf("gateway: credential-bindings bucket unavailable: %w", err)
	}

	sup := substrate.NewConsumerSupervisor(conn)
	spec := substrate.ConsumerSpec{
		Name:          credentialBindingsConsumerName,
		Stream:        bootstrap.CoreEventsStreamName,
		FilterSubject: credentialBindingsFilterSubject,
		DeliverPolicy: substrate.DeliverAll,
		Handler:       credentialBindingsHandler(conn, hb, logger),
		Classify:      classifyCredentialBindingsError,
		Probe:         func(ctx context.Context) error { return conn.KVStatus(ctx, credentialbinding.BucketName) },
		Health:        &credentialBindingsIssueSink{hb: hb},
		Logger:        logger,
	}
	if err := sup.Add(ctx, spec); err != nil {
		return nil, fmt.Errorf("gateway: attach credential-bindings consumer: %w", err)
	}

	deadline := time.Now().Add(credentialBindingsCatchUpTimeout)
	for {
		caughtUp, err := conn.ConsumerCaughtUp(ctx, bootstrap.CoreEventsStreamName, credentialBindingsConsumerName)
		if err == nil && caughtUp {
			break
		}
		if time.Now().After(deadline) {
			logger.Warn("gateway: credential-bindings cold-start catch-up timed out; continuing (self-heals as the durable drains)")
			break
		}
		select {
		case <-ctx.Done():
			return sup, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return sup, nil
}

// credentialBindingsHandler folds identity.claimed/identity.rebound
// (write/overwrite) and identity.unbound (delete) events into the
// credential-bindings bucket, keyed by the raw credential actor (A) so
// Resolve(actorID) is an O(1) point lookup. Must be idempotent
// (at-least-once delivery): a redelivered claim or rebound re-puts the same
// key with the same value; a redelivered unbind re-deletes an already-absent
// key (KVDelete is a no-op-safe soft delete). A rebound after a claim folds
// last and wins — stream-ordered, single writer (multi-credential-identity-
// linking-design.md §3.3). identity.unbound is the plane's one explicit
// row-set shrink (Contract #11 §11.4, design §8) — every other fold here
// only ever writes or overwrites.
func credentialBindingsHandler(conn *substrate.Conn, hb *Heartbeater, logger *slog.Logger) substrate.SupervisedHandler {
	return func(ctx context.Context, msg substrate.Message) (substrate.Decision, error) {
		if len(msg.Body) == 0 {
			return substrate.Ack, nil
		}
		var eb credentialBindingEventBody
		if err := json.Unmarshal(msg.Body, &eb); err != nil {
			logger.Warn("gateway: credential-bindings event body unparseable; dropping", "error", err)
			hb.SetIssue(credentialBindingsPoisonIssueKey, severityError, "credentialBindings.malformedEvent",
				"credential-bindings event body unparseable, dropped: "+err.Error())
			return substrate.Ack, nil
		}
		if eb.EventType != "identity.claimed" && eb.EventType != "identity.rebound" && eb.EventType != "identity.unbound" {
			// FilterSubject scopes delivery to events.identity.>, so a
			// sibling identity-domain event (e.g. identity.provisioned)
			// legitimately arrives here too — ignore anything but a
			// claim/rebound/unbind.
			return substrate.Ack, nil
		}
		if eb.Payload.ActorKey == "" || eb.Payload.IdentityKey == "" {
			logger.Warn("gateway: "+eb.EventType+" event missing actorKey/identityKey; dropping")
			hb.SetIssue(credentialBindingsPoisonIssueKey, severityError, "credentialBindings.missingFields",
				eb.EventType+" event missing actorKey/identityKey, dropped")
			return substrate.Ack, nil
		}

		if eb.EventType == "identity.unbound" {
			// Mirrors the ErrKeyNotFound guard every sibling delete-fold in
			// this codebase uses for the same redelivered-delete scenario
			// (revocationMaterializer's actorUnrevoked fold, Loom's actuator,
			// healthkv's consumer sink): a redelivered unbind targeting an
			// already-deleted key is a no-op, not a failure.
			if err := conn.KVDelete(ctx, credentialbinding.BucketName, eb.Payload.ActorKey); err != nil && !errors.Is(err, substrate.ErrKeyNotFound) {
				return credentialBindingWriteFailed(hb, logger, eb.Payload.ActorKey, err)
			}
			return substrate.Ack, nil
		}

		doc, err := json.Marshal(map[string]any{"identityKey": eb.Payload.IdentityKey})
		if err != nil {
			return substrate.Ack, nil // unreachable (map[string]any always marshals)
		}
		if _, err := conn.KVPut(ctx, credentialbinding.BucketName, eb.Payload.ActorKey, doc); err != nil {
			return credentialBindingWriteFailed(hb, logger, eb.Payload.ActorKey, err)
		}
		return substrate.Ack, nil
	}
}

// credentialBindingWriteFailed mirrors revocationWriteFailed's poison-pill
// doctrine: an invalid-key error can never succeed on redelivery (the actor
// key itself is unputtable), so it is Termed with a Health issue; any other
// failure is transient (Nak, at-least-once redelivery preserved).
func credentialBindingWriteFailed(hb *Heartbeater, logger *slog.Logger, actorKey string, err error) (substrate.Decision, error) {
	if substrate.IsInvalidKeyError(err) {
		logger.Error("gateway: credential-bindings event dropped — unputtable actor key", "actor", actorKey, "error", err)
		hb.SetIssue(credentialBindingsPoisonIssueKey, severityError, "credentialBindings.unputtableKey",
			"credential-bindings claim dropped for unputtable actor key "+actorKey+": "+err.Error())
		return substrate.Term, fmt.Errorf("gateway: credential-bindings %s: unputtable key, dropping: %w", actorKey, err)
	}
	return substrate.Nak, fmt.Errorf("gateway: credential-bindings %s: %w", actorKey, err)
}

// classifyCredentialBindingsError mirrors classifyRevocationError — see its
// doc comment for the poison-pill/ClassInfra-vs-ClassTerminal reasoning.
func classifyCredentialBindingsError(err error) substrate.FailureClass {
	if substrate.IsInvalidKeyError(err) {
		return substrate.ClassTerminal
	}
	return substrate.ClassInfra
}

// credentialBindingsIssueSink bridges the ConsumerSupervisor's pause
// lifecycle to the Contract #5 heartbeat's issue set. Unlike
// heartbeatIssueSink (revocation) this carries no dedicated
// Connected/LastSeq health-schema fields — credential-binding resolution is
// an additive, best-effort seam (ConfigureCredentialBindings), not a
// security kill-switch, so a generic issue is enough observability.
type credentialBindingsIssueSink struct {
	hb *Heartbeater
}

func (s *credentialBindingsIssueSink) SetActive(context.Context) error {
	s.hb.ClearIssue(credentialBindingsIssueKey)
	return nil
}

func (s *credentialBindingsIssueSink) SetPaused(_ context.Context, reason substrate.PauseReason, lastErr string) error {
	s.hb.SetIssue(credentialBindingsIssueKey, severityError, "credentialBindings.consumerDisconnected",
		"credential-bindings consumer paused ("+string(reason)+"): "+lastErr)
	return nil
}

func (s *credentialBindingsIssueSink) Load(context.Context) (substrate.HealthStatus, substrate.PauseReason, error) {
	return substrate.StatusActive, "", nil
}
