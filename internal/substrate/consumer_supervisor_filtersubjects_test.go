package substrate

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// TestSupervisor_FilterSubjects_MultiSubjectDelivery proves a spec configured
// with the FilterSubjects set receives every listed subject and nothing else —
// the multi-filter the Processor's processor-main durable needs to cover all four
// operation lanes from one supervised consumer.
func TestSupervisor_FilterSubjects_MultiSubjectDelivery(t *testing.T) {
	t.Parallel()
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

// TestSupervisor_FilterSubjects_ConsumerConfigShowsThem proves a spec
// configured with FilterSubjects creates a durable whose server-side config
// carries that exact set (and an empty FilterSubject) — the config-shape half
// of the FilterSubjects contract, complementing
// TestSupervisor_FilterSubjects_MultiSubjectDelivery's delivery-behavior half.
func TestSupervisor_FilterSubjects_ConsumerConfigShowsThem(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	stream := "ops-filtersubjects-config"
	if err := c.EnsureStream(ctx, StreamSpec{Name: stream, Subjects: []string{"ops.>"}}); err != nil {
		t.Fatalf("EnsureStream: %v", err)
	}

	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	want := []string{"ops.default", "ops.meta", "ops.urgent"}
	spec := ConsumerSpec{
		Name:           "sup-filtersubjects-config",
		Stream:         stream,
		FilterSubjects: want,
		Handler:        func(_ context.Context, _ Message) (Decision, error) { return Ack, nil },
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}

	info := consumerInfoByName(ctx, t, c, stream, "sup-filtersubjects-config")
	if info.Config.FilterSubject != "" {
		t.Fatalf("Config.FilterSubject = %q, want empty when FilterSubjects is set", info.Config.FilterSubject)
	}
	got := append([]string(nil), info.Config.FilterSubjects...)
	sort.Strings(got)
	sortedWant := append([]string(nil), want...)
	sort.Strings(sortedWant)
	if len(got) != len(sortedWant) {
		t.Fatalf("Config.FilterSubjects = %v, want %v", got, sortedWant)
	}
	for i := range got {
		if got[i] != sortedWant[i] {
			t.Fatalf("Config.FilterSubjects = %v, want %v", got, sortedWant)
		}
	}
}

// TestSupervisor_Add_FilterSubjectAndFilterSubjectsMutuallyExclusive proves
// validateSpec's fail-loud guard: a spec setting both FilterSubject and
// FilterSubjects is rejected before any consumer is created, rather than
// silently letting one win (the multi-filter form takes precedence in
// createConsumer, which would otherwise make a caller's FilterSubject typo
// silently vanish instead of erroring).
func TestSupervisor_Add_FilterSubjectAndFilterSubjectsMutuallyExclusive(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	stream := "ops-filtersubjects-exclusive"
	if err := c.EnsureStream(ctx, StreamSpec{Name: stream, Subjects: []string{"ops.>"}}); err != nil {
		t.Fatalf("EnsureStream: %v", err)
	}

	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	spec := ConsumerSpec{
		Name:           "sup-filtersubjects-exclusive",
		Stream:         stream,
		FilterSubject:  "ops.default",
		FilterSubjects: []string{"ops.meta", "ops.urgent"},
		Handler:        func(_ context.Context, _ Message) (Decision, error) { return Ack, nil },
	}
	err := sup.Add(ctx, spec)
	if err == nil {
		t.Fatal("Add with both FilterSubject and FilterSubjects set must fail, got nil error")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("Add error = %q, want it to name the mutual-exclusion violation", err.Error())
	}
	if sup.IsManaged("sup-filtersubjects-exclusive") {
		t.Fatal("a spec that fails validation must never become managed")
	}
}

// TestSupervisor_Message_HeaderReplyInbox proves the supervised Message exposes
// delivered-message headers via Message.Header — the seam the Processor commit
// path uses to read the caller's Lattice-Reply-Inbox for a request-reply answer.
func TestSupervisor_Message_HeaderReplyInbox(t *testing.T) {
	t.Parallel()
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

// TestVerifyStoredFilterSubjects pins the self-heal read-back createConsumer
// runs when the client library rejects a create/update response as
// "multiple filter subjects not supported": the server is the authority on
// what was stored, so a stored config carrying exactly the requested filter
// set serves the consumer handle, and any mismatch leaves the original error
// standing (nil, nil).
func TestVerifyStoredFilterSubjects(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	stream := "ops-verifyfs"
	if err := c.EnsureStream(ctx, StreamSpec{Name: stream, Subjects: []string{"vfs.>"}}); err != nil {
		t.Fatalf("EnsureStream: %v", err)
	}
	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	want := []string{"vfs.a.>", "vfs.b.>"}
	if _, err := c.js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable: "vfs-probe", AckPolicy: jetstream.AckExplicitPolicy, FilterSubjects: want,
	}); err != nil {
		t.Fatalf("create probe consumer: %v", err)
	}

	cons, err := sup.verifyStoredFilterSubjects(ctx, stream, "vfs-probe", want)
	if err != nil {
		t.Fatalf("verify matching set: %v", err)
	}
	if cons == nil {
		t.Fatal("stored config matches the requested set — the create succeeded server-side and must be accepted")
	}

	cons, err = sup.verifyStoredFilterSubjects(ctx, stream, "vfs-probe", []string{"vfs.a.>", "vfs.c.>"})
	if err != nil || cons != nil {
		t.Fatalf("mismatched stored set must yield (nil, nil); got cons=%v err=%v", cons, err)
	}

	cons, err = sup.verifyStoredFilterSubjects(ctx, stream, "vfs-probe", []string{"vfs.a.>"})
	if err != nil || cons != nil {
		t.Fatalf("shorter requested set must yield (nil, nil); got cons=%v err=%v", cons, err)
	}
}
