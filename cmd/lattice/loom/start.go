package loom

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// loomPatternClass is the meta-vertex class a Loom pattern definition carries.
// The same literal Weaver's registry indexes on.
const loomPatternClass = "meta.loomPattern"

// newStartCommand creates the `loom start` subcommand — the operator's way to
// begin a pattern instance.
//
// This is an OPERATION, not a control-plane request: the other subcommands in
// this group talk to lattice.ctrl.loom.* because they inspect and steer the
// engine, whereas starting a pattern is a state change and so goes through the
// Processor like any other write (P2). `lattice op submit --operation-type
// StartLoomPattern` can construct the same envelope by hand; what this adds is
// the two things a hand-rolled submit gets wrong — resolving a pattern's
// canonical name to the meta-vertex key, and stamping that key as
// authContext.target, which per-pattern authorization anchors on (Contract #10
// §10.8).
func newStartCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var subject string
	var instanceID string

	cmd := &cobra.Command{
		Use:   "start <patternRef>",
		Short: "Start a Loom pattern instance for a subject",
		Long: `start submits StartLoomPattern for the named pattern and subject.

<patternRef> is either a pattern's canonical name (e.g. identityErasure) or its
meta-vertex key (vtx.meta.<NanoID>). A canonical name is resolved against the
installed meta.loomPattern vertices, and the resolved key is what the operation
carries — both as the payload's patternRef and as authContext.target.`,
		Example: `  lattice loom start identityErasure --subject vtx.identity.<NanoID>
  lattice loom start vtx.meta.<NanoID> --subject vtx.identity.<NanoID>`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			if actor == "" {
				return fmt.Errorf("--actor is required (or set via credential file)")
			}
			if strings.TrimSpace(subject) == "" {
				return fmt.Errorf("--subject is required (the vertex the instance runs for)")
			}

			ctx, cancel := context.WithTimeout(context.Background(), output.DefaultTimeout)
			defer cancel()

			conn, err := output.Connect(ctx, *natsURL)
			if err != nil {
				if *outputFmt == "json" {
					_ = output.PrintJSONError("ConnectionError", err.Error())
					os.Exit(1)
				}
				return err
			}
			defer conn.Close()

			patternKey, err := resolvePatternKey(ctx, conn, args[0])
			if err != nil {
				if *outputFmt == "json" {
					_ = output.PrintJSONError("PatternNotFound", err.Error())
					os.Exit(1)
				}
				return err
			}

			requestID, err := substrate.NewNanoID()
			if err != nil {
				return fmt.Errorf("generate requestId: %w", err)
			}

			payload := map[string]string{
				"patternRef": patternKey,
				"subjectKey": strings.TrimSpace(subject),
			}
			if id := strings.TrimSpace(instanceID); id != "" {
				payload["instanceId"] = id
			}
			payloadBytes, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("marshal payload: %w", err)
			}

			env := &processor.OperationEnvelope{
				RequestID:     requestID,
				Lane:          processor.LaneDefault,
				OperationType: "StartLoomPattern",
				Actor:         actor,
				SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
				Class:         "loomLifecycle",
				Payload:       json.RawMessage(payloadBytes),
				// Pattern-as-target (Contract #10 §10.8): per-pattern
				// authorization anchors on the pattern definition vertex, which
				// is why the reference is resolved before submitting rather
				// than passed through.
				AuthContext: &processor.AuthContext{Target: patternKey},
			}

			reply, err := output.SubmitOp(ctx, conn, env)
			if err != nil {
				if *outputFmt == "json" {
					_ = output.PrintJSONError("SubmitError", err.Error())
					os.Exit(1)
				}
				return fmt.Errorf("submit: %w", err)
			}
			if reply.Status == processor.ReplyStatusRejected {
				if *outputFmt == "json" {
					_ = output.PrintJSONError(string(reply.Error.Code), reply.Error.Message)
				} else {
					fmt.Fprintf(os.Stderr, "rejected: %s — %s\n", reply.Error.Code, reply.Error.Message)
				}
				os.Exit(1)
			}

			// The instance id is the caller-supplied one when given, else the
			// op's requestId — the default the DDL applies, echoed here so the
			// operator can hand it straight to `loom inspect`.
			landedInstance := requestID
			if id := strings.TrimSpace(instanceID); id != "" {
				landedInstance = id
			}
			if *outputFmt == "json" {
				return output.PrintJSON(map[string]string{
					"requestId":  reply.RequestID,
					"instanceId": landedInstance,
					"patternKey": patternKey,
					"subjectKey": strings.TrimSpace(subject),
					"status":     string(reply.Status),
				})
			}
			fmt.Printf("requestId:  %s\ninstanceId: %s\npattern:    %s\nsubject:    %s\nstatus:     %s\n",
				reply.RequestID, landedInstance, patternKey, strings.TrimSpace(subject), reply.Status)
			fmt.Printf("\ninspect it with: lattice loom inspect %s\n", landedInstance)
			return nil
		},
	}

	cmd.Flags().StringVar(&subject, "subject", "", "subject vertex key the instance runs for (vtx.<type>.<NanoID>)")
	cmd.Flags().StringVar(&instanceID, "instance-id", "", "optional stable instance id (bare NanoID); absent → the op's requestId")
	cmd.Flags().StringVar(&actor, "actor", "", "actor key (defaults to credential file actorKey)")
	_ = cmd.MarkFlagRequired("subject")
	return cmd
}

