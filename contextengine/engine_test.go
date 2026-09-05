package contextengine

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

type fixtureEngine struct {
	Default
	assemble func(context.Context, Assembly) (Plan, error)
	compact  func(context.Context, sessions.Snapshot, sessions.CompactionRequest) (sessions.CompactionRequest, error)
}

func (f *fixtureEngine) Assemble(ctx context.Context, a Assembly) (Plan, error) {
	if f.assemble != nil {
		return f.assemble(ctx, a)
	}
	return f.Default.Assemble(ctx, a)
}
func (f *fixtureEngine) PrepareCompaction(ctx context.Context, s sessions.Snapshot, r sessions.CompactionRequest) (sessions.CompactionRequest, error) {
	if f.compact != nil {
		return f.compact(ctx, s, r)
	}
	return f.Default.PrepareCompaction(ctx, s, r)
}
func message(text string) []providers.Message {
	return []providers.Message{{Role: "user", Content: text}}
}
func assemblyFixture() Assembly {
	return Assembly{Version: 1, History: message("history"), Memory: message("memory"), Skills: message("skills"), Current: message("current")}
}

func TestAssemblyDefaultAndIsolatedCustomSelection(t *testing.T) {
	a := assemblyFixture()
	out, err := Assemble(context.Background(), nil, a)
	if err != nil || len(out.Messages) != 4 || !out.MemoryIncluded || !out.SkillsIncluded {
		t.Fatal("default assembly", err)
	}
	engine := &fixtureEngine{assemble: func(ctx context.Context, copy Assembly) (Plan, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing deadline")
		}
		copy.History[0].Content = "forged"
		copy.Current[0].Role = "system"
		return Plan{1, []Tier{HistoryTier, SkillsTier, CurrentTier}}, nil
	}}
	out, err = Assemble(context.Background(), engine, a)
	if err != nil || out.MemoryIncluded || !out.SkillsIncluded || len(out.Messages) != 3 || out.Messages[0].Content != "history" || out.Messages[2].Role != "user" {
		t.Fatal("callback mutated canonical bundles", err)
	}
	out.Messages[0].Content = "changed"
	if a.History[0].Content != "history" {
		t.Fatal("output aliases source")
	}
}

