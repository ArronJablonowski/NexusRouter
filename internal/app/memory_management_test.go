package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/memory"
)

func TestMemoryManagementExistingStoreCASAndRedaction(t *testing.T) {
	svc, cfg := autoFixture(t)
	svc.settings.Memory.Enabled = false
	svc.settings.Memory.Scope = "project"
	svc.secret = func(string) string { return "private-secret" }
	ctx := context.Background()
	f := contextMemoryFact("fact")
	f.Content = "preference private-secret"
	f.Provenance = "operator private-secret"
	if svc.PutMemory(ctx, f, 0) != ErrAdmission {
		t.Fatal("created missing storage")
	}
	if _, err := svc.Memory(ctx, f.ID); err != ErrAdmission {
		t.Fatal("missing read")
	}
	if _, err := os.Stat(cfg.Telemetry.Database); !os.IsNotExist(err) {
		t.Fatal("created database")
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := svc.PutMemory(ctx, f, 0); err != nil {
		t.Fatal(err)
	}
	raw, err := db.GetMemory(ctx, "project", f.ID)
	if err != nil || strings.Contains(raw.Content, "private-secret") || !strings.Contains(raw.Content, "[REDACTED]") {
		t.Fatal(raw, err)
	}
	got, err := svc.Memory(ctx, f.ID)
	if err != nil || !reflect.DeepEqual(raw, got) {
		t.Fatal(got, err)
	}
	list, err := svc.Memories(ctx, "", "preference", 100, false)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	if svc.PutMemory(ctx, f, 0) != memory.ErrConflict || svc.DeleteMemory(ctx, f.ID, 2) != memory.ErrConflict {
		t.Fatal("CAS bypass")
	}
	got.Revision++
	got.Updated = got.Updated.Add(time.Second)
	got.Content = "corrected"
	if err := svc.PutMemory(ctx, got, 1); err != nil {
		t.Fatal(err)
	}
	bad := got
	bad.Scope = "other"
	if svc.PutMemory(ctx, bad, 1) != ErrAdmission {
		t.Fatal("scope bypass")
	}
	bad.ID = "private-secret"
	bad.Scope = "project"
	if svc.PutMemory(ctx, bad, 1) != ErrAdmission {
		t.Fatal("secret identity accepted")
	}
	if err := svc.DeleteMemory(ctx, f.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Memory(ctx, f.ID); err != memory.ErrConflict {
		t.Fatal("deleted fact readable")
	}
}

type managementStore struct {
	memory.Store
	facts  []memory.Fact
	panics bool
	calls  int
}

func (s *managementStore) QueryMemory(ctx context.Context, q memory.Query) ([]memory.Fact, error) {
	s.calls++
	if s.panics {
		panic("private backend error")
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
		panic("unbounded")
	}
	return s.facts, nil
}
func (s *managementStore) GetMemory(context.Context, string, string) (memory.Fact, error) {
	return memory.Fact{}, errors.New("private backend error")
}

func TestMemoryManagementCustomStoreBoundaries(t *testing.T) {
	for _, mode := range []string{"scope", "duplicate", "order", "expiry", "filter", "limit", "secret_id", "panic", "valid"} {
		t.Run(mode, func(t *testing.T) {
			svc, _ := autoFixture(t)
			svc.settings.Memory.Scope = "project"
			svc.secret = func(string) string { return "secret" }
			f := contextMemoryFact("a")
			f.Content = "target secret"
			store := &managementStore{facts: []memory.Fact{f}}
			svc.memoryStore = store
			switch mode {
			case "scope":
				store.facts[0].Scope = "other"
			case "duplicate":
				store.facts = append(store.facts, f)
			case "order":
				b := f
				b.ID = "b"
				store.facts = []memory.Fact{b, f}
			case "expiry":
				store.facts[0].Expires = time.Unix(101, 0)
			case "filter":
				store.facts[0].Content = "unrelated"
			case "limit":
				for range 3 {
					store.facts = append(store.facts, f)
				}
			case "secret_id":
				store.facts[0].ID = "secret"
			case "panic":
				store.panics = true
			}
			got, err := svc.Memories(context.Background(), "", "target", 3, false)
			if mode == "valid" {
				if err != nil || len(got) != 1 || got[0].Content != "target [REDACTED]" || store.facts[0].Content != "target secret" {
					t.Fatal(got, err)
				}
			} else if err != ErrAdmission || got != nil {
				t.Fatal("invalid backend response escaped", got, err)
			}
			if fact, err := svc.Memory(context.Background(), "a"); err != ErrAdmission || !reflect.DeepEqual(fact, memory.Fact{}) {
				t.Fatal("raw backend error escaped")
			}
		})
	}
}

func TestMemoryManagementCanceledAndDisabledScope(t *testing.T) {
	svc, _ := autoFixture(t)
	store := &managementStore{}
	svc.memoryStore = store
	if _, err := svc.Memories(context.Background(), "", "", 1, false); err != ErrAdmission {
		t.Fatal("unconfigured scope allowed")
	}
	svc.settings.Memory.Scope = "project"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, ctx} {
		if _, err := svc.Memories(ctx, "", "", 1, false); err != ErrAdmission {
			t.Fatal(err)
		}
	}
	if store.calls != 0 {
		t.Fatal("denied request reached store")
	}
}

func TestMemoryManagementBoundsEscapedPage(t *testing.T) {
	svc, _ := autoFixture(t)
	svc.settings.Memory.Scope = "project"
	store := &managementStore{}
	for i := range 30 {
		f := contextMemoryFact(fmt.Sprintf("fact-%02d", i))
		f.Content = strings.Repeat("\x01", 65536)
		store.facts = append(store.facts, f)
	}
	svc.memoryStore = store
	if page, err := svc.Memories(context.Background(), "", "", 100, false); err != ErrAdmission || page != nil {
		t.Fatal("oversized escaped response allowed")
	}
}
