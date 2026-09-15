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
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

type fixtureEngine struct {
	Default
	assemble func(context.Context, Assembly) (Plan, error)
	compact  func(context.Context, sessions.Snapshot, sessions.CompactionRequest) (sessions.CompactionRequest, error)
	describe func(context.Context) runtime.ContextEngineIdentity
}

func (f *fixtureEngine) Descriptor(ctx context.Context) runtime.ContextEngineIdentity {
	if f.describe != nil {
		return f.describe(ctx)
	}
	identity, _ := runtime.NewContextEngineIdentity("fixture.engine", "v1")
	return identity
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

func TestCompactionPreservesAndIsolatesContextLineage(t *testing.T) {
	prior, request := compactionFixture()
	messages, checkpoint, err := sessions.PrepareContinuation(prior, request)
	if err != nil {
		t.Fatal(err)
	}
	lineage, err := runtime.ExtendContextLineage(nil, "prior-child", 1, checkpoint, prior.Messages)
	if err != nil {
		t.Fatal(err)
	}
	source := sessions.Snapshot{TaskID: "prior-child", State: "completed", Sequence: 10, Messages: messages, ContextLineage: lineage}
	engine := &fixtureEngine{compact: func(_ context.Context, got sessions.Snapshot, selected sessions.CompactionRequest) (sessions.CompactionRequest, error) {
		if got.ContextLineage == nil || got.ContextLineage.Digest != lineage.Digest {
			t.Fatal("lineage missing from callback snapshot")
		}
		got.ContextLineage.Epochs[0].TaskID = "mutated"
		got.ContextLineage.ToolCallIDs = append(got.ContextLineage.ToolCallIDs, "forged")
		return selected, nil
	}}
	if _, err = SelectCompaction(context.Background(), engine, source, request); err != nil {
		t.Fatal(err)
	}
	if source.ContextLineage.Digest != lineage.Digest || source.ContextLineage.Epochs[0].TaskID != "prior-child" || len(source.ContextLineage.ToolCallIDs) != len(lineage.ToolCallIDs) {
		t.Fatal("callback mutation escaped isolated lineage")
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

type undescribedEngine struct{}

func (undescribedEngine) Estimate(ctx context.Context, r providers.Request) (int, error) {
	return providers.EstimateWith(ctx, nil, r)
}
func (undescribedEngine) Assemble(ctx context.Context, a Assembly) (Plan, error) {
	return (Default{}).Assemble(ctx, a)
}
func (undescribedEngine) PrepareCompaction(_ context.Context, _ sessions.Snapshot, r sessions.CompactionRequest) (sessions.CompactionRequest, error) {
	return r, nil
}

func TestDescribeEngineDefaultAndCustomContract(t *testing.T) {
	builtin, err := DescribeEngine(context.Background(), nil)
	if err != nil || builtin.Validate() != nil || builtin.ID != "darwin.default" || builtin.Revision != "v1" {
		t.Fatal("invalid built-in descriptor", builtin, err)
	}
	explicit, err := DescribeEngine(context.Background(), Default{})
	if err != nil || explicit != builtin {
		t.Fatal("nil and explicit defaults diverged", explicit, err)
	}
	pointer, err := DescribeEngine(context.Background(), &Default{})
	if err != nil || pointer != builtin {
		t.Fatal("default pointer identity diverged", pointer, err)
	}
	custom := &fixtureEngine{}
	first, err := DescribeEngine(context.Background(), custom)
	second, secondErr := DescribeEngine(context.Background(), custom)
	if err != nil || secondErr != nil || first != second || first.Validate() != nil {
		t.Fatal("custom descriptor is not stable", first, second, err, secondErr)
	}
	// Descriptor identity is required for compaction, but not ordinary context
	// assembly while existing extensions migrate to the durable contract.
	if _, err := DescribeEngine(context.Background(), undescribedEngine{}); err != ErrEngine {
		t.Fatal("undescribed engine admitted", err)
	}
	if _, err := Assemble(context.Background(), undescribedEngine{}, assemblyFixture()); err != nil {
		t.Fatal("descriptor requirement leaked into ordinary assembly", err)
	}
	// Embedding Default supplies behavior, not the built-in engine identity.
	type embeddedDefault struct{ Default }
	if _, err := DescribeEngine(context.Background(), embeddedDefault{}); err != ErrEngine {
		t.Fatal("embedded default inherited built-in identity", err)
	}
	reserved, _ := runtime.NewContextEngineIdentity("darwin.default", "v1")
	if _, err := DescribeEngine(context.Background(), &fixtureEngine{describe: func(context.Context) runtime.ContextEngineIdentity { return reserved }}); err != ErrEngine {
		t.Fatal("custom engine claimed reserved built-in identity", err)
	}
}

func TestDescribeEngineRejectsTypedNilPanicAndMalformedIdentity(t *testing.T) {
	var typedNil *fixtureEngine
	if _, err := DescribeEngine(context.Background(), typedNil); err != ErrEngine {
		t.Fatal("typed nil descriptor admitted", err)
	}
	for _, mode := range []string{"panic", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			engine := &fixtureEngine{describe: func(context.Context) runtime.ContextEngineIdentity {
				if mode == "panic" {
					panic("private descriptor failure")
				}
				return runtime.ContextEngineIdentity{Version: 1, ID: "fixture.engine", Revision: "v1", Digest: strings.Repeat("0", 64)}
			}}
			if identity, err := DescribeEngine(context.Background(), engine); err != ErrEngine || identity != (runtime.ContextEngineIdentity{}) {
				t.Fatal("unsafe descriptor admitted", identity, err)
			}
		})
	}
}

func TestCompactionRejectsMissingOrUnstableDescriptor(t *testing.T) {
	source, request := compactionFixture()
	if selected, identity, err := SelectCompactionDescribed(context.Background(), undescribedEngine{}, source, request); err != ErrEngine || selected.Keep != 0 || identity != (runtime.ContextEngineIdentity{}) {
		t.Fatal("undescribed selector admitted", selected, err)
	}
	if selected, err := SelectCompaction(context.Background(), undescribedEngine{}, source, request); err != nil || !reflect.DeepEqual(selected, request) {
		t.Fatal("legacy undescribed selector compatibility changed", selected, err)
	}
	first, _ := runtime.NewContextEngineIdentity("fixture.engine", "v1")
	second, _ := runtime.NewContextEngineIdentity("fixture.engine", "v2")
	descriptions, callbacks := 0, 0
	engine := &fixtureEngine{
		describe: func(context.Context) runtime.ContextEngineIdentity {
			descriptions++
			if descriptions == 1 {
				return first
			}
			return second
		},
		compact: func(_ context.Context, _ sessions.Snapshot, r sessions.CompactionRequest) (sessions.CompactionRequest, error) {
			callbacks++
			return r, nil
		},
	}
	if selected, identity, err := SelectCompactionDescribed(context.Background(), engine, source, request); err != ErrEngine || selected.Keep != 0 || identity != (runtime.ContextEngineIdentity{}) || callbacks != 1 || descriptions != 2 {
		t.Fatal("descriptor drift admitted", selected, err, callbacks, descriptions)
	}
}

func TestDescribedCompactionRejectsSecondDescriptorFailures(t *testing.T) {
	for _, mode := range []string{"panic", "malformed", "cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			source, request := compactionFixture()
			ctx, cancel := context.WithCancel(context.Background())
			if mode == "timeout" {
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			}
			defer cancel()
			valid, _ := runtime.NewContextEngineIdentity("fixture.engine", "v1")
			calls := 0
			engine := &fixtureEngine{describe: func(callCtx context.Context) runtime.ContextEngineIdentity {
				calls++
				if calls == 1 {
					return valid
				}
				switch mode {
				case "panic":
					panic("private second descriptor failure")
				case "malformed":
					return runtime.ContextEngineIdentity{Version: 1, ID: "fixture.engine", Revision: "v1", Digest: strings.Repeat("0", 64)}
				case "cancel":
					cancel()
				case "timeout":
					<-callCtx.Done()
				}
				return valid
			}}
			selected, identity, err := SelectCompactionDescribed(ctx, engine, source, request)
			if err != ErrEngine || selected.Keep != 0 || identity != (runtime.ContextEngineIdentity{}) || calls != 2 {
				t.Fatal("unsafe second descriptor admitted", selected, identity, err, calls)
			}
		})
	}
}

func TestDescribeEngineCooperativeTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	engine := &fixtureEngine{describe: func(callCtx context.Context) runtime.ContextEngineIdentity {
		<-callCtx.Done()
		identity, _ := runtime.NewContextEngineIdentity("fixture.engine", "v1")
		return identity
	}}
	if identity, err := DescribeEngine(ctx, engine); err != ErrEngine || identity != (runtime.ContextEngineIdentity{}) {
		t.Fatal("timed-out descriptor admitted", identity, err)
	}
}

func TestDescribedCompactionCancellationPrecedesDescriptor(t *testing.T) {
	source, request := compactionFixture()
	calls := 0
	engine := &fixtureEngine{describe: func(context.Context) runtime.ContextEngineIdentity {
		calls++
		identity, _ := runtime.NewContextEngineIdentity("fixture.engine", "v1")
		return identity
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if selected, identity, err := SelectCompactionDescribed(ctx, engine, source, request); err != ErrEngine || selected.Keep != 0 || identity != (runtime.ContextEngineIdentity{}) || calls != 0 {
		t.Fatal("canceled described selection reached descriptor", selected, identity, err, calls)
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
