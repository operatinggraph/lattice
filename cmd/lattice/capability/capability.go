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

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	capabilityauthor "github.com/operatinggraph/lattice/packages/capability-author"
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
	var bootstrapJSONPath string

	cmd := &cobra.Command{
		Use:   "capability",
		Short: "Inspect and review AI-authored capability proposals",
	}
	// An approve of a "grant" artifact resolves the system-actor set, whose
	// discovery predicate is keyed on the primordial roleOperator NanoID, so
	// this group reads the same bootstrap file the platform daemons load at
	// start-up. Same flag + env source as `lattice bootstrap`
	// (cmd/lattice/bootstrap/bootstrap.go:23).
	cmd.PersistentFlags().StringVar(&bootstrapJSONPath, "bootstrap-json", "./lattice.bootstrap.json",
		"path to lattice.bootstrap.json (env: BOOTSTRAP_JSON_PATH)")

	cmd.AddCommand(newListCommand(natsURL, outputFmt))
	cmd.AddCommand(newReviewCommand(natsURL, outputFmt, defaultActor, &bootstrapJSONPath))
	return cmd
}

// errBootstrapIdentifiers marks a failure to obtain the primordial identifier
// table — a machine/configuration fault, not a verdict on the proposal. It
// exists so `-o json` can report it under its own code: a caller must be able
// to tell "this machine cannot validate the proposal" from "this proposal no
// longer validates", and both funnel through the same return. `lattice
// bootstrap` draws the same distinction with the same code
// (cmd/lattice/bootstrap/bootstrap.go:114).
var errBootstrapIdentifiers = errors.New("bootstrap identifiers unavailable")

// resolveBootstrapJSONPath applies the BOOTSTRAP_JSON_PATH override to the
// --bootstrap-json flag value, matching `lattice bootstrap`'s precedence
// (cmd/lattice/bootstrap/bootstrap.go:110): the environment wins, so a
// deployment that exports it once needs no per-invocation flag.
func resolveBootstrapJSONPath(flagValue string) string {
	if envPath := os.Getenv("BOOTSTRAP_JSON_PATH"); envPath != "" {
		return envPath
	}
	return flagValue
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

func newReviewCommand(natsURL, outputFmt, defaultActor, bootstrapJSONPath *string) *cobra.Command {
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
				verdict, err := freshApprovalVerdict(ctx, conn, proposalID, resolveBootstrapJSONPath(*bootstrapJSONPath))
				if err != nil {
					if *outputFmt == "json" {
						code := "ValidationError"
						if errors.Is(err, errBootstrapIdentifiers) {
							code = "BootstrapLoadError"
						}
						return output.PrintJSONError(code, err.Error())
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
func freshApprovalVerdict(ctx context.Context, conn *substrate.Conn, proposalID, bootstrapJSONPath string) (map[string]any, error) {
	row, err := readProposal(ctx, conn, proposalID)
	if err != nil {
		return nil, err
	}
	if row.ReviewState != "pending" {
		return nil, fmt.Errorf("proposal %s is %q, not pending", proposalID, row.ReviewState)
	}

	var held []pkgmgr.HeldPermission
	if row.Kind == "grant" {
		// The system-actor set decides the requester's key routing
		// (capabilitykv.ClassAwarePlatformKey), so it is discovered from the
		// live graph here. Its predicate matches holdsRole links against the
		// primordial roleOperator NanoID, which lives in the bootstrap file
		// and nowhere else, so the identifier table is loaded first: without
		// it the predicate matches nothing and every actor — the primordial
		// admin and the kernel service actors included — routes as ordinary.
		//
		// Both reads sit inside the "grant" branch because no other artifact
		// kind consults either: an approve of a lens or opMeta proposal needs
		// no bootstrap file present, and pays for no core-kv listing. This is
		// a one-shot CLI invocation, so that listing runs once per approve.
		if lErr := bootstrap.Load(bootstrapJSONPath); lErr != nil {
			return nil, fmt.Errorf("%w: load from %s (set --bootstrap-json or BOOTSTRAP_JSON_PATH): %w",
				errBootstrapIdentifiers, bootstrapJSONPath, lErr)
		}
		systemActorKeys, sErr := bootstrap.SystemActorKeys(ctx, conn)
		if sErr != nil {
			if errors.Is(sErr, bootstrap.ErrPrimordialIDsUnloaded) {
				return nil, fmt.Errorf("%w: %w", errBootstrapIdentifiers, sErr)
			}
			return nil, fmt.Errorf("discover system actor keys: %w", sErr)
		}
		held, err = pkgmgr.ReadHeldPermissions(ctx, conn, systemActorKeys, row.RequesterID)
		if err != nil {
			return nil, fmt.Errorf("read requester %s held permissions: %w", row.RequesterID, err)
		}
	}

	var sensitiveAspects pkgmgr.SensitiveAspectResolver
	if row.Kind == "opMeta" {
		sensitiveAspects, err = newLiveSensitiveAspectResolver(ctx, conn)
		if err != nil {
			return nil, fmt.Errorf("load live DDL catalog for sensitive-aspect check: %w", err)
		}
	}

	report, err := pkgmgr.ValidateCapabilityArtifact(row.Kind, json.RawMessage(row.Content), fullCypherParser{}, held, sensitiveAspects)
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

// ddlCacheSensitiveResolver adapts internal/processor.DDLCache to
// pkgmgr.SensitiveAspectResolver: an aspectType DDL's CanonicalName IS the
// bare aspect local name (e.g. "ssn"/"dob" — packages/identity-domain/ddls.go),
// so Lookup(aspectLocalName).Sensitive is exactly the live authority the §5
// condition-2 rule-2 check (sensitive-ref-mac-provenance-design.md §7) needs.
type ddlCacheSensitiveResolver struct {
	cache *processor.DDLCache
}

func (r ddlCacheSensitiveResolver) IsSensitiveAspect(aspectLocalName string) bool {
	ref, ok := r.cache.Lookup(aspectLocalName)
	return ok && ref.Sensitive
}

// newLiveSensitiveAspectResolver builds a pkgmgr.SensitiveAspectResolver
// backed by a one-shot DDLCache scan of the live catalog — the approve-time
// freshness re-check §5 requires (the record-time verdict may be stale by
// the time an operator approves; this CLI always re-validates against what's
// actually installed NOW, same posture as heldPermissionsForActor's live
// read for the grant kind).
func newLiveSensitiveAspectResolver(ctx context.Context, conn *substrate.Conn) (pkgmgr.SensitiveAspectResolver, error) {
	cache := processor.NewDDLCache(conn, bootstrap.CoreKVBucket, nil)
	if err := cache.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("refresh DDL cache: %w", err)
	}
	return ddlCacheSensitiveResolver{cache: cache}, nil
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
