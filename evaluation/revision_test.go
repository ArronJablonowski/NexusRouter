package evaluation

import (
	"testing"
	"time"

	"darwinrouter/routing"
)

func revisionFixture() (Record, Record) {
	prior := Record{Version: 1, ID: "judge", TaskID: "task", AttemptID: "attempt", Key: routing.Key{Model: "model", Provider: "provider", Domain: "creative", Profile: "default"}, Checks: []Check{{Source: LLMJudge, Reference: "audit", Passed: false}}, AllowJudge: true, ExecutionSucceeded: true, Time: time.Now()}
	next := prior
	next.ID = "user"
	next.AllowJudge = false
	next.Checks = []Check{{Source: UserFeedback, Reference: "feedback", Passed: true}}
	return prior, next
}
func TestUserRevisionOverridesSubjectiveJudge(t *testing.T) {
	prior, next := revisionFixture()
	if err := ValidateRevision(prior, next); err != nil {
		t.Fatal(err)
	}
	prior = next
	next.ID = "correction"
	next.Checks = []Check{{Source: UserFeedback, Reference: "correction", Passed: false}}
	if err := ValidateRevision(prior, next); err != nil {
		t.Fatal(err)
	}
}
func TestRevisionCannotChangeObjectiveEvidenceOrMetrics(t *testing.T) {
	for _, change := range []func(*Record, *Record){
		func(p, n *Record) { p.Checks = []Check{{Source: Deterministic, Reference: "tests", Passed: false}} },
		func(p, n *Record) { p.Checks = []Check{{Source: ToolResult, Reference: "tool", Passed: false}} },
		func(p, n *Record) { n.Cost = 1 }, func(p, n *Record) { n.Time = n.Time.Add(time.Second) },
		func(p, n *Record) { n.Key.Domain = "coding" }, func(p, n *Record) { n.ExecutionSucceeded = false },
		func(p, n *Record) { n.AllowJudge = true }, func(p, n *Record) { n.Checks = []Check{{Source: LLMJudge, Reference: "judge2", Passed: true}} },
	} {
		prior, next := revisionFixture()
		change(&prior, &next)
		if ValidateRevision(prior, next) == nil {
			t.Fatal("invalid revision accepted")
		}
	}
}
