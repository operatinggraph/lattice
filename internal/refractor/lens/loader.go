package lens

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	rulesStreamName    = "MATERIALIZER_RULES"
	rulesSubjectFilter = "materializer.rules.>"
	loaderDurableName  = "materializer-rule-loader"

	// reconnectDelay is the back-off pause before reopening the message iterator
	// after a transient receive error.
	reconnectDelay = 5 * time.Second
)

// UpdateCallback is called when an existing rule is updated (not on first load).
// old is a snapshot of the previous version; new is the updated version.
// kind is the result of ClassifyUpdate(old, new).
// The callback is called outside the Loader's mutex, after the rule is indexed and ACK'd.
type UpdateCallback func(old, new *Rule, kind UpdateKind)

// Loader subscribes to the rules JetStream stream and maintains a thread-safe
// rule index. On Start, it loads the latest version of every known rule
// (DeliverLastPerSubjectPolicy), then receives future updates in real time (hot reload).
type Loader struct {
	js             jetstream.JetStream
	mu             sync.RWMutex
	rules          map[string]*Rule
	started        atomic.Bool
	cons           jetstream.Consumer // retained for iterator reconnects
	updateCallback UpdateCallback     // optional; set via SetUpdateCallback before Start
	loadCallback   func(*Rule)        // optional; set via SetLoadCallback before Start
}

// SetUpdateCallback registers a callback to be invoked whenever an existing rule
// is updated (not on first load). Must be called before Start.
// The callback is invoked outside the Loader's internal mutex and after the
// message has been ACK'd — it is safe to call loader.Get inside the callback.
// Panics if called after Start.
func (l *Loader) SetUpdateCallback(fn UpdateCallback) {
	if l.started.Load() {
		panic("rule: SetUpdateCallback must be called before Start")
	}
	l.updateCallback = fn
}

// SetLoadCallback registers a callback invoked for every successfully loaded rule,
// including first loads and hot-reload updates. The callback fires in a goroutine
// after the rule is indexed and the message ACK'd. Panics if called after Start.
//
// When both SetUpdateCallback and SetLoadCallback are registered, the update
// callback fires first on hot-reload updates (preserving the existing contract).
//
// r.Sequence may be 0 if JetStream message metadata was unavailable at load time
// (a warning is logged in that case). Callers that propagate the sequence — e.g.
// reporter.SetRuleSequence(r.Sequence) — must guard against zero: a zero sequence
// must not overwrite a previously cached valid sequence number.
func (l *Loader) SetLoadCallback(fn func(*Rule)) {
	if l.started.Load() {
		panic("rule: SetLoadCallback must be called before Start")
	}
	l.loadCallback = fn
}

// NewLoader initialises JetStream from the given NATS connection.
// It does NOT create the stream or consumer — call Start to do that.
func NewLoader(nc *nats.Conn) (*Loader, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("init jetstream: %w", err)
	}
	return &Loader{
		js:    js,
		rules: make(map[string]*Rule),
	}, nil
}

// Start creates the rules stream (idempotent), creates a durable consumer with
// DeliverLastPerSubjectPolicy, and starts the consume goroutine in the background.
// Cancel ctx to stop. Returns after the goroutine is launched.
// Start must be called at most once; subsequent calls return an error.
func (l *Loader) Start(ctx context.Context) error {
	if !l.started.CompareAndSwap(false, true) {
		return fmt.Errorf("rule loader: Start called more than once")
	}

	_, err := l.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:              rulesStreamName,
		Subjects:          []string{rulesSubjectFilter},
		MaxMsgsPerSubject: 1, // retain only the latest version per rule subject
		Storage:           jetstream.FileStorage,
		Retention:         jetstream.LimitsPolicy,
	})
	if err != nil {
		return fmt.Errorf("create rules stream: %w", err)
	}

	cons, err := l.js.CreateOrUpdateConsumer(ctx, rulesStreamName, jetstream.ConsumerConfig{
		Durable:       loaderDurableName,
		FilterSubject: rulesSubjectFilter,
		DeliverPolicy: jetstream.DeliverLastPerSubjectPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return fmt.Errorf("create rules consumer: %w", err)
	}

	l.cons = cons
	go l.consumeLoop(ctx)
	return nil
}

// consumeLoop is the background goroutine. It opens a message iterator from the
// stored consumer and processes messages until ctx is cancelled. On transient
// iterator errors it waits reconnectDelay then reopens the iterator.
func (l *Loader) consumeLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}

		mc, err := l.cons.Messages()
		if err != nil {
			slog.Error("rule loader: open message iterator", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(reconnectDelay):
			}
			continue
		}

		l.drainMessages(ctx, mc)
	}
}

