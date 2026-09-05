package skills

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func procedureFixture(task, session string, tools ...string) WorkflowProcedure {
	return WorkflowProcedure{Version: 1, Candidate: WorkflowCandidate{TaskID: task, SessionID: session, Domain: "code", Privacy: "local_only", EvaluationID: "evaluation", EvaluationDigest: strings.Repeat("a", 64), SourceDigest: strings.Repeat("b", 64), SourceSequence: 4}, Profile: "default", Tools: append([]string{}, tools...)}
}

func TestWorkflowGroupGoldenIdentityAndOwnedCanonicalSources(t *testing.T) {
	input := []WorkflowProcedure{procedureFixture("task-z", "session-a", "read_file", "run.tests"), procedureFixture("task-b", "session-b", "read_file", "run.tests"), procedureFixture("task-a", "session-a", "read_file", "run.tests")}
	groups, err := BuildWorkflowGroups(input)
	if err != nil || len(groups) != 1 || groups[0].Validate() != nil {
		t.Fatal(groups, err)
	}
	g := groups[0]
	if g.ID != "f39dd1e90e4546b021815124f9ab62a79fa603af751f6e9f430c98fe97584c63" || len(g.Sources) != 2 || g.Sources[0].TaskID != "task-a" || g.Sources[1].TaskID != "task-b" {
		t.Fatal("unstable identity or session selection", g)
	}
	slices.Reverse(input)
	again, err := BuildWorkflowGroups(input)
	if err != nil || !reflect.DeepEqual(groups, again) {
		t.Fatal("input order changed grouping", again, err)
	}
	input[0].Tools[0] = "changed"
	input[0].Candidate.Domain = "other"
	if groups[0].Tools[0] != "read_file" || groups[0].Sources[0].Domain != "code" {
		t.Fatal("result aliases input")
	}
	groups[0].Tools[0] = "caller-change"
	groups[0].Sources[0].TaskID = "caller-change"
	if again[0].Tools[0] != "read_file" || again[0].Sources[0].TaskID != "task-a" {
		t.Fatal("results alias")
	}
	// Identity excludes source membership but each selected source remains valid.
	g.Sources = []WorkflowCandidate{procedureFixture("task-c", "session-c").Candidate, procedureFixture("task-d", "session-d").Candidate}
	g.Tools = []string{"read_file", "run.tests"}
	if g.Validate() != nil || g.ID != again[0].ID {
		t.Fatal("source membership changed grouping identity")
	}
}

func TestWorkflowGroupsExactSequenceProfileDomainAndOmissions(t *testing.T) {
	input := []WorkflowProcedure{}
	for i, tools := range [][]string{{"read_file", "run.tests"}, {"run.tests", "read_file"}, {"read_file", "read_file"}} {
		for _, suffix := range []string{"a", "b"} {
			input = append(input, procedureFixture(string(rune('a'+i))+suffix, "s"+string(rune('a'+i))+suffix, tools...))
		}
	}
	profile := procedureFixture("profile-a", "profile-a", "read_file", "run.tests")
	profile.Profile = "other"
	domain := procedureFixture("domain-a", "domain-a", "read_file", "run.tests")
	domain.Candidate.Domain = "creative"
	input = append(input, profile, domain, procedureFixture("empty", "empty"))
	groups, err := BuildWorkflowGroups(input)
	if err != nil || len(groups) != 3 {
		t.Fatal(groups, err)
	}
	for i, g := range groups {
		if g.Validate() != nil || (i > 0 && groups[i-1].ID >= g.ID) {
			t.Fatal("groups not ordered", groups)
		}
	}
	for _, only := range [][]WorkflowProcedure{{procedureFixture("a", "a")}, {procedureFixture("a", "a", "read")}, {procedureFixture("a", "same", "read"), procedureFixture("b", "same", "read")}} {
		groups, err := BuildWorkflowGroups(only)
		if err != nil || groups == nil || len(groups) != 0 {
			t.Fatal("empty/single-session group emitted", groups, err)
		}
	}
}

func TestWorkflowProcedureAndGroupValidationBoundaries(t *testing.T) {
	for _, mode := range []string{"version", "candidate", "profile", "nil-tools", "too-many-tools", "blank-tool", "invalid-tool", "long-tool"} {
		p := procedureFixture("task", "session", "read_file")
		switch mode {
		case "version":
			p.Version = 2
		case "candidate":
			p.Candidate.TaskID = "../bad"
		case "profile":
			p.Profile = strings.Repeat("a", 65)
		case "nil-tools":
			p.Tools = nil
		case "too-many-tools":
			p.Tools = make([]string, 65)
		case "blank-tool":
			p.Tools = []string{""}
		case "invalid-tool":
			p.Tools = []string{"../tool"}
		case "long-tool":
			p.Tools = []string{strings.Repeat("a", 129)}
		}
		if p.Validate() == nil {
			t.Fatal("malformed procedure accepted", mode)
		}
		if _, err := BuildWorkflowGroups([]WorkflowProcedure{p}); err == nil {
			t.Fatal("malformed input grouped", mode)
		}
	}
	p := procedureFixture("task", "session", strings.Repeat("a", 128))
	if p.Validate() != nil {
		t.Fatal("valid max tool length denied")
	}
	if _, err := BuildWorkflowGroups(nil); err == nil {
		t.Fatal("nil input accepted")
	}
	if _, err := BuildWorkflowGroups(make([]WorkflowProcedure, 21)); err == nil {
		t.Fatal("unbounded input accepted")
	}
	if _, err := BuildWorkflowGroups([]WorkflowProcedure{p, p}); err == nil {
		t.Fatal("duplicate task accepted")
	}
	for _, mode := range []string{"id", "algorithm", "domain", "profile", "nil-tools", "empty-tools", "version", "few-sources", "duplicate-session", "unsorted", "wrong-domain"} {
		groups, _ := BuildWorkflowGroups([]WorkflowProcedure{procedureFixture("a", "a", "read"), procedureFixture("b", "b", "read")})
		g := groups[0]
		switch mode {
		case "id":
			g.ID = strings.Repeat("f", 64)
		case "algorithm":
			g.Algorithm = "other"
		case "domain":
			g.Domain = "other"
		case "profile":
			g.Profile = "other"
		case "nil-tools":
			g.Tools = nil
		case "empty-tools":
			g.Tools = []string{}
		case "version":
			g.Version = 2
		case "few-sources":
			g.Sources = g.Sources[:1]
		case "duplicate-session":
			g.Sources[1].SessionID = g.Sources[0].SessionID
		case "unsorted":
			slices.Reverse(g.Sources)
		case "wrong-domain":
			g.Sources[0].Domain = "other"
		}
		if g.Validate() == nil {
			t.Fatal("malformed group accepted", mode)
		}
	}
}
