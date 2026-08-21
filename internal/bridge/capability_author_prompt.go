package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/modelrunner/wire"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// promptTemplateVersion identifies the authoring template below. It is hashed
// into every proposal's promptHash, so a template edit is visible as a changed
// hash even when the intent and the catalog are unchanged — the difference
// between "the operator asked something else" and "we ask differently now".
const promptTemplateVersion = "capability-author/v1"

// capabilityAuthorToolName is the single tool the model is forced to answer
// through. Its input schema IS this adapter's output contract.
const capabilityAuthorToolName = "emit_capability_proposal"

// capabilityAuthorSystemPrompt is the authoring rulebook. Every rule below
// restates a check the deterministic validator actually runs
// (targetId/gap-column token shape, the missing_<gap> convention, the per-action
// required fields, the reserved param) so a compliant answer passes validation
// and a non-compliant one gets told exactly which rule it broke.
const capabilityAuthorSystemPrompt = `You author ONE Lattice weaver target from an operator's plain-language intent.

A weaver target is a convergence rule. A LENS projects one row per entity, with
boolean ` + "`missing_<gap>`" + ` columns marking what is not yet true. The TARGET says what
to do while a gap is open, and the Weaver keeps dispatching until it closes. You
do not write lenses: you bind the target to a lens that ALREADY EXISTS in the
catalog you are given.

You answer only by calling the emit_capability_proposal tool. The rules:

1. content.targetId is a single key token: letters, digits, '_' and '-' only. No
   dots, spaces or wildcards. Use a short camelCase name that reads as the thing
   being kept true, e.g. "coldOnboardingReminder".
2. content.lensRef is the canonicalName of a lens from the catalog. Read that
   lens's spec and use ONLY the missing_<gap> columns it actually RETURNs.
3. Every gaps[].gapColumn is one of that lens's missing_<gap> columns, spelled
   exactly. A column always starts with "missing_" and is otherwise letters,
   digits, '_' and '-'.
4. Each gap declares exactly one action, with that action's required fields:
     triggerLoom - pattern + subject
     assignTask  - operation + assignee + target
     directOp    - operation
     surface     - issueCode, and issueSeverity is "warning" or "error"
                   (leave it empty for the "warning" default)
   Leave every field the chosen action does not use as "" — blank fields are
   dropped, not recorded.
5. A gap's params[] and reads[] carry either literal values or "row.<column>"
   templates the engine resolves from the violation row at dispatch time, e.g.
   "row.identityKey". Only columns the bound lens RETURNs exist. The param name
   "expectedRevision" is reserved by the engine — never set it.
6. Name only operations and loom patterns that appear in the catalog. If nothing
   there remediates the intent, prefer a "surface" gap that raises a named health
   issue over inventing an operation that does not exist.
7. content.description is one or two plain sentences saying what this target
   keeps true, in the operator's own terms. A human reads it on the target
   roster forever after. Never leave it empty.
8. rationale says which lens you bound to and why those actions remediate the
   intent. If the intent genuinely needs a lens that does NOT exist yet, still
   emit the target: set lensRef to the canonicalName you propose for the new
   lens, and sketch that lens's openCypher (MATCH ... RETURN key, missing_<gap>)
   inside rationale so the operator can finish it in the Weaver Target Studio.
9. confidence is your own 0..1 score for this proposal. Be honest — a low score
   is useful information, not a failure.

You are proposing, not installing. Everything you emit is deterministically
validated and then reviewed by a human before it can take effect.`

// authoringPrompt is the user turn: the operator's own words, then the catalog
// the target must be authored within. When the catalog was capped to fit the
// payload wall it says so, so the model treats an absent entry as "not shown",
// not "does not exist".
func authoringPrompt(intent, catalog string, truncated bool) string {
	var b strings.Builder
	b.WriteString("Operator intent:\n")
	b.WriteString(intent)
	b.WriteString("\n\nInstalled catalog (JSON):\n")
	b.WriteString(catalog)
	if truncated {
		b.WriteString("\n\nNote: the catalog was truncated to fit a size limit — some installed entries are not shown. Bind only to a lens that IS shown here; if none fits, propose the lens in your rationale.")
	}
	b.WriteString("\n\nAuthor one weaver target that makes that intent true.")
	return b.String()
}

