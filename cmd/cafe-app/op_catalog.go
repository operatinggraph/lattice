package main

import (
	"encoding/json"
	"net/http"
	"strings"

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
	DispatchClassChoices  []string `json:"dispatchClassChoices"`
	DispatchAuthContext   string   `json:"dispatchAuthContext"`
	DispatchTargetField   string   `json:"dispatchTargetField"`
	DispatchTargetType    string   `json:"dispatchTargetType"`
	DispatchReads         []string `json:"dispatchReads"`
	DispatchOptionalReads []string `json:"dispatchOptionalReads"`

	// DispatchEnumerations names the class-(e) kv.Links walks the dispatched
	// op runs (Contract #2 §2.5): each a {hub, relation, direction} the
	// Starlark submitter is allowed to enumerate live rather than declare as a
	// Reads/OptionalReads key. Dropping it here would not degrade the FE — it
	// never renders — but would desync the descriptor from what the package
	// actually declared, defeating this projection's purpose as the one place
	// the wire spelling and the vocabulary meet.
	DispatchEnumerations []opEnumeration `json:"dispatchEnumerations"`

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
	// through unevaluated: this projection builds no evaluator itself — the
	// shared internal/descriptorform module does, checking it against
	// context.row (strict JSON scalar equality) and failing closed the same
	// way when the row is absent or lacks the named column.
	DispatchVisibleWhen *opVisibleWhen `json:"dispatchVisibleWhen"`

	// CeremonyMintedSecretHashField names the schema field carrying the
	// lowercase-hex sha256 of a secret the CLIENT mints and Lattice must never
	// learn, and CeremonyRevealTitle/CeremonyRevealHelp are the copy for the
	// one-time display of the plaintext once the write lands
	// (pkgmgr.OpCeremonySpec). Dropping these here would not degrade the FE, it
	// would hand it an op it cannot tell apart from an ordinary one: the hash
	// field would render as a text box asking a person to type a 64-char digest
	// whose preimage nobody holds, and an accepted submission would arm a secret
	// no one can ever present. The contract the column carries is fail-closed at
	// the FE — a client that cannot perform the ceremony must decline to OFFER
	// the op at all, never fall back to rendering the field.
	CeremonyMintedSecretHashField string `json:"ceremonyMintedSecretHashField"`
	CeremonyRevealTitle           string `json:"ceremonyRevealTitle"`
	CeremonyRevealHelp            string `json:"ceremonyRevealHelp"`

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

// opEnumeration is pkgmgr.EnumerationSpec's projected form: one declared
// kv.Links walk the dispatched op runs, named by its hub, the link relation
// walked, and the direction the hub sits in the link.
type opEnumeration struct {
	Hub       string `json:"hub"`
	Relation  string `json:"relation"`
	Direction string `json:"direction"`
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
	Ceremony          *opCeremony       `json:"ceremony,omitempty"`
	Sensitive         bool              `json:"sensitive,omitempty"`
	GrantedToRoles    []string          `json:"grantedToRoles"`
}

// opCeremony is pkgmgr.OpCeremonySpec's projected form. It sits at the TOP
// level beside Dispatch rather than inside it: a ceremony is a property of the
// operation itself — what the client must DO before it can submit at all —
// not of how the submission is routed.
type opCeremony struct {
	MintedSecretHashField string `json:"mintedSecretHashField"`
	RevealTitle           string `json:"revealTitle,omitempty"`
	RevealHelp            string `json:"revealHelp,omitempty"`
}

type opDispatch struct {
	Class         string            `json:"class,omitempty"`
	ClassChoices  []string          `json:"classChoices,omitempty"`
	AuthContext   string            `json:"authContext,omitempty"`
	TargetField   string            `json:"targetField,omitempty"`
	TargetType    string            `json:"targetType,omitempty"`
	ContextParams map[string]string `json:"contextParams,omitempty"`
	Reads         []string          `json:"reads,omitempty"`
	OptionalReads []string          `json:"optionalReads,omitempty"`
	Enumerations  []opEnumeration   `json:"enumerations,omitempty"`
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
	if p.DispatchClass != "" || len(p.DispatchClassChoices) > 0 || p.DispatchAuthContext != "" ||
		p.DispatchTargetField != "" ||
		p.DispatchTargetType != "" || len(p.DispatchContextParams) > 0 || len(p.DispatchReads) > 0 ||
		len(p.DispatchOptionalReads) > 0 || len(p.DispatchEnumerations) > 0 || p.DispatchVisibleWhen != nil {
		d.Dispatch = &opDispatch{
			Class:         p.DispatchClass,
			ClassChoices:  p.DispatchClassChoices,
			AuthContext:   p.DispatchAuthContext,
			TargetField:   p.DispatchTargetField,
			TargetType:    p.DispatchTargetType,
			ContextParams: p.DispatchContextParams,
			Reads:         p.DispatchReads,
			OptionalReads: p.DispatchOptionalReads,
			Enumerations:  p.DispatchEnumerations,
			VisibleWhen:   p.DispatchVisibleWhen,
		}
	}

	// MintedSecretHashField is what makes a ceremony a ceremony — the reveal
	// copy alone describes nothing to perform — so the whole object is omitted
	// unless it is set, the same way the dispatch object above is omitted when
	// nothing about it is set. An op with no ceremony must be indistinguishable
	// from one whose package never declared the vocabulary at all.
	if p.CeremonyMintedSecretHashField != "" {
		d.Ceremony = &opCeremony{
			MintedSecretHashField: p.CeremonyMintedSecretHashField,
			RevealTitle:           p.CeremonyRevealTitle,
			RevealHelp:            p.CeremonyRevealHelp,
		}
	}
	return d
}

// handleOpCatalog implements GET /api/op-catalog — the op descriptors this
// app's staff forms render from, served from the edge-manifest `opCatalog`
// lens read model (NOT Core KV, which a vertical app may not read:
// lattice-architecture.md P5).
//
// The browser never talks to NATS, so this is a thin proxy in the same shape as
// the app's other lens-backed read handlers: list the bucket's keys, point-read
// each, re-nest into the descriptor vocabulary.
//
// An optional `?types=Op1,Op2` narrows the point-reads to exactly those
// operationTypes instead of listing the whole cross-vertical bucket — safe
// because the lens keys each row on its own operationType (IntoKey), so a
// stale or unknown name in the list just yields no row for that key
// (computeOpCatalog already skips a miss), never an error. Omit it to get
// the full catalog, unchanged.
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
	var keys []string
	if types := r.URL.Query().Get("types"); types != "" {
		keys = strings.Split(types, ",")
	} else {
		var err error
		keys, err = conn.KVListKeys(ctx, bucket)
		if err != nil {
			s.writeError(w, http.StatusBadGateway,
				"list "+bucket+": "+err.Error()+" (is edge-manifest installed and the Refractor projecting?)")
			return
		}
	}
	catalog := computeOpCatalog(keys, s.kvGetter(ctx, bucket))
	s.writeJSON(w, http.StatusOK, map[string]any{"catalog": catalog, "count": len(catalog)})
}
