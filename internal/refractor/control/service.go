package control

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/refractor/engine"
	"github.com/asolgan/lattice/internal/refractor/health"
	"github.com/asolgan/lattice/internal/refractor/lens"
	"github.com/asolgan/lattice/internal/refractor/subjects"
)

// validateSampleSize is the maximum number of Core KV entries sampled by the validate op.
const validateSampleSize = 10

// validateTimeout bounds the total time allowed for the validate op (NFR10 — CI-practical latency).
const validateTimeout = 5 * time.Second

// Resumer is implemented by any component that can be unblocked from a structural or manual pause.
// *pipeline.Pipeline satisfies this interface via its Resume method.
// Defined here so internal/control does not import internal/pipeline (architecture boundary).
type Resumer interface {
	Resume(ctx context.Context)
}

// Pauser is implemented by any component that can be manually paused.
// *pipeline.Pipeline satisfies this interface via its Pause method.
// Defined here so internal/control does not import internal/pipeline (architecture boundary).
type Pauser interface {
	Pause(ctx context.Context)
}

// RuleGetter is a read-only interface for looking up loaded rules by ID.
// *lens.Loader satisfies this via its Get method.
type RuleGetter interface {
	Get(ruleID string) (*lens.Rule, bool)
}

// Rebuilder is implemented by any component that can perform an in-place rebuild
// of the rule's target store. *pipeline.Pipeline satisfies this via its Rebuild method.
// Defined here so internal/control does not import internal/pipeline (architecture boundary).
type Rebuilder interface {
	Rebuild(ctx context.Context, truncate bool) error
}

// Deleter is implemented by any component that can cleanly stop a rule and remove
// its associated NATS resources. Typically implemented as an orchestrator closure that:
//  1. Cancels the pipeline's run context and waits for Run() to return.
//  2. Calls consumer.Manager.Remove(ctx, ruleID) to delete the durable consumer.
//  3. Calls health.Reporter.Delete(ctx) to remove the health KV entry.
//
// Defined here so internal/control does not import internal/pipeline (architecture boundary).
type Deleter interface {
	Delete(ctx context.Context) error
}

// ControlRequest is the JSON payload sent to materializer.control for all operations.
type ControlRequest struct {
	Op       string `json:"op"`                 // operation: "health" | "validate" | "rebuild" | "pause" | "resume" | "delete"
	RuleID   string `json:"ruleId"`             // target rule identifier
	Team     string `json:"team,omitempty"`     // optional; used for context in "validate" op
	Truncate bool   `json:"truncate,omitempty"` // used by "rebuild" op; default false
}

// ControlResponse is the JSON payload returned by the control service.
// On success (health op): Entry fields are present (promoted from embedded *health.Entry).
// On success (validate op): Validate field is present; Entry fields are absent.
// On success (rebuild op): Rebuild field is present; Entry fields are absent.
// On success (pause op): Pause field is present; Entry fields are absent.
// On success (resume op): Resume field is present; Entry fields are absent.
// On success (delete op): Delete field is present; Entry fields are absent.
// On error: only "error" field is present.
type ControlResponse struct {
	*health.Entry                              // embedded; nil on non-health ops → fields absent in JSON
	Error    string          `json:"error,omitempty"`
	Validate *ValidateResult `json:"validate,omitempty"` // present only for "validate" op
	Rebuild  *RebuildResult  `json:"rebuild,omitempty"`  // present only for "rebuild" op
	Pause    *PauseResult    `json:"pause,omitempty"`    // present only for "pause" op
	Resume   *ResumeResult   `json:"resume,omitempty"`   // present only for "resume" op
	Delete   *DeleteResult   `json:"delete,omitempty"`   // present only for "delete" op
}

// RebuildResult is the async acknowledgement returned by the "rebuild" op.
// Started is always true when the op is accepted; the rebuild runs asynchronously.
type RebuildResult struct {
	Started bool `json:"started"`
}

// PauseResult is the synchronous acknowledgement returned by the "pause" op.
// Paused is always true when the op is accepted.
type PauseResult struct {
	Paused bool `json:"paused"`
}