func TestAssemblyToolPairsOwnedAndUnsplit(t *testing.T) {
	a := assemblyFixture()
	a.History = []providers.Message{{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call", Name: "tool", Arguments: json.RawMessage(`{"x":1}`)}}}, {Role: "tool", ToolCallID: "call", Content: "result"}}
	out, err := Assemble(context.Background(), nil, a)
	if err != nil || providers.ValidateMessages(out.Messages) != nil {
		t.Fatal(err)
	}
	out.Messages[0].ToolCalls[0].Arguments[2] = 'y'
	if string(a.History[0].ToolCalls[0].Arguments) != `{"x":1}` {
		t.Fatal("arguments alias source")
	}
	a.History = a.History[:1]
	if _, err := Assemble(context.Background(), nil, a); err == nil {
		t.Fatal("incomplete tier admitted")
	}
}

func TestAssemblyRejectsInvalidPlansAndBounds(t *testing.T) {
	for _, order := range [][]Tier{{CurrentTier}, {HistoryTier, CurrentTier, MemoryTier}, {HistoryTier, HistoryTier, CurrentTier}, {HistoryTier, "unknown", CurrentTier}, {MemoryTier, CurrentTier}} {
		engine := &fixtureEngine{assemble: func(context.Context, Assembly) (Plan, error) { return Plan{1, order}, nil }}
		if out, err := Assemble(context.Background(), engine, assemblyFixture()); err == nil || out.Messages != nil {
			t.Fatal("invalid plan admitted")
		}
	}
	for _, mutate := range []func(*Assembly){func(a *Assembly) { a.Version = 2 }, func(a *Assembly) { a.Current = nil }, func(a *Assembly) { a.Current[0].Role = "unknown" }, func(a *Assembly) { a.Current[0].Content = string([]byte{255}) }, func(a *Assembly) { a.History[0].Content = strings.Repeat("x", maxBytes) }, func(a *Assembly) { a.History[0].Content = strings.Repeat("<", maxBytes/6) }} {
		a := assemblyFixture()
		mutate(&a)
		called := false
		engine := &fixtureEngine{assemble: func(context.Context, Assembly) (Plan, error) { called = true; return Plan{}, nil }}
		if _, err := Assemble(context.Background(), engine, a); err == nil || called {
			t.Fatal("unbounded/malformed input reached callback")
		}
	}
}

func TestAssemblyCancellationPanicTypedNil(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Assemble(ctx, nil, assemblyFixture()); err == nil {
		t.Fatal("cancellation ignored")
	}
	var nilEngine *fixtureEngine
	if _, err := Assemble(context.Background(), nilEngine, assemblyFixture()); err == nil {
		t.Fatal("typed nil admitted")
	}
	for _, mode := range []string{"panic", "error", "cancel"} {
		ctx, cancel := context.WithCancel(context.Background())
		engine := &fixtureEngine{assemble: func(context.Context, Assembly) (Plan, error) {
			switch mode {
			case "panic":
				panic("private")
			case "error":
				return Plan{}, errors.New("private")
			default:
				cancel()
				return Plan{1, []Tier{HistoryTier, CurrentTier}}, nil
			}
		}}
		out, err := Assemble(ctx, engine, assemblyFixture())
		cancel()
		if err != ErrEngine || out.Messages != nil {
			t.Fatal("unsafe callback outcome")
		}
	}
}

func compactionFixture() (sessions.Snapshot, sessions.CompactionRequest) {
	return sessions.Snapshot{TaskID: "task", State: "completed", Sequence: 10, Messages: []providers.Message{{Role: "user", Content: "older question"}, {Role: "assistant", Content: "older answer"}, {Role: "user", Content: "recent question"}, {Role: "assistant", Content: "recent answer"}}}, sessions.CompactionRequest{Keep: 1, Summary: sessions.Summary{Decisions: []string{"operator decision"}}}
}

func TestCompactionCanonicalAndIsolated(t *testing.T) {
	source, request := compactionFixture()
	engine := &fixtureEngine{compact: func(ctx context.Context, s sessions.Snapshot, r sessions.CompactionRequest) (sessions.CompactionRequest, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("no deadline")
		}
		s.Messages[0].Content = "forged"
		r.Keep = 2
		return r, nil
	}}
	messages, record, err := Compact(context.Background(), engine, source, request)
	want, expected, canonicalErr := sessions.PrepareContinuation(source, sessions.CompactionRequest{Keep: 2, Summary: request.Summary})
	if err != nil || canonicalErr != nil || !reflect.DeepEqual(messages, want) || !reflect.DeepEqual(record, expected) {
		t.Fatal("not canonical compaction", err)
	}
	messages[len(messages)-1].Content = "changed"
	record.Summary.Decisions[0] = "changed"
	if source.Messages[3].Content != "recent answer" || request.Summary.Decisions[0] != "operator decision" {
		t.Fatal("compaction aliases source")
	}
	selected, err := SelectCompaction(context.Background(), nil, source, request)
	if err != nil || !reflect.DeepEqual(selected, request) {
		t.Fatal("default not passthrough", err)
	}
}

func TestCompactionRejectsUnsafeSelection(t *testing.T) {
	for _, mode := range []string{"drop", "summary", "overkeep", "panic", "error", "cancel"} {
		source, request := compactionFixture()
		request.Keep = 2
		ctx, cancel := context.WithCancel(context.Background())
		engine := &fixtureEngine{compact: func(_ context.Context, s sessions.Snapshot, r sessions.CompactionRequest) (sessions.CompactionRequest, error) {
			switch mode {
			case "drop":
				r.Keep = 1
			case "summary":
				r.Summary.Decisions[0] = "forged"
			case "overkeep":
				r.Keep = 4
			case "panic":
				panic("private")
			case "error":
				return r, errors.New("private")
			case "cancel":
				cancel()
			}
			return r, nil
		}}
		out, err := SelectCompaction(ctx, engine, source, request)
		cancel()
		if err != ErrEngine || out.Keep != 0 {
			t.Fatal("unsafe compaction selected", mode)
		}
		if request.Summary.Decisions[0] != "operator decision" {
			t.Fatal("callback mutated summary")
		}
	}
}

func TestCompactionRejectsInvalidSourceBeforeCallback(t *testing.T) {
	for _, mutate := range []func(*sessions.Snapshot){
		func(s *sessions.Snapshot) { s.State = "running" },
		func(s *sessions.Snapshot) { s.InterruptedTurn = true },
		func(s *sessions.Snapshot) { s.UncertainEffects = true },
		func(s *sessions.Snapshot) { s.Pending = map[string]sessions.Pending{"call": {}} },
		func(s *sessions.Snapshot) { s.Messages[0].Content = string([]byte{255}) },
		func(s *sessions.Snapshot) { s.Messages[0].Content = strings.Repeat("x", maxBytes) },
		func(s *sessions.Snapshot) { s.MessageSequences = []int64{1} },
	} {
		source, request := compactionFixture()
		mutate(&source)
		called := false
		engine := &fixtureEngine{compact: func(context.Context, sessions.Snapshot, sessions.CompactionRequest) (sessions.CompactionRequest, error) {
			called = true
			return request, nil
		}}
		if _, err := SelectCompaction(context.Background(), engine, source, request); err != ErrEngine || called {
			t.Fatal("invalid source reached custom selection")
		}
	}
	source, request := compactionFixture()
	var nilEngine *fixtureEngine
	if _, err := SelectCompaction(context.Background(), nilEngine, source, request); err != ErrEngine {
		t.Fatal("typed nil compaction engine accepted")
	}
	if _, _, err := Compact(nil, nil, source, request); err != ErrEngine {
		t.Fatal("nil context accepted")
	}
}

func TestDefaultEstimateUsesBuiltIn(t *testing.T) {
	r := providers.Request{Messages: message("text")}
	want, _ := providers.EstimateContext(r)
	got, err := (Default{}).Estimate(context.Background(), r)
	if err != nil || got != want {
		t.Fatal("default estimate changed")
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := (Default{}).Estimate(ctx, r); err == nil {
		t.Fatal("canceled estimate admitted")
	}
}
