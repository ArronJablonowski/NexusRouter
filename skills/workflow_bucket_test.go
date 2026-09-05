package skills

import (
	"fmt"
	"reflect"
	"testing"
)

func TestWorkflowBucketMergeMatchesGroupAndOwnsSlices(t *testing.T) {
	p := procedureFixture("task-z", "session-a", "read_file", "run.tests")
	b, err := NewWorkflowBucket(p)
	if err != nil || b.Validate() != nil {
		t.Fatal(b, err)
	}
	if _, err := b.Group(); err == nil {
		t.Fatal("singleton became group")
	}
	p.Tools[0] = "changed"
	if b.Tools[0] != "read_file" {
		t.Fatal("constructor aliases input")
	}
	for _, p := range []WorkflowProcedure{procedureFixture("task-b", "session-b", "read_file", "run.tests"), procedureFixture("task-a", "session-a", "read_file", "run.tests")} {
		b, err = b.Merge(p)
		if err != nil {
			t.Fatal(err)
		}
	}
	g, err := b.Group()
	if err != nil || g.ID != "f39dd1e90e4546b021815124f9ab62a79fa603af751f6e9f430c98fe97584c63" || g.Sources[0].TaskID != "task-a" || len(g.Sources) != 2 {
		t.Fatal(g, err)
	}
	retry, err := b.Merge(procedureFixture("task-a", "session-a", "read_file", "run.tests"))
	if err != nil || !reflect.DeepEqual(retry, b) {
		t.Fatal(retry, err)
	}
	g.Tools[0] = "changed"
	g.Sources[0].TaskID = "changed"
	retry.Tools[0] = "changed"
	retry.Sources[0].TaskID = "changed"
	if b.Tools[0] != "read_file" || b.Sources[0].TaskID != "task-a" {
		t.Fatal("group/retry aliases bucket")
	}
}

func TestWorkflowBucketBoundedDeterministicRetention(t *testing.T) {
	build := func(reverse bool) WorkflowBucket {
		var b WorkflowBucket
		for i := 0; i < 40; i++ {
			n := i
			if reverse {
				n = 39 - i
			}
			p := procedureFixture(fmt.Sprintf("task-%02d", n), fmt.Sprintf("session-%02d", n%25), "read")
			var err error
			if i == 0 {
				b, err = NewWorkflowBucket(p)
			} else {
				b, err = b.Merge(p)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		return b
	}
	a, b := build(false), build(true)
	if !reflect.DeepEqual(a, b) || len(a.Sources) != 20 || a.Sources[19].TaskID != "task-19" {
		t.Fatal(a, b)
	}
}

func TestWorkflowBucketRejectsConflictsAndMalformed(t *testing.T) {
	p := procedureFixture("a", "a", "read", "test")
	b, _ := NewWorkflowBucket(p)
	for _, mode := range []string{"candidate", "domain", "profile", "order", "empty"} {
		q := p
		q.Tools = append([]string{}, p.Tools...)
		switch mode {
		case "candidate":
			q.Candidate.SourceSequence++
		case "domain":
			q.Candidate.Domain = "other"
		case "profile":
			q.Profile = "other"
		case "order":
			q.Tools = []string{"test", "read"}
		case "empty":
			q.Tools = []string{}
		}
		if _, err := b.Merge(q); err == nil {
			t.Fatal("accepted", mode)
		}
	}
	if _, err := NewWorkflowBucket(procedureFixture("a", "a")); err == nil {
		t.Fatal("empty procedure")
	}
	for _, mode := range []string{"id", "sources", "session", "domain", "order", "version"} {
		q := b
		q.Sources = append([]WorkflowCandidate{}, b.Sources...)
		switch mode {
		case "id":
			q.ID = "bad"
		case "sources":
			q.Sources = nil
		case "session":
			s := p.Candidate
			s.TaskID = "b"
			q.Sources = append(q.Sources, s)
		case "domain":
			q.Sources[0].Domain = "other"
		case "order":
			q.Sources = append(q.Sources, p.Candidate)
		case "version":
			q.Version = 2
		}
		if q.Validate() == nil {
			t.Fatal("accepted", mode)
		}
		if _, err := q.Merge(p); err == nil {
			t.Fatal("merged invalid", mode)
		}
	}
}
