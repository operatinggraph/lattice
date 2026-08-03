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

// controlRequest sends one Refractor control-plane request for lensID and
// decodes the response. Shape and transport are reproject.go's: a plain NATS
// request on lattice.ctrl.refractor.<lensId>.<op> stamped with the actor
// header, since the control endpoints are NATS Services responders rather than
// JetStream.
//
// The actor must hold ctrl.refractor.<op> at scope "any"
// (internal/controlauth.CapabilityKVChecker matches exactly, with no wildcard
// branch, so root is not implicitly permitted). On the dev and demo stacks
// `make dev-seed-console-operator` provisions an identity holding
// consoleOperator's grants and persists its key — that is the --actor to pass.
func controlRequest(natsURL, lensID, op, actorHeader string, req control.ControlRequest) (control.ControlResponse, error) {
	var resp control.ControlResponse
	subject := "lattice.ctrl.refractor." + lensID + "." + op
	body, err := json.Marshal(req)
	if err != nil {
		return resp, fmt.Errorf("encode request: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), output.DefaultTimeout)
	defer cancel()

	conn, err := output.Connect(ctx, natsURL)
	if err != nil {
		return resp, err
	}
	defer conn.Close()

	msg := controlauth.NewActorRequestMsg(subject, actorHeader)
	msg.Data = body
	reply, err := conn.NATS().RequestMsgWithContext(ctx, msg)
	if err != nil {
		return resp, fmt.Errorf("request %s: %w", subject, err)
	}
	if err := json.Unmarshal(reply.Data, &resp); err != nil {
		return resp, fmt.Errorf("decode response from %s: %w", subject, err)
	}
	if resp.Error != "" {
		return resp, fmt.Errorf("%s", resp.Error)
	}
	return resp, nil
}

// newPauseCommand stops a lens consuming without deactivating it. The pause is
// persisted in Health KV and restored on the next boot, so it survives a
// process cycle and is cleared only by resume.
func newPauseCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor, actorToken string
	cmd := &cobra.Command{
		Use:   "pause <lensId>",
		Short: "Pause a Lens's consumer (persists across restart until resume)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			resp, err := controlRequest(*natsURL, args[0], "pause", output.ResolveActorHeader(actor, actorToken), control.ControlRequest{})
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}
			if *outputFmt == "json" {
				return output.PrintJSON(resp.Pause)
			}
			fmt.Printf("lens %q paused (persists across restart until resume)\n", args[0])
			return nil
		},
	}
	output.AddActorFlags(cmd, &actor, &actorToken)
	return cmd
}

// newResumeCommand clears a lens's pause, whatever raised it. It is the only
// instrument that reaches a STRUCTURAL pause: those are held until a human
// reconciles the cause, and Loupe's resume button is deliberately absent from
// the hosted demo posture's read-only op set — so an operator with NATS
// credentials resumes from here. Run `lattice lens health <lensId>` first: on a
// structural pause the entry's lastError is the cause that must be fixed before
// resuming, or the lens will simply pause again on its next write.
func newResumeCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor, actorToken string
	cmd := &cobra.Command{
		Use:   "resume <lensId>",
		Short: "Resume a paused Lens (the only path for a structural pause)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			resp, err := controlRequest(*natsURL, args[0], "resume", output.ResolveActorHeader(actor, actorToken), control.ControlRequest{})
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}
			if *outputFmt == "json" {
				return output.PrintJSON(resp.Resume)
			}
			fmt.Printf("lens %q resumed\n", args[0])
			return nil
		},
	}
	output.AddActorFlags(cmd, &actor, &actorToken)
	return cmd
}

// newRebuildCommand re-scans a lens's source stream from the beginning and
// re-projects every row. --truncate empties the target first, which is the
// difference between reconciling a drifted read model and rebuilding one whose
// stale rows must not survive. The op is asynchronous: it acknowledges the
// start, and `lens lag` reports the drain.
func newRebuildCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor, actorToken string
	var truncate bool
	cmd := &cobra.Command{
		Use:   "rebuild <lensId>",
		Short: "Re-scan and re-project a Lens from the start of its stream",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			resp, err := controlRequest(*natsURL, args[0], "rebuild", output.ResolveActorHeader(actor, actorToken), control.ControlRequest{Truncate: truncate})
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}
			if *outputFmt == "json" {
				return output.PrintJSON(resp.Rebuild)
			}
			mode := "target preserved"
			if truncate {
				mode = "target truncated first"
			}
			fmt.Printf("lens %q rebuild started (%s); watch `lattice lens lag` to follow the drain\n", args[0], mode)
			return nil
		},
	}
	cmd.Flags().BoolVar(&truncate, "truncate", false, "empty the target before re-projecting")
	output.AddActorFlags(cmd, &actor, &actorToken)
	return cmd
}

// newHealthCommand prints one lens's health entry. It is the command that
// answers "why is this lens down" without a browser: for a paused lens it
// renders both pauseReason — which names only the tier — and lastError, the
// recorded cause that names the column, table or constraint the projection
// actually failed on.
func newHealthCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor, actorToken string
	cmd := &cobra.Command{
		Use:   "health <lensId>",
		Short: "Show one Lens's health entry, including a pause's recorded cause",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			resp, err := controlRequest(*natsURL, args[0], "health", output.ResolveActorHeader(actor, actorToken), control.ControlRequest{})
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}
			if resp.Entry == nil {
				err := fmt.Errorf("health: no entry returned for lens %q", args[0])
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}
			if *outputFmt == "json" {
				return output.PrintJSON(resp.Entry)
			}
			e := resp.Entry
			fmt.Printf("lens\t%s\n", e.RuleID)
			fmt.Printf("status\t%s\n", e.Status)
			if e.PauseReason != nil {
				fmt.Printf("pauseReason\t%s\n", *e.PauseReason)
			}
			if e.LastError != nil {
				fmt.Printf("lastError\t%s\n", *e.LastError)
			}
			fmt.Printf("errorCount\t%d\n", e.ErrorCount)
			fmt.Printf("consumerLag\t%d\n", e.ConsumerLag)
			fmt.Printf("lastUpdated\t%s\n", e.LastUpdated)
			return nil
		},
	}
	output.AddActorFlags(cmd, &actor, &actorToken)
	return cmd
}
