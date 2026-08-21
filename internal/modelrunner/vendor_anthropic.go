package modelrunner

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// anthropicVendor calls the Anthropic Messages API through the official Go
// SDK. It is the only place in the platform that holds a vendor credential and
// speaks to a third party.
type anthropicVendor struct {
	client anthropic.Client
	apiKey string
}

// NewAnthropicVendor builds the production VendorCaller. apiKey is held only
// to scrub it out of error strings; the SDK client carries its own copy for
// the Authorization header.
func NewAnthropicVendor(apiKey string) VendorCaller {
	return &anthropicVendor{
		client: anthropic.NewClient(option.WithAPIKey(apiKey)),
		apiKey: apiKey,
	}
}

// Generate performs one model call and returns the forced tool's input JSON.
//
// Three choices are load-bearing:
//
//   - The call streams and the final message is accumulated. A turn with
//     adaptive thinking can run for minutes with nothing on the wire, which a
//     non-streaming HTTP request reads as an idle connection and drops.
//   - The single tool is strict (`strict: true` plus
//     `additionalProperties: false`) and tool_choice forces it. The model
//     therefore cannot answer in prose, cannot invent fields, and cannot call
//     anything else — the caller's schema is the only exit.
//   - No fallback routing is enabled. A fallback would answer with a model the
//     caller never asked for, and the whole point of recording Model and Usage
//     here is that a proposal's provenance names the model that really wrote
//     it. A refusal is reported as a refusal.
//
// Thinking is deliberately unset: the default model runs adaptive thinking on
// its own, and an explicit budget is rejected by this model family.
func (v *anthropicVendor) Generate(ctx context.Context, req VendorRequest) (VendorResult, error) {
	tool := anthropic.ToolParam{
		Name:        req.Tool.Name,
		Description: anthropic.String(req.Tool.Description),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: req.Tool.InputSchema.Properties,
			Required:   req.Tool.InputSchema.Required,
			// Strict tool use requires the schema to close itself; the runner
			// supplies this rather than trusting the caller to remember.
			ExtraFields: map[string]any{"additionalProperties": false},
		},
		Strict: anthropic.Bool(true),
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: req.MaxTokens,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(req.Prompt)),
		},
		Tools:      []anthropic.ToolUnionParam{{OfTool: &tool}},
		ToolChoice: forceTool(req.Tool.Name),
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}

	stream := v.client.Messages.NewStreaming(ctx, params)
	var msg anthropic.Message
	for stream.Next() {
		if err := msg.Accumulate(stream.Current()); err != nil {
			return VendorResult{}, v.wrap(fmt.Errorf("accumulate stream event: %w", err))
		}
	}
	if err := stream.Err(); err != nil {
		return VendorResult{}, v.wrap(fmt.Errorf("model call: %w", err))
	}

	out := VendorResult{
		Model:        string(msg.Model),
		InputTokens:  msg.Usage.InputTokens,
		OutputTokens: msg.Usage.OutputTokens,
	}

	if msg.StopReason == anthropic.StopReasonRefusal {
		out.Refused = true
		out.RefusalCategory = string(msg.StopDetails.Category)
		return out, nil
	}

	input, ok := toolInput(msg, req.Tool.Name)
	if !ok {
		// Forced tool choice makes this unreachable on a well-formed turn, so
		// it means the turn ended early — output truncated at MaxTokens is the
		// usual cause. Report the stop reason rather than a bare "no output":
		// it is the difference between "ask for more tokens" and "something
		// changed at the vendor".
		return out, v.wrap(fmt.Errorf("response carried no %q tool call (stop_reason=%s)",
			req.Tool.Name, msg.StopReason))
	}
	out.Output = input
	return out, nil
}

// forceTool builds the tool_choice union that pins the model to exactly one
// call of the named tool.
func forceTool(name string) anthropic.ToolChoiceUnionParam {
	return anthropic.ToolChoiceUnionParam{
		OfTool: &anthropic.ToolChoiceToolParam{
			Name:                   name,
			DisableParallelToolUse: anthropic.Bool(true),
		},
	}
}

// toolInput pulls the named tool call's input JSON out of the response,
// unparsed. The runner is domain-free: whatever the schema admitted is the
// caller's to interpret.
func toolInput(msg anthropic.Message, name string) (json.RawMessage, bool) {
	for _, block := range msg.Content {
		use, ok := block.AsAny().(anthropic.ToolUseBlock)
		if !ok || use.Name != name {
			continue
		}
		if len(use.Input) > 0 && json.Valid(use.Input) {
			return json.RawMessage(append([]byte(nil), use.Input...)), true
		}
		if raw := use.JSON.Input.Raw(); raw != "" && json.Valid([]byte(raw)) {
			return json.RawMessage(raw), true
		}
	}
	return nil, false
}

// wrap scrubs the credential out of an error before it can reach a log line or
// a result row. SDK errors echo request context, and a redaction that lives
// only at the recording site is one refactor away from being bypassed.
func (v *anthropicVendor) wrap(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("anthropic: %s", Redact(err.Error(), v.apiKey))
}
