// Package capability implements the lattice capability command group — the
// CLI review-and-apply affordance for AI-authored capability proposals
// (ai-authored-capabilities-design.md §3.3, Fire 2's remaining checkpoint
// item). Mirrors cmd/lattice/candidates' list-lens + submit-op shape.
package capability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asolgan/lattice/cmd/lattice/output"
	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	capabilityauthor "github.com/asolgan/lattice/packages/capability-author"
)

// proposalsBucket is the NATS-KV bucket the capability-author package's
// capabilityProposals Lens projects into — kept in lockstep with the Lens
// declaration itself (packages/capability-author/lenses.go) rather than a
// second literal that could silently drift.
const proposalsBucket = capabilityauthor.CapabilityProposalsBucket

// proposalRow mirrors the capabilityProposalsSpec lens's output columns
// (packages/capability-author/lenses.go).
type proposalRow struct {
	Key                 string  `json:"key"`
	ProposalKey         string  `json:"proposalKey"`
	RequesterID         string  `json:"requesterId"`
	Intent              string  `json:"intent"`
	Kind                string  `json:"kind"`
	Content             string  `json:"content"`
	TargetMode          string  `json:"targetMode"`
	TargetPackageName   string  `json:"targetPackageName"`
	Rationale           string  `json:"rationale"`
	Confidence          float64 `json:"confidence"`
	ValidationState     string  `json:"validationState"`
	ValidationReport    string  `json:"validationReport"`
	ReviewState         string  `json:"reviewState"`
	ReviewInvalidReason string  `json:"reviewInvalidReason"`
	ReviewedAt          string  `json:"reviewedAt"`
	AppliedAt           string  `json:"appliedAt"`
}

// NewCommand returns the cobra.Command for the capability command group.
func NewCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "capability",
		Short: "Inspect and review AI-authored capability proposals",
	}
	cmd.AddCommand(newListCommand(natsURL, outputFmt))
	cmd.AddCommand(newReviewCommand(natsURL, outputFmt, defaultActor))
	return cmd
}

func newListCommand(natsURL, outputFmt *string) *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List AI-authored capability proposals from the capabilityProposals Lens",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), output.DefaultTimeout)
			defer cancel()

			conn, err := output.Connect(ctx, *natsURL)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ConnectionError", err.Error())
				}
				return err
			}
			defer conn.Close()

			rows, err := readProposals(ctx, conn)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ListError", err.Error())
				}
				return err
			}

			if !all {
				filtered := make([]proposalRow, 0, len(rows))
				for _, r := range rows {
					if r.ReviewState == "pending" {
						filtered = append(filtered, r)
					}
				}
				rows = filtered
			}

			if *outputFmt == "json" {
				return output.PrintJSON(rows)
			}
			if len(rows) == 0 {
				fmt.Println("(no proposals)")
				return nil
			}
			fmt.Printf("%-38s %-10s %-30s %-10s %s\n", "PROPOSAL_KEY", "KIND", "TARGET_PACKAGE", "STATE", "INTENT")
			for _, r := range rows {
				fmt.Printf("%-38s %-10s %-30s %-10s %s\n", r.ProposalKey, r.Kind, r.TargetPackageName, r.ReviewState, r.Intent)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "show every proposal, not just pending ones")
	return cmd
}

func newReviewCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var approve, reject bool

	cmd := &cobra.Command{
		Use:   "review <proposalId>",
		Short: "Approve or reject a pending capability proposal",
		Long: `review submits a ReviewCapabilityProposal verdict for a pending
AI-authored capability proposal (design §3.3).

A reject needs no re-check. An approve re-runs the record-time §5
deterministic-validation boundary against the LIVE catalog (the openCypher
parser for a "lens" artifact; the requester's currently-held permissions for
a "grant" artifact) and attaches the fresh verdict — the Processor fail-
closes the approve to invalid if it no longer validates.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			if actor == "" {
				return fmt.Errorf("--actor is required (or set via credential file)")
			}
			if approve == reject {
				return fmt.Errorf("exactly one of --approve or --reject is required")
			}
			proposalID := args[0]
			if err := validateBareID(proposalID); err != nil {
				return fmt.Errorf("proposalId: %w", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), output.DefaultTimeout)
			defer cancel()

			conn, err := output.Connect(ctx, *natsURL)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ConnectionError", err.Error())
				}
				return err
			}
			defer conn.Close()

			payload := map[string]any{"proposalId": proposalID}
			if approve {
				verdict, err := freshApprovalVerdict(ctx, conn, proposalID)
				if err != nil {
					if *outputFmt == "json" {
						return output.PrintJSONError("ValidationError", err.Error())
					}
					return err
				}
				payload["verdict"] = "approve"
				payload["validation"] = verdict
			} else {
				payload["verdict"] = "reject"
			}

			payloadBytes, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("marshal payload: %w", err)
			}

			requestID, err := substrate.NewNanoID()
			if err != nil {
				return fmt.Errorf("generate requestId: %w", err)
			}
			proposalKey := "vtx.capabilityproposal." + proposalID
			env := &processor.OperationEnvelope{
				RequestID:     requestID,
				Lane:          processor.LaneDefault,
				OperationType: "ReviewCapabilityProposal",
				Actor:         actor,
				SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
				Payload:       json.RawMessage(payloadBytes),
				// read-posture class (a) — proposalId is addressed directly by
				// the caller, no claim indirection (script-read-posture-design
				// §13).
				ContextHint: &processor.ContextHint{Reads: []string{proposalKey + ".review"}},
			}

			reply, err := output.SubmitOp(ctx, conn, env)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("SubmitError", err.Error())
				}
				return err
			}
			if reply.Status == processor.ReplyStatusRejected {
				if *outputFmt == "json" {
					return output.PrintJSONError(string(reply.Error.Code), reply.Error.Message)
				}
				fmt.Fprintf(os.Stderr, "rejected: %s — %s\n", reply.Error.Code, reply.Error.Message)
				os.Exit(1)
			}
			if *outputFmt == "json" {
				return output.PrintJSON(reply)
			}
			fmt.Printf("requestId:    %s\nopTrackerKey: %s\nstatus:       %s\n",
				reply.RequestID, reply.OpTrackerKey, reply.Status)
			return nil
		},
	}

	cmd.Flags().StringVar(&actor, "actor", "", "actor key (defaults to credential file actorKey)")
	cmd.Flags().BoolVar(&approve, "approve", false, "approve the proposal")
	cmd.Flags().BoolVar(&reject, "reject", false, "reject the proposal")
	return cmd
}

// freshApprovalVerdict re-runs the §5 deterministic-validation boundary
// against the LIVE catalog/registry for the named pending proposal
// (ai-authored-capabilities-design.md §5 point 3 — record-time and
// approve-time can drift) and returns the {state, report} payload the
// ReviewCapabilityProposal op's "validation" field requires on an approve.
func freshApprovalVerdict(ctx context.Context, conn *substrate.Conn, proposalID string) (map[string]any, error) {
	row, err := readProposal(ctx, conn, proposalID)
	if err != nil {
		return nil, err
	}
	if row.ReviewState != "pending" {
		return nil, fmt.Errorf("proposal %s is %q, not pending", proposalID, row.ReviewState)
	}

	var held []pkgmgr.HeldPermission
	if row.Kind == "grant" {
		held, err = heldPermissionsForActor(ctx, conn, row.RequesterID)
		if err != nil {
			return nil, fmt.Errorf("read requester %s held permissions: %w", row.RequesterID, err)
		}
	}

	report, err := pkgmgr.ValidateCapabilityArtifact(row.Kind, json.RawMessage(row.Content), fullCypherParser{}, held)
	if err != nil {
		return nil, fmt.Errorf("validate artifact: %w", err)
	}

	verdict := map[string]any{}
	if report.Valid {
		verdict["state"] = "valid"
	} else {
		verdict["state"] = "invalid"
		verdict["report"] = strings.Join(report.Errors, "; ")
	}
	return verdict, nil
}

// heldPermissionsForActor reads the actor's live Contract #6 §6.1 capability
// projection from the capability-kv bucket (bootstrap.CapabilityKVBucket —
// the platform-scope "cap.<rest>" key and the role-derived "cap.roles.<rest>"
// key, the same disjoint keys internal/processor/step3_auth_capability.go's
// readAndMergeDoc reads) and
// returns the union of both docs' platformPermissions as HeldPermission —
// the basis for the "grant" kind's §5 scope check. A key that doesn't exist
// contributes no permissions (deny-closed union, not an error) — mirrors the
// Processor authorizer's own "absent key is an empty skip" posture.
func heldPermissionsForActor(ctx context.Context, conn *substrate.Conn, actor string) ([]pkgmgr.HeldPermission, error) {
	rest, ok := strings.CutPrefix(actor, "vtx.")
	if !ok {
		return nil, fmt.Errorf("actor %q lacks vtx. prefix", actor)
	}

	var held []pkgmgr.HeldPermission
	for _, key := range []string{"cap." + rest, "cap.roles." + rest} {
		entry, err := conn.KVGet(ctx, bootstrap.CapabilityKVBucket, key)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				continue // absent key = no permissions from this source, not an error
			}
			return nil, fmt.Errorf("read %s: %w", key, err)
		}
		doc, err := processor.ParseCapabilityDoc(entry.Value)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", key, err)
		}
		for _, p := range doc.PlatformPermissions {
			held = append(held, pkgmgr.HeldPermission{OperationType: p.OperationType, Scope: p.Scope})
		}
	}
	return held, nil
}

// validateBareID rejects a proposal id carrying key-shape metacharacters —
// the same bare-id discipline the capabilityproposal DDL script itself
// enforces (required_bare_id in packages/capability-author/ddls.go). Without
// this, a proposal id containing "." would silently address a different (or
// malformed) KV key instead of failing with a clear message.
func validateBareID(id string) error {
	if id == "" {
		return fmt.Errorf("must not be empty")
	}
	if strings.ContainsAny(id, ".*> \t\n") {
		return fmt.Errorf("must carry no dots / key segments, wildcards, or whitespace; got %q", id)
	}
	return nil
}

// readProposal reads a single proposal row by its bare proposal id.
func readProposal(ctx context.Context, conn *substrate.Conn, proposalID string) (*proposalRow, error) {
	if err := validateBareID(proposalID); err != nil {
		return nil, fmt.Errorf("proposalId: %w", err)
	}
	key := "vtx.capabilityproposal." + proposalID
	entry, err := conn.KVGet(ctx, proposalsBucket, key)
	if err != nil {
		return nil, fmt.Errorf("read %s from %s: %w (is the proposal id correct, and has RecordCapabilityProposal run?)", key, proposalsBucket, err)
	}
	var row proposalRow
	if err := json.Unmarshal(entry.Value, &row); err != nil {
		return nil, fmt.Errorf("parse %s: %w", key, err)
	}
	return &row, nil
}

// readProposals lists every row in the capabilityProposals Lens.
func readProposals(ctx context.Context, conn *substrate.Conn) ([]proposalRow, error) {
	keys, err := conn.KVListKeys(ctx, proposalsBucket)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w (is capability-author package installed?)", proposalsBucket, err)
	}
	rows := make([]proposalRow, 0, len(keys))
	for _, k := range keys {
		entry, err := conn.KVGet(ctx, proposalsBucket, k)
		if err != nil {
			continue
		}
		var row proposalRow
		if err := json.Unmarshal(entry.Value, &row); err != nil {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}