// resolvePatternKey turns a pattern reference into the meta-vertex key the
// operation must carry. Three forms resolve, the same three Weaver's registry
// accepts from a playbook (`internal/weaver/registry.go` indexPattern): the
// pattern's own `patternId`, the bare vertex NanoID, and the full
// `vtx.meta.<NanoID>` key.
//
// A key reference is VERIFIED, not trusted — a reference naming a meta-vertex
// of some other class would authorize against the wrong vertex, and the refusal
// that follows names neither the pattern nor the reason.
//
// The name comes off the pattern's `.spec` aspect, which is where a patternId
// lives; a loom pattern carries no `.canonicalName` aspect, so the reader
// `lattice lens` uses for meta-vertices finds nothing here.
func resolvePatternKey(ctx context.Context, conn *substrate.Conn, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("pattern reference must not be empty")
	}

	patterns, err := loomPatterns(ctx, conn)
	if err != nil {
		return "", err
	}

	var matched []string
	for _, p := range patterns {
		if ref == p.key || ref == p.vertexID || (p.patternID != "" && ref == p.patternID) {
			matched = append(matched, p.key)
		}
	}

	switch len(matched) {
	case 1:
		return matched[0], nil
	case 0:
		if strings.HasPrefix(ref, "vtx.meta.") {
			return "", fmt.Errorf("%s is not a live meta.loomPattern vertex (installed patterns: %s)",
				ref, patternNames(patterns))
		}
		return "", fmt.Errorf("no live Loom pattern named %q (installed patterns: %s)", ref, patternNames(patterns))
	default:
		// Two live vertices answering to one reference is a corrupt registry,
		// not a caller error: picking either would start a pattern the operator
		// did not choose. Weaver's index silently keeps the last writer; an
		// operator command must not.
		return "", fmt.Errorf("pattern reference %q resolves to %d live meta.loomPattern vertices (%s) — pass the vtx.meta.<NanoID> key to disambiguate",
			ref, len(matched), strings.Join(matched, ", "))
	}
}

type patternRef struct {
	key       string
	vertexID  string
	patternID string
}

func loomPatterns(ctx context.Context, conn *substrate.Conn) ([]patternRef, error) {
	allKeys, err := conn.KVListKeys(ctx, bootstrap.CoreKVBucket)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", bootstrap.CoreKVBucket, err)
	}
	var out []patternRef
	for _, k := range allKeys {
		if !strings.HasPrefix(k, "vtx.meta.") || strings.Count(k, ".") != 2 {
			continue
		}
		entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, k)
		if err != nil {
			continue
		}
		var doc struct {
			Class     string `json:"class"`
			IsDeleted bool   `json:"isDeleted"`
		}
		if err := json.Unmarshal(entry.Value, &doc); err != nil {
			continue
		}
		if doc.Class != loomPatternClass || doc.IsDeleted {
			continue
		}
		out = append(out, patternRef{
			key:       k,
			vertexID:  strings.TrimPrefix(k, "vtx.meta."),
			patternID: specPatternID(ctx, conn, k),
		})
	}
	return out, nil
}

// specPatternID reads `patternId` off the pattern's `.spec` aspect. The body
// may be the spec itself or the spec wrapped in the standard envelope's `data`,
// so it is unwrapped on the `steps` sentinel — the same probe Weaver's registry
// applies to the same aspect.
//
// Returns "" when the aspect is absent, unreadable, tombstoned or carries no
// patternId; such a pattern is still reachable by its key or vertex id, it
// simply has no name to match.
func specPatternID(ctx context.Context, conn *substrate.Conn, vertexKey string) string {
	entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, vertexKey+".spec")
	if err != nil {
		return ""
	}
	var envelope struct {
		IsDeleted bool                       `json:"isDeleted"`
		Steps     json.RawMessage            `json:"steps"`
		Data      map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &envelope); err != nil || envelope.IsDeleted {
		return ""
	}
	body := envelope.Data
	if len(envelope.Steps) > 0 {
		// The spec is at the top level, not under `data`.
		var flat map[string]json.RawMessage
		if err := json.Unmarshal(entry.Value, &flat); err != nil {
			return ""
		}
		body = flat
	}
	raw, ok := body["patternId"]
	if !ok {
		return ""
	}
	var id string
	if err := json.Unmarshal(raw, &id); err != nil {
		return ""
	}
	return id
}

// patternNames renders the installed set for an error message. A pattern whose
// spec carries no patternId is listed by key so the operator can still reach it.
func patternNames(patterns []patternRef) string {
	if len(patterns) == 0 {
		return "none installed"
	}
	names := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if p.patternID != "" {
			names = append(names, p.patternID)
		} else {
			names = append(names, p.key)
		}
	}
	return strings.Join(names, ", ")
}
