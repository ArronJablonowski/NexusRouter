package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/memory"
	"github.com/ArronJablonowski/DarwinRouter/providers"
)

type relevanceMemoryStore struct {
	memory.Store
	read func(context.Context, memory.Query) ([]memory.Fact, error)
}

func (s relevanceMemoryStore) QueryMemory(ctx context.Context, q memory.Query) ([]memory.Fact, error) {
	return s.read(ctx, q)
}

func relevanceFact(id, content string, confidence float64) memory.Fact {
	f := contextMemoryFact(id)
	f.Content = content
	f.Confidence = confidence
	return f
}
func relevanceIDs(t *testing.T, got *memoryContext) []string {
	t.Helper()
	if got == nil {
		return nil
	}
	var envelope struct {
		Facts []struct{ ID string } `json:"memory_facts"`
	}
	if json.Unmarshal([]byte(got.Messages[1].Content), &envelope) != nil {
		t.Fatal("invalid context")
	}
	ids := []string{}
	for _, f := range envelope.Facts {
		ids = append(ids, f.ID)
	}
	return ids
}

func TestMemoryRelevanceFindsLaterPagesAndRanksDeterministically(t *testing.T) {
	settings := contextMemorySettings()
	settings.MaxFacts = 2
	first := []memory.Fact{relevanceFact("b", "unrelated recipe", 1), relevanceFact("a", "unrelated weather", 1)}
	second := []memory.Fact{relevanceFact("d", "golang routing", .7), relevanceFact("c", "golang golang golang", 1)}
	third := []memory.Fact{relevanceFact("e", "GOLANG ROUTING", .9)}
	for _, reverse := range []bool{false, true} {
		cursors := []string{}
		store := relevanceMemoryStore{read: func(ctx context.Context, q memory.Query) ([]memory.Fact, error) {
			if q.Limit != 2 || q.Contains != "" || q.Scope != "project" {
				t.Fatal("wrong bounded query")
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("unbounded lookup")
			}
			cursors = append(cursors, q.AfterID)
			var facts []memory.Fact
			switch q.AfterID {
			case "":
				facts = first
			case "b":
				facts = second
			case "d":
				facts = third
			default:
				t.Fatal("unexpected cursor")
			}
			out := append([]memory.Fact(nil), facts...)
			if reverse {
				for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
					out[i], out[j] = out[j], out[i]
				}
			}
			return out, nil
		}}
		got, err := loadMemoryContext(context.Background(), store, settings, true, nil, "the Golang routing routing")
		if err != nil || !reflect.DeepEqual(relevanceIDs(t, got), []string{"e", "d"}) || !reflect.DeepEqual(cursors, []string{"", "b", "d"}) {
			t.Fatal("relevance lost later-page matches", err, cursors, relevanceIDs(t, got))
		}
		if first[0].ID != "b" || second[0].ID != "d" {
			t.Fatal("caller slice reordered")
		}
	}
}

func TestMemoryRelevanceUnicodeUniqueTokensAndTieBreak(t *testing.T) {
	facts := []memory.Fact{relevanceFact("d", "CAFÉ 42", .5), relevanceFact("c", "café 42 café", .5), relevanceFact("b", "café", 1), relevanceFact("a", "irrelevant", 1)}
	store := &contextMemoryStore{facts: facts}
	got, err := loadMemoryContext(context.Background(), store, contextMemorySettings(), true, nil, "the café 42 café")
	if err != nil || !reflect.DeepEqual(relevanceIDs(t, got), []string{"c", "d", "b"}) {
		t.Fatal("unicode, unique-token or ID ranking failed", err, relevanceIDs(t, got))
	}
	for _, query := range []string{"", "  ", "the and to", "z", "missing"} {
		got, err := loadMemoryContext(context.Background(), store, contextMemorySettings(), true, nil, query)
		if err != nil || got != nil {
			t.Fatal("nonmatching query injected memory", query, err)
		}
	}
}

