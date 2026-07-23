// Package rolesanchors resolves an authenticated actor's role-derived grant
// keys and residence/workplace anchors for the Gateway's whoami response
// (persona-worlds-design.md §10 Fire P1: GET /v1/actor gains roles[] +
// anchors[]). Roles read the rbac-domain capabilityRoles projection through
// internal/capabilitykv (the same cap.roles.<actor> doc the Processor's
// step-3 platform read consults); anchors read the identity-domain
// package's own identityAnchors lens bucket
// (packages/identity-domain.IdentityAnchorsBucket). Both halves degrade to
// an empty result on any absence or error — a soft whoami hint, never a
// caller-visible error — mirroring internal/gateway/identityindexhint's
// warn-and-degrade posture.
package rolesanchors

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/operatinggraph/lattice/internal/capabilitykv"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// AnchorsBucketName is the canonical identity-anchors bucket
// (packages/identity-domain.IdentityAnchorsBucket).
const AnchorsBucketName = "identity-anchors"

// Logger is the minimal logging surface Resolver needs. *slog.Logger and
// internal/gateway.Logger both satisfy it structurally.
type Logger interface {
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

type nopLogger struct{}

func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}

// kvGetter is the minimal read surface the anchors half needs —
// *substrate.KV satisfies it; the interface keeps the package test-fakeable
// without a live NATS connection (mirrors identityindexhint.kvGetter /
// credentialbinding.kvGetter).
type kvGetter interface {
	Get(ctx context.Context, key string) (*substrate.KVEntry, error)
}

// Anchor is one residence/workplace anchor the identityAnchors lens projects
// (packages/identity-domain's identityAnchors lens: each `anchors` entry is
// `{key,name,container,containerName,relation}`).
type Anchor struct {
	Key           string `json:"key"`
	Name          string `json:"name"`
	Container     string `json:"container"`
	ContainerName string `json:"containerName"`
	Relation      string `json:"relation"`
}

// anchorsDoc is the identityAnchors lens-projected document shape.
type anchorsDoc struct {
	Anchors []Anchor `json:"anchors"`
}

// Resolver resolves an actor's role-derived grant keys and residence/
// workplace anchors for the whoami response. Safe for concurrent use (it
// holds only read handles).
type Resolver struct {
	capabilityReader capabilitykv.KVGetter
	capabilityBucket string
	anchorsKV        kvGetter
	logger           Logger
}

// New builds a Resolver over capabilityReader (e.g. *substrate.Conn, paired
// with capabilityBucket = bootstrap.CapabilityKVBucket) and an
// already-opened identity-anchors bucket handle (obtain via
// substrate.Conn.OpenKV(ctx, rolesanchors.AnchorsBucketName)). anchorsKV may
// be nil (the identityAnchors lens hasn't activated yet, or the bucket
// failed to open) — Resolve then reports no anchors, never an error.
func New(capabilityReader capabilitykv.KVGetter, capabilityBucket string, anchorsKV kvGetter, logger Logger) *Resolver {
	if logger == nil {
		logger = nopLogger{}
	}
	return &Resolver{
		capabilityReader: capabilityReader,
		capabilityBucket: capabilityBucket,
		anchorsKV:        anchorsKV,
		logger:           logger,
	}
}

// Resolve reports actorKey's role-derived grant keys and residence/
// workplace anchors, degrading to an empty result on any read/parse failure
// or absence — a soft whoami hint, never a caller-visible error.
func (r *Resolver) Resolve(ctx context.Context, actorKey string) (roles []string, anchors []Anchor) {
	return r.resolveRoles(ctx, actorKey), r.resolveAnchors(ctx, actorKey)
}

// resolveRoles reads the rbac-domain capabilityRoles doc
// (cap.roles.<actorSuffix>) via the shared capabilitykv.ReadAndMerge —
// absent (KeyNotFound) merges to a nil doc, which reports empty roles, not
// an error (capabilitykv/keys.go:28-34, read.go).
func (r *Resolver) resolveRoles(ctx context.Context, actorKey string) []string {
	key, err := capabilitykv.RolesKeyFromActor(actorKey)
	if err != nil {
		r.logger.Warn("rolesanchors: derive roles key failed", "actor", actorKey, "error", err)
		return nil
	}
	doc, _, err := capabilitykv.ReadAndMerge(ctx, r.capabilityReader, r.capabilityBucket, []string{key})
	if err != nil {
		r.logger.Warn("rolesanchors: capability doc read failed", "actor", actorKey, "error", err)
		return nil
	}
	if doc == nil {
		return nil
	}
	return doc.Roles
}

// resolveAnchors reads the identityAnchors lens doc (anchors.<actorSuffix>),
// deriving the actor suffix the same way capabilitykv's RolesKeyFromActor
// does (strip the "vtx." prefix).
func (r *Resolver) resolveAnchors(ctx context.Context, actorKey string) []Anchor {
	if r.anchorsKV == nil {
		return nil
	}
	suffix, ok := strings.CutPrefix(actorKey, substrate.VertexPrefix+".")
	if !ok {
		r.logger.Warn("rolesanchors: actor key lacks vtx prefix", "actor", actorKey)
		return nil
	}
	entry, err := r.anchorsKV.Get(ctx, "anchors."+suffix)
	switch {
	case err == nil:
		var doc anchorsDoc
		if uerr := json.Unmarshal(entry.Value, &doc); uerr != nil {
			r.logger.Warn("rolesanchors: malformed anchors doc", "actor", actorKey, "error", uerr)
			return nil
		}
		return doc.Anchors
	case errors.Is(err, substrate.ErrKeyNotFound):
		return nil
	default:
		r.logger.Warn("rolesanchors: anchors read failed", "actor", actorKey, "error", err)
		return nil
	}
}
