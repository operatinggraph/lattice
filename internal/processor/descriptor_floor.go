package processor

import (
	"log/slog"
	"strings"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// applyDescriptorFloor enforces Contract #2 §2.5's "the descriptor's
// disposition is a floor the envelope cannot raise": where an operation's
// op-meta descriptor declares a key under `optionalReads`, an envelope naming
// that same key under `reads` MUST NOT harden it — the merged disposition is
// the weaker of the two.
//
// # Why this is the enforcement point and the dispatchers were not
//
// contextHint is client-supplied end to end. The Gateway copies `reads`
// verbatim out of the request body (internal/gateway/gateway.go) and step 3
// authorizes on operationType + actor + authContext without inspecting it, so
// converting our own dispatchers to `optionalReads` closed our clients and
// nobody else's. On an anti-enumeration path that gap is the whole exposure:
// an operation whose script adjudicates an absent target generically — one
// indistinguishable rejection for "no such entity" and "wrong secret" — is
// re-opened as an existence oracle by any caller who declares the target
// `reads` and reads `HydrationMiss`'s `details.missingKey`, one guess per
// request. This is the seam where the submitter's declaration stops being the
// last word.
//
// # Precedence
//
// The floor is applied to the ENVELOPE's declaration, at the head of step 4,
// BEFORE `derive_reads` runs and therefore before mergeDerivedReads. Three
// consequences, in the order they matter:
//
//  1. mergeDerivedReads' "envelope's disposition stands" rule then sees the
//     POST-demotion envelope. A derived `reads` entry colliding with a demoted
//     key finds it already declared and skips it, so the key keeps the weaker
//     disposition — the same weakest-wins answer the derived-vs-envelope rule
//     already gives, reached without a second rule.
//
//  2. The demotion MOVES the key rather than copying it. step4_hydrate's
//     "a key in both lists keeps fail-closed reads semantics" would otherwise
//     re-harden on the very next loop, silently undoing this.
//
//  3. `egressReads` is floored too, but by a DIFFERENT action, because the
//     obvious one is a plaintext leak. Moving an egress key into optionalReads
//     would hand the script the decrypted body where the submitter asked for a
//     `$sensitiveRef` the bridge opens — a disposition change in the dangerous
//     direction. What the oracle actually rides on is narrower: an absent
//     egress key calls the same markRequiredAbsent as an absent `reads` key
//     (step4_hydrate), and RequiredAbsent is a FLAT map that does not record
//     which list put a key there, so `contextHint.egressReads: [<guess>]`
//     reproduces HydrationMiss + details.missingKey verbatim. So a floored
//     egress key STAYS in egressReads — presence still authors the ref,
//     unchanged — and only its ABSENCE is made tolerant, via
//     EgressAbsenceTolerant. Nothing about a present document moves.
//
//     The collision rules are unaffected either way: ParseEnvelope rejects a
//     key declared under egressReads AND either read list, the Reads→
//     OptionalReads move keeps both sides inside "either read list", and the
//     egress arm moves nothing at all — so a demotion can neither create such
//     a collision nor mask one, and mergeDerivedReads' re-check over the
//     merged set sees the egress set it would have seen.
//
// # Direction of failure
//
// An operation with NO descriptor demotes nothing. That is the ordinary case
// — most of the corpus carries no Dispatch block — and it is not a silent
// hardening: the rule has no subject, because there is no descriptor declaring
// the key optional. The same holds for a descriptor whose optionalReads
// templates the Processor cannot resolve (`{me.<type>}`, `{entity.<column>}`
// name a key derived from the CLIENT's projected view, which the Processor
// does not have). Those are logged, not guessed at.
//
// Demoting on doubt is deliberately NOT the fallback, and the asymmetry is
// worth stating: demotion is the weaker disposition for the oracle but the
// DANGEROUS one for correctness — softening a read the operation genuinely
// depends on turns a HydrationMiss into a silent None, which is the failure
// the §2.5 authoring rule exists to prevent. So the floor demotes exactly the
// keys a resolvable descriptor names, and nothing else.
func applyDescriptorFloor(base declaredReads, templates []string, env *OperationEnvelope, logger *slog.Logger) declaredReads {
	if len(templates) == 0 || (len(base.Reads) == 0 && len(base.EgressReads) == 0) {
		return base
	}
	floor := resolveDescriptorFloor(templates, env, logger)
	if len(floor) == 0 {
		return base
	}

	// The egress arm: mark, never move. See the header's precedence note 3 —
	// relocating an egress key would swap a bridge-opened ref for plaintext.
	for _, k := range base.EgressReads {
		if _, isFloor := floor[k]; !isFloor {
			continue
		}
		if base.EgressAbsenceTolerant == nil {
			base.EgressAbsenceTolerant = map[string]struct{}{}
		}
		base.EgressAbsenceTolerant[k] = struct{}{}
	}

	optional := make(map[string]struct{}, len(base.OptionalReads))
	for _, k := range base.OptionalReads {
		optional[k] = struct{}{}
	}

	// Both lists are rebuilt rather than sliced in place: base.Reads is the
	// envelope's own JSON-decoded backing array, and mergeDerivedReads states
	// the same invariant for the same reason — nothing here may write into
	// storage the envelope owns.
	kept := make([]string, 0, len(base.Reads))
	demoted := make([]string, 0, len(base.Reads))
	for _, k := range base.Reads {
		if _, isFloor := floor[k]; !isFloor {
			kept = append(kept, k)
			continue
		}
		demoted = append(demoted, k)
		if _, dup := optional[k]; dup {
			// Already declared optional too. Dropping it from Reads is the
			// whole demotion: leaving it would let step4_hydrate's
			// both-lists rule keep the fail-closed reading.
			continue
		}
		optional[k] = struct{}{}
		base.OptionalReads = append(append([]string{}, base.OptionalReads...), k)
	}
	if len(demoted) == 0 {
		return base
	}
	base.Reads = kept
	logger.Info("step4: descriptor floor demoted declared reads to optional",
		"operationType", env.OperationType, "requestId", env.RequestID, "keys", demoted)
	return base
}

// resolveDescriptorFloor substitutes a descriptor's optionalReads templates
// against this envelope and returns the concrete keys among them.
//
// A template that still carries an unresolved placeholder after substitution
// yields NO key — the descriptor is not naming a determinate key for this
// envelope, so there is nothing for the floor to be about. Same for a
// substitution that leaves a hole (an empty segment): that is not a shorter
// key, it is a different and invalid one, and the client-side resolver
// (cmd/facet/web/app.js's wholeKey) drops it for the same reason.
func resolveDescriptorFloor(templates []string, env *OperationEnvelope, logger *slog.Logger) map[string]struct{} {
	var payload map[string]interface{}
	if len(env.Payload) > 0 {
		payload, _ = jsonToGenericMap(env.Payload)
	}
	floor := make(map[string]struct{}, len(templates))
	for _, tpl := range templates {
		key, ok := substituteDescriptorTemplate(tpl, env, payload)
		if !ok {
			// WARN, not Debug, and the level is the point: "the control did
			// not apply" has to be at least as visible as "the control fired",
			// or the only thing production shows is the enforced case and the
			// gap is invisible at exactly the levels operators run.
			//
			// Not a defect in the descriptor — the client-only vocabulary is
			// legitimate — but it does mean this key has no floor, so the
			// envelope's own disposition is the last word for it.
			logger.Warn("step4: descriptor optionalRead template is not server-resolvable; NO floor applied for this key",
				"operationType", env.OperationType, "requestId", env.RequestID, "template", tpl)
			continue
		}
		floor[key] = struct{}{}
	}
	return floor
}

// substituteDescriptorTemplate expands the server-resolvable half of the
// descriptor key-template vocabulary (the whole vocabulary lives in
// cmd/facet/web/app.js's substituteTemplate, which is the authority a client
// resolves with).
//
// Resolvable here: `{actor}`, `{service}`, `{payload.<field>}`, each with the
// optional `:id` suffix that asks for the Contract #1 bare id instead of the
// whole vertex key. Not resolvable: `{me.<type>}` and `{entity.<column>}`,
// which name a vertex out of the caller's own projected view — the edge
// manifest's self-anchors and the row being displayed — neither of which the
// Processor has or should reconstruct.
//
// Returns ok=false when any placeholder is left unexpanded or the expansion
// leaves an empty segment.
func substituteDescriptorTemplate(tpl string, env *OperationEnvelope, payload map[string]interface{}) (string, bool) {
	var b strings.Builder
	rest := tpl
	for {
		open := strings.Index(rest, "{")
		if open < 0 {
			b.WriteString(rest)
			break
		}
		close := strings.Index(rest[open:], "}")
		if close < 0 {
			return "", false
		}
		close += open
		b.WriteString(rest[:open])
		expr := rest[open+1 : close]
		rest = rest[close+1:]

		bareID := false
		if strings.HasSuffix(expr, ":id") {
			bareID = true
			expr = strings.TrimSuffix(expr, ":id")
		}
		var val string
		switch {
		case expr == "actor":
			val = env.Actor
		case expr == "service":
			if env.AuthContext != nil {
				val = env.AuthContext.Service
			}
		case strings.HasPrefix(expr, "payload."):
			s, isString := payload[strings.TrimPrefix(expr, "payload.")].(string)
			if !isString {
				return "", false
			}
			val = s
		default:
			return "", false
		}
		if val == "" {
			return "", false
		}
		if bareID {
			_, id, parsed := substrate.ParseVertexKey(val)
			if !parsed {
				return "", false
			}
			val = id
		}
		b.WriteString(val)
	}
	out := b.String()
	if out == "" || strings.Contains(out, "{") {
		return "", false
	}
	for _, seg := range strings.Split(out, ".") {
		if seg == "" {
			return "", false
		}
	}
	return out, true
}
