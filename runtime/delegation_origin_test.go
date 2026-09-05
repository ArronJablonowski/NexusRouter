package runtime

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDelegationOriginValidationAndClone(t *testing.T) {
	base := DelegationOrigin{Version: 1, TurnID: "turn", AttemptID: "attempt", ToolCallID: "call", ToolName: "delegate"}
	if base.Validate() != nil {
		t.Fatal("single origin rejected")
	}
	for i := 0; i < 4; i++ {
		o := base
		o.ToolName = "delegate_batch"
		o.BatchIndex = &i
		if o.Validate() != nil {
			t.Fatal("valid batch index rejected")
		}
		clone := o.Clone()
		*clone.BatchIndex = 9
		if i == 9 {
			t.Fatal("clone aliases caller index")
		}
	}
	for name, mutate := range map[string]func(*DelegationOrigin){
		"version": func(o *DelegationOrigin) { o.Version = 2 }, "empty turn": func(o *DelegationOrigin) { o.TurnID = "" }, "blank attempt": func(o *DelegationOrigin) { o.AttemptID = " \t" }, "long turn": func(o *DelegationOrigin) { o.TurnID = strings.Repeat("x", 129) }, "long call": func(o *DelegationOrigin) { o.ToolCallID = strings.Repeat("x", 257) }, "control": func(o *DelegationOrigin) { o.ToolCallID = "call\u0085" }, "invalid utf8": func(o *DelegationOrigin) { o.AttemptID = "\xff" }, "unknown tool": func(o *DelegationOrigin) { o.ToolName = "shell" }, "single index": func(o *DelegationOrigin) { i := 0; o.BatchIndex = &i }, "batch missing index": func(o *DelegationOrigin) { o.ToolName = "delegate_batch" }, "negative": func(o *DelegationOrigin) { i := -1; o.ToolName = "delegate_batch"; o.BatchIndex = &i }, "too high": func(o *DelegationOrigin) { i := 4; o.ToolName = "delegate_batch"; o.BatchIndex = &i },
	} {
		t.Run(name, func(t *testing.T) {
			o := base
			mutate(&o)
			if o.Validate() == nil {
				t.Fatal("invalid origin accepted")
			}
		})
	}
}

func TestDelegationOriginOnlyOnChildStartAndOptionalForHistory(t *testing.T) {
	base := Event{Version: 1, ID: "event", TaskID: "child", SessionID: "session", CorrelationID: "child", Sequence: 1, Time: time.Now(), Kind: TaskStarted, Data: Data{ParentTaskID: "parent"}}
	if base.Validate() != nil {
		t.Fatal("historical start requires new origin")
	}
	zero := 0
	base.Data.DelegationOrigin = &DelegationOrigin{Version: 1, TurnID: "turn", AttemptID: "attempt", ToolCallID: "call", ToolName: "delegate_batch", BatchIndex: &zero}
	if base.Validate() != nil {
		t.Fatal("child origin rejected")
	}
	body, err := json.Marshal(base)
	if err != nil || !strings.Contains(string(body), `"batch_index":0`) {
		t.Fatal("zero batch index omitted")
	}
	var decoded Event
	if json.Unmarshal(body, &decoded) != nil || decoded.Validate() != nil || decoded.Data.DelegationOrigin.BatchIndex == nil {
		t.Fatal("origin roundtrip failed")
	}
	for _, kind := range []Kind{TaskCompleted, TaskFailed, WorkerStarted, EvaluationRecorded, ToolStarted} {
		e := base
		e.Kind = kind
		if e.Validate() == nil {
			t.Fatal("origin allowed outside start", kind)
		}
	}
	base.Data.ParentTaskID = ""
	if base.Validate() == nil {
		t.Fatal("root task accepted delegation origin")
	}
}
