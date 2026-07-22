// Package loom implements the lattice loom command group: operator
// list/consumers/inspect/pause/resume controls for the Loom orchestration
// engine, via the lattice.ctrl.loom.* NATS Services control plane.
package loom

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/controlauth"
	"github.com/operatinggraph/lattice/internal/loom/control"
)

// validateName rejects a name that is empty or contains a "." before the request
// is published. The per-name control subject is lattice.ctrl.loom.<name>.<op> and
// the endpoints subscribe a single-token wildcard for <name>, so a dotted (or
// empty) name builds a subject no endpoint matches — the request would otherwise
// hang to the client timeout with an opaque "no responders" rather than a clear
// error. Instance ids are NanoIDs and managed-consumer names are dot-free single
// tokens, so this mirrors the server-side name shape.
func validateName(kind, name string) error {
	if name == "" {
		return fmt.Errorf("%s must not be empty", kind)
	}
	if strings.Contains(name, ".") {
		return fmt.Errorf("%s %q must not contain '.' (a %s is a single dot-free token)", kind, name, kind)
	}
	return nil
}

// NewCommand returns the cobra.Command for the loom command group.
// defaultActor is the credential-file actor key (op.NewCommand's third arg);
// each subcommand also accepts its own --actor override.
func NewCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "loom",
		Short: "Operate the Loom engine (list/consumers/inspect/pause/resume)",
	}
	cmd.AddCommand(newListCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newConsumersCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newInspectCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newPauseCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newResumeCommand(natsURL, outputFmt, defaultActor))
	return cmd
}

// addActorFlag registers the --actor and --actor-token flags shared by every
// subcommand, defaulting --actor at RunE time to *defaultActor (the
// credential-file actorKey) when unset. A resolved-empty actor is NOT an
// error here (unlike the write-path `op submit`): in Fire 1 posture (no JWT
// trust root configured server-side) the capability gate is not enforced, so
// an anonymous request must keep working. --actor-token carries a signed
// actor JWT (Fire 2, control-plane-capability-authz-design.md — mint one
// with `gateway dev-token -sub <identityNanoID>` against a dev-mode server);
// when set it is stamped in place of --actor and wins if both are given,
// since presenting a token is the deliberate opt-in to verified-actor mode.
func addActorFlag(cmd *cobra.Command, actor, actorToken *string) {
	cmd.Flags().StringVar(actor, "actor", "", "actor key stamped on the control request (defaults to credential file actorKey)")
	cmd.Flags().StringVar(actorToken, "actor-token", "", "signed actor JWT stamped on the control request (Fire 2 verified-actor mode; overrides --actor)")
}

// resolveActorHeader picks the control-request HeaderActor value: actorToken
// wins when non-empty (verified-actor mode), otherwise the raw actor key
// (Fire 1 self-asserted mode).
func resolveActorHeader(actor, actorToken string) string {
	if actorToken != "" {
		return actorToken
	}
	return actor
}

// request sends a control-plane request to subject, stamping actorHeader as
// the Lattice-Actor header when non-empty, and decodes the
// control.ControlResponse. Connection is via output.Connect's raw *nats.Conn
// (conn.NATS()) since the loom-control endpoints are plain NATS Services
// responders, not JetStream.
func request(natsURL, subject, actorHeader string) (control.ControlResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), output.DefaultTimeout)
	defer cancel()

	conn, err := output.Connect(ctx, natsURL)
	if err != nil {
		return control.ControlResponse{}, err
	}
	defer conn.Close()

	reply, err := conn.NATS().RequestMsgWithContext(ctx, controlauth.NewActorRequestMsg(subject, actorHeader))
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
		Short: "List Loom instances (running + retained terminals)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			resp, err := request(*natsURL, control.ListSubject(), resolveActorHeader(actor, actorToken))
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}

			if *outputFmt == "json" {
				return output.PrintJSON(resp.Instances)
			}
			if len(resp.Instances) == 0 {
				fmt.Println("(no instances)")
				return nil
			}
			fmt.Printf("%-24s %-24s %-20s %-8s %-10s %s\n",
				"INSTANCE_ID", "PATTERN_REF", "SUBJECT_KEY", "CURSOR", "STATUS", "RETRIES")
			for _, in := range resp.Instances {
				fmt.Printf("%-24s %-24s %-20s %-8d %-10s %d\n",
					in.InstanceID, in.PatternRef, in.SubjectKey, in.Cursor, in.Status, in.RetryCount)
			}
			return nil
		},
	}
	addActorFlag(cmd, &actor, &actorToken)
	return cmd
}

func newConsumersCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var actorToken string
	cmd := &cobra.Command{
		Use:   "consumers",
		Short: "List the engine's managed consumers and their pause state",
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			resp, err := request(*natsURL, control.ConsumersSubject(), resolveActorHeader(actor, actorToken))
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}

			if *outputFmt == "json" {
				return output.PrintJSON(resp.Consumers)
			}
			if len(resp.Consumers) == 0 {
				fmt.Println("(no managed consumers)")
				return nil
			}
			fmt.Printf("%-30s %s\n", "CONSUMER", "STATE")
			for _, c := range resp.Consumers {
				fmt.Printf("%-30s %s\n", c.Name, c.State)
			}
			return nil
		},
	}
	addActorFlag(cmd, &actor, &actorToken)
	return cmd
}

func newInspectCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var actorToken string
	cmd := &cobra.Command{
		Use:   "inspect <instanceId>",
		Short: "Inspect one Loom instance and its current step",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			instanceID := args[0]
			if err := validateName("instanceId", instanceID); err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}
			resp, err := request(*natsURL, control.NameSubject(instanceID, "inspect"), resolveActorHeader(actor, actorToken))
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}

			if *outputFmt == "json" {
				return output.PrintJSON(resp.Instance)
			}
			d := resp.Instance
			if d == nil {
				fmt.Println("(no instance detail)")
				return nil
			}
			fmt.Printf("instanceId:  %s\n", d.Instance.InstanceID)
			fmt.Printf("patternRef:  %s\n", d.Instance.PatternRef)
			fmt.Printf("subjectKey:  %s\n", d.Instance.SubjectKey)
			fmt.Printf("cursor:      %d\n", d.Instance.Cursor)
			fmt.Printf("status:      %s\n", d.Instance.Status)
			fmt.Printf("retryCount:  %d\n", d.Instance.RetryCount)
			fmt.Printf("terminal:    %t\n", d.Terminal)
			if d.CurrentStep == nil {
				fmt.Println("currentStep: (none)")
				return nil
			}
			fmt.Printf("currentStep: kind=%s", d.CurrentStep.Kind)
			if d.CurrentStep.Operation != "" {
				fmt.Printf(" operation=%s", d.CurrentStep.Operation)
			}
			if d.CurrentStep.Adapter != "" {
				fmt.Printf(" adapter=%s", d.CurrentStep.Adapter)
			}
			if d.CurrentStep.InstanceOp != "" {
				fmt.Printf(" instanceOp=%s", d.CurrentStep.InstanceOp)
			}
			if d.CurrentStep.ReplyOp != "" {
				fmt.Printf(" replyOp=%s", d.CurrentStep.ReplyOp)
			}
			fmt.Println()
			return nil
		},
	}
	addActorFlag(cmd, &actor, &actorToken)
	return cmd
}

func newPauseCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var actorToken string
	cmd := &cobra.Command{
		Use:   "pause <consumerName>",
		Short: "Pause a managed Loom consumer (persists across restart until resume)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			name := args[0]
			if err := validateName("consumerName", name); err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}
			resp, err := request(*natsURL, control.NameSubject(name, "pause"), resolveActorHeader(actor, actorToken))
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}

			if *outputFmt == "json" {
				return output.PrintJSON(resp.Pause)
			}
			note := "persists across restart until resume"
			if resp.Pause != nil && resp.Pause.Note != "" {
				note = resp.Pause.Note
			}
			fmt.Printf("consumer %q paused (%s)\n", name, note)
			return nil
		},
	}
	addActorFlag(cmd, &actor, &actorToken)
	return cmd
}

func newResumeCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var actorToken string
	cmd := &cobra.Command{
		Use:   "resume <consumerName>",
		Short: "Resume a paused Loom consumer",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = *defaultActor
			}
			name := args[0]
			if err := validateName("consumerName", name); err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}
			resp, err := request(*natsURL, control.NameSubject(name, "resume"), resolveActorHeader(actor, actorToken))
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}

			if *outputFmt == "json" {
				return output.PrintJSON(resp.Resume)
			}
			fmt.Printf("consumer %q resumed\n", name)
			return nil
		},
	}
	addActorFlag(cmd, &actor, &actorToken)
	return cmd
}
