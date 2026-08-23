package main

import (
	"encoding/json"
	"net/http"

	edgemanifest "github.com/operatinggraph/lattice/packages/edge-manifest"
)

// opCatalogProjection is one row of the edge-manifest `opCatalog` lens, read
// from its NATS-KV read-model bucket (P5: an application reads the lens
// projection, never Core KV). The column names are the lens's RETURN aliases;
// the flattening is the lens's, not the vocabulary's, so this struct is where
// the two spellings meet and nowhere else in the app has to know about it.
//
// Every descriptor column is optional by construction: an op meta that adopted
// none of the vocabulary still projects a row with them all null, which is the
// row the FE reads as "not renderable" and declines to offer.
type opCatalogProjection struct {
	OperationType string `json:"operationType"`
	OpMetaKey     string `json:"opMetaKey"`

	Title       string `json:"title"`
	ShortLabel  string `json:"shortLabel"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Tone        string `json:"tone"`
	SubmitLabel string `json:"submitLabel"`
	Group       string `json:"group"`

	InputSchema       string            `json:"inputSchema"`
	FieldDescriptions map[string]string `json:"fieldDescriptions"`

	DispatchClass         string   `json:"dispatchClass"`
	DispatchAuthContext   string   `json:"dispatchAuthContext"`
	DispatchTargetField   string   `json:"dispatchTargetField"`
	DispatchTargetType    string   `json:"dispatchTargetType"`
	DispatchReads         []string `json:"dispatchReads"`
	DispatchOptionalReads []string `json:"dispatchOptionalReads"`

	// DispatchContextParams names the schema fields the CLIENT fills from its
	// own context and never renders, each mapped to the template it fills them
	// from (pkgmgr.OpDispatchSpec.ContextParams: "how a self-scope entity-key
	// param is declared rather than asked of the visitor as a raw vertex key").
	// Dropping it here would not degrade the FE, it would break the op in two
	// directions at once: the field renders after all, asking a person to type
	// a `vtx.<type>.<NanoID>` the descriptor promised they would never see —
	// and once the owning package stops marking that field required BECAUSE it
	// delegated the value to this column, the field renders empty and the
	// payload reaches the Processor without a value the script requires.
	DispatchContextParams map[string]string `json:"dispatchContextParams"`

	// DispatchVisibleWhen gates whether the op is OFFERED at all, against the
	// state of the target row (pkgmgr.OpDispatchSpec.VisibleWhen: "offered only
	// when the row's Field column equals Equals"). Dropping it here would not
	// degrade the FE, it would make it fail OPEN — an op the package says to
	// hide in this state would be offered in every state. It is threaded
	// through unevaluated: no evaluator is built here, and the FE
	// treats a row that carries one as not-offerable, which is the same
	// fail-closed answer the descriptor's own contract gives a client
	// evaluating it against a row that lacks the named column.
	DispatchVisibleWhen *opVisibleWhen `json:"dispatchVisibleWhen"`

	Sensitive      bool     `json:"sensitive"`
	GrantedToRoles []string `json:"grantedToRoles"`
}

// opVisibleWhen is pkgmgr.OpVisibleWhenSpec's projected form. Equals is `any`
// because the vocabulary's own equality is over a JSON value — bool, string or
// number — and narrowing it to a string here would silently rewrite the
// condition an evaluator has to honour.
type opVisibleWhen struct {
	Field  string `json:"field"`
	Equals any    `json:"equals"`
}

// opDescriptor is the shape the FE renders from: the flat projection row
// re-nested back into the descriptor vocabulary the owning package declared
// (presentation / inputSchema / fieldDescriptions / dispatch), so the browser
// reads an op the way `pkgmgr.OpMetaSpec` writes one.
type opDescriptor struct {
	OperationType     string            `json:"operationType"`
	Presentation      map[string]string `json:"presentation,omitempty"`
	InputSchema       string            `json:"inputSchema,omitempty"`
	FieldDescriptions map[string]string `json:"fieldDescriptions,omitempty"`
	Dispatch          *opDispatch       `json:"dispatch,omitempty"`
	Sensitive         bool              `json:"sensitive,omitempty"`
	GrantedToRoles    []string          `json:"grantedToRoles"`
}

type opDispatch struct {
	Class         string            `json:"class,omitempty"`
	AuthContext   string            `json:"authContext,omitempty"`
	TargetField   string            `json:"targetField,omitempty"`
	TargetType    string            `json:"targetType,omitempty"`
	ContextParams map[string]string `json:"contextParams,omitempty"`
	Reads         []string          `json:"reads,omitempty"`
	OptionalReads []string          `json:"optionalReads,omitempty"`
	VisibleWhen   *opVisibleWhen    `json:"visibleWhen,omitempty"`
}

// computeOpCatalog assembles the operationType-keyed descriptor map from the
// `opCatalog` lens read model. A row that fails to decode or carries no
// operationType is skipped: the lens keys on operationType, so such a row can
// only be a torn or tombstoned entry, never a describable op.
//
// A row with no dispatch is kept, not dropped. The FE's own offerability rule
// is "has an inputSchema AND a dispatch class"; keeping the row lets it say
// which op it declined to offer, where a dropped row is indistinguishable from
// a projection that has not caught up.
func computeOpCatalog(keys []string, get kvGetter) map[string]opDescriptor {
	out := map[string]opDescriptor{}
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p opCatalogProjection
		if json.Unmarshal(raw, &p) != nil || p.OperationType == "" {
			continue
		}
		out[p.OperationType] = p.toDescriptor()
	}
	return out
}

func (p opCatalogProjection) toDescriptor() opDescriptor {
	d := opDescriptor{
		OperationType:     p.OperationType,
		InputSchema:       p.InputSchema,
		FieldDescriptions: p.FieldDescriptions,
		Sensitive:         p.Sensitive,
		GrantedToRoles:    p.GrantedToRoles,
	}
	if d.GrantedToRoles == nil {
		d.GrantedToRoles = []string{}
	}

	presentation := map[string]string{}
	for k, v := range map[string]string{
		"title": p.Title, "shortLabel": p.ShortLabel, "description": p.Description,
		"icon": p.Icon, "tone": p.Tone, "submitLabel": p.SubmitLabel, "group": p.Group,
	} {
		if v != "" {
			presentation[k] = v
		}
	}
	if len(presentation) > 0 {
		d.Presentation = presentation
	}

	// VisibleWhen counts toward "this row has a dispatch": a row carrying ONLY
	// a visibility condition still has to reach the FE, because that condition
	// is what withholds the op. Dropping the whole dispatch object because no
	// other field was set would restore the fail-open this column exists to
	// close.
	if p.DispatchClass != "" || p.DispatchAuthContext != "" || p.DispatchTargetField != "" ||
		p.DispatchTargetType != "" || len(p.DispatchContextParams) > 0 || len(p.DispatchReads) > 0 ||
		len(p.DispatchOptionalReads) > 0 || p.DispatchVisibleWhen != nil {
		d.Dispatch = &opDispatch{
			Class:         p.DispatchClass,
			AuthContext:   p.DispatchAuthContext,
			TargetField:   p.DispatchTargetField,
			TargetType:    p.DispatchTargetType,
			ContextParams: p.DispatchContextParams,
			Reads:         p.DispatchReads,
			OptionalReads: p.DispatchOptionalReads,
			VisibleWhen:   p.DispatchVisibleWhen,
		}
	}
	return d
}

// handleOpCatalog implements GET /api/op-catalog — the op descriptors this
// app's task-completion modal renders its forms from, served from the
// edge-manifest `opCatalog` lens read model (NOT Core KV, which a vertical app
// may not read: lattice-architecture.md P5).
//
// The browser never talks to NATS, so this is a thin proxy in the same shape as
// handleListings: list the bucket's keys, point-read each, re-nest into the
// descriptor vocabulary.
func (s *server) handleOpCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusBadRequest, "GET required")
		return
	}
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	bucket := edgemanifest.OpCatalogBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is edge-manifest installed and the Refractor projecting?)")
		return
	}
	get := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, bucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	catalog := computeOpCatalog(keys, get)
	s.writeJSON(w, http.StatusOK, map[string]any{"catalog": catalog, "count": len(catalog)})
}