// correctionPrompt is the repair pass: the original turn, the artifact that came
// back, and the deterministic validator's own errors. The model is told to fix
// exactly those and change nothing else, so a correction cannot quietly become a
// different proposal.
func correctionPrompt(base string, draft []byte, report string) string {
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\n--- correction pass ---\nYour previous answer did not pass deterministic validation.\n\nWhat you proposed:\n")
	b.Write(draft)
	b.WriteString("\n\nValidation errors:\n")
	b.WriteString(report)
	b.WriteString("\n\nEmit a corrected proposal. Fix every error listed above and change nothing else.")
	return b.String()
}

// promptDigest hashes the template version together with both turns, so the
// recorded promptHash identifies the whole ask and not just its variable half.
func promptDigest(system, prompt string) string {
	sum := sha256.New()
	sum.Write([]byte(promptTemplateVersion))
	sum.Write([]byte{0})
	sum.Write([]byte(system))
	sum.Write([]byte{0})
	sum.Write([]byte(prompt))
	return hex.EncodeToString(sum.Sum(nil))
}

// capabilityAuthorTool is the strict tool schema the model must answer through.
//
// Every object closes itself (additionalProperties:false) and requires every
// property it declares — the shape strict tool use expects, and the reason the
// gaps and params maps are expressed as lists: a closed schema cannot describe
// an object whose keys the model chooses. Fields that only some actions use are
// present-but-empty rather than optional, and the assembler drops the blanks.
// The runner supplies the top-level object's type and additionalProperties.
func capabilityAuthorTool() wire.Tool {
	str := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	gap := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required": []string{"gapColumn", "action", "pattern", "subject", "adapter",
			"operation", "assignee", "target", "params", "reads", "issueCode", "issueSeverity"},
		"properties": map[string]any{
			"gapColumn": str("the bound lens's missing_<gap> column this action remediates, spelled exactly"),
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"triggerLoom", "assignTask", "directOp", "surface"},
				"description": "how the Weaver remediates this gap",
			},
			"pattern":   str("triggerLoom only: the loom pattern to run; \"\" otherwise"),
			"subject":   str("triggerLoom only: the pattern's subject, usually a row.<column> template; \"\" otherwise"),
			"adapter":   str("rarely used: the external adapter name; \"\" otherwise"),
			"operation": str("assignTask/directOp only: the operation type to dispatch; \"\" otherwise"),
			"assignee":  str("assignTask only: who the task is assigned to; \"\" otherwise"),
			"target":    str("assignTask only: what the task is about; \"\" otherwise"),
			"params": map[string]any{
				"type":        "array",
				"description": "the dispatched op's params: literals or row.<column> templates. Empty list if none.",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"key", "value"},
					"properties": map[string]any{
						"key":   str("the param name (never \"expectedRevision\" — the engine reserves it)"),
						"value": str("a literal, or a row.<column> template resolved from the violation row"),
					},
				},
			},
			"reads": map[string]any{
				"type":        "array",
				"description": "keys the dispatched op must read, as row.<column> templates. Empty list if none.",
				"items":       map[string]any{"type": "string"},
			},
			"issueCode":     str("surface only: the health issue code raised while the gap is open; \"\" otherwise"),
			"issueSeverity": str("surface only: \"warning\" or \"error\"; \"\" for the warning default"),
		},
	}
	return wire.Tool{
		Name:        capabilityAuthorToolName,
		Description: "Propose one weaver target for the operator's intent, bound to an existing lens.",
		InputSchema: wire.ToolSchema{
			Required: []string{"kind", "content", "rationale", "confidence"},
			Properties: map[string]any{
				"kind": map[string]any{
					"type":        "string",
					"enum":        []string{CapabilityAuthorKind},
					"description": "the artifact kind proposed; this adapter authors weaver targets only",
				},
				"content": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"targetId", "lensRef", "description", "gaps"},
					"properties": map[string]any{
						"targetId":    str("a single key token: letters, digits, '_' and '-' only"),
						"lensRef":     str("the canonicalName of the lens whose rows this target converges"),
						"description": str("one or two plain sentences saying what this target keeps true"),
						"gaps": map[string]any{
							"type":        "array",
							"description": "one entry per missing_<gap> column this target remediates",
							"items":       gap,
						},
					},
				},
				"rationale":  str("which lens you bound to, why these actions remediate the intent, and — if the lens does not exist yet — a sketch of its openCypher"),
				"confidence": map[string]any{"type": "number", "description": "your own 0..1 confidence in this proposal"},
			},
		},
	}
}

