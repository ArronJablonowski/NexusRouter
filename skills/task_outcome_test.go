package skills

import (
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestTaskOutcomeValidation(t *testing.T) {
	good := TaskOutcome{Version: 1, TaskID: "task", SessionID: "session", State: "canceled", Sequence: 1, OutputChecks: []TaskOutputCheck{}}
	if good.Validate() != nil {
		t.Fatal("legacy unknown rejected")
	}
	for _, mutate := range []func(*TaskOutcome){
		func(o *TaskOutcome) { o.Version = 0 },
		func(o *TaskOutcome) { o.Sequence = 10001 },
		func(o *TaskOutcome) { o.OutputChecks = nil },
		func(o *TaskOutcome) { o.EvaluationID = "unbacked" },
		func(o *TaskOutcome) { o.AttemptID = "unbacked" },
		func(o *TaskOutcome) { o.SkillContext = &runtime.SkillContextUse{Version: 99} },
		func(o *TaskOutcome) { o.Privacy = "public" },
		func(o *TaskOutcome) { o.ParentTaskID = o.TaskID },
		func(o *TaskOutcome) { o.RetryOfTaskID = "invalid\n" },
	} {
		next := good
		mutate(&next)
		if next.Validate() == nil {
			t.Fatal("invalid projection accepted", next)
		}
	}
}

func TestTaskOutcomeRejectsUnboundedOrTextQualityReferences(t *testing.T) {
	for _, ref := range []string{"", "raw output text", "line\nbreak", string([]byte{255}), strings.Repeat("a", 129), "private/path", "évidence", `raw"diagnostic`, `raw\diagnostic`, ":noprefix"} {
		out := TaskOutcome{Version: 1, TaskID: "task", SessionID: "session", State: "completed", Sequence: 4, AttemptID: "attempt", Key: &routing.Key{Model: "model:tag", Provider: "provider", Domain: "creative", Profile: "default"}, EvaluationID: "evaluation", EvaluationDigest: strings.Repeat("a", 64), Quality: &evaluation.Outcome{Source: evaluation.UserFeedback, References: []string{ref}}, OutputChecks: []TaskOutputCheck{}}
		if out.Validate() == nil {
			t.Fatal("invalid evidence reference accepted")
		}
	}
}
