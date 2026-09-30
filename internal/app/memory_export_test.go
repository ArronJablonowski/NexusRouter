package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/memory"
)

type exportMemoryFixture struct {
	memory.Store
	fn func(context.Context, string, time.Time) (memory.ExportSnapshot, error)
}

func (s *exportMemoryFixture) ExportMemory(ctx context.Context, scope string, now time.Time) (memory.ExportSnapshot, error) {
	return s.fn(ctx, scope, now)
}

func TestMemoryExportCustomOwnershipAndRotatedSecrets(t *testing.T) {
	svc, _ := autoFixture(t)
	svc.settings.Memory.Scope = "project"
	svc.settings.Memory.Enabled = false
	secret := "old-private"
	svc.secret = func(string) string { return secret }
	f := contextMemoryFact("fact")
	f.Content = "old-private new-private preference"
	f.Provenance = "operator new-private"
	facts := []memory.Fact{f}
	calls := 0
	svc.memoryStore = &exportMemoryFixture{fn: func(ctx context.Context, scope string, now time.Time) (memory.ExportSnapshot, error) {
		calls++
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
			t.Fatal("missing deadline")
		}
		secret = "new-private"
		return memory.ExportSnapshot{Version: 1, Scope: scope, CapturedAt: now, Facts: facts}, nil
	}}
	got, err := svc.ExportMemory(context.Background())
	if err != nil || got.Validate() != nil || calls != 1 || len(got.Facts) != 1 {
		t.Fatal(got, err, calls)
	}
	if strings.Contains(got.Facts[0].Content, "private") || strings.Contains(got.Facts[0].Provenance, "private") || !reflect.DeepEqual(facts, []memory.Fact{f}) {
		t.Fatal("redaction leaked or mutated backend")
	}
	got.Facts[0].Content = "caller mutation"
	if facts[0].Content != f.Content {
		t.Fatal("shared output slice")
	}
}

func TestMemoryExportRejectsInvalidOrUnsupportedBackend(t *testing.T) {
	for _, mode := range []string{"unsupported", "panic", "error", "scope", "timestamp", "nil-facts", "duplicate", "secret-id", "invalid", "canceled", "too-many", "redacted-overflow", "redacted-envelope-overflow"} {
		t.Run(mode, func(t *testing.T) {
			svc, _ := autoFixture(t)
			svc.settings.Memory.Scope = "project"
			svc.secret = func(string) string { return "private-secret" }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			svc.memoryStore = &exportMemoryFixture{fn: func(_ context.Context, scope string, now time.Time) (memory.ExportSnapshot, error) {
				f := contextMemoryFact("fact")
				snapshot := memory.ExportSnapshot{Version: 1, Scope: scope, CapturedAt: now, Facts: []memory.Fact{f}}
				switch mode {
				case "panic":
					panic("private backend error")
				case "error":
					return snapshot, errors.New("private backend error")
				case "scope":
					snapshot.Scope = "foreign"
				case "timestamp":
					snapshot.CapturedAt = now.Add(time.Second)
				case "nil-facts":
					snapshot.Facts = nil
				case "duplicate":
					snapshot.Facts = append(snapshot.Facts, f)
				case "secret-id":
					snapshot.Facts[0].ID = "private-secret"
				case "invalid":
					snapshot.Facts[0].Confidence = 2
				case "canceled":
					cancel()
				case "too-many":
					snapshot.Facts = make([]memory.Fact, memory.ExportMaxFacts+1)
				case "redacted-overflow":
					svc.secret = func(string) string { return "x" }
					snapshot.Facts = make([]memory.Fact, 20)
					for i := range snapshot.Facts {
						ff := f
						ff.ID = string(rune('a' + i))
						ff.Content = strings.Repeat("x", 65536)
						snapshot.Facts[i] = ff
					}
				case "redacted-envelope-overflow":
					svc.secret = func(string) string { return "x" }
					snapshot.Facts = make([]memory.Fact, 220)
					for i := range snapshot.Facts {
						ff := f
						ff.ID = fmt.Sprintf("fact-%04d", i)
						ff.Content = strings.Repeat("x", 4000)
						snapshot.Facts[i] = ff
					}
					if snapshot.Validate() != nil {
						t.Fatal("original envelope invalid")
					}
				}
				return snapshot, nil
			}}
			if mode == "unsupported" {
				svc.memoryStore = &managementStore{}
			}
			got, err := svc.ExportMemory(ctx)
			if err != ErrAdmission || !reflect.DeepEqual(got, memory.ExportSnapshot{}) {
				t.Fatal("partial or invalid export", err)
			}
		})
	}
}
