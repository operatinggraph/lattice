package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// consumerRoleKey is identity-domain's own consumer role, resolved the same
// deterministic way the package's own script and the Gateway resolve it.
func consumerRoleKey() string {
	return "vtx.role." + pkgmgr.RoleID("identity-domain", "consumer")
}

// consumerGrantLinkKey is the holdsRole link ProvisionConsumerIdentity creates
// for a fresh credential. Declared optionalReads for the same load-bearing
// reason the Gateway declares it: the script distinguishes "already granted"
// (no-op) from "grant missing" (create) by this key alone, and an undeclared
// key hydrates as absent — so an actor who already holds the grant would take
// the create branch and RevisionConflict on a live key.
func consumerGrantLinkKey(actorKey, roleKey string) string {
	actorID, ok := strings.CutPrefix(actorKey, "vtx.identity.")
	if !ok || actorID == "" {
		return ""
	}
	roleID, ok := strings.CutPrefix(roleKey, "vtx.role.")
	if !ok || roleID == "" {
		return ""
	}
	return "lnk.identity." + actorID + ".holdsRole.role." + roleID
}

// newProvisionCommand builds `lattice identity provision --actor <A>`.
//
// The Gateway runs this same op as a first-touch pre-flight for every actor it
// authenticates, so a person arriving through the web path always has a
// credential vertex before they reach a ceremony. The CLI has no such
// pre-flight: `lattice identity claim --actor <A>` submits straight to the
// Processor under a credential file, and ClaimIdentity refuses an actor whose
// vertex was never minted (`credential-not-provisioned`). This command is the
// missing half — it mirrors the Gateway's pre-flight at the authority level
// the op requires. That authority is NOT the same as `create-unclaimed`'s:
// CreateUnclaimedIdentity is granted to operator/frontOfHouse/backOfHouse,
// while ProvisionConsumerIdentity is operator/identityProvisioner only, because
// minting an identity vertex is the privileged act ClaimIdentity refuses to
// perform itself. A front-desk credential that can create an unclaimed identity
// is denied here, and correctly so.
func newProvisionCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var targetActor string
	var roleKey string

	cmd := &cobra.Command{
		Use:   "provision",
		Short: "Mint the identity vertex for a raw sign-in credential",
		Long: `provision submits a ProvisionConsumerIdentity operation, establishing the
identity vertex and consumer role grant for a raw credential actor.

The Gateway does this automatically for actors that authenticate through it.
Run this for a credential that reaches the Processor another way — a CLI
credential file, a direct submitter — before it claims an identity.

  lattice identity provision --target-actor vtx.identity.<NanoID>

--target-actor defaults to --actor, so provisioning the credential you are
submitting as needs neither flag beyond the credential file.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			if actor == "" {
				return fmt.Errorf("--actor is required (or set via credential file)")
			}
			if targetActor == "" {
				targetActor = actor
			}
			if !strings.HasPrefix(targetActor, "vtx.identity.") {
				return fmt.Errorf("--target-actor must be a vtx.identity.<NanoID> key, got %q", targetActor)
			}
			if roleKey == "" {
				roleKey = consumerRoleKey()
			}

			payloadBytes, err := json.Marshal(map[string]string{
				"targetActorKey":  targetActor,
				"consumerRoleKey": roleKey,
			})
			if err != nil {
				return fmt.Errorf("marshal payload: %w", err)
			}

			requestID, err := substrate.NewNanoID()
			if err != nil {
				return fmt.Errorf("generate requestId: %w", err)
			}

			// The target actor rides optionalReads, never reads: not existing
			// yet is the ordinary case and the whole point of the call, so a
			// reads declaration would fault HydrationMiss on exactly the
			// branch that does the work. The role vertex is the opposite — a
			// pinned, always-live key whose absence is a wiring fault.
			optionalReads := []string{targetActor}
			if grantLink := consumerGrantLinkKey(targetActor, roleKey); grantLink != "" {
				optionalReads = append(optionalReads, grantLink)
			}

			env := &processor.OperationEnvelope{
				RequestID: requestID,
				Lane:      processor.LaneDefault,
				// op-name: (submits) the "identity provision" CLI command submits this to mint the identity vertex for a raw sign-in credential, carrying the target actor key and the consumer role to grant it.
				OperationType: "ProvisionConsumerIdentity",
				Actor:         actor,
				SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
				Class:         "identity",
				Payload:       json.RawMessage(payloadBytes),
				ContextHint: &processor.ContextHint{
					Reads:         []string{roleKey},
					OptionalReads: optionalReads,
				},
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			conn, err := output.Connect(ctx, *natsURL)
			if err != nil {
				if *outputFmt == "json" {
					_ = output.PrintJSONError("ConnectionError", err.Error())
					return nil
				}
				return err
			}
			defer conn.Close()

			reply, err := submitOp(ctx, conn, env)
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
			fmt.Printf("requestId:    %s\nopTrackerKey: %s\nstatus:       %s\nactorKey:     %s\n",
				reply.RequestID, reply.OpTrackerKey, reply.Status, targetActor)
			return nil
		},
	}

	cmd.Flags().StringVar(&actor, "actor", "", "submitting actor key (defaults to credential file actorKey)")
	cmd.Flags().StringVar(&targetActor, "target-actor", "", "credential actor to provision (defaults to --actor)")
	cmd.Flags().StringVar(&roleKey, "role", "", "role to grant (defaults to identity-domain's consumer role)")
	return cmd
}