// --- the catalog ------------------------------------------------------------

// catalogRow is one row of the capabilityAuthorContext lens read model — field
// names mirror that lens's RETURN aliases verbatim
// (packages/capability-author/lenses.go), so decoding is a direct unmarshal.
// A meta that is not an op self-description projects an empty permittedCommands;
// a meta that is not a lens/weaverTarget/loomPattern projects a null spec.
type catalogRow struct {
	Key               string          `json:"key"`
	Class             string          `json:"class"`
	CanonicalName     string          `json:"canonicalName"`
	Description       string          `json:"description"`
	Spec              json.RawMessage `json:"spec"`
	PermittedCommands []string        `json:"permittedCommands"`
	InputSchema       json.RawMessage `json:"inputSchema"`
	FieldDescriptions json.RawMessage `json:"fieldDescriptions"`
}

// catalogArtifact is one installed lens / weaver target / loom pattern as the
// author sees it: what it is called, what it is for, and its body.
type catalogArtifact struct {
	CanonicalName string          `json:"canonicalName"`
	Description   string          `json:"description,omitempty"`
	Spec          json.RawMessage `json:"spec"`
}

// catalogOperation is one installed operation's self-description: enough to
// name it correctly and template its params.
type catalogOperation struct {
	CanonicalName     string          `json:"canonicalName"`
	Description       string          `json:"description,omitempty"`
	PermittedCommands []string        `json:"permittedCommands"`
	InputSchema       json.RawMessage `json:"inputSchema,omitempty"`
	FieldDescriptions json.RawMessage `json:"fieldDescriptions,omitempty"`
}

// catalogView is the filtered catalog serialised into the prompt. Grouping is
// what makes it readable; sorting by key inside each group is what makes it
// reproducible. Truncated records that the size cap dropped rows, so the prompt
// can tell the model the catalog is partial.
type catalogView struct {
	Lenses        []catalogArtifact  `json:"lenses"`
	WeaverTargets []catalogArtifact  `json:"weaverTargets"`
	LoomPatterns  []catalogArtifact  `json:"loomPatterns"`
	Operations    []catalogOperation `json:"operations"`
	Truncated     bool               `json:"truncated,omitempty"`
}

// catalogSnapshot is the exact catalog one authoring request reasoned over, its
// digest, and whether it was capped to fit the payload wall.
type catalogSnapshot struct {
	serialized string
	hash       string
	truncated  bool
}

// Meta classes the catalog lens projects. A row is grouped by its class; the
// three artifact classes are the ones carrying a `spec` body.
const (
	metaClassLens         = "meta.lens"
	metaClassWeaverTarget = "meta.weaverTarget"
	metaClassLoomPattern  = "meta.loomPattern"
)

// readCatalog reads the capability-author catalog read model and renders the
// slice of it an author needs, capped to fit the payload wall.
//
// This is a lens-target read, not a Core KV read: the catalog is the
// capabilityAuthorContext lens's own projection, which is exactly the surface
// this adapter is allowed to reason over.
//
// It fails when there is no lens to bind to — an unreadable bucket, an empty
// one, or a projection with no lens rows. That is the "capability-author not
// provisioned" condition, and Execute turns it into a terminal, visible failure:
// redelivery cannot fix it, and a target must bind to an existing lens.
func (a *CapabilityAuthor) readCatalog(ctx context.Context) (catalogSnapshot, error) {
	view, _, err := a.readCatalogRows(ctx)
	if err != nil {
		return catalogSnapshot{}, err
	}
	if len(view.Lenses) == 0 {
		return catalogSnapshot{}, fmt.Errorf("capabilityAuthor: catalog bucket %s exposes no lens to bind a target to — the capabilityAuthorContext lens has not projected", a.contextBucket)
	}
	truncated := capCatalogView(&view)
	view.Truncated = truncated
	body, err := json.Marshal(view)
	if err != nil {
		return catalogSnapshot{}, fmt.Errorf("capabilityAuthor: encode catalog: %w", err)
	}
	sum := sha256.Sum256(body)
	return catalogSnapshot{serialized: string(body), hash: hex.EncodeToString(sum[:]), truncated: truncated}, nil
}

