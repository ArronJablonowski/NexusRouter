package skills

import (
	"encoding/json"
	"strings"
	"testing"
)

func candidateFixture() WorkflowCandidate {
	return WorkflowCandidate{TaskID: "task-b", SessionID: "session", Domain: "creative", Privacy: "local_only", EvaluationID: "evidence", EvaluationDigest: strings.Repeat("a", 64), SourceDigest: strings.Repeat("b", 64), SourceSequence: 5}
}

func TestWorkflowCandidatePageBoundsAndMetadata(t *testing.T) {
	p := WorkflowCandidatePage{Version: 1, Domain: "creative", Candidates: []WorkflowCandidate{candidateFixture()}, Scanned: 2, Next: "task-c"}
	if p.Validate("task-a", 2) != nil {
		t.Fatal("valid page rejected")
	}
	body, err := json.Marshal(p)
	if err != nil || strings.Contains(string(body), "steps") || strings.Contains(string(body), "messages") {
		t.Fatal("metadata contract contains conversation fields")
	}
	for name, change := range map[string]func(*WorkflowCandidatePage){
		"version":        func(p *WorkflowCandidatePage) { p.Version = 2 },
		"nil":            func(p *WorkflowCandidatePage) { p.Candidates = nil },
		"count":          func(p *WorkflowCandidatePage) { p.Scanned = 0 },
		"over":           func(p *WorkflowCandidatePage) { p.Scanned = 3 },
		"missing-next":   func(p *WorkflowCandidatePage) { p.Next = "" },
		"backwards-next": func(p *WorkflowCandidatePage) { p.Next = "task-a" },
		"outside-window": func(p *WorkflowCandidatePage) { p.Next = "task-ab" },
		"wrong-domain":   func(p *WorkflowCandidatePage) { p.Domain = "code" },
		"duplicate":      func(p *WorkflowCandidatePage) { p.Candidates = append(p.Candidates, p.Candidates[0]) },
		"digest":         func(p *WorkflowCandidatePage) { p.Candidates[0].SourceDigest = strings.Repeat("G", 64) },
		"privacy":        func(p *WorkflowCandidatePage) { p.Candidates[0].Privacy = "unknown" },
		"sequence":       func(p *WorkflowCandidatePage) { p.Candidates[0].SourceSequence = 10001 },
	} {
		t.Run(name, func(t *testing.T) {
			q := p
			q.Candidates = append([]WorkflowCandidate{}, p.Candidates...)
			change(&q)
			if q.Validate("task-a", 2) == nil {
				t.Fatal("invalid page accepted")
			}
		})
	}
	for _, limit := range []int{0, 21} {
		if p.Validate("", limit) == nil {
			t.Fatal("invalid limit")
		}
	}
	for _, scanned := range []int{0, 1, 2} {
		q := WorkflowCandidatePage{Version: 1, Domain: "creative", Candidates: []WorkflowCandidate{}, Scanned: scanned}
		if scanned == 2 {
			q.Next = "task-z"
		}
		if q.Validate("", 2) != nil {
			t.Fatal("empty eligible set rejected")
		}
	}
}
