package telemetry

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func observedToolsValidationFixture(t *testing.T) (*Store, string, skills.GenerationAttempt, skills.WorkflowSelection, []evaluation.Record) {
	t.Helper()
	store, path := generationStore(t)
	records := []evaluation.Record{
		procedureFixture(t, store, "task-a", "", runtime.NoEffect),
		procedureFixture(t, store, "task-b", "", runtime.NoEffect),
	}
	procedures, err := store.SkillWorkflowProcedures(context.Background(), []string{"task-a", "task-b"})
	if err != nil {
		t.Fatal(err)
	}
	groups, err := skills.BuildWorkflowGroups(procedures)
	if err != nil || len(groups) != 1 {
		t.Fatal(groups, err)
	}
	key := skills.Key{Scope: "project", Name: "observed-tools"}
	selection, err := skills.NewWorkflowSelection(key, groups[0].ID, skills.ObservedToolsAlgorithm, "generator", strings.Repeat("a", 64), groups[0].Sources, time.Unix(200, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	selection, err = store.SaveWorkflowSelection(context.Background(), selection)
	if err != nil {
		t.Fatal(err)
	}
	sessions, evidence := []string{}, []string{}
	for _, source := range selection.Sources {
		sessions = append(sessions, source.SessionID)
		evidence = append(evidence, source.EvaluationDigest)
	}
	slices.Sort(sessions)
	slices.Sort(evidence)
	draft := skills.Draft{Key: key, Privacy: skills.PrivacyLocalOnly, Description: "Repeat observed checks", SourceSessions: sessions, SourceEvidence: evidence, Steps: []string{"Read the input", "Run the checks"}, RequiredTools: []string{"read_file", "run_tests"}, ValidationCases: []string{"candidate text remains inert"}}
	attempt := skills.GenerationAttempt{Version: 1, ID: selection.ID, Key: key, Model: "generator-model", Provider: "local", InputDigest: strings.Repeat("b", 64), SourceSessions: sessions, SourceEvidence: evidence, Status: "started", StartedAt: time.Unix(201, 0).UTC()}
	if err = store.BeginSkillGeneration(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	attempt.Status = "drafted"
	attempt.FinishedAt = time.Unix(202, 0).UTC()
	attempt.Result = &skills.ModelDraftResult{Draft: draft, Model: attempt.Model}
	if err = store.FinishSkillGeneration(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	return store, path, attempt, selection, records
}

func TestObservedToolsValidationSnapshotCoherentExactAndOwned(t *testing.T) {
	store, _, attempt, selection, _ := observedToolsValidationFixture(t)
	got, err := store.ObservedToolsValidationSnapshot(context.Background(), "project", attempt.ID)
	if err != nil || !reflect.DeepEqual(got.Attempt, attempt) || !reflect.DeepEqual(got.Selection, selection) || got.Group.ID != selection.Group || !got.LocalOnly {
		t.Fatal(got, err)
	}
	got.Attempt.SourceSessions[0] = "mutated"
	got.Selection.Sources[0].TaskID = "mutated"
	got.Group.Tools[0] = "mutated"
	again, err := store.ObservedToolsValidationSnapshot(context.Background(), "project", attempt.ID)
	if err != nil || reflect.DeepEqual(got, again) {
		t.Fatal("returned data aliases durable snapshot", again, err)
	}
}

func TestObservedToolsValidationSnapshotRejectsStaleAndConcurrentEvidence(t *testing.T) {
	for _, mode := range []string{"stale", "concurrent", "judge-only"} {
		t.Run(mode, func(t *testing.T) {
			store, path, attempt, _, records := observedToolsValidationFixture(t)
			prior := records[0]
			reviseWith := func(writer *Store) {
				next := prior
				next.ID = "revision-" + mode
				next.Checks = []evaluation.Check{{Source: evaluation.UserFeedback, Reference: "revision-" + mode, Passed: true}}
				if mode == "judge-only" {
					next.ID = prior.ID
					next.Checks = []evaluation.Check{{Source: evaluation.LLMJudge, Reference: "judge-only", Passed: true}}
					next.AllowJudge = true
					body, marshalErr := json.Marshal(next)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					if _, updateErr := writer.db.ExecContext(context.Background(), `UPDATE evaluations SET body=? WHERE id=?`, body, prior.ID); updateErr != nil {
						t.Fatal(updateErr)
					}
					return
				}
				if err := writer.SupersedeEvaluation(context.Background(), prior.ID, next); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "concurrent" {
				writer, err := Open(context.Background(), path)
				if err != nil {
					t.Fatal(err)
				}
				defer writer.Close()
				if got, err := store.observedToolsValidationSnapshot(context.Background(), "project", attempt.ID, func() { reviseWith(writer) }); err == nil || !reflect.DeepEqual(got, ObservedToolsValidationSnapshot{}) {
					t.Fatal("concurrent revision accepted", got, err)
				}
				return
			}
			reviseWith(store)
			if got, err := store.ObservedToolsValidationSnapshot(context.Background(), "project", attempt.ID); err == nil || !reflect.DeepEqual(got, ObservedToolsValidationSnapshot{}) {
				t.Fatal("stale evidence accepted", got, err)
			}
		})
	}
}

func TestObservedToolsValidationSnapshotGuards(t *testing.T) {
	store, _, attempt, _, _ := observedToolsValidationFixture(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, request := range []struct {
		ctx     context.Context
		scope   string
		attempt string
	}{{nil, "project", attempt.ID}, {canceled, "project", attempt.ID}, {context.Background(), "other", attempt.ID}, {context.Background(), "project", "missing"}} {
		if got, err := store.ObservedToolsValidationSnapshot(request.ctx, request.scope, request.attempt); err == nil || !reflect.DeepEqual(got, ObservedToolsValidationSnapshot{}) {
			t.Fatal("invalid request accepted", request, got, err)
		}
	}
}