// lensIndex resolves the canonicalName→NanoID map of every installed lens, for
// binding a target's authored lensRef. It reads the FULL catalog (never the
// capped prompt view — the model can only name a lens it was shown, so the index
// must cover them all), and tolerates an empty catalog: an empty map simply
// resolves nothing, and the draft records invalid. Only a real read failure is
// an error, and it is transient (the poll re-arms; CallDeadline backstops).
func (a *CapabilityAuthor) lensIndex(ctx context.Context) (map[string]string, error) {
	_, index, err := a.readCatalogRows(ctx)
	return index, err
}

// readCatalogRows lists and reads the catalog bucket once and returns both the
// grouped view and the lens canonicalName→NanoID index built from the same
// rows. Keys are read sorted, so both outputs are byte-stable regardless of the
// order the underlying batch read returned. An error is only a transport
// failure — emptiness is not an error here (each caller decides what an empty
// catalog means).
func (a *CapabilityAuthor) readCatalogRows(ctx context.Context) (catalogView, map[string]string, error) {
	keys, err := a.conn.KVListKeys(ctx, a.contextBucket)
	if err != nil {
		return catalogView{}, nil, fmt.Errorf("capabilityAuthor: list catalog bucket %s: %w", a.contextBucket, err)
	}
	sort.Strings(keys)
	entries, err := a.conn.KVGetMulti(ctx, a.contextBucket, keys)
	if err != nil {
		return catalogView{}, nil, fmt.Errorf("capabilityAuthor: read catalog bucket %s: %w", a.contextBucket, err)
	}
	view, index := buildCatalogView(keys, func(key string) []byte {
		if e, ok := entries[key]; ok && e != nil {
			return e.Value
		}
		return nil
	})
	return view, index, nil
}

// buildCatalogView filters and groups the catalog rows named by keys, reading
// each row's bytes through value, and returns the prompt view plus the lens
// canonicalName→NanoID index. Keys are walked in the caller's order, so a sorted
// key list yields a byte-stable view regardless of how the underlying read
// returned the rows. A row that does not decode, carries no key, or is neither
// an op self-description nor a spec-bearing artifact is skipped: the catalog is
// a prompt, and a poison row should cost the author nothing. Every lens spec is
// sanitised before it enters the view — the DSN and RLS posture never reach the
// vendor.
func buildCatalogView(keys []string, value func(string) []byte) (catalogView, map[string]string) {
	view := catalogView{
		Lenses:        []catalogArtifact{},
		WeaverTargets: []catalogArtifact{},
		LoomPatterns:  []catalogArtifact{},
		Operations:    []catalogOperation{},
	}
	index := map[string]string{}
	for _, key := range keys {
		raw := value(key)
		if len(raw) == 0 {
			continue
		}
		var row catalogRow
		if json.Unmarshal(raw, &row) != nil || row.Key == "" {
			continue
		}
		if len(row.PermittedCommands) > 0 {
			view.Operations = append(view.Operations, catalogOperation{
				CanonicalName:     row.CanonicalName,
				Description:       row.Description,
				PermittedCommands: row.PermittedCommands,
				InputSchema:       row.InputSchema,
				FieldDescriptions: row.FieldDescriptions,
			})
			continue
		}
		if !hasSpec(row.Spec) {
			continue
		}
		artifact := catalogArtifact{
			CanonicalName: row.CanonicalName,
			Description:   row.Description,
			Spec:          sanitizeLensSpec(row.Spec),
		}
		switch row.Class {
		case metaClassLens:
			view.Lenses = append(view.Lenses, artifact)
			// Index by canonicalName, keeping the first (installed canonicalNames
			// are unique; first-wins is deterministic under the sorted walk).
			// Only a real NanoID enters the map, so a resolved lensRef is always
			// install-legal.
			if id := strings.TrimPrefix(row.Key, metaKeyPrefix); id != row.Key && substrate.IsValidNanoID(id) {
				if _, seen := index[row.CanonicalName]; !seen && row.CanonicalName != "" {
					index[row.CanonicalName] = id
				}
			}
		case metaClassWeaverTarget:
			view.WeaverTargets = append(view.WeaverTargets, artifact)
		case metaClassLoomPattern:
			view.LoomPatterns = append(view.LoomPatterns, artifact)
		}
	}
	return view, index
}

