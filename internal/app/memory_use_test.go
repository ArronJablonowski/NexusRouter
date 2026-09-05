package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/memory"
	"github.com/ArronJablonowski/DarwinRouter/providers"
)

type memoryUseStoreFixture struct {
	contextMemoryStore
	used       []memory.Fact
	touchErr   error
	panicTouch bool
}

func (s *memoryUseStoreFixture) TouchMemoryFact(ctx context.Context, fact memory.Fact, now time.Time) error {
	if s.panicTouch {
		panic("private backend error")
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 3*time.Second {
		panic("missing bounded deadline")
	}
	fact.LastUse = now
	s.used = append(s.used, fact)
	return s.touchErr
}

type memoryUseProviderFixture struct {
	providers.Provider
	calls int
}

func (p *memoryUseProviderFixture) Stream(context.Context, providers.Request, func(providers.Chunk) error) error {
	p.calls++
	return nil
}

func TestMemoryUseFirstDispatchOwnsSelectedRevisions(t *testing.T) {
	s := &memoryUseStoreFixture{}
	p := &memoryUseProviderFixture{}
	fact := contextMemoryFact("original-secret-id")
	selected := &memoryContext{Refs: []memory.Fact{fact}}
	wrapped := withMemoryUse(p, s, selected)
	selected.Refs[0].ID = "mutated"
	if len(s.used) != 0 {
		t.Fatal("selection touched memory")
	}
	for range 2 {
		if err := wrapped.Stream(context.Background(), providers.Request{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if p.calls != 2 || len(s.used) != 1 || s.used[0].ID != fact.ID || s.used[0].Scope != fact.Scope || s.used[0].Revision != fact.Revision || s.used[0].LastUse.IsZero() {
		t.Fatal(p.calls, s.used)
	}
	legacy := &contextMemoryStore{}
	if withMemoryUse(p, legacy, selected) != p {
		t.Fatal("legacy store wrapped")
	}
}

func TestMemoryUseFailureNeverDispatchesOrLeaks(t *testing.T) {
	for _, mode := range []string{"conflict", "panic", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			s := &memoryUseStoreFixture{}
			p := &memoryUseProviderFixture{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "conflict":
				s.touchErr = memory.ErrConflict
			case "panic":
				s.panicTouch = true
			case "canceled":
				cancel()
			}
			wrapped := withMemoryUse(p, s, &memoryContext{Refs: []memory.Fact{contextMemoryFact("one")}})
			for range 2 {
				if err := wrapped.Stream(ctx, providers.Request{}, nil); !errors.Is(err, ErrAdmission) {
					t.Fatal(err)
				}
			}
			if p.calls != 0 || len(s.used) > 1 {
				t.Fatal(p.calls, s.used)
			}
		})
	}
}

func TestMemoryUseApplicationAdmission(t *testing.T) {
	for _, mode := range []string{"dispatch", "routing_denied", "context_denied", "revision_changed"} {
		t.Run(mode, func(t *testing.T) {
			svc, _ := autoFixture(t)
			fact := contextMemoryFact("selected")
			s := &memoryUseStoreFixture{contextMemoryStore: contextMemoryStore{facts: []memory.Fact{fact}}}
			svc.memoryStore = s
			svc.settings.Memory = contextMemorySettings()
			svc.settings.Memory.LocalOnly = true
			r := Request{ModelID: "a", Prompt: "Use the useful fact"}
			switch mode {
			case "routing_denied":
				r.Capabilities = []string{"unavailable"}
			case "context_denied":
				svc.settings.Models[0].ContextTokens = 1
			case "revision_changed":
				s.touchErr = memory.ErrConflict
			}
			out, err := svc.Run(context.Background(), r)
			if mode == "dispatch" {
				if err != nil || out.Text == "" || len(s.used) != 1 || s.used[0].ID != fact.ID {
					t.Fatal(out, err, s.used)
				}
			} else if err == nil {
				t.Fatal("denied request succeeded", out)
			}
			if (mode == "routing_denied" || mode == "context_denied") && len(s.used) != 0 {
				t.Fatal("denied request touched memory", s.used)
			}
		})
	}
}

func TestMemoryUseSQLiteTouchesOnlyDispatchedSelection(t *testing.T) {
	svc, cfg := autoFixture(t)
	ctx := context.Background()
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	selected, irrelevant := contextMemoryFact("selected"), contextMemoryFact("irrelevant")
	selected.Content = "prefer indigo colors"
	irrelevant.Content = "garden strawberries"
	for _, f := range []memory.Fact{selected, irrelevant} {
		if err := db.PutMemory(ctx, f, 0); err != nil {
			t.Fatal(err)
		}
	}
	svc.settings.Memory = contextMemorySettings()
	svc.settings.Memory.LocalOnly = true
	if _, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "indigo colors"}); err != nil {
		t.Fatal(err)
	}
	a, err := db.GetMemory(ctx, "project", selected.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.GetMemory(ctx, "project", irrelevant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.LastUse.IsZero() || !b.LastUse.IsZero() || a.Revision != selected.Revision || !a.Updated.Equal(selected.Updated) {
		t.Fatal(a, b)
	}
}