// drainMessages reads messages from mc until ctx is cancelled or mc returns an
// error. On a non-context error it logs and returns so consumeLoop can reopen.
func (l *Loader) drainMessages(ctx context.Context, mc jetstream.MessagesContext) {
	defer mc.Stop()
	for {
		msg, err := mc.Next()
		if err != nil {
			if ctx.Err() != nil {
				return // clean shutdown
			}
			slog.Error("rule loader: receive error, will reconnect", "err", err)
			return
		}

		// Cross-check: the payload id must match the rule-id segment of the subject
		// (materializer.rules.<team>.<rule-id>). The payload is the source of truth;
		// a mismatch means the message was routed incorrectly and must not be indexed.
		subjectParts := strings.Split(msg.Subject(), ".")
		if len(subjectParts) < 4 {
			slog.Error("rule loader: malformed subject", "subject", msg.Subject())
			if err := msg.Term(); err != nil {
				slog.Error("rule loader: term failed", "err", err, "subject", msg.Subject())
			}
			continue
		}
		subjectRuleID := subjectParts[len(subjectParts)-1]

		r, parseErr := Parse(msg.Data())
		if parseErr != nil {
			slog.Error("rule loader: invalid rule payload", "err", parseErr, "subject", msg.Subject())
			if err := msg.Term(); err != nil {
				slog.Error("rule loader: term failed", "err", err, "subject", msg.Subject())
			}
			continue
		}

		// Populate Rule.Sequence from JetStream message metadata.
		if meta, metaErr := msg.Metadata(); metaErr == nil {
			r.Sequence = meta.Sequence.Stream
		} else {
			slog.Warn("rule loader: could not read message metadata", "ruleId", r.ID, "err", metaErr)
		}

		if r.ID != subjectRuleID {
			slog.Error("rule loader: payload id does not match subject rule-id segment",
				"payloadID", r.ID, "subjectRuleID", subjectRuleID, "subject", msg.Subject())
			if err := msg.Term(); err != nil {
				slog.Error("rule loader: term failed", "err", err, "subject", msg.Subject())
			}
			continue
		}

		// Capture old rule (if any) before overwriting; used to fire update callback.
		l.mu.Lock()
		var oldRule *Rule
		if existing, ok := l.rules[r.ID]; ok {
			snapshot := *existing
			// Deep-copy the Key slice so the snapshot is fully independent of the live rule.
			snapshot.Into.Key = append([]string(nil), existing.Into.Key...)
			oldRule = &snapshot
		}
		newRule := *r
		l.rules[newRule.ID] = &newRule
		l.mu.Unlock()

		slog.Info("rule loaded", "ruleId", r.ID, "team", r.Team)
		if err := msg.Ack(); err != nil {
			slog.Error("rule loader: ack failed", "err", err, "ruleId", r.ID)
		}

		// Fire the update callback outside the lock and after ACK so callbacks
		// can safely call loader.Get and are not blocked by the indexing mutex.
		// The callback is only fired when a prior version existed (not on first load).
		// Fired in a goroutine to avoid slow callbacks stalling the consume loop;
		// panics inside the callback are recovered and logged rather than crashing.
		if oldRule != nil && l.updateCallback != nil {
			cb := l.updateCallback
			captured := newRule
			go func() {
				defer func() {
					if rec := recover(); rec != nil {
						slog.Error("rule loader: update callback panicked", "recover", rec)
					}
				}()
				cb(oldRule, &captured, ClassifyUpdate(oldRule, &captured))
			}()
		}

		// Fire the load callback for EVERY successful rule load (first loads and updates).
		if l.loadCallback != nil {
			captured := newRule
			cb := l.loadCallback
			go func() {
				defer func() {
					if rec := recover(); rec != nil {
						slog.Error("rule loader: load callback panicked", "recover", rec, "ruleId", captured.ID)
					}
				}()
				cb(&captured)
			}()
		}
	}
}

// Get returns a copy of the active rule for the given ID, or (nil, false) if not found.
func (l *Loader) Get(ruleID string) (*Rule, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	r, ok := l.rules[ruleID]
	if !ok {
		return nil, false
	}
	cp := *r
	return &cp, true
}

// All returns a snapshot of copies of all currently active rules.
func (l *Loader) All() []*Rule {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]*Rule, 0, len(l.rules))
	for _, r := range l.rules {
		cp := *r
		out = append(out, &cp)
	}
	return out
}
