package telemetry

import (
	"context"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestWorkflowSelectionSaveRaceRestartAndImmutableRetry(t *testing.T) {
	s, path := generationStore(t)
	ctx := context.Background()
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	a := workflowSelectionFixture(t, "workflow")
	inputs := []skills.WorkflowSelection{a, a}
	inputs[1].CreatedAt = a.CreatedAt.Add(time.Second)
	var results [2]skills.WorkflowSelection
	var errs [2]error
	var wg sync.WaitGroup
	for i, store := range []*Store{s, other} {
		wg.Add(1)
		go func(i int, store *Store) {
			defer wg.Done()
			results[i], errs[i] = store.SaveWorkflowSelection(ctx, inputs[i])
		}(i, store)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || !reflect.DeepEqual(results[0], results[1]) {
		t.Fatal("race changed immutable selection", results, errs)
	}
	if results[0].CreatedAt != inputs[0].CreatedAt && results[0].CreatedAt != inputs[1].CreatedAt {
		t.Fatal("server invented timestamp")
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	saved, err := ro.WorkflowSelection(ctx, a.Key.Scope, a.ID)
	if err != nil || !reflect.DeepEqual(saved, results[0]) {
		t.Fatal("restart lost selection", saved, err)
	}
	before := selectionRows(t, s)
	a.CreatedAt = a.CreatedAt.Add(2 * time.Hour)
	again, err := s.SaveWorkflowSelection(ctx, a)
	if err != nil || !reflect.DeepEqual(again, saved) {
		t.Fatal("timestamp retry not immutable", again, err)
	}
	if !reflect.DeepEqual(before, selectionRows(t, s)) {
		t.Fatal("exact retry rewrote selection")
	}
	if _, err := ro.SaveWorkflowSelection(ctx, a); err == nil {
		t.Fatal("readonly save allowed")
	}
	if !reflect.DeepEqual(before, selectionRows(t, s)) {
		t.Fatal("readonly save mutated")
	}
}

func selectionRows(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT id||':'||scope||':'||name||':'||CAST(body AS TEXT) FROM workflow_selections ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			t.Fatal(err)
		}
		out = append(out, body)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestWorkflowSelectionScopedPaginationAndNoMutation(t *testing.T) {
	s, path := generationStore(t)
	ctx := context.Background()
	var ids []string
	for _, name := range []string{"one", "two", "three"} {
		a := workflowSelectionFixture(t, name)
		if _, err := s.SaveWorkflowSelection(ctx, a); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.ID)
	}
	sort.Strings(ids)
	before := selectionRows(t, s)
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	first, err := ro.ListWorkflowSelections(ctx, "project", "", 2)
	if err != nil || len(first) != 2 || first[0].ID != ids[0] || first[1].ID != ids[1] {
		t.Fatal(first, err)
	}
	last, err := ro.ListWorkflowSelections(ctx, "project", ids[1], 2)
	if err != nil || len(last) != 1 || last[0].ID != ids[2] {
		t.Fatal(last, err)
	}
	empty, err := ro.ListWorkflowSelections(ctx, "other", "", 2)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatal(empty, err)
	}
	if _, err := ro.WorkflowSelection(ctx, "other", ids[0]); err == nil {
		t.Fatal("cross-scope read")
	}
	if !reflect.DeepEqual(before, selectionRows(t, s)) {
		t.Fatal("reads mutated records")
	}
	// Database bytes must also remain unchanged across a closed read-only reopen.
	ro.Close()
	s.Close()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.ListWorkflowSelections(ctx, "project", "", 10); err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	after, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(body, after) {
		t.Fatal("readonly reopen mutated database", err)
	}
}

func TestWorkflowSelectionCorruptionAndInvalidInputs(t *testing.T) {
	for _, mode := range []string{"scope", "name", "id", "invalid-json", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := generationStore(t)
			ctx := context.Background()
			a := workflowSelectionFixture(t, "workflow")
			if _, err := s.SaveWorkflowSelection(ctx, a); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "scope":
				_, _ = s.db.Exec(`UPDATE workflow_selections SET scope='other'`)
			case "name":
				_, _ = s.db.Exec(`UPDATE workflow_selections SET name='other'`)
			case "id":
				_, _ = s.db.Exec(`UPDATE workflow_selections SET id=?`, strings.Repeat("f", 64))
			case "invalid-json":
				_, _ = s.db.Exec(`UPDATE workflow_selections SET body='private-corrupt-body'`)
			case "oversize":
				_, _ = s.db.Exec(`UPDATE workflow_selections SET body=?`, strings.Repeat("x", 65537))
			}
			if mode != "scope" {
				valid := workflowSelectionFixture(t, "unaffected")
				if _, err := s.SaveWorkflowSelection(ctx, valid); err != nil {
					t.Fatal(err)
				}
			}
			before := selectionRows(t, s)
			scope, id := a.Key.Scope, a.ID
			if mode == "scope" {
				scope = "other"
			}
			if mode == "id" {
				id = strings.Repeat("f", 64)
			}
			if got, err := s.WorkflowSelection(ctx, scope, id); err == nil || !reflect.DeepEqual(got, skills.WorkflowSelection{}) || strings.Contains(err.Error(), "private-corrupt-body") {
				t.Fatal("corrupt selection accepted", got, err)
			}
			if got, err := s.ListWorkflowSelections(ctx, scope, "", 100); err == nil || len(got) != 0 {
				t.Fatal("corrupt list returned results", got, err)
			}
			if !reflect.DeepEqual(before, selectionRows(t, s)) {
				t.Fatal("corruption repaired")
			}
		})
	}
	s, _ := generationStore(t)
	a := workflowSelectionFixture(t, "workflow")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.SaveWorkflowSelection(ctx, a); err == nil {
		t.Fatal("canceled save")
	}
	if _, err := s.WorkflowSelection(ctx, "project", a.ID); err == nil {
		t.Fatal("canceled read")
	}
	if _, err := s.ListWorkflowSelections(ctx, "project", "", 10); err == nil {
		t.Fatal("canceled list")
	}
	for _, limit := range []int{0, 101} {
		if _, err := s.ListWorkflowSelections(context.Background(), "project", "", limit); err == nil {
			t.Fatal("invalid limit")
		}
	}
	bad := a
	bad.ID = "invalid"
	if _, err := s.SaveWorkflowSelection(context.Background(), bad); err == nil {
		t.Fatal("invalid record saved")
	}
	if len(selectionRows(t, s)) != 0 {
		t.Fatal("invalid requests mutated")
	}
}

// This is host-attributed metadata; saving it deliberately does not establish
// that its source task journals exist or that any workflow may execute.
func workflowSelectionFixture(t *testing.T, name string) skills.WorkflowSelection {
	t.Helper()
	sources := []skills.WorkflowCandidate{}
	for _, suffix := range []string{"a", "b"} {
		sources = append(sources, skills.WorkflowCandidate{TaskID: "task-" + suffix, SessionID: "session-" + suffix, Domain: "creative", Privacy: "local_only", EvaluationID: "evaluation-" + suffix, EvaluationDigest: strings.Repeat("a", 64), SourceDigest: strings.Repeat("b", 64), SourceSequence: 4})
	}
	a, err := skills.NewWorkflowSelection(skills.Key{Scope: "project", Name: name}, "creative", "exact-domain-v1", "model", strings.Repeat("c", 64), sources, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return a
}
