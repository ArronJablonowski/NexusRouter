package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func generationStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "generation.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

type recordedGenerationProvider func(context.Context, providers.Request, func(providers.Chunk) error) error

func (p recordedGenerationProvider) Models(context.Context) ([]string, error) {
	return []string{"generator"}, nil
}
func (p recordedGenerationProvider) Stream(ctx context.Context, r providers.Request, emit func(providers.Chunk) error) error {
	return p(ctx, r, emit)
}

func TestSkillGenerationRecordedInferenceDurableBoundaries(t *testing.T) {
	for _, mode := range []string{"drafted", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			s, path := generationStore(t)
			base := context.Background()
			ctx, cancel := context.WithCancel(base)
			defer cancel()
			calls := 0
			provider := recordedGenerationProvider(func(callCtx context.Context, r providers.Request, emit func(providers.Chunk) error) error {
				calls++
				ro, err := OpenReadOnly(base, path)
				if err != nil {
					t.Error(err)
					return err
				}
				started, err := ro.SkillGenerationAttempt(base, "recorded")
				ro.Close()
				if err != nil || started.Status != "started" || started.Result != nil {
					t.Errorf("dispatch preceded durable attempt: %+v %v", started, err)
				}
				if len(r.Tools) != 0 || r.Model != "generator" {
					t.Error("unexpected generation authority", r.Model, len(r.Tools))
				}
				if mode == "canceled" {
					cancel()
					return callCtx.Err()
				}
				return emit(providers.Chunk{Text: `{"version":1,"description":"Run checks","tags":[],"steps":["Run tests"],"required_tools":[],"configuration":"","risks":[],"validation_cases":["Known fixture"]}`, Done: true, FinishReason: "stop", Usage: &providers.Usage{InputTokens: 20, OutputTokens: 8}})
			})
			generator := skills.ModelGenerator{Provider: provider, Model: "generator", ContextTokens: 20000, Timeout: time.Second}
			examples := []skills.WorkflowExample{
				{SessionID: "session-a", TaskID: "task-a", Domain: "code", Steps: []string{"Run tests"}, Checks: []evaluation.Check{{Source: evaluation.Deterministic, Reference: "check-a", Passed: true}}},
				{SessionID: "session-b", TaskID: "task-b", Domain: "code", Steps: []string{"Run tests"}, Checks: []evaluation.Check{{Source: evaluation.Deterministic, Reference: "check-b", Passed: true}}},
			}
			out, err := generator.GenerateRecorded(ctx, s, "recorded", "local", skills.Key{Scope: "project", Name: "checks"}, examples)
			if calls != 1 {
				t.Fatal("unexpected dispatch count", calls)
			}
			if mode == "drafted" {
				if err != nil || out.Status != "drafted" || out.Result == nil {
					t.Fatal(out, err)
				}
			} else if err == nil {
				t.Fatal("canceled generation succeeded")
			}
			stored, readErr := s.SkillGenerationAttempt(base, "recorded")
			if readErr != nil {
				t.Fatal(readErr)
			}
			if mode == "drafted" {
				if stored.Status != "drafted" || stored.Result == nil || stored.Result.Usage == nil || stored.Result.Usage.InputTokens != 20 {
					t.Fatal("draft not durable", stored)
				}
			} else if stored.Status != "failed" || stored.Code != "canceled" || stored.Result != nil {
				t.Fatal("canceled caller prevented cleanup", stored)
			}
			before, _ := json.Marshal(stored)
			if _, err := generator.GenerateRecorded(base, s, "recorded", "local", skills.Key{Scope: "project", Name: "checks"}, examples); err == nil || calls != 1 {
				t.Fatal("same attempt redispatched", err, calls)
			}
			after, err := s.SkillGenerationAttempt(base, "recorded")
			encoded, _ := json.Marshal(after)
			if err != nil || string(encoded) != string(before) {
				t.Fatal("duplicate mutated terminal attempt", err)
			}
			var events int
			if err := s.db.QueryRow(`SELECT count(*) FROM events`).Scan(&events); err != nil || events != 0 {
				t.Fatal("generation created runtime journal", events, err)
			}
		})
	}
}

func generationAttemptFixture(id string) skills.GenerationAttempt {
	return skills.GenerationAttempt{Version: 1, ID: id, Key: skills.Key{Scope: "project", Name: "checks"}, Model: "generator", Provider: "local", InputDigest: strings.Repeat("a", 64), SourceSessions: []string{"session-a", "session-b"}, SourceEvidence: []string{"check-a", "check-b"}, Status: "started", EstimatedCost: .01, StartedAt: time.Unix(200, 0).UTC()}
}

func draftedGeneration(a skills.GenerationAttempt) skills.GenerationAttempt {
	a.Status = "drafted"
	a.FinishedAt = a.StartedAt.Add(time.Second)
	a.Result = &skills.ModelDraftResult{Model: a.Model, Elapsed: time.Second, Usage: &providers.Usage{InputTokens: 20, OutputTokens: 8}, Draft: skills.Draft{Key: a.Key, Description: "Run shared checks", SourceSessions: append([]string(nil), a.SourceSessions...), SourceEvidence: append([]string(nil), a.SourceEvidence...), Steps: []string{"Run tests"}, ValidationCases: []string{"Known fixture"}}}
	return a
}

