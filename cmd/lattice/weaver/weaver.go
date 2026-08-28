// Package weaver implements the lattice weaver command group: operator
// list/disable/enable/revoke/reset-confidence/reset-budget/replay-target
// controls for Weaver convergence targets (FR30), via the
// lattice.ctrl.weaver.* NATS Services control plane.
package weaver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/controlauth"
	"github.com/operatinggraph/lattice/internal/substrate/keys"
	"github.com/operatinggraph/lattice/internal/weaver/control"
)

// validateTargetID rejects a targetId that is empty or contains a "." before
// the request is published. The control subject is
// lattice.ctrl.weaver.<targetId>.<op> and the endpoints subscribe a
// single-token wildcard for <targetId>, so a dotted (or empty) targetId builds
// a subject no endpoint matches — the request would otherwise hang to the
// client timeout with an opaque "no responders" rather than a clear error.
// Registered target ids are dot-free single tokens (install-validated), so this
// mirrors the server-side targetId shape.
func validateTargetID(targetID string) error {
	if targetID == "" {
		return fmt.Errorf("targetId must not be empty")
	}
	if strings.Contains(targetID, ".") {
		return fmt.Errorf("targetId %q must not contain '.' (a registered targetId is a single dot-free token)", targetID)
	}
	return nil
}

// validateEntityID rejects an entityId that is not the §10.2 bare NanoID the
// weaver-state key shape requires. The server validates it too — a control
// endpoint never trusts its caller — but rejecting here turns a typo into an
// immediate, local message instead of a round trip.
func validateEntityID(entityID string) error {
	if !keys.IsValidNanoID(entityID) {
		return fmt.Errorf("entityId %q must be a %d-character NanoID", entityID, keys.NanoIDLength)
	}
	return nil
}

// validateGapColumn rejects a gapColumn that is not a single-token missing_*
// column. Contract #10 §10.2 names every gap column that way, and the
// weaver-state key it forms is split positionally, so a dotted value would
// build a key nothing can parse.
func validateGapColumn(gapColumn string) error {
	if !strings.HasPrefix(gapColumn, "missing_") {
		return fmt.Errorf("gapColumn %q must be a missing_* column (Contract #10 §10.2)", gapColumn)
	}
	if strings.ContainsAny(gapColumn, ". ") {
		return fmt.Errorf("gapColumn %q must be a single token", gapColumn)
	}
	return nil
}

// NewCommand returns the cobra.Command for the weaver command group.
// defaultActor is the credential-file actor key (op.NewCommand's third arg);
// each subcommand also accepts its own --actor override.
func NewCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "weaver",
		Short: "Operate Weaver convergence targets (list/disable/enable/revoke/reset-confidence/reset-budget/replay-target)",
	}
	cmd.AddCommand(newListCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newDisableCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newEnableCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newRevokeCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newResetConfidenceCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newResetBudgetCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newReplayTargetCommand(natsURL, outputFmt, defaultActor))
	return cmd
}

// request sends a control-plane request to subject with no body — see
// requestWithBody for the ops that carry one — stamping actorHeader as
// the Lattice-Actor header when non-empty, and decodes the
// control.ControlResponse. Connection is via output.Connect's raw *nats.Conn
// (conn.NATS()) since the weaver-control endpoints are plain NATS Services
// responders, not JetStream.
func request(natsURL, subject, actorHeader string) (control.ControlResponse, error) {
	return requestWithBody(natsURL, subject, actorHeader, nil)
}

// requestWithBody is request with a JSON payload, for a per-gap op whose
// arguments do not fit the control subject: the endpoints are registered on a
// single-token wildcard (lattice.ctrl.weaver.*.<op>), so only the targetId can
// ride the subject. body nil sends an empty request.
func requestWithBody(natsURL, subject, actorHeader string, body any) (control.ControlResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), output.DefaultTimeout)
	defer cancel()

	conn, err := output.Connect(ctx, natsURL)
	if err != nil {
		return control.ControlResponse{}, err
	}
	defer conn.Close()

	msg := controlauth.NewActorRequestMsg(subject, actorHeader)
	if body != nil {
		payload, mErr := json.Marshal(body)
		if mErr != nil {
			return control.ControlResponse{}, fmt.Errorf("encode %s request: %w", subject, mErr)
		}
		msg.Data = payload
	}
	reply, err := conn.NATS().RequestMsgWithContext(ctx, msg)
	if err != nil {
		return control.ControlResponse{}, fmt.Errorf("request %s: %w", subject, err)
	}

	var resp control.ControlResponse
	if err := json.Unmarshal(reply.Data, &resp); err != nil {
		return control.ControlResponse{}, fmt.Errorf("decode response from %s: %w", subject, err)
	}
	if resp.Error != "" {
		return resp, fmt.Errorf("%s", resp.Error)
	}
	return resp, nil
}

func newListCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var actorToken string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered Weaver convergence targets",
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			resp, err := request(*natsURL, control.ListSubject(), output.ResolveActorHeader(actor, actorToken))
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}

			if *outputFmt == "json" {
				return output.PrintJSON(resp.Targets)
			}
			if len(resp.Targets) == 0 {
				fmt.Println("(no registered targets)")
				return nil
			}
			fmt.Printf("%-20s %-30s %-10s %s\n", "TARGET_ID", "LENS_REF", "STATE", "GAPS")
			for _, t := range resp.Targets {
				fmt.Printf("%-20s %-30s %-10s %v\n", t.TargetID, t.LensRef, t.State, t.Gaps)
			}
			return nil
		},
	}
	output.AddActorFlags(cmd, &actor, &actorToken)
	return cmd
}

func newDisableCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var actorToken string
	cmd := &cobra.Command{
		Use:   "disable <targetId>",
		Short: "Disable a Weaver convergence target (pause dispatch)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			targetID := args[0]
			if err := validateTargetID(targetID); err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}
			resp, err := request(*natsURL, control.TargetSubject(targetID, "disable"), output.ResolveActorHeader(actor, actorToken))
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}

			if *outputFmt == "json" {
				return output.PrintJSON(resp.Disable)
			}
			fmt.Printf("target %q disabled\n", targetID)
			return nil
		},
	}
	output.AddActorFlags(cmd, &actor, &actorToken)
	return cmd
}

func newEnableCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var actorToken string
	cmd := &cobra.Command{
		Use:   "enable <targetId>",
		Short: "Enable a Weaver convergence target (resume dispatch)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			targetID := args[0]
			if err := validateTargetID(targetID); err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}
			resp, err := request(*natsURL, control.TargetSubject(targetID, "enable"), output.ResolveActorHeader(actor, actorToken))
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}

			if *outputFmt == "json" {
				return output.PrintJSON(resp.Enable)
			}
			fmt.Printf("target %q enabled\n", targetID)
			return nil
		},
	}
	output.AddActorFlags(cmd, &actor, &actorToken)
	return cmd
}

func newRevokeCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var actorToken string
	cmd := &cobra.Command{
		Use:   "revoke <targetId>",
		Short: "Revoke a Weaver convergence target (remove durable + in-flight marks; stays disabled)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			targetID := args[0]
			if err := validateTargetID(targetID); err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}
			resp, err := request(*natsURL, control.TargetSubject(targetID, "revoke"), output.ResolveActorHeader(actor, actorToken))
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}

			if *outputFmt == "json" {
				return output.PrintJSON(resp.Revoke)
			}
			fmt.Printf("target %q revoked\n", targetID)
			return nil
		},
	}
	output.AddActorFlags(cmd, &actor, &actorToken)
	return cmd
}

// newResetConfidenceCommand builds `lattice weaver reset-confidence
// <targetId>` — the middle rung of the operator-severity ladder between
// disable (deletes nothing) and revoke (deletes everything): it drains the
// target's `__effect` confidence windows and nothing else, clearing a standing
// LensEffectMismatch raised by windows the pre-5b58f66 bookkeeping polluted.
func newResetConfidenceCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var actorToken string
	cmd := &cobra.Command{
		Use:   "reset-confidence <targetId>",
		Short: "Drain a target's __effect confidence windows (advisory data only; dispatch state untouched)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			targetID := args[0]
			if err := validateTargetID(targetID); err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}
			resp, err := request(*natsURL, control.TargetSubject(targetID, "resetConfidence"), output.ResolveActorHeader(actor, actorToken))
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}

			if *outputFmt == "json" {
				return output.PrintJSON(resp.ResetConfidence)
			}
			deleted := 0
			if resp.ResetConfidence != nil {
				deleted = resp.ResetConfidence.WindowsDeleted
			}
			fmt.Printf("target %q confidence reset (%d window(s) deleted)\n", targetID, deleted)
			return nil
		},
	}
	output.AddActorFlags(cmd, &actor, &actorToken)
	return cmd
}

