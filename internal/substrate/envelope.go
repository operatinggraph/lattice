package substrate

import (
	"encoding/json"
	"time"
)

// DocumentEnvelope is the universal Core KV document envelope per
// Contract #1 §1.3. Every vertex, aspect, link, and op tracker carries
// these fields. Aspects and links extend it with additional traversal
// pointers (see AspectEnvelope, LinkEnvelope).
//
// JSON field names are locked to Contract #1 §1.3 exact spelling.
type DocumentEnvelope struct {
	Key              string         `json:"key"`
	Class            string         `json:"class"`
	IsDeleted        bool           `json:"isDeleted"`
	CreatedAt        string         `json:"createdAt"`
	CreatedBy        string         `json:"createdBy"`
	CreatedByOp      string         `json:"createdByOp"`
	LastModifiedAt   string         `json:"lastModifiedAt"`
	LastModifiedBy   string         `json:"lastModifiedBy"`
	LastModifiedByOp string         `json:"lastModifiedByOp"`
	Data             map[string]any `json:"data"`
}

// AspectEnvelope extends DocumentEnvelope with the aspect-specific
// vertexKey and localName pointers (Contract #1 §1.3).
type AspectEnvelope struct {
	DocumentEnvelope
	VertexKey string `json:"vertexKey"`
	LocalName string `json:"localName"`
}

// LinkEnvelope extends DocumentEnvelope with the sourceVertex/targetVertex
// pointers and the link's localName (Contract #1 §1.3). sourceVertex is key
// segments 1-3 (the DDL-declared source side); targetVertex is segments 4-6
// (the target side). The pointers mirror the key's segment order — they are
// not a runtime createdAt ordering.
type LinkEnvelope struct {
	DocumentEnvelope
	SourceVertex string `json:"sourceVertex"`
	TargetVertex string `json:"targetVertex"`
	LocalName    string `json:"localName"`
}

// FormatTimestamp returns t formatted as the Contract #1 ISO 8601 string
// (RFC3339Nano in UTC). All envelope timestamps use this format.
func FormatTimestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// NewDocumentEnvelope constructs a fresh document envelope with universal
// fields populated for a creation event. The caller must populate Key,
// optional Data, and (for aspects/links) the type-specific extensions.
//
// createdAt and lastModifiedAt are set to time.Now() in UTC. For
// deterministic timestamps (bootstrap, replay) use
// NewDocumentEnvelopeAt.
//
// actor is the full vertex key of the creating identity (e.g.,
// "vtx.identity.<NanoID>"); opTracker is the full vertex key of the
// op-tracker that committed this creation (Contract #1 §1.3 field
// semantics). For self-referential trackers (Contract #4 §4.1), the op
// tracker's own key is passed for both actor and opTracker.
func NewDocumentEnvelope(class, actor, opTracker string) DocumentEnvelope {
	return NewDocumentEnvelopeAt(class, actor, opTracker, time.Now())
}

// NewDocumentEnvelopeAt is like NewDocumentEnvelope but uses an explicit
// timestamp. Used by primordial bootstrap (deterministic time) and by
// replay tooling (recovered timestamps).
func NewDocumentEnvelopeAt(class, actor, opTracker string, ts time.Time) DocumentEnvelope {
	if class == "" {
		panic("substrate: NewDocumentEnvelopeAt: class must not be empty")
	}
	if actor == "" {
		panic("substrate: NewDocumentEnvelopeAt: actor must not be empty")
	}
	if opTracker == "" {
		panic("substrate: NewDocumentEnvelopeAt: opTracker must not be empty")
	}
	stamp := FormatTimestamp(ts)
	return DocumentEnvelope{
		Class:            class,
		IsDeleted:        false,
		CreatedAt:        stamp,
		CreatedBy:        actor,
		CreatedByOp:      opTracker,
		LastModifiedAt:   stamp,
		LastModifiedBy:   actor,
		LastModifiedByOp: opTracker,
		Data:             nil,
	}
}

// Update mutates the lastModified* triplet in place. Used by Processor
// commit step 7 (envelope rewrite) when an existing document is updated.
// Pass the new modifier's identity key and the current operation's
// tracker key.
func (e *DocumentEnvelope) Update(actor, opTracker string) {
	e.UpdateAt(actor, opTracker, time.Now())
}

// UpdateAt is the explicit-timestamp form of Update.
func (e *DocumentEnvelope) UpdateAt(actor, opTracker string, ts time.Time) {
	if actor == "" {
		panic("substrate: UpdateAt: actor must not be empty")
	}
	if opTracker == "" {
		panic("substrate: UpdateAt: opTracker must not be empty")
	}
	e.LastModifiedAt = FormatTimestamp(ts)
	e.LastModifiedBy = actor
	e.LastModifiedByOp = opTracker
}

// Marshal serializes the envelope to canonical JSON. Provided as a
// convenience to keep callers from importing encoding/json directly for
// the common case.
func (e DocumentEnvelope) Marshal() ([]byte, error) { return json.Marshal(e) }

// Marshal serializes an AspectEnvelope to JSON.
func (e AspectEnvelope) Marshal() ([]byte, error) { return json.Marshal(e) }

// Marshal serializes a LinkEnvelope to JSON.
func (e LinkEnvelope) Marshal() ([]byte, error) { return json.Marshal(e) }