// ResumeResult is the synchronous acknowledgement returned by the "resume" op.
// Resumed is always true when the op is accepted.
type ResumeResult struct {
	Resumed bool `json:"resumed"`
}

// DeleteResult is the synchronous acknowledgement returned by the "delete" op.
// Deleted is always true when the op is accepted.
type DeleteResult struct {
	Deleted bool `json:"deleted"`
}

// ValidateResult is returned by the "validate" op. It contains a best-effort
// field-presence report based on a sample of current Core KV entries.
type ValidateResult struct {
	SampleSize   int           `json:"sampleSize"`
	FieldReports []FieldReport `json:"fieldReports"`
	Warnings     []string      `json:"warnings,omitempty"` // fields absent from all sampled entries
}

// FieldReport describes the presence of one referenced field in the Core KV sample.
type FieldReport struct {
	Field   string `json:"field"`   // full expression, e.g. "a.id"
	FoundIn int    `json:"foundIn"` // number of sampled entries containing this property
	Present bool   `json:"present"` // true if foundIn > 0
}

// Service coordinates control operations (pause, resume, health query, validate, rebuild, delete).
// It maintains a registry of active pipeline interfaces keyed by ruleID.
// The orchestrator (cmd/materializer) registers each pipeline when it starts and unregisters it when it stops.
//
// Zero-downtime migration pattern (FR32): two rules with different IDs may run simultaneously.
// Register both rules before cutting over application traffic, then delete the old rule when
// the migration is complete and correctness has been verified.
// CONSTRAINT: two rules targeting the same table is undefined behavior — write order across
// independent pipelines is non-deterministic. Only rules targeting different tables may safely
// run in parallel.
type Service struct {
	mu                sync.Mutex
	resumerByRuleID   map[string]Resumer
	pauserByRuleID    map[string]Pauser
	rebuilderByRuleID map[string]Rebuilder
	deleterByRuleID   map[string]Deleter
	reporters         map[string]*health.Reporter
	sub               *nats.Subscription // set by StartNATSListener; nil until started
	ruleGetter        RuleGetter         // set via SetRuleGetter; used by validate op
	coreKV            jetstream.KeyValue  // set via SetCoreKV; used by validate op
}

// NewService creates a new Service with empty registries.
func NewService() *Service {
	return &Service{
		resumerByRuleID:   make(map[string]Resumer),
		pauserByRuleID:    make(map[string]Pauser),
		rebuilderByRuleID: make(map[string]Rebuilder),
		deleterByRuleID:   make(map[string]Deleter),
		reporters:         make(map[string]*health.Reporter),
	}
}

// SetRuleGetter registers the rule lookup interface used by the validate op.
// *lens.Loader satisfies RuleGetter. Thread-safe; may be called at any time.
func (s *Service) SetRuleGetter(rg RuleGetter) {
	s.mu.Lock()
	s.ruleGetter = rg
	s.mu.Unlock()
}

// SetCoreKV registers the Core KV handle used by the validate op to sample entries.
// Thread-safe; may be called at any time.
func (s *Service) SetCoreKV(kv jetstream.KeyValue) {
	s.mu.Lock()
	s.coreKV = kv
	s.mu.Unlock()
}