// newResetBudgetCommand builds `lattice weaver reset-budget <targetId>
// <entityId> <gapColumn>` — the un-park verb. A gap whose §10.8 retry budget is
// spent stops dispatching and holds a standing GapBudgetExhausted issue; once
// the operator has fixed whatever the retries were failing against, this is
// what lets it try again.
//
// Scope is one gap, deliberately: the budget is per-(target, entity, gap), so a
// target-wide reset would re-arm parks nobody looked at. The verb writes the
// count to 0 and stops — it neither clears the issue nor dispatches, and it
// does not check whether anything WILL dispatch. The next reconciler sweep pass
// (≤ 1 min) decides that: it dispatches a gap that is still violating, open,
// unsuppressed and markless on a registered, enabled target, and skips one that
// is not. So a successful reset means the budget is re-armed, never that the
// gap has tried again or is certain to.
func newResetBudgetCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var actorToken string
	cmd := &cobra.Command{
		Use:   "reset-budget <targetId> <entityId> <gapColumn>",
		Short: "Re-arm one gap's exhausted retry budget so the next sweep pass dispatches it again",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			targetID, entityID, gapColumn := args[0], args[1], args[2]
			for _, err := range []error{
				validateTargetID(targetID),
				validateEntityID(entityID),
				validateGapColumn(gapColumn),
			} {
				if err != nil {
					if *outputFmt == "json" {
						return output.PrintJSONError("ControlError", err.Error())
					}
					return err
				}
			}
			resp, err := requestWithBody(*natsURL, control.TargetSubject(targetID, "resetBudget"),
				output.ResolveActorHeader(actor, actorToken),
				control.ResetBudgetRequest{EntityID: entityID, GapColumn: gapColumn})
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}

			if *outputFmt == "json" {
				return output.PrintJSON(resp.ResetBudget)
			}
			previous := 0
			if resp.ResetBudget != nil {
				previous = resp.ResetBudget.PreviousCount
			}
			fmt.Printf("target %q entity %q gap %q retry budget re-armed (was %d); the next sweep pass dispatches the gap if it is still dispatchable\n",
				targetID, entityID, gapColumn, previous)
			return nil
		},
	}
	output.AddActorFlags(cmd, &actor, &actorToken)
	return cmd
}

// newReplayTargetCommand builds `lattice weaver replay-target <targetId>` — the
// re-enumeration verb. It recreates the target's lane-1 durable so
// DeliverLastPerSubject re-delivers the target's CURRENT row set through the
// unchanged evaluation ladder, and every row still violating dispatches again.
//
// It is for the rows the standing decline loop cannot reach: a row that was
// declined and ACKED (nothing owes it a redelivery), and a target whose
// Nak'd-pending set outlived a NATS restart with no armed redelivery timer and
// no traffic of its own to re-arm one. Ordinary operation needs it for neither.
//
// One invocation costs O(the target's current rows) and re-fires the episode of
// every violating row whose mark has aged past its lease, so it is manual by
// design: run it holding evidence. The reported count is how many rows the
// recreated durable had queued when the engine answered — a snapshot taken
// while the pump is already draining, not a total.
func newReplayTargetCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var actorToken string
	cmd := &cobra.Command{
		Use:   "replay-target <targetId>",
		Short: "Re-deliver a target's current rows through the evaluation ladder (recreates its lane-1 durable)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			targetID := args[0]
			if err := validateTargetID(targetID); err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}
			resp, err := request(*natsURL, control.TargetSubject(targetID, "replayTarget"), output.ResolveActorHeader(actor, actorToken))
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}

			if *outputFmt == "json" {
				return output.PrintJSON(resp.ReplayTarget)
			}
			queued := 0
			if resp.ReplayTarget != nil {
				queued = resp.ReplayTarget.RowsQueued
			}
			fmt.Printf("target %q replayed (%d row(s) queued at the durable when it answered); every row still violating dispatches again\n",
				targetID, queued)
			return nil
		},
	}
	output.AddActorFlags(cmd, &actor, &actorToken)
	return cmd
}
