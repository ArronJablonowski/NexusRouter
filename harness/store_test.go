package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func openTestEvidence(t *testing.T) (*EvidenceStore, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "evidence")
	s, e := OpenEvidenceStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}
func TestEvidencePersistenceRevisionsAndRanking(t *testing.T) {
	ctx := context.Background()
	s, dir := openTestEvidence(t)
	actual := identity("actual-model", "pi")
	e, r := observation("durable", actual, testClass, true)
	if err := s.AppendReview(ctx, r, testNow); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.AppendExecution(ctx, e, testNow); err != nil {
			t.Fatal(err)
		}
		if err := s.AppendReview(ctx, r, testNow); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.Snapshot(ctx, testNow)
	if err != nil {
		t.Fatal(err)
	}
	want := selectRoute(t, request(), DefaultPolicy(), []Candidate{candidate(actual)}, snapshot(t, []Execution{e}, []Review{r}), .9)
	got := selectRoute(t, request(), DefaultPolicy(), []Candidate{candidate(actual)}, first, .9)
	if !reflect.DeepEqual(got, want) {
		t.Fatal("persistent replay diverged")
	}
	revised := r
	revised.ID = "revision"
	revised.ExpectedHead = r.ID
	revised.Verdict = "failed"
	revised.Quality = 0
	if err = s.AppendReview(ctx, revised, testNow); err != nil {
		t.Fatal(err)
	}
	if err = s.AppendReview(ctx, r, testNow); err != nil {
		t.Fatal("old exact retry must remain idempotent", err)
	}
	conflict := r
	conflict.Quality = .5
	if err = s.AppendReview(ctx, conflict, testNow); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	changed := e
	changed.OutputSHA256 = hash("changed")
	if err = s.AppendExecution(ctx, changed, testNow); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := OpenEvidenceStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	persisted, err := restarted.Snapshot(ctx, testNow)
	if err != nil {
		t.Fatal(err)
	}
	got = selectRoute(t, request(), DefaultPolicy(), []Candidate{candidate(actual)}, persisted, .9)
	want = selectRoute(t, request(), DefaultPolicy(), []Candidate{candidate(actual)}, snapshot(t, []Execution{e}, []Review{r, revised}), .9)
	if !reflect.DeepEqual(got, want) || got.Primary.ConfirmedSamples != 1 {
		t.Fatal("restart lost/recounted revision")
	}
	withdrawn := Review{Version: 1, ID: "withdrawal", ExecutionDigest: r.ExecutionDigest, ExpectedHead: revised.ID, Verdict: "withdrawn", Reviewer: "operator", CreatedAt: testNow}
	if err = restarted.AppendReview(ctx, withdrawn, testNow); err != nil {
		t.Fatal(err)
	}
	persisted, err = restarted.Snapshot(ctx, testNow)
	if err != nil {
		t.Fatal(err)
	}
	got = selectRoute(t, request(), DefaultPolicy(), []Candidate{candidate(actual)}, persisted, .9)
	if got.Primary.ConfirmedSamples != 0 || got.Primary.EffectiveSamples != 0 {
		t.Fatal("withdrawn evidence voted")
	}
}
func TestEvidenceConcurrentIndependentWritersUseExactHead(t *testing.T) {
	ctx := context.Background()
	a, dir := openTestEvidence(t)
	b, err := OpenEvidenceStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	e, r := observation("race", identity("model", "hermes"), testClass, true)
	if err = a.AppendExecution(ctx, e, testNow); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, store := range []*EvidenceStore{a, b} {
		wg.Add(1)
		go func(i int, s *EvidenceStore) {
			defer wg.Done()
			review := r
			if i == 1 {
				review.ID = "other-review"
				review.Verdict = "failed"
			}
			<-start
			results <- s.AppendReview(ctx, review, testNow)
		}(i, store)
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
	view, err := b.Snapshot(ctx, testNow)
	if err != nil {
		t.Fatal(err)
	}
	selected := selectRoute(t, request(), DefaultPolicy(), []Candidate{candidate(e.Actual)}, view, .9)
	if selected.Primary.ConfirmedSamples != 1 {
		t.Fatal("concurrent writers inflated evidence")
	}
}
func TestEvidenceFailuresCorruptionAndPrivateStorage(t *testing.T) {
	ctx := context.Background()
	s, dir := openTestEvidence(t)
	for _, status := range []string{"infrastructure_failed", "canceled", "indeterminate"} {
		e, r := observation(status, identity("model", "goose"), testClass, true)
		e.Status = status
		r.ExecutionDigest, _ = e.Digest()
		if err := s.AppendExecution(ctx, e, testNow); err != nil {
			t.Fatal(err)
		}
		if err := s.AppendReview(ctx, r, testNow); !errors.Is(err, ErrInvalid) {
			t.Fatal("failed lineage quality admitted", err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	e, _ := observation("canceled-write", identity("model", "pi"), testClass, true)
	if err := s.AppendExecution(canceled, e, testNow); err == nil {
		t.Fatal("canceled transaction committed")
	}
	view, err := s.Snapshot(ctx, testNow)
	if err != nil || len(view.entries) != 3 {
		t.Fatal("failed append changed store", err)
	}
	// Local corruption must never silently produce a usable ranking snapshot.
	if _, err = s.db.Exec("UPDATE executions SET body=? WHERE id=?", []byte(`{}`), "canceled"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Snapshot(ctx, testNow); err == nil {
		t.Fatal("corrupt evidence accepted")
	}
	s.Close()
	if err = os.Chmod(filepath.Join(dir, "harness-evidence.sqlite"), 0644); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenEvidenceStore(dir); err == nil {
		reopened.Close()
		t.Fatal("public database accepted")
	}
	if _, err = OpenEvidenceStore("relative"); err == nil {
		t.Fatal("relative storage accepted")
	}
}