// Register records a Resumer and health.Reporter for the given ruleID.
// Overwrites any prior registration for the same ruleID (safe for hot-reload).
// Panics if r is nil — a nil Resumer would cause a runtime panic in ResumeRule.
func (s *Service) Register(ruleID string, r Resumer, reporter *health.Reporter) {
	if r == nil {
		panic("control: Register: Resumer must not be nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resumerByRuleID[ruleID] = r
	if reporter != nil {
		s.reporters[ruleID] = reporter
	}
}

// Unregister removes all registry entries (Resumer, Pauser, Rebuilder, Deleter, Reporter) for ruleID.
// No-op for any map that does not contain ruleID.
func (s *Service) Unregister(ruleID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.resumerByRuleID, ruleID)
	delete(s.pauserByRuleID, ruleID)
	delete(s.rebuilderByRuleID, ruleID)
	delete(s.deleterByRuleID, ruleID)
	delete(s.reporters, ruleID)
}

// RegisterRebuilder records a Rebuilder for the given ruleID.
// Overwrites any prior registration (safe for hot-reload).
// Panics if r is nil.
func (s *Service) RegisterRebuilder(ruleID string, r Rebuilder) {
	if r == nil {
		panic("control: RegisterRebuilder: Rebuilder must not be nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rebuilderByRuleID[ruleID] = r
}

// UnregisterRebuilder removes the Rebuilder entry for ruleID.
// No-op if ruleID is not registered.
func (s *Service) UnregisterRebuilder(ruleID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rebuilderByRuleID, ruleID)
}

// RegisterPauser records a Pauser for the given ruleID.
// Overwrites any prior registration (safe for hot-reload).
// Panics if p is nil.
func (s *Service) RegisterPauser(ruleID string, p Pauser) {
	if p == nil {
		panic("control: RegisterPauser: Pauser must not be nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pauserByRuleID[ruleID] = p
}

// UnregisterPauser removes the Pauser entry for ruleID.
// No-op if ruleID is not registered.
func (s *Service) UnregisterPauser(ruleID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pauserByRuleID, ruleID)
}

// RegisterDeleter records a Deleter for the given ruleID.
// Overwrites any prior registration (safe for hot-reload).
// Panics if d is nil.
func (s *Service) RegisterDeleter(ruleID string, d Deleter) {
	if d == nil {
		panic("control: RegisterDeleter: Deleter must not be nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleterByRuleID[ruleID] = d
}

// UnregisterDeleter removes the Deleter entry for ruleID.
// No-op if ruleID is not registered.
func (s *Service) UnregisterDeleter(ruleID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.deleterByRuleID, ruleID)
}

// ResumeRule unblocks a structural pause for the given ruleID.
// Returns an error if ruleID is not registered.
// Pipeline.Resume sets health KV to active internally; this method does not touch health KV directly.
func (s *Service) ResumeRule(ctx context.Context, ruleID string) error {
	s.mu.Lock()
	r, ok := s.resumerByRuleID[ruleID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("control: rule %q not registered", ruleID)
	}
	r.Resume(ctx)
	return nil
}

// StartNATSListener registers a NATS queue subscription on subjects.Control()
// ("materializer.control") that handles JSON control requests. The queue group
// "materializer-control" ensures exactly one instance handles each request when
// multiple Materializer processes are running (stateless, NFR14).
// The subscription is unsubscribed when ctx is cancelled.
// Returns an error if the subscription cannot be established or if already started.
func (s *Service) StartNATSListener(ctx context.Context, nc *nats.Conn) error {
	s.mu.Lock()
	if s.sub != nil {
		s.mu.Unlock()
		return fmt.Errorf("control: NATS listener already started")
	}
	s.mu.Unlock()

	sub, err := nc.QueueSubscribe(subjects.Control(), "materializer-control", s.handleControlMsg)
	if err != nil {
		return fmt.Errorf("control: subscribe to %s: %w", subjects.Control(), err)
	}
	s.sub = sub
	go func() {
		<-ctx.Done()
		if err := sub.Unsubscribe(); err != nil {
			slog.Error("control: unsubscribe from control subject", "err", err)
		}
	}()
	return nil
}

// handleControlMsg is the NATS message handler for all control requests.
// It parses the JSON request, dispatches by op, and writes a JSON response.
func (s *Service) handleControlMsg(msg *nats.Msg) {
	var req ControlRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		s.respond(msg, ControlResponse{Error: fmt.Sprintf("invalid request: %s", err.Error())})
		return
	}

	switch req.Op {
	case "health":
		resp := s.getHealth(context.Background(), req.RuleID)
		s.respond(msg, resp)
	case "validate":
		ctx, cancel := context.WithTimeout(context.Background(), validateTimeout)
		defer cancel()
		resp := s.validateRule(ctx, req.RuleID)
		s.respond(msg, resp)
	case "rebuild":
		resp := s.rebuildRule(req.RuleID, req.Truncate)
		s.respond(msg, resp)
	case "pause":
		resp := s.pauseRule(context.Background(), req.RuleID)
		s.respond(msg, resp)
	case "resume":
		resp := s.resumeRule(context.Background(), req.RuleID)
		s.respond(msg, resp)
	case "delete":
		resp := s.deleteRule(context.Background(), req.RuleID)
		s.respond(msg, resp)
	default:
		s.respond(msg, ControlResponse{Error: fmt.Sprintf("unknown operation: %s", req.Op)})
	}
}

// getHealth returns the health KV entry for ruleID as a ControlResponse.
// Returns an error response if the rule is not registered or the KV read fails.
func (s *Service) getHealth(ctx context.Context, ruleID string) ControlResponse {
	s.mu.Lock()
	reporter, ok := s.reporters[ruleID]
	s.mu.Unlock()
	if !ok {
		return ControlResponse{Error: fmt.Sprintf("rule %q not registered", ruleID)}
	}
	entry, err := reporter.GetStatus(ctx)
	if err != nil {
		return ControlResponse{Error: err.Error()}
	}
	return ControlResponse{Entry: &entry}
}

// rebuildRule is async: it looks up the registered Rebuilder, launches the rebuild in
// a background goroutine, and returns an ack immediately (AC5, AC6).
// Errors from the background rebuild are logged via slog; they do not surface to the caller.
func (s *Service) rebuildRule(ruleID string, truncate bool) ControlResponse {
	s.mu.Lock()
	r, ok := s.rebuilderByRuleID[ruleID]
	s.mu.Unlock()
	if !ok {
		return ControlResponse{Error: fmt.Sprintf("rule %q not registered", ruleID)}
	}
	go func() {
		if err := r.Rebuild(context.Background(), truncate); err != nil {
			slog.Error("control: rebuild failed", "ruleId", ruleID, "err", err)
		}
	}()
	return ControlResponse{Rebuild: &RebuildResult{Started: true}}
}

// pauseRule calls the registered Pauser for ruleID to halt its fetch loop.
// Synchronous: returns an ack after Pause() returns (FR30, AC1, AC5).
func (s *Service) pauseRule(ctx context.Context, ruleID string) ControlResponse {
	s.mu.Lock()
	p, ok := s.pauserByRuleID[ruleID]
	s.mu.Unlock()
	if !ok {
		return ControlResponse{Error: fmt.Sprintf("rule %q not registered", ruleID)}
	}
	p.Pause(ctx)
	return ControlResponse{Pause: &PauseResult{Paused: true}}
}

// resumeRule calls the registered Resumer for ruleID to unblock its pause.
// Synchronous: returns an ack after Resume() returns (FR31, AC2, AC6).
func (s *Service) resumeRule(ctx context.Context, ruleID string) ControlResponse {
	s.mu.Lock()
	r, ok := s.resumerByRuleID[ruleID]
	s.mu.Unlock()
	if !ok {
		return ControlResponse{Error: fmt.Sprintf("rule %q not registered", ruleID)}
	}
	r.Resume(ctx)
	return ControlResponse{Resume: &ResumeResult{Resumed: true}}
}

// deleteRule stops the rule and cleans up its NATS consumer and health KV entry.
// Synchronous: calls d.Delete(ctx) (which cancels the pipeline, removes the consumer,
// and deletes the health KV entry), then calls Unregister to remove all registrations.
// Returns error response if ruleID is not registered or if Delete fails (FR39, AC1, AC4).
//
// The Deleter is removed from deleterByRuleID under the lock BEFORE d.Delete(ctx) is
// called. This prevents a concurrent second deleteRule call from also retrieving the
// Deleter and running a double-delete in parallel — the second call will see nothing in
// the map and return "not registered" immediately.
func (s *Service) deleteRule(ctx context.Context, ruleID string) ControlResponse {
	s.mu.Lock()
	d, ok := s.deleterByRuleID[ruleID]
	if ok {
		// Remove now so a concurrent deleteRule for the same ruleID fails fast.
		delete(s.deleterByRuleID, ruleID)
	}
	s.mu.Unlock()
	if !ok {
		return ControlResponse{Error: fmt.Sprintf("rule %q not registered", ruleID)}
	}
	if err := d.Delete(ctx); err != nil {
		return ControlResponse{Error: fmt.Sprintf("delete %q: %s", ruleID, err.Error())}
	}
	s.Unregister(ruleID) // cleans remaining four registries (deleterByRuleID already cleared above)
	return ControlResponse{Delete: &DeleteResult{Deleted: true}}
}

// validateRule samples Core KV entries and checks whether the MATCH/RETURN clause
// fields are present in the sample. Returns a best-effort field-presence report.
func (s *Service) validateRule(ctx context.Context, ruleID string) ControlResponse {
	s.mu.Lock()
	rg := s.ruleGetter
	coreKV := s.coreKV
	s.mu.Unlock()

	if rg == nil {
		return ControlResponse{Error: "validate: rule getter not configured"}
	}
	if coreKV == nil {
		return ControlResponse{Error: "validate: Core KV not configured"}
	}

	r, ok := rg.Get(ruleID)
	if !ok {
		return ControlResponse{Error: fmt.Sprintf("rule %q not loaded", ruleID)}
	}

	query, err := engine.Parse(r.Match)
	if err != nil {
		return ControlResponse{Error: fmt.Sprintf("validate: parse match: %s", err)}
	}
	plan, err := engine.Compile(query, r.Into.Key)
	if err != nil {
		return ControlResponse{Error: fmt.Sprintf("validate: compile plan: %s", err)}
	}

	// Stream keys from Core KV; stop after validateSampleSize for fast CI response.
	lister, err := coreKV.ListKeys(ctx)
	if err != nil {
		// Empty bucket or context error — not a hard failure; return all-absent report.
		return ControlResponse{Validate: buildEmptyValidateResult(plan.Columns)}
	}
	defer lister.Stop() //nolint:errcheck

	var sampledKeys []string
	for key := range lister.Keys() {
		sampledKeys = append(sampledKeys, key)
		if len(sampledKeys) >= validateSampleSize {
			break
		}
	}

	// For each sampled key, decode the JSON value and count property hits.
	propertyHits := make(map[string]int) // property name → count of entries containing it
	sampleSize := 0
	for _, key := range sampledKeys {
		entry, err := coreKV.Get(ctx, key)
		if err != nil {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal(entry.Value(), &doc); err != nil {
			continue // skip non-JSON entries (e.g. deleted markers)
		}
		sampleSize++
		for _, col := range plan.Columns {
			if _, exists := doc[col.Property]; exists {
				propertyHits[col.Property]++
			}
		}
	}

	// Build one FieldReport per unique Expression; warn on absent fields.
	reports := make([]FieldReport, 0, len(plan.Columns))
	warnings := make([]string, 0)
	seen := make(map[string]bool)
	for _, col := range plan.Columns {
		if seen[col.Expression] {
			continue
		}
		seen[col.Expression] = true
		foundIn := propertyHits[col.Property]
		present := foundIn > 0
		reports = append(reports, FieldReport{
			Field:   col.Expression,
			FoundIn: foundIn,
			Present: present,
		})
		if !present {
			warnings = append(warnings, fmt.Sprintf("field %q not found in any sampled Core KV entry", col.Expression))
		}
	}

	return ControlResponse{Validate: &ValidateResult{
		SampleSize:   sampleSize,
		FieldReports: reports,
		Warnings:     warnings,
	}}
}

// buildEmptyValidateResult returns a ValidateResult with all fields absent (sampleSize=0).
// Used when Core KV is unreachable or empty.
func buildEmptyValidateResult(columns []engine.Column) *ValidateResult {
	reports := make([]FieldReport, 0, len(columns))
	warnings := make([]string, 0)
	seen := make(map[string]bool)
	for _, col := range columns {
		if seen[col.Expression] {
			continue
		}
		seen[col.Expression] = true
		reports = append(reports, FieldReport{Field: col.Expression, FoundIn: 0, Present: false})
		warnings = append(warnings, fmt.Sprintf("field %q not found in any sampled Core KV entry", col.Expression))
	}
	return &ValidateResult{SampleSize: 0, FieldReports: reports, Warnings: warnings}
}

// respond marshals v to JSON and sends it as a NATS reply.
// Logs errors rather than returning them — the caller cannot do anything useful with them.
func (s *Service) respond(msg *nats.Msg, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		slog.Error("control: marshal response", "err", err)
		return
	}
	if err := msg.Respond(data); err != nil {
		slog.Error("control: send response", "err", err)
	}
}
