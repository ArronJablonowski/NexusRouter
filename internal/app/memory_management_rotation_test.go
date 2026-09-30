package app

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/memory"
)

type rotatingMemoryReadStore struct {
	memory.Store
	fact   memory.Fact
	rotate func()
}

func (s *rotatingMemoryReadStore) GetMemory(context.Context, string, string) (memory.Fact, error) {
	s.rotate()
	return s.fact, nil
}

func (s *rotatingMemoryReadStore) QueryMemory(context.Context, memory.Query) ([]memory.Fact, error) {
	s.rotate()
	return []memory.Fact{s.fact}, nil
}

func TestMemoryManagementReadCredentialRotation(t *testing.T) {
	for _, action := range []string{"get", "query"} {
		for _, collision := range []string{"text", "id", "scope", "cursor", "filter"} {
			if action == "get" && (collision == "cursor" || collision == "filter") {
				continue
			}
			t.Run(action+"/"+collision, func(t *testing.T) {
				svc, _ := autoFixture(t)
				svc.settings.Memory.Scope = "project"
				credential := "old-credential"
				svc.secret = func(string) string { return credential }
				f := contextMemoryFact("fact")
				f.Content = "target old-credential new-credential"
				f.Provenance = "source old-credential new-credential"
				next := "new-credential"
				after, contains := "", ""
				switch collision {
				case "id":
					next = f.ID
				case "scope":
					next = f.Scope
				case "cursor":
					after, next = "earlier", "earlier"
				case "filter":
					contains, next = "target", "target"
				}
				store := &rotatingMemoryReadStore{fact: f, rotate: func() { credential = next }}
				svc.memoryStore = store
				var got memory.Fact
				var err error
				if action == "get" {
					got, err = svc.Memory(context.Background(), f.ID)
				} else {
					var page []memory.Fact
					page, err = svc.Memories(context.Background(), after, contains, 10, false)
					if err != nil && page != nil {
						t.Fatal("denied query returned partial facts")
					}
					if err == nil {
						if len(page) != 1 {
							t.Fatal("unexpected page size")
						}
						got = page[0]
					}
				}
				if collision == "text" {
					if err != nil || strings.Contains(got.Content, "credential") || strings.Contains(got.Provenance, "credential") || got.Content != "target [REDACTED] [REDACTED]" {
						t.Fatal("rotated credential escaped read boundary")
					}
				} else if err != ErrAdmission || !reflect.DeepEqual(got, memory.Fact{}) {
					t.Fatal("rotated identity/filter credential escaped read boundary")
				}
				if store.fact != f {
					t.Fatal("inspection mutated backend fact")
				}
			})
		}
	}
}
