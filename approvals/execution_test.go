package approvals

import (
	"encoding/json"
	"testing"
	"time"
)

func executionFixture() ExecutionStatus {
	r := validRequest()
	return ExecutionStatus{Version: 1, Approval: Record{Request: r, State: Pending}, TaskState: "running", Sequence: 4, CallState: "open", ScopeWriterState: "none", ObservedAt: r.CreatedAt.Add(time.Second)}
}

func TestExecutionStatusValidObservations(t *testing.T) {
	for _, task := range []string{"running", "completed", "failed", "canceled"} {
		for _, call := range []string{"open", "completed"} {
			for _, writer := range []string{"none", "live", "expired"} {
				for _, effect := range []string{"", "none", "confirmed", "uncertain"} {
					s := executionFixture()
					s.TaskState, s.CallState, s.ScopeWriterState, s.RecordedEffect = task, call, writer, effect
					wantValid := (call == "open" && effect == "" && task != "completed") || (call == "completed" && effect != "" && !(effect == "uncertain" && task == "completed"))
					before, _ := json.Marshal(s)
					if got := s.Validate() == nil; got != wantValid {
						t.Fatalf("task=%s call=%s writer=%s effect=%s gotvalid=%v", task, call, writer, effect, got)
					}
					after, _ := json.Marshal(s)
					if string(before) != string(after) {
						t.Fatal("validation mutated observation")
					}
				}
			}
		}
	}
}

func TestExecutionStatusRejectsInvalidMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ExecutionStatus)
	}{
		{"version", func(s *ExecutionStatus) { s.Version = 2 }},
		{"approval", func(s *ExecutionStatus) { s.Approval.Request.ID = "" }},
		{"sequence_zero", func(s *ExecutionStatus) { s.Sequence = 0 }},
		{"sequence_negative", func(s *ExecutionStatus) { s.Sequence = -1 }},
		{"sequence_large", func(s *ExecutionStatus) { s.Sequence = 10001 }},
		{"task_state", func(s *ExecutionStatus) { s.TaskState = "dead" }},
		{"call_state", func(s *ExecutionStatus) { s.CallState = "unknown" }},
		{"writer_state", func(s *ExecutionStatus) { s.ScopeWriterState = "released" }},
		{"effect", func(s *ExecutionStatus) { s.CallState = "completed"; s.RecordedEffect = "success" }},
		{"time_zero", func(s *ExecutionStatus) { s.ObservedAt = time.Time{} }},
		{"time_nonutc", func(s *ExecutionStatus) { s.ObservedAt = s.ObservedAt.In(time.FixedZone("east", 3600)) }},
		{"time_early", func(s *ExecutionStatus) { s.ObservedAt = time.Date(1969, 1, 1, 0, 0, 0, 0, time.UTC) }},
		{"time_late", func(s *ExecutionStatus) { s.ObservedAt = time.Date(2261, 1, 1, 0, 0, 0, 0, time.UTC) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := executionFixture()
			tc.edit(&s)
			if s.Validate() == nil {
				t.Fatal("invalid observation accepted")
			}
		})
	}
	for _, sequence := range []int64{1, 10000} {
		s := executionFixture()
		s.Sequence = sequence
		if err := s.Validate(); err != nil {
			t.Fatal(sequence, err)
		}
	}
}
