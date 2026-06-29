package substrate

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// TestSupervisor_FilterSubjects_MultiSubjectDelivery proves a spec configured
// with the FilterSubjects set receives every listed subject and nothing else —
// the multi-filter the Processor's processor-main durable needs to cover all four
// operation lanes from one supervised consumer.
func TestSupervisor_FilterSubjects_MultiSubjectDelivery(t *testing.T) {
	c, ctx := newTestConn(t)
	stream := "ops-filtersubjects"
	if err := c.EnsureStream(ctx, StreamSpec{Name: stream, Subjects: []string{"ops.>"}}); err != nil {
		t.Fatalf("EnsureStream: %v", err)
	}

	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	var (
		mu   sync.Mutex
		seen []string
	)
	got := make(chan struct{}, 8)
	spec := ConsumerSpec{
		Name:           "sup-filtersubjects",
		Stream:         stream,
		FilterSubjects: []string{"ops.default", "ops.meta"},
		Handler: func(_ context.Context, m Message) (Decision, error) {
			mu.Lock()
			seen = append(seen, m.Subject)
			mu.Unlock()
			got <- struct{}{}
			return Ack, nil
		},
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}

	for _, subj := range []string{"ops.default", "ops.meta", "ops.urgent"} {
		if err := c.PublishCore(ctx, subj, []byte(`{"v":1}`)); err != nil {
			t.Fatalf("publish %q: %v", subj, err)
		}
	}

	// Expect exactly the two filtered subjects; ops.urgent must never arrive.
	for i := 0; i < 2; i++ {
		select {
		case <-got:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of 2 filtered messages delivered", i)
		}
	}
	// Give a stray ops.urgent delivery a chance to (wrongly) arrive.
	select {
	case <-got:
		t.Fatalf("an unfiltered subject was delivered")
	case <-time.After(300 * time.Millisecond):
	}

	mu.Lock()
	defer mu.Unlock()
	sort.Strings(seen)
	if len(seen) != 2 || seen[0] != "ops.default" || seen[1] != "ops.meta" {
		t.Fatalf("delivered subjects = %v, want [ops.default ops.meta]", seen)
	}
}

// TestSupervisor_Message_HeaderReplyInbox proves the supervised Message exposes
// delivered-message headers via Message.Header — the seam the Processor commit
// path uses to read the caller's Lattice-Reply-Inbox for a request-reply answer.
func TestSupervisor_Message_HeaderReplyInbox(t *testing.T) {
	c, ctx := newTestConn(t)
	stream := "ops-header"
	if err := c.EnsureStream(ctx, StreamSpec{Name: stream, Subjects: []string{"ops.>"}}); err != nil {
		t.Fatalf("EnsureStream: %v", err)
	}

	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	type captured struct {
		inbox        string
		hasHeader    bool
		replyPresent bool
		missing      string
	}
	resultCh := make(chan captured, 1)
	spec := ConsumerSpec{
		Name:          "sup-header",
		Stream:        stream,
		FilterSubject: "ops.default",
		Handler: func(_ context.Context, m Message) (Decision, error) {
			resultCh <- captured{
				hasHeader:    m.Header != nil,
				inbox:        m.Header("Lattice-Reply-Inbox"),
				replyPresent: m.ReplySubject != "",
				missing:      m.Header("No-Such-Header"),
			}
			return Ack, nil
		},
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}

	msg := &nats.Msg{
		Subject: "ops.default",
		Data:    []byte(`{"v":1}`),
		Header:  nats.Header{"Lattice-Reply-Inbox": []string{"_INBOX.reply.xyz"}},
	}
	if err := c.NATS().PublishMsg(msg); err != nil {
		t.Fatalf("PublishMsg: %v", err)
	}

	select {
	case r := <-resultCh:
		if !r.hasHeader {
			t.Fatalf("Message.Header was nil; the supervisor must populate it")
		}
		if r.inbox != "_INBOX.reply.xyz" {
			t.Fatalf("Header(Lattice-Reply-Inbox) = %q, want _INBOX.reply.xyz", r.inbox)
		}
		if r.missing != "" {
			t.Fatalf("Header(absent) = %q, want empty", r.missing)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("message with reply-inbox header not delivered")
	}
}