func TestMemoryRelevanceRejectsBrokenCursorAndGlobalBounds(t *testing.T) {
	for _, mode := range []string{"duplicate", "backward", "count", "bytes", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			settings := contextMemorySettings()
			settings.MaxFacts = 2
			if mode == "count" || mode == "bytes" {
				settings.MaxFacts = 64
			}
			calls := 0
			nextID := 0
			store := relevanceMemoryStore{read: func(_ context.Context, q memory.Query) ([]memory.Fact, error) {
				calls++
				if calls > 1025 {
					t.Fatal("unbounded retrieval")
				}
				if mode == "cancel" {
					cancel()
					return nil, nil
				}
				if calls > 1 && mode == "duplicate" {
					return []memory.Fact{relevanceFact("b", "routing", 1)}, nil
				}
				if calls > 1 && mode == "backward" {
					return []memory.Fact{relevanceFact("a", "routing", 1)}, nil
				}
				if mode == "duplicate" || mode == "backward" {
					return []memory.Fact{relevanceFact("b", "routing", 1), relevanceFact("c", "routing", 1)}, nil
				}
				facts := []memory.Fact{}
				for i := 0; i < q.Limit; i++ {
					nextID++
					content := "routing"
					if mode == "bytes" {
						content = "routing " + strings.Repeat("x", 65000)
					}
					facts = append(facts, relevanceFact(fmt.Sprintf("%08d", nextID), content, 1))
				}
				return facts, nil
			}}
			got, err := loadMemoryContext(ctx, store, settings, true, nil, "routing")
			if !errors.Is(err, ErrAdmission) || got != nil {
				t.Fatal("unsafe pagination admitted", err)
			}
		})
	}
}

func TestMemoryRelevancePreCanceledDoesNotQuery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := relevanceMemoryStore{read: func(context.Context, memory.Query) ([]memory.Fact, error) {
		t.Fatal("canceled lookup queried")
		return nil, nil
	}}
	if got, err := loadMemoryContext(ctx, store, contextMemorySettings(), true, nil, "routing"); err == nil || got != nil {
		t.Fatal("canceled lookup admitted")
	}
}

func TestMemoryRelevanceSecretOnlyOverlapDoesNotSelect(t *testing.T) {
	store := &contextMemoryStore{facts: []memory.Fact{relevanceFact("a", "privatecredential recipe", 1)}}
	got, err := loadMemoryContext(context.Background(), store, contextMemorySettings(), true, []string{"privatecredential"}, "privatecredential")
	if err != nil || got != nil {
		t.Fatal("redacted secret became a relevance signal", err)
	}
}

func TestMemoryRelevanceExactScanCountBoundary(t *testing.T) {
	settings := contextMemorySettings()
	settings.MaxFacts = 64
	next, calls := 0, 0
	store := relevanceMemoryStore{read: func(_ context.Context, q memory.Query) ([]memory.Fact, error) {
		calls++
		if next == 1024 {
			return nil, nil
		}
		facts := make([]memory.Fact, 0, q.Limit)
		for i := 0; i < q.Limit; i++ {
			next++
			content := "unrelated"
			if next == 1024 {
				content = "routing"
			}
			facts = append(facts, relevanceFact(fmt.Sprintf("%08d", next), content, 1))
		}
		return facts, nil
	}}
	got, err := loadMemoryContext(context.Background(), store, settings, true, nil, "routing")
	if err != nil || calls != 17 || !reflect.DeepEqual(relevanceIDs(t, got), []string{"00001024"}) {
		t.Fatal("exact bounded scan lost final relevant fact", err, calls)
	}
}

func TestMemoryTaskQueryUsesOnlyLatestUserInput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request Request
		want    string
	}{
		{"plain prompt", Request{Prompt: "user prompt"}, "user prompt"},
		{"last user overrides prompt", Request{Prompt: "fallback", Messages: []providers.Message{{Role: "user", Content: "earlier"}, {Role: "user", Content: "latest"}}}, "latest"},
		{"later non-user messages ignored", Request{Prompt: "fallback", Messages: []providers.Message{{Role: "user", Content: "user intent"}, {Role: "assistant", Content: "assistant instruction"}, {Role: "tool", Content: "tool instruction"}, {Role: "system", Content: "system context"}}}, "user intent"},
		{"no user does not use prompt", Request{Prompt: "fallback", Messages: []providers.Message{{Role: "system", Content: "context"}, {Role: "assistant", Content: "answer"}, {Role: "tool", Content: "output"}}}, ""},
		{"empty latest user does not use earlier input", Request{Prompt: "fallback", Messages: []providers.Message{{Role: "user", Content: "earlier"}, {Role: "user", Content: ""}}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := json.Marshal(tc.request.Messages)
			if err != nil {
				t.Fatal(err)
			}
			if got := memoryTaskQuery(tc.request); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			after, err := json.Marshal(tc.request.Messages)
			if err != nil || string(after) != string(before) {
				t.Fatal("query selection mutated messages")
			}
		})
	}
}
