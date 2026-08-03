// Package loom implements the lattice loom command group: operator
// list/consumers/inspect/pause/resume/redrive controls for the Loom
// orchestration engine, via the lattice.ctrl.loom.* NATS Services control
// plane.
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
		Short: "Operate the Loom engine (list/consumers/inspect/pause/resume/redrive)",
	}
	cmd.AddCommand(newListCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newConsumersCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newInspectCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newPauseCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newResumeCommand(natsURL, outputFmt, defaultActor))
	cmd.AddCommand(newRedriveCommand(natsURL, outputFmt, defaultActor))
	return cmd
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
			resp, err := request(*natsURL, control.ListSubject(), output.ResolveActorHeader(actor, actorToken))
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
	output.AddActorFlags(cmd, &actor, &actorToken)
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
			resp, err := request(*natsURL, control.ConsumersSubject(), output.ResolveActorHeader(actor, actorToken))
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
	output.AddActorFlags(cmd, &actor, &actorToken)
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
			resp, err := request(*natsURL, control.NameSubject(instanceID, "inspect"), output.ResolveActorHeader(actor, actorToken))
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
	output.AddActorFlags(cmd, &actor, &actorToken)
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
			resp, err := request(*natsURL, control.NameSubject(name, "pause"), output.ResolveActorHeader(actor, actorToken))
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
	output.AddActorFlags(cmd, &actor, &actorToken)
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
			resp, err := request(*natsURL, control.NameSubject(name, "resume"), output.ResolveActorHeader(actor, actorToken))
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
	output.AddActorFlags(cmd, &actor, &actorToken)
	return cmd
}

func newRedriveCommand(natsURL, outputFmt, defaultActor *string) *cobra.Command {
	var actor string
	var actorToken string
	cmd := &cobra.Command{
		Use:   "redrive <instanceId>",
		Short: "Resume a FAILED instance at its recorded cursor (never restarts it)",
		Long: "Resume a FAILED instance at its recorded cursor — the only step it never completed. " +
			"This never restarts the instance under a fresh id: doing so would re-execute every step " +
			"the failed run already committed. Refuses (typed error) if the instance is not failed, its " +
			"pattern is no longer loaded, or its cursor no longer indexes a step in the current pattern " +
			"definition (the pattern was edited since the failure).",
		Args: cobra.ExactArgs(1),
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
			resp, err := request(*natsURL, control.NameSubject(instanceID, "redrive"), output.ResolveActorHeader(actor, actorToken))
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ControlError", err.Error())
				}
				return err
			}

			if *outputFmt == "json" {
				return output.PrintJSON(resp.Redrive)
			}
			fmt.Printf("instance %q redriven\n", instanceID)
			return nil
		},
	}
	output.AddActorFlags(cmd, &actor, &actorToken)
	return cmd
}
