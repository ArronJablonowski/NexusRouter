package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestScopedRankingSeparatesIdenticalConfigurations(t *testing.T) {
	id := identity("same-model", "same-harness")
	c := candidate(id)
	good, pass := observation("same-task", id, testClass, true)
	bad, fail := observation("same-task", id, testClass, false)
	a := ScopedCandidate{Scope: "node-a", Candidate: c, Evidence: snapshot(t, []Execution{bad}, []Review{fail})}
	b := ScopedCandidate{Scope: "node-b", Candidate: c, Evidence: snapshot(t, []Execution{good}, []Review{pass})}
	for _, cs := range [][]ScopedCandidate{{a, b}, {b, a}} {
		got, err := SelectScoped(request(), DefaultPolicy(), cs, testNow, 0)
		if err != nil || got.Primary.Scope != "node-b" || len(got.Ranked) != 2 || got.Ranked[0].Ranked.ConfirmedSamples != 1 || got.Ranked[1].Ranked.ConfirmedSamples != 1 {
			t.Fatal(got, err)
		}
	}
	b.Candidate.CapacityAvailable = false
	got, err := SelectScoped(request(), DefaultPolicy(), []ScopedCandidate{a, b}, testNow, 0)
	if err != nil || got.Primary.Scope != "node-a" || len(got.Excluded) != 1 || got.Excluded[0].Exclusion.Reasons[0] != "capacity" {
		t.Fatal(got, err)
	}
	if _, err = SelectScoped(request(), DefaultPolicy(), []ScopedCandidate{a, a}, testNow, 0); !errors.Is(err, ErrInvalid) {
		t.Fatal("duplicate scope identity", err)
	}
}
func TestScopedRankingPreservesOrdinaryScoresAndExploration(t *testing.T) {
	id := identity("one", "pi")
	e, r := observation("pass", id, testClass, true)
	s := snapshot(t, []Execution{e}, []Review{r})
	c := candidate(id)
	local, err := Select(request(), DefaultPolicy(), []Candidate{c}, s, testNow, 0)
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := SelectScoped(request(), DefaultPolicy(), []ScopedCandidate{{"node-a", c, s}}, testNow, 0)
	if err != nil || scoped.Primary.Ranked != local.Primary || scoped.Reason != local.Reason {
		t.Fatal(scoped, local, err)
	}
	unknown := candidate(identity("two", "goose"))
	empty := snapshot(t, nil, nil)
	candidates := []ScopedCandidate{{"node-a", c, s}, {"node-b", unknown, empty}}
	p := DefaultPolicy()
	p.Exploration = .25
	req := request()
	got, err := SelectScoped(req, p, candidates, testNow, 0)
	if err != nil || got.Explored || got.Primary.Scope != "node-a" {
		t.Fatal(got, err)
	}
	req.AllowExploration = true
	got, err = SelectScoped(req, p, candidates, testNow, 0)
	if err != nil || !got.Explored || got.Primary.Scope != "node-b" {
		t.Fatal(got, err)
	}
}
func TestReadOnlyEvidenceCannotWriteOrCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "evidence")
	if _, err := OpenEvidenceStoreReadOnly(dir); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("reader created directory", err)
	}
	writer, err := OpenEvidenceStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	e, r := observation("pass", identity("model", "pi"), testClass, true)
	if err = writer.AppendExecution(context.Background(), e, testNow); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenEvidenceStoreReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err = reader.AppendReview(context.Background(), r, testNow); err == nil {
		t.Fatal("read-only ledger accepted write")
	}
	if _, err = reader.Snapshot(context.Background(), testNow); err != nil {
		t.Fatal(err)
	}
	if err = writer.AppendReview(context.Background(), r, testNow); err != nil {
		t.Fatal(err)
	}
	s, err := reader.Snapshot(context.Background(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Select(request(), DefaultPolicy(), []Candidate{candidate(e.Actual)}, s, testNow, 0)
	if err != nil || got.Primary.ConfirmedSamples != 1 {
		t.Fatal("reader missed committed WAL review", got, err)
	}
}