// metaKeyPrefix is the Contract #1 meta-vertex key prefix; the bare NanoID after
// it is the id the Weaver control surface resolves a lensRef to.
const metaKeyPrefix = "vtx.meta."

// sensitiveLensTargetConfigKeys are the lens targetConfig subfields that must
// never reach the vendor: the Postgres DSN (a live credential) and the
// row-level-security posture (which columns are holder-gated, whether the lens
// is protected, and the grant wiring). The author needs the cypher shape,
// column names, canonicalName and description — none of these
// (internal/pkgmgr/build.go lensSpecBody).
var sensitiveLensTargetConfigKeys = []string{"dsn", "secureColumns", "protected", "grantTable", "grantSource"}

// sanitizeLensSpec strips the sensitive targetConfig subfields from a lens spec
// before it enters the prompt. A spec that does not parse as an object is
// dropped entirely (returned as JSON null) rather than passed through — a
// partially-understood body must never leak. A non-lens spec has no
// targetConfig, so it round-trips unchanged (bar key reordering, which does not
// matter for a prompt).
func sanitizeLensSpec(spec json.RawMessage) json.RawMessage {
	var m map[string]any
	if err := json.Unmarshal(spec, &m); err != nil {
		return json.RawMessage("null")
	}
	if tc, ok := m["targetConfig"].(map[string]any); ok {
		for _, k := range sensitiveLensTargetConfigKeys {
			delete(tc, k)
		}
		m["targetConfig"] = tc
	}
	out, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage("null")
	}
	return out
}

// hasSpec reports whether a row's spec column carries a body rather than the
// JSON null every non-artifact meta projects there.
func hasSpec(spec json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(spec))
	return trimmed != "" && trimmed != "null"
}

// capCatalogView bounds the view to the payload budget, dropping the
// least-relevant rows first — style-example weaver targets, then loom patterns,
// then operations, and lenses last (a target must bind to one). It enforces a
// total row count and a serialized-byte ceiling, and reports whether it dropped
// anything. The last lens is never dropped, so the caller's "at least one lens"
// guarantee (checked before capping) survives.
func capCatalogView(view *catalogView) bool {
	truncated := false
	for (totalCatalogRows(view) > maxCatalogRows || catalogViewSize(view) > maxCatalogBytes) && dropLowestPriorityRow(view) {
		truncated = true
	}
	return truncated
}

// totalCatalogRows counts every row across the four groups.
func totalCatalogRows(view *catalogView) int {
	return len(view.Lenses) + len(view.WeaverTargets) + len(view.LoomPatterns) + len(view.Operations)
}

// catalogViewSize is the serialized byte size of the view — the quantity the
// NATS payload wall actually bounds.
func catalogViewSize(view *catalogView) int {
	body, err := json.Marshal(view)
	if err != nil {
		return 0
	}
	return len(body)
}

// dropLowestPriorityRow removes one row from the lowest-priority non-empty group
// and reports whether it removed anything. Priority (kept longest): lenses,
// operations, loom patterns, weaver targets. A single lens is never dropped.
func dropLowestPriorityRow(view *catalogView) bool {
	switch {
	case len(view.WeaverTargets) > 0:
		view.WeaverTargets = view.WeaverTargets[:len(view.WeaverTargets)-1]
	case len(view.LoomPatterns) > 0:
		view.LoomPatterns = view.LoomPatterns[:len(view.LoomPatterns)-1]
	case len(view.Operations) > 0:
		view.Operations = view.Operations[:len(view.Operations)-1]
	case len(view.Lenses) > 1:
		view.Lenses = view.Lenses[:len(view.Lenses)-1]
	default:
		return false
	}
	return true
}
