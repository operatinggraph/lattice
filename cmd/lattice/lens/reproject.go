package lens

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/controlauth"
	"github.com/operatinggraph/lattice/internal/refractor/control"
)

// newReprojectCommand re-executes ONE actor's projection for an
// actor-aggregate lens and reconciles the stored row against the graph
// (capability-projection-reconciliation-design.md §3.1). It is the targeted
// heal for an actor whose capability document went missing when a CDC event
// was lost to a pipeline-availability gap — the case a full `rebuild` would
// otherwise be the only instrument for.
//
// Converged output means the stored row already matched: no write was made,
// which is the expected result against a healthy bucket.
func newReprojectCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actorKey string
	var actor string
	var actorToken string

	cmd := &cobra.Command{
		Use:   "reproject <lensId>",
		Short: "Re-execute one actor's projection and reconcile the stored row",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actorKey == "" {
				return fmt.Errorf("--actor-key is required")
			}
			if actor == "" {
				actor = *defaultActor
			}
			subject := "lattice.ctrl.refractor." + args[0] + ".reproject"
			body, err := json.Marshal(control.ControlRequest{ActorKey: actorKey})
			if err != nil {
				return fmt.Errorf("encode request: %w", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), output.DefaultTimeout)
			defer cancel()

			conn, err := output.Connect(ctx, *natsURL)
			if err != nil {
				return err
			}
			defer conn.Close()

			msg := controlauth.NewActorRequestMsg(subject, output.ResolveActorHeader(actor, actorToken))
			msg.Data = body
			reply, err := conn.NATS().RequestMsgWithContext(ctx, msg)
			if err != nil {
				return fmt.Errorf("request %s: %w", subject, err)
			}
			var resp control.ControlResponse
			if err := json.Unmarshal(reply.Data, &resp); err != nil {
				return fmt.Errorf("decode response from %s: %w", subject, err)
			}
			if resp.Error != "" {
				return fmt.Errorf("%s", resp.Error)
			}
			if resp.Reproject == nil {
				return fmt.Errorf("reproject: empty response from %s", subject)
			}

			if *outputFmt == "json" {
				return output.PrintJSON(resp.Reproject)
			}
			// "no projection" is distinct from "converged": the lens produced no
			// row for this actor at all, so nothing was compared and nothing
			// written. Reporting that as converged would let a mistyped actor key
			// read as a clean bill of health.
			state := "no projection for this actor"
			switch {
			case resp.Reproject.Verdict == "blocked":
				// The repair provably did not land — the stored watermark
				// outranks the reconciliation's token. Rendering this as
				// anything quieter is the exact misreport this verdict exists
				// to end.
				state = "BLOCKED: " + resp.Reproject.VerdictReason
			case resp.Reproject.Deleted:
				state = "healed: row deleted"
			case resp.Reproject.Wrote:
				state = "healed: row written"
			case resp.Reproject.Converged:
				state = "converged (no write)"
			case resp.Reproject.Verdict == "unverified" && resp.Reproject.VerdictReason != "":
				state = "unverified: " + resp.Reproject.VerdictReason
			}
			fmt.Printf("%s\t%s\tprojectionSeq=%d\n", resp.Reproject.Actor, state, resp.Reproject.ProjectionSeq)
			return nil
		},
	}

	cmd.Flags().StringVar(&actorKey, "actor-key", "", "vertex key of the actor whose row is reconciled (required)")
	output.AddActorFlags(cmd, &actor, &actorToken)
	return cmd
}
