package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/spf13/cobra"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	internalbootstrap "github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// retireScanTimeout bounds the stranded-epoch scan and the final
// re-verification scan — generous relative to verify-kernel's own budget
// because this is a deliberate, occasional operator action, not a boot-path
// check.
const retireScanTimeout = 30 * time.Second

// retireOpReplyTimeout bounds each individual op submission and each
// individual liveness check, mirroring cmd/lattice/op's own opReplyTimeout: a
// fresh deadline per call so one slow reply cannot starve the calls after it.
const retireOpReplyTimeout = 10 * time.Second

// retireRecommendationsTimeout bounds printReinstallRecommendations, which
// reads one permission per revoked grant — potentially as many as the
// deployment has stranded grants, so it gets its own generous budget rather
// than sharing the one-shot scan's.
const retireRecommendationsTimeout = 30 * time.Second

func newRetireStrandedEpochCommand(natsURL, bootstrapJSONPath, defaultActor *string) *cobra.Command {
	var (
		dryRun bool
		actor  string
	)

	cmd := &cobra.Command{
		Use:   "retire-stranded-epoch",
		Short: "Revoke a stranded prior-epoch operator role's edges; recommend the grant-restoring reinstalls",
		Long: `retire-stranded-epoch finds every live role named "operator" that this
deployment's primordial table does not name (left behind when
lattice.bootstrap.json is regenerated without a Core KV wipe — see
primordial-epoch-stranded-authority-design.md) and tombstones every live
holdsRole/grantedBy edge into it, via the existing rbac-domain
RevokeRole/RevokePermission operations. Revoking those edges kills every
projection that makes the stranded role root-equivalent (design doc §4.1)
without touching a byte of graph data.

Revocation does not restore the permissions the dead epoch's edges carried to
the CURRENT role. For each revoked grant, this command reads the permission
vertex's declaredBy field and prints the "make reinstall-package" command that
re-mints it against the current role (design doc §7 item 3) — it does not run
that command itself.

The stranded role vertex, its permission vertices, and any stranded capability
lens vertices are never touched: all three carry data.protected=true, and
retiring them needs a guard exemption this command does not have (design doc
§7 item 4 and item 2).

Before touching anything, this command verifies that the LOADED
lattice.bootstrap.json actually corresponds to the target NATS deployment
(--nats-url / the connected stack): a mismatched id file makes the scan see
the deployment's real, live, current operator role as "stranded", and
revoking it would brick the deployment. Refuses outright if that check fails.

PRECONDITION: every Processor replica must already have restarted on the
CURRENT lattice.bootstrap.json before this command runs. Each Processor holds
its own epoch's twelve kernel topology links as a protected set, composed from
the table it loaded at start-up; a replica still running on the table this
command is retiring holds the stranded epoch's edges as its own and refuses to
revoke them. This command verifies the id file against Core KV, not against the
running Processors, so it cannot check that for you — but a revocation refused
that way is reported as epoch skew by name, and the remedy is to roll the
Processors and re-run.

Exit 0 only if nothing is stranded, or every stranded epoch was fully and
verifiably neutralized (re-checked after the run, never assumed from
submission replies alone) and every reinstall recommendation was printed.
Exit 1 on a precondition failure, an unreadable edge set (a lower bound, never
treated as clean), any unresolved revocation failure, or an incomplete
recommendations pass.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonPath := *bootstrapJSONPath
			if envPath := os.Getenv("BOOTSTRAP_JSON_PATH"); envPath != "" {
				jsonPath = envPath
			}
			if err := internalbootstrap.Load(jsonPath); err != nil {
				return fmt.Errorf("load bootstrap IDs from %s: %w", jsonPath, err)
			}

			if actor == "" {
				actor = *defaultActor
			}
			if actor == "" && !dryRun {
				return fmt.Errorf("--actor is required to submit revocations (or set via credential file); pass --dry-run to preview without one")
			}

			scanCtx, cancel := context.WithTimeout(context.Background(), retireScanTimeout)
			defer cancel()

			conn, err := output.Connect(scanCtx, *natsURL)
			if err != nil {
				return err
			}
			defer conn.Close()

			kv, err := conn.JetStream().KeyValue(scanCtx, internalbootstrap.CoreKVBucket)
			if err != nil {
				return fmt.Errorf("open Core KV: %w", err)
			}

			// Precondition: the loaded id file must actually name a role this
			// bucket verifiably holds live. A mismatched file (wrong
			// deployment, stale copy, wrong --nats-url) makes the scan below
			// see the deployment's real, live, current operator role as
			// "stranded" — indistinguishable from a genuine one from inside
			// the scan alone.
			reachable, err := internalbootstrap.CurrentEpochOperatorReachable(scanCtx, kv)
			if err != nil {
				return fmt.Errorf("verify the loaded id file against this deployment: %w", err)
			}
			if !reachable {
				return fmt.Errorf(
					"refusing: this deployment's own operator role (per the loaded %s) is not verifiably live and held — "+
						"the loaded id file may not correspond to this NATS deployment (--nats-url %s). "+
						"Acting on StrandedOperatorEpochs's report here would risk revoking the CURRENT epoch's own edges. "+
						"Verify --bootstrap-json/BOOTSTRAP_JSON_PATH and --nats-url both name the SAME deployment before retrying",
					jsonPath, *natsURL)
			}

			epochs, err := internalbootstrap.StrandedOperatorEpochs(scanCtx, kv)
			if err != nil {
				return fmt.Errorf("scan for stranded epochs: %w", err)
			}
			if len(epochs) == 0 {
				fmt.Println("no stranded operator epochs found")
				return nil
			}

			var (
				failed bool
				allOps []internalbootstrap.RevocationOp
			)
			for _, epoch := range epochs {
				fmt.Printf("stranded role %s: %d holder(s), %d reachable-via, %d grant(s), %d unreadable edge(s)\n",
					epoch.RoleKey, len(epoch.Holders), len(epoch.ReachableVia), len(epoch.GrantedBy), epoch.UnreadableEdges)

				// UnreadableEdges means Holders/ReachableVia/GrantedBy are a
				// LOWER BOUND (strandedepoch.go's own doc comment) — this run
				// can only revoke what it could read, so it must never claim
				// the epoch is fully neutralized.
				if epoch.UnreadableEdges > 0 {
					fmt.Fprintf(os.Stderr,
						"  %d edge(s) could not be read — this epoch's edge set is a LOWER BOUND; even a fully"+
							" successful run below cannot prove this epoch is neutralized\n", epoch.UnreadableEdges)
					failed = true
				}

				ops, err := internalbootstrap.PlanStrandedEpochRetirement(epoch)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  plan error: %v\n", err)
					failed = true
					continue
				}
				allOps = append(allOps, ops...)
			}

			// Ordered ONCE, across every epoch's ops together — not per epoch.
			// If the submitting actor's authority derives from one stranded
			// epoch's own role (a credential that predates the LATEST
			// rotation but not an earlier one, with two or more stranded
			// epochs live at once), ordering only within each epoch's own
			// slice would still revoke that epoch's self-edge before later
			// epochs are processed, denying every submission after it.
			orderSubmittingActorLast(allOps, actor)

			var revokedPermKeys []string
			for _, op := range allOps {
				if dryRun {
					fmt.Printf("  [dry-run] would submit %s %v (declares read %s)\n",
						op.OperationType, op.Payload, op.LinkKey)
					continue
				}

				if !revokeOne(kv, conn, actor, op, &revokedPermKeys) {
					failed = true
				}
			}

			if dryRun {
				if failed {
					return fmt.Errorf("one or more epochs cannot be previewed cleanly — see above")
				}
				return nil
			}

			// Re-verify rather than trust the submission replies alone: a
			// reply is a claim about one op, not about the graph. Re-scanning
			// the SAME epochs is what actually proves (or disproves) that
			// this run closed them.
			recCtx, recCancel := context.WithTimeout(context.Background(), retireScanTimeout)
			remaining, err := internalbootstrap.StrandedOperatorEpochs(recCtx, kv)
			recCancel()
			switch {
			case err != nil:
				fmt.Fprintf(os.Stderr, "cannot re-verify: %v\n", err)
				failed = true
			default:
				for _, r := range remaining {
					if r.Severity() != internalbootstrap.StrandedSeverityInert {
						fmt.Fprintf(os.Stderr, "  %s: still live authority after this run — %s\n", r.RoleKey, r.Report())
						failed = true
					}
				}
			}

			if err := printReinstallRecommendations(kv, revokedPermKeys); err != nil {
				fmt.Fprintf(os.Stderr, "reinstall recommendations incomplete: %v\n", err)
				failed = true
			}

			if failed {
				return fmt.Errorf("one or more revocations failed, or could not be verified — see above")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the planned revocations without submitting them")
	cmd.Flags().StringVar(&actor, "actor", "", "actor key to submit revocations as (defaults to credential file actorKey)")
	return cmd
}

// orderSubmittingActorLast moves any RevokeRole op whose actorKey is actor to
// the end of ops, in place. If the submitting actor's own credential
// predates the rotation — the natural case for an operator who has not yet
// refreshed it — it is itself one of the stranded epoch's Holders. Revoking
// that edge FIRST would strip the submitter's own operator-equivalent
// authorization mid-run, denying every submission after it and leaving the
// epoch partially revoked. Ordering it last means every OTHER edge is already
// gone by the time (if ever) the submitter cuts off its own access.
func orderSubmittingActorLast(ops []internalbootstrap.RevocationOp, actor string) {
	sort.SliceStable(ops, func(i, j int) bool {
		return !isSelfRevokeRole(ops[i], actor) && isSelfRevokeRole(ops[j], actor)
	})
}

func isSelfRevokeRole(op internalbootstrap.RevocationOp, actor string) bool {
	// op-name: (policy) the verb whose self-targeted instance orderSubmittingActorLast defers to the end of the run, so revoking the submitter's own edge never cuts off its authorization mid-batch pin=TestIsSelfRevokeRole
	if op.OperationType != "RevokeRole" {
		return false
	}
	key, _ := op.Payload["actorKey"].(string)
	return key == actor
}

// revokeOne carries out one revocation and reports whether it ended in a
// verifiably safe state (already gone, or freshly revoked) — false means the
// caller must mark the run failed. It never trusts a submission failure at
// face value: output.SubmitOp publishes before it waits for a reply
// (cmd/lattice/output/submit.go), so a reply timeout does not mean the op
// never committed, and a concurrent revoker can turn a live check into a
// rejection between the check and the submit. Both cases are resolved by
// re-reading the link's own state rather than trusting either race's error
// text.
func revokeOne(kv jetstream.KeyValue, conn *substrate.Conn, actor string, op internalbootstrap.RevocationOp, revokedPermKeys *[]string) bool {
	liveCtx, liveCancel := context.WithTimeout(context.Background(), retireOpReplyTimeout)
	live, err := linkIsLive(liveCtx, kv, op.LinkKey)
	liveCancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  %s %v: cannot check %s: %v\n", op.OperationType, op.Payload, op.LinkKey, err)
		return false
	}
	if !live {
		fmt.Printf("  %s %v: already satisfied (%s is absent or tombstoned)\n",
			op.OperationType, op.Payload, op.LinkKey)
		recordIfGrant(op, revokedPermKeys)
		return true
	}

	submitErr := submitRevocation(conn, actor, op)
	if submitErr == nil {
		fmt.Printf("  %s %v: revoked\n", op.OperationType, op.Payload)
		recordIfGrant(op, revokedPermKeys)
		return true
	}

	// The submission errored or was rejected — but that is not yet the
	// answer. Re-read the link: if it is gone now, the op reached the
	// Processor and committed (or a concurrent caller got there first)
	// despite the error this CLI saw, and the outcome is exactly the one
	// this run wanted.
	recheckCtx, recheckCancel := context.WithTimeout(context.Background(), retireOpReplyTimeout)
	stillLive, recheckErr := linkIsLive(recheckCtx, kv, op.LinkKey)
	recheckCancel()
	if recheckErr == nil && !stillLive {
		fmt.Printf("  %s %v: revoked (confirmed live after a submission error: %v)\n",
			op.OperationType, op.Payload, submitErr)
		recordIfGrant(op, revokedPermKeys)
		return true
	}

	fmt.Fprintf(os.Stderr, "  %s %v: FAILED: %v\n", op.OperationType, op.Payload, submitErr)
	return false
}

// linkIsLive reads key directly and reports whether it exists and is not
// soft-tombstoned. Used to check a revocation's target link BEFORE
// submitting, so an already-revoked edge (a prior partial run, or a
// concurrent operator) is detected cheaply and treated as success rather than
// submitted speculatively and diagnosed from the rejection.
func linkIsLive(ctx context.Context, kv jetstream.KeyValue, key string) (bool, error) {
	entry, err := kv.Get(ctx, key)
	if err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", key, err)
	}
	var doc struct {
		IsDeleted bool `json:"isDeleted"`
	}
	if err := json.Unmarshal(entry.Value(), &doc); err != nil {
		return false, fmt.Errorf("parse %s: %w", key, err)
	}
	return !doc.IsDeleted, nil
}

// recordIfGrant appends op's permKey to *revokedPermKeys when op is a
// RevokePermission — the set printReinstallRecommendations later resolves to
// owning packages. RevokeRole ops (holders / reachableVia) contribute
// nothing here: only grants have a declaring package to recommend
// reinstalling.
func recordIfGrant(op internalbootstrap.RevocationOp, revokedPermKeys *[]string) {
	// op-name: (policy) the verb that carries a permKey resolving to an owning package, so only its revocations reach printReinstallRecommendations — a RevokeRole has no package to recommend pin=TestRecordIfGrant
	if op.OperationType != "RevokePermission" {
		return
	}
	if permKey, ok := op.Payload["permKey"].(string); ok {
		*revokedPermKeys = append(*revokedPermKeys, permKey)
	}
}

// submitRevocation submits one RevokeRole/RevokePermission op, declaring its
// LinkKey in ContextHint.Reads exactly as the Starlark script requires
// (Contract #2 §2.5).
func submitRevocation(conn *substrate.Conn, actor string, op internalbootstrap.RevocationOp) error {
	payload, err := json.Marshal(op.Payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	requestID, err := substrate.NewNanoID()
	if err != nil {
		return fmt.Errorf("generate requestId: %w", err)
	}

	env := processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          processor.LaneDefault,
		OperationType: op.OperationType,
		Actor:         actor,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Payload:       payload,
		ContextHint:   &processor.ContextHint{Reads: []string{op.LinkKey}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), retireOpReplyTimeout)
	defer cancel()

	reply, err := output.SubmitOp(ctx, conn, &env)
	if err != nil {
		return err
	}
	if reply.Status == processor.ReplyStatusRejected {
		code, msg := processor.ErrorCode(""), ""
		if reply.Error != nil {
			code, msg = reply.Error.Code, reply.Error.Message
		}
		return revocationRejection(code, msg, op.LinkKey)
	}
	return nil
}

// revocationRejection renders a rejected revocation reply as the error the run
// reports.
//
// ProtectedKey gets its own diagnosis. Each Processor protects the twelve
// kernel topology links of the epoch whose lattice.bootstrap.json it loaded at
// start-up, so a replica that has NOT restarted since the table was regenerated
// still holds the retired epoch's edges as its own and refuses to revoke them —
// non-deterministically per op in a mixed roll, since the reply comes from
// whichever replica took the message. Read as a generic rejection, that looks
// like the retirement being impossible; read for what it is, it names an
// operator action (roll the Processors) and a re-run that then succeeds.
func revocationRejection(code processor.ErrorCode, msg, linkKey string) error {
	if code == processor.ErrCodeProtectedKey {
		return fmt.Errorf(
			"rejected: %s — %s: epoch skew — a Processor still holds the RETIRED table, so it protects %s"+
				" as its own kernel topology. Restart every Processor replica on the current"+
				" lattice.bootstrap.json and re-run; in a mixed roll this refusal appears on some"+
				" revocations and not others, depending on which replica takes the message",
			code, msg, linkKey)
	}
	return fmt.Errorf("rejected: %s — %s", code, msg)
}

// printReinstallRecommendations reads declaredBy off every permission this
// run revoked and prints one de-duplicated, sorted `make reinstall-package`
// line per owning package — design doc §7 item 3's grounded vehicle. It never
// runs the command itself (mirrors `bootstrap verify`'s own
// diagnose-and-print-the-remedy convention, bootstrap.go:164).
//
// A returned error means the recommendation set is INCOMPLETE — some
// declaredBy read failed — not that no packages need reinstalling; the
// caller must not read a nil error as "nothing to reinstall" in that case.
//
// Every candidate name is checked against pkgregistry — the same compiled,
// trusted registry `lattice-pkg install` itself refuses to act outside of
// (cmd/lattice-pkg/main.go) — before it is ever printed: declaredBy is
// ordinary permission data, rewritable by ordinary ops (UpdatePermission),
// so it is untrusted input being rendered into a copy-pasteable shell
// command, not a value this process minted itself.
//
// Uses its OWN fresh timeout, not the caller's scan-scoped one: it reads one
// permission per revoked grant, potentially as many as the deployment has
// stranded grants, and must not fail silently just because the revocation
// loop ahead of it already spent most of a shared budget.
func printReinstallRecommendations(kv jetstream.KeyValue, revokedPermKeys []string) error {
	if len(revokedPermKeys) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), retireRecommendationsTimeout)
	defer cancel()

	packages := map[string]bool{}
	// incomplete counts every reason a revoked grant's package could NOT be
	// named in the printed set below — an unreadable declaredBy and an
	// untrusted (not-in-registry) one are different causes but the same
	// consequence: the caller must not treat a nil error as "every revoked
	// grant's reinstall is accounted for" when either one occurred.
	var incomplete int
	for _, permKey := range revokedPermKeys {
		name, ok, err := internalbootstrap.PermissionDeclaredBy(ctx, kv, permKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  cannot read declaredBy for %s: %v\n", permKey, err)
			incomplete++
			continue
		}
		if !ok {
			continue
		}
		if _, known := pkgregistry.Lookup(name); !known {
			fmt.Fprintf(os.Stderr,
				"  %s declares package %q, which is not in the compiled package registry — not"+
					" printing a reinstall command for it (declaredBy is ordinary, rewritable permission"+
					" data, not a trusted value)\n", permKey, name)
			incomplete++
			continue
		}
		packages[name] = true
	}

	if len(packages) > 0 {
		names := make([]string, 0, len(packages))
		for name := range packages {
			names = append(names, name)
		}
		sort.Strings(names)

		fmt.Println("\nRevoked grants were declared by the following packages — re-apply each to")
		fmt.Println("re-mint its permissions against the CURRENT operator role:")
		for _, name := range names {
			fmt.Printf("  make reinstall-package PKG=packages/%s\n", name)
		}
	}

	if incomplete > 0 {
		return fmt.Errorf("%d revoked grant(s) could not be attributed to a trusted package — the printed set above may be incomplete", incomplete)
	}
	return nil
}