func TestSkillGenerationLifecycleRestartAndNoJournalMutation(t *testing.T) {
	s, path := generationStore(t)
	ctx := context.Background()
	a := generationAttemptFixture("attempt-a")
	if err := s.BeginSkillGeneration(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginSkillGeneration(ctx, a); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate begin granted dispatch", err)
	}
	done := draftedGeneration(a)
	if err := s.FinishSkillGeneration(ctx, done); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishSkillGeneration(ctx, done); err != nil {
		t.Fatal("terminal retry failed", err)
	}
	if err := s.BeginSkillGeneration(ctx, a); !errors.Is(err, ErrConflict) {
		t.Fatal("terminal ID reused", err)
	}
	failed := a
	failed.Status = "failed"
	failed.Code = "generation_failed"
	failed.FinishedAt = done.FinishedAt
	if err := s.FinishSkillGeneration(ctx, failed); !errors.Is(err, ErrConflict) {
		t.Fatal("terminal outcome rewritten", err)
	}
	for _, table := range []string{"events", "fitness"} {
		var count int
		if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("generation mutated unrelated state", table, count, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	got, err := ro.SkillGenerationAttempt(ctx, a.ID)
	if err != nil || !reflect.DeepEqual(got, done) {
		t.Fatal("restart changed result", got, done, err)
	}
	if err := ro.BeginSkillGeneration(ctx, generationAttemptFixture("read-only")); err == nil {
		t.Fatal("readonly begin wrote")
	}
	if err := ro.FinishSkillGeneration(ctx, done); err == nil {
		t.Fatal("readonly finish admitted mutation API")
	}
	got.Result.Draft.Steps[0] = "changed"
	again, err := ro.SkillGenerationAttempt(ctx, a.ID)
	if err != nil || again.Result.Draft.Steps[0] != "Run tests" {
		t.Fatal("read result aliases persistence", again, err)
	}
}

func TestSkillGenerationBeginAndFinishRaces(t *testing.T) {
	s, path := generationStore(t)
	ctx := context.Background()
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	a := generationAttemptFixture("raced")
	run := func(fn func(*Store, int) error) []error {
		var group sync.WaitGroup
		results := make(chan error, 2)
		for i, db := range []*Store{s, other} {
			group.Add(1)
			go func(i int, db *Store) { defer group.Done(); results <- fn(db, i) }(i, db)
		}
		group.Wait()
		close(results)
		out := []error{}
		for err := range results {
			out = append(out, err)
		}
		return out
	}
	checkOne := func(results []error) {
		t.Helper()
		wins := 0
		for _, err := range results {
			if err == nil {
				wins++
			} else if !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
		}
		if wins != 1 {
			t.Fatal("race did not select exactly one winner", wins)
		}
	}
	checkOne(run(func(db *Store, _ int) error { return db.BeginSkillGeneration(ctx, a) }))
	checkOne(run(func(db *Store, i int) error {
		done := draftedGeneration(a)
		if i == 1 {
			done.Result.Draft.Description = "Another valid draft"
		}
		return db.FinishSkillGeneration(ctx, done)
	}))
	got, err := s.SkillGenerationAttempt(ctx, a.ID)
	if err != nil || got.Status != "drafted" {
		t.Fatal(got, err)
	}
	if err := s.FinishSkillGeneration(ctx, got); err != nil {
		t.Fatal("winner retry failed", err)
	}
}

func TestSkillGenerationFinishRequiresExactStartedBinding(t *testing.T) {
	for _, change := range []func(*skills.GenerationAttempt){
		func(a *skills.GenerationAttempt) { a.Model = "different"; a.Result.Model = "different" },
		func(a *skills.GenerationAttempt) { a.Provider = "different" },
		func(a *skills.GenerationAttempt) { a.Key.Name = "different"; a.Result.Draft.Key = a.Key },
		func(a *skills.GenerationAttempt) { a.InputDigest = strings.Repeat("b", 64) },
		func(a *skills.GenerationAttempt) { a.EstimatedCost = .02 },
		func(a *skills.GenerationAttempt) { a.StartedAt = a.StartedAt.Add(time.Millisecond) },
		func(a *skills.GenerationAttempt) {
			a.SourceSessions = []string{"session-c", "session-d"}
			a.Result.Draft.SourceSessions = append([]string(nil), a.SourceSessions...)
		},
		func(a *skills.GenerationAttempt) {
			a.SourceEvidence = []string{"check-c"}
			a.Result.Draft.SourceEvidence = append([]string(nil), a.SourceEvidence...)
		},
	} {
		s, _ := generationStore(t)
		ctx := context.Background()
		a := generationAttemptFixture("bound")
		if err := s.BeginSkillGeneration(ctx, a); err != nil {
			t.Fatal(err)
		}
		done := draftedGeneration(a)
		change(&done)
		if err := s.FinishSkillGeneration(ctx, done); !errors.Is(err, ErrConflict) {
			t.Fatal("changed attempt binding accepted", err)
		}
		got, err := s.SkillGenerationAttempt(ctx, a.ID)
		if err != nil || got.Status != "started" {
			t.Fatal("conflict mutated attempt", got, err)
		}
	}
	s, _ := generationStore(t)
	if err := s.FinishSkillGeneration(context.Background(), draftedGeneration(generationAttemptFixture("unknown"))); err == nil {
		t.Fatal("finish created unknown attempt")
	}
}

func TestSkillGenerationScopedPagination(t *testing.T) {
	s, path := generationStore(t)
	ctx := context.Background()
	for _, id := range []string{"z", "a", "m"} {
		a := generationAttemptFixture(id)
		if err := s.BeginSkillGeneration(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	other := generationAttemptFixture("other")
	other.Key.Scope = "elsewhere"
	if err := s.BeginSkillGeneration(ctx, other); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	first, err := ro.ListSkillGenerationAttempts(ctx, "project", "", 2)
	if err != nil || len(first) != 2 || first[0].ID != "a" || first[1].ID != "m" {
		t.Fatal(first, err)
	}
	last, err := ro.ListSkillGenerationAttempts(ctx, "project", "m", 2)
	if err != nil || len(last) != 1 || last[0].ID != "z" {
		t.Fatal(last, err)
	}
	empty, err := ro.ListSkillGenerationAttempts(ctx, "project", "z", 2)
	if err != nil || len(empty) != 0 {
		t.Fatal(empty, err)
	}
	for _, a := range first {
		if a.Key.Scope != "project" || a.Status != "started" {
			t.Fatal("cross-scope read or mutation", a)
		}
	}
	for _, limit := range []int{0, 101, -1} {
		if _, err := ro.ListSkillGenerationAttempts(ctx, "project", "", limit); err == nil {
			t.Fatal("invalid page limit accepted", limit)
		}
	}
}

func TestSkillGenerationCorruptionFailsReadsWithoutPartialResults(t *testing.T) {
	for _, tc := range []struct {
		name, column string
		value        any
	}{
		{"malformed body", "body", []byte(`{"private":"malformed"}`)},
		{"oversized body", "body", []byte(strings.Repeat("x", 2<<20))},
		{"scope mismatch", "scope", "elsewhere"},
		{"name mismatch", "name", "other"},
		{"status mismatch", "status", "drafted"},
		{"id mismatch", "id", "renamed"},
		{"oversized name", "name", strings.Repeat("a", 65)},
		{"oversized status", "status", strings.Repeat("a", 65)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := generationStore(t)
			ctx := context.Background()
			a := generationAttemptFixture("a")
			if err := s.BeginSkillGeneration(ctx, a); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec("UPDATE skill_generation_attempts SET "+tc.column+"=? WHERE id=?", tc.value, a.ID); err != nil {
				t.Fatal(err)
			}
			readID := a.ID
			if tc.column == "id" {
				readID = "renamed"
			}
			if got, err := s.SkillGenerationAttempt(ctx, readID); !errors.Is(err, skills.ErrInvalid) || got.ID != "" {
				t.Fatal("corrupt attempt admitted", got, err)
			}
			scope := "project"
			if tc.column == "scope" {
				scope = "elsewhere"
			}
			if got, err := s.ListSkillGenerationAttempts(ctx, scope, "", 10); !errors.Is(err, skills.ErrInvalid) || len(got) != 0 {
				t.Fatal("corrupt listing admitted", got, err)
			}
		})
	}
}

func TestSkillGenerationFailedTerminalAndCanceledCalls(t *testing.T) {
	for _, code := range []string{"generation_failed", "canceled", "persistence_failed"} {
		s, _ := generationStore(t)
		ctx := context.Background()
		a := generationAttemptFixture(code)
		if err := s.BeginSkillGeneration(ctx, a); err != nil {
			t.Fatal(err)
		}
		a.Status, a.Code, a.FinishedAt = "failed", code, a.StartedAt.Add(time.Second)
		if err := s.FinishSkillGeneration(ctx, a); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishSkillGeneration(ctx, a); err != nil {
			t.Fatal("failed retry rejected", err)
		}
		got, err := s.SkillGenerationAttempt(ctx, a.ID)
		if err != nil || got.Status != "failed" || got.Code != code || got.Result != nil {
			t.Fatal(got, err)
		}
	}
	s, _ := generationStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.BeginSkillGeneration(ctx, generationAttemptFixture("canceled")); err == nil {
		t.Fatal("canceled begin dispatched")
	}
	if _, err := s.SkillGenerationAttempt(ctx, "canceled"); err == nil {
		t.Fatal("canceled read accepted")
	}
	if _, err := s.ListSkillGenerationAttempts(ctx, "project", "", 10); err == nil {
		t.Fatal("canceled list accepted")
	}
}
