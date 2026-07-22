package bootstrap

import (
	"encoding/json"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// BootstrapTime is the canonical createdAt/lastModifiedAt for all primordial
// entries. Using a fixed timestamp makes bootstrap output deterministic and
// reproducible.
var BootstrapTime = time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)

// MakeVertexEnvelope constructs a vertex envelope (Contract #1 §1.3) using
// substrate's universal envelope helper. All provenance fields point to the
// primordial bootstrap identity + op.
func MakeVertexEnvelope(key, class string, data any) ([]byte, error) {
	env := substrate.NewDocumentEnvelopeAt(class, BootstrapIdentityKey, BootstrapOpKey, BootstrapTime)
	env.Key = key
	if data != nil {
		dataMap, err := toMap(data)
		if err != nil {
			return nil, err
		}
		env.Data = dataMap
	}
	return json.Marshal(env)
}

// MakeAspectEnvelope constructs an aspect envelope. vertexKey is the parent
// vertex (key segments 1-3). localName is key segment 4.
func MakeAspectEnvelope(key, vertexKey, localName, class string, data any) ([]byte, error) {
	base := substrate.NewDocumentEnvelopeAt(class, BootstrapIdentityKey, BootstrapOpKey, BootstrapTime)
	base.Key = key
	if data != nil {
		dataMap, err := toMap(data)
		if err != nil {
			return nil, err
		}
		base.Data = dataMap
	}
	env := substrate.AspectEnvelope{
		DocumentEnvelope: base,
		VertexKey:        vertexKey,
		LocalName:        localName,
	}
	return json.Marshal(env)
}

// MakeLinkEnvelope constructs a link envelope. sourceVertex is key segments
// 1-3, targetVertex is segments 4-6 (after localName).
func MakeLinkEnvelope(key, sourceVertex, targetVertex, localName, class string, data any) ([]byte, error) {
	base := substrate.NewDocumentEnvelopeAt(class, BootstrapIdentityKey, BootstrapOpKey, BootstrapTime)
	base.Key = key
	if data != nil {
		dataMap, err := toMap(data)
		if err != nil {
			return nil, err
		}
		base.Data = dataMap
	}
	env := substrate.LinkEnvelope{
		DocumentEnvelope: base,
		SourceVertex:     sourceVertex,
		TargetVertex:     targetVertex,
		LocalName:        localName,
	}
	return json.Marshal(env)
}

// MakeBootstrapOpEnvelope constructs the special bootstrap op tracker envelope.
// Per Contract #7 §7.2: self-referential provenance (the tracker IS the op record).
// Per Contract #4 §4.1: createdByOp/lastModifiedByOp both point to the tracker itself.
func MakeBootstrapOpEnvelope() ([]byte, error) {
	// Self-referential provenance: both actor and op tracker resolve to the
	// bootstrap op key itself for the universal envelope's provenance fields
	// (the bootstrap identity is the actor; the tracker references itself
	// for createdByOp per Contract #4 §4.1).
	env := substrate.NewDocumentEnvelopeAt("op.bootstrap", BootstrapIdentityKey, BootstrapOpKey, BootstrapTime)
	env.Key = BootstrapOpKey
	env.Data = map[string]any{
		"status":        "committed",
		"operationType": "PrimordialBootstrap",
		"requestId":     BootstrapOpID,
		"note":          "Synthetic platform genesis op tracker. No TTL — permanent record.",
	}
	return json.Marshal(env)
}

// toMap converts an arbitrary Go value into the map[string]any shape
// substrate's envelope expects for the Data field. Round-trips via json.
func toMap(v any) (map[string]any, error) {
	if m, ok := v.(map[string]any); ok {
		return m, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}
