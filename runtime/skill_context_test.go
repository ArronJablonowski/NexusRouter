package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func skillUse() *runtime.SkillContextUse {
	return &runtime.SkillContextUse{Version: 1, Complete: true, References: []runtime.SkillReference{{Scope: "project", Name: "lookup", Version: strings.Repeat("a", 32), Digest: strings.Repeat("b", 64)}}}
}

func TestSkillContextShapeAndPlacement(t *testing.T) {
	for _, use := range []*runtime.SkillContextUse{nil, {Version: 1}, {Version: 1, Complete: true}, skillUse()} {
		if err := use.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for name, change := range map[string]func(*runtime.SkillContextUse){
		"schema":              func(s *runtime.SkillContextUse) { s.Version = 2 },
		"incomplete":          func(s *runtime.SkillContextUse) { s.Complete = false },
		"empty_scope":         func(s *runtime.SkillContextUse) { s.References[0].Scope = "" },
		"long_name":           func(s *runtime.SkillContextUse) { s.References[0].Name = strings.Repeat("a", 65) },
		"leading_punctuation": func(s *runtime.SkillContextUse) { s.References[0].Name = "_name" },
		"unicode":             func(s *runtime.SkillContextUse) { s.References[0].Name = "námé" },
		"path":                func(s *runtime.SkillContextUse) { s.References[0].Scope = "a/b" },
		"uppercase_version":   func(s *runtime.SkillContextUse) { s.References[0].Version = strings.Repeat("A", 32) },
		"short_digest":        func(s *runtime.SkillContextUse) { s.References[0].Digest = "abc" },
		"duplicate":           func(s *runtime.SkillContextUse) { s.References = append(s.References, s.References[0]) },
		"capacity":            func(s *runtime.SkillContextUse) { s.References = make([]runtime.SkillReference, 17) },
	} {
		t.Run(name, func(t *testing.T) {
			use := skillUse()
			change(use)
			if use.Validate() == nil {
				t.Fatal("invalid attribution accepted")
			}
		})
	}
	e := runtime.Event{Version: 1, ID: "event", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 1, Time: time.Now(), Kind: runtime.TaskStarted, Data: runtime.Data{SkillContext: skillUse()}}
	if e.Validate() != nil {
		t.Fatal("valid task attribution rejected")
	}
	e.Kind = runtime.TaskCompleted
	if e.Validate() == nil {
		t.Fatal("misplaced attribution accepted")
	}
}

func TestSkillContextLoopFreezesBeforeJournalAndPersistsBeforeProvider(t *testing.T) {
	r := runRequest()
	r.SkillContext = skillUse()
	var persisted *runtime.SkillContextUse
	l := runtime.Loop{Journal: journal(func(_ context.Context, _ int64, e runtime.Event) error {
		if e.Kind == runtime.TaskStarted {
			r.SkillContext.References[0].Name = "caller-mutated"
			persisted = e.Data.SkillContext
			if persisted.References[0].Name != "lookup" {
				t.Fatal("request alias escaped")
			}
		}
		return nil
	}), Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		if persisted == nil {
			t.Fatal("provider before attribution persistence")
		}
		return emit(providers.Chunk{Text: "done", Done: true, FinishReason: "stop"})
	})}
	if _, err := l.Run(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if persisted.References[0].Name != "lookup" {
		t.Fatal("persisted identity changed")
	}
}

func TestSkillContextInvalidOrPersistenceFailureNeverDispatches(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		r := runRequest()
		r.SkillContext = skillUse()
		if invalid {
			r.SkillContext.Complete = false
		}
		writes, calls := 0, 0
		l := runtime.Loop{Journal: journal(func(context.Context, int64, runtime.Event) error { writes++; return errors.New("fixture") }), Provider: model(func(context.Context, providers.Request, func(providers.Chunk) error) error { calls++; return nil })}
		_, err := l.Run(context.Background(), r)
		if err == nil || calls != 0 || invalid && writes != 0 || !invalid && writes != 1 {
			t.Fatalf("invalid=%v writes=%d calls=%d error=%v", invalid, writes, calls, err)
		}
	}
}
