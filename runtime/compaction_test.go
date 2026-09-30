package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func compactionFixture() *runtime.ContextCompaction {
	return &runtime.ContextCompaction{Version: 1, SourceTaskID: "parent", SourceSequence: 10, SourceDigest: strings.Repeat("ab", 32), RemovedMessages: 4, Summary: runtime.ContextSummary{Decisions: []string{"Keep the requested design"}, PendingWork: []string{"Complete the tests"}}}
}

func TestCompactionMetadataValidation(t *testing.T) {
	for _, mutate := range []func(*runtime.ContextCompaction){
		func(c *runtime.ContextCompaction) { c.Version = 2 }, func(c *runtime.ContextCompaction) { c.SourceTaskID = "other" },
		func(c *runtime.ContextCompaction) { c.SourceSequence = 0 }, func(c *runtime.ContextCompaction) { c.RemovedMessages = 0 },
		func(c *runtime.ContextCompaction) { c.SourceDigest = strings.Repeat("AB", 32) }, func(c *runtime.ContextCompaction) { c.SourceDigest = strings.Repeat("x", 64) },
		func(c *runtime.ContextCompaction) { c.SourceDigest = "a" }, func(c *runtime.ContextCompaction) { c.Summary = runtime.ContextSummary{} },
		func(c *runtime.ContextCompaction) { c.Summary.Decisions = []string{" "} }, func(c *runtime.ContextCompaction) { c.Summary.Decisions = []string{string([]byte{255})} },
		func(c *runtime.ContextCompaction) { c.Summary.Decisions = make([]string, 129) }, func(c *runtime.ContextCompaction) { c.Summary.Artifacts = []string{strings.Repeat("a", 64<<10)} },
	} {
		c := compactionFixture()
		mutate(c)
		if err := c.Validate("parent"); err == nil {
			t.Fatal("invalid metadata accepted", c)
		}
	}
	c := compactionFixture()
	if err := c.Validate(""); err == nil {
		t.Fatal("missing parent")
	}
	e := runtime.Event{Version: 1, ID: "e", TaskID: "task", SessionID: "s", Sequence: 1, CorrelationID: "task", Time: time.Now(), Kind: runtime.TaskStarted, Data: runtime.Data{ParentTaskID: "parent", Compaction: c}}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	e.Kind = runtime.TaskCompleted
	if err := e.Validate(); err == nil {
		t.Fatal("metadata accepted outside task start")
	}
}

func TestVersionTwoCompactionRequiresLineageEvent(t *testing.T) {
	checkpoint := compactionFixture()
	checkpoint.Version = 2
	checkpoint.SourceStateDigest = strings.Repeat("a", 64)
	for _, kind := range []runtime.Kind{runtime.TaskStarted, runtime.ContextCompacted} {
		event := runtime.Event{Version: 1, ID: "event", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 1, Time: time.Now().UTC(), Kind: kind,
			Data: runtime.Data{ParentTaskID: "parent", Compaction: checkpoint, Messages: []providers.Message{{Role: "user", Content: "replacement"}}, ReplacedMessages: 1}}
		if kind == runtime.TaskStarted {
			event.Data.ReplacedMessages = 0
		}
		if event.Validate() == nil {
			t.Fatalf("%s accepted version-two compaction without lineage", kind)
		}
	}
}

func TestCompactionPersistedBeforeProviderAndDetached(t *testing.T) {
	s, _ := store(t)
	r := runRequest()
	r.ParentTaskID = "parent"
	r.Compaction = compactionFixture()
	providerCalled := false
	l := runtime.Loop{Journal: journal(func(ctx context.Context, seq int64, e runtime.Event) error {
		if e.Kind == runtime.TaskStarted {
			r.Compaction.Summary.Decisions[0] = "caller mutation"
			r.Compaction.SourceDigest = "invalid"
			if e.Data.Compaction.Summary.Decisions[0] != "Keep the requested design" || e.Data.Compaction.SourceDigest != strings.Repeat("ab", 32) {
				t.Fatal("metadata aliases caller")
			}
		}
		return s.Append(ctx, seq, e)
	}), Provider: model(func(ctx context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		providerCalled = true
		events, err := s.Read(ctx, "task", 0, 100)
		if err != nil || len(events) < 1 || events[0].Data.Compaction == nil || len(events[0].Data.Messages) == 0 {
			t.Fatal("dispatch preceded durable compaction", events, err)
		}
		return emit(providers.Chunk{Text: "done", Done: true, FinishReason: "stop"})
	})}
	if _, err := l.Run(context.Background(), r); err != nil || !providerCalled {
		t.Fatal(err)
	}
}

func TestInvalidCompactionBlocksAllEffects(t *testing.T) {
	r := runRequest()
	r.ParentTaskID = "parent"
	r.Compaction = compactionFixture()
	r.Compaction.SourceSequence = 0
	l := runtime.Loop{Journal: journal(func(context.Context, int64, runtime.Event) error { t.Fatal("invalid metadata persisted"); return nil }), Provider: model(func(context.Context, providers.Request, func(providers.Chunk) error) error {
		t.Fatal("invalid metadata dispatched")
		return nil
	})}
	if _, err := l.Run(context.Background(), r); !errors.Is(err, runtime.ErrInvalidRun) {
		t.Fatal(err)
	}
}

func TestCompactionInitialPersistenceFailurePreventsDispatch(t *testing.T) {
	r := runRequest()
	r.ParentTaskID, r.Compaction = "parent", compactionFixture()
	appends := 0
	l := runtime.Loop{
		Journal: journal(func(_ context.Context, sequence int64, e runtime.Event) error {
			appends++
			if sequence != 0 || e.Kind != runtime.TaskStarted || e.Data.Compaction == nil || len(e.Data.Messages) == 0 {
				t.Fatal("compaction and messages were not in the initial atomic event", e)
			}
			return errors.New("injected journal failure")
		}),
		Provider: model(func(context.Context, providers.Request, func(providers.Chunk) error) error {
			t.Fatal("provider dispatched without durable compaction")
			return nil
		}),
	}
	if _, err := l.Run(context.Background(), r); !errors.Is(err, runtime.ErrPersistence) || appends != 1 {
		t.Fatal("failed initial persistence was not terminal", appends, err)
	}
}
