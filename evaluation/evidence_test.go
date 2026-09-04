package evaluation

import "testing"

func TestEvidencePrecedence(t *testing.T) {
	checks := []Check{{LLMJudge, "judge", true}, {UserFeedback, "feedback", true}, {Deterministic, "test", false}, {Deterministic, "schema", true}}
	out, err := Resolve(checks, true)
	if err != nil || out.Accepted || out.Source != Deterministic || len(out.References) != 2 {
		t.Fatalf("%+v %v", out, err)
	}
	for _, c := range [][]Check{nil, {{LLMJudge, "judge", true}}, {{Source("self_rating"), "model", true}}, {{Deterministic, "same", true}, {ToolResult, "same", true}}} {
		if _, err := Resolve(c, false); err == nil {
			t.Fatal("unsupported evidence accepted")
		}
	}
}
