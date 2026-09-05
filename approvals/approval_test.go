package approvals

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validRequest() Request {
	now := time.Date(2026, 9, 5, 12, 0, 0, 1, time.UTC)
	return Request{Version: 1, ID: "approval-1", TaskID: "task-1", TurnID: "turn_1", ToolCallID: "call-1", ToolName: "write_file", Scope: "workspace:report.txt", ArgumentsDigest: strings.Repeat("a", 64), SchemaDigest: strings.Repeat("b", 64), PolicyDigest: strings.Repeat("c", 64), CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
}

func validDecision(r Request) Decision {
	return Decision{ID: "decision-1", Actor: "operator@example.test", Allowed: true, Time: r.CreatedAt.Add(time.Second)}
}

func TestRequestValidation(t *testing.T) {
	if err := validRequest().Validate(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		edit func(*Request)
	}{
		{"version_zero", func(r *Request) { r.Version = 0 }},
		{"future_version", func(r *Request) { r.Version = 2 }},
		{"empty_id", func(r *Request) { r.ID = "" }},
		{"long_id", func(r *Request) { r.ID = strings.Repeat("a", 129) }},
		{"unicode_id", func(r *Request) { r.ID = "审批" }},
		{"space_id", func(r *Request) { r.ID = "approval 1" }},
		{"empty_task", func(r *Request) { r.TaskID = "" }},
		{"invalid_task", func(r *Request) { r.TaskID = "../task" }},
		{"empty_turn", func(r *Request) { r.TurnID = "" }},
		{"invalid_turn", func(r *Request) { r.TurnID = "turn\n" }},
		{"empty_call", func(r *Request) { r.ToolCallID = "" }},
		{"invalid_call", func(r *Request) { r.ToolCallID = "call:1" }},
		{"empty_tool", func(r *Request) { r.ToolName = "" }},
		{"digit_first_tool", func(r *Request) { r.ToolName = "1tool" }},
		{"hyphen_tool", func(r *Request) { r.ToolName = "write-file" }},
		{"long_tool", func(r *Request) { r.ToolName = strings.Repeat("x", 65) }},
		{"empty_scope", func(r *Request) { r.Scope = "" }},
		{"untrimmed_scope", func(r *Request) { r.Scope = " workspace" }},
		{"wildcard_scope", func(r *Request) { r.Scope = "workspace/*" }},
		{"control_scope", func(r *Request) { r.Scope = "work\x00space" }},
		{"unicode_control_scope", func(r *Request) { r.Scope = "work\u0085space" }},
		{"invalid_utf8_scope", func(r *Request) { r.Scope = string([]byte{0xff}) }},
		{"long_scope", func(r *Request) { r.Scope = strings.Repeat("x", 257) }},
		{"empty_arguments_digest", func(r *Request) { r.ArgumentsDigest = "" }},
		{"short_schema_digest", func(r *Request) { r.SchemaDigest = strings.Repeat("a", 63) }},
		{"uppercase_policy_digest", func(r *Request) { r.PolicyDigest = strings.Repeat("A", 64) }},
		{"nonhex_arguments_digest", func(r *Request) { r.ArgumentsDigest = strings.Repeat("g", 64) }},
		{"long_policy_digest", func(r *Request) { r.PolicyDigest = strings.Repeat("a", 65) }},
		{"zero_created", func(r *Request) { r.CreatedAt = time.Time{} }},
		{"zero_expiry", func(r *Request) { r.ExpiresAt = time.Time{} }},
		{"equal_expiry", func(r *Request) { r.ExpiresAt = r.CreatedAt }},
		{"past_expiry", func(r *Request) { r.ExpiresAt = r.CreatedAt.Add(-time.Nanosecond) }},
		{"long_lifetime", func(r *Request) { r.ExpiresAt = r.CreatedAt.Add(10*time.Minute + time.Nanosecond) }},
		{"nonutc_created", func(r *Request) { r.CreatedAt = r.CreatedAt.In(time.FixedZone("east", 3600)) }},
		{"nonutc_expiry", func(r *Request) { r.ExpiresAt = r.ExpiresAt.In(time.FixedZone("east", 3600)) }},
		{"early_year", func(r *Request) {
			r.CreatedAt = time.Date(1969, 12, 31, 23, 59, 0, 1, time.UTC)
			r.ExpiresAt = r.CreatedAt.Add(time.Second)
		}},
		{"late_year", func(r *Request) {
			r.CreatedAt = time.Date(2261, 1, 1, 0, 0, 0, 1, time.UTC)
			r.ExpiresAt = r.CreatedAt.Add(time.Second)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validRequest()
			tt.edit(&r)
			if err := r.Validate(); err == nil {
				t.Fatal("accepted invalid request")
			}
		})
	}
}

func TestRequestValidationBoundaries(t *testing.T) {
	r := validRequest()
	r.ID = strings.Repeat("a", 128)
	r.TaskID, r.TurnID, r.ToolCallID = r.ID, r.ID, r.ID
	r.ToolName = "_" + strings.Repeat("a", 63)
	r.Scope = strings.Repeat("x", 256)
	r.ExpiresAt = r.CreatedAt.Add(10 * time.Minute)
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, year := range []int{1970, 2260} {
		r.CreatedAt = time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
		r.ExpiresAt = r.CreatedAt.Add(time.Nanosecond)
		if err := r.Validate(); err != nil {
			t.Fatalf("year %d: %v", year, err)
		}
	}
}

func TestRecordStates(t *testing.T) {
	r := validRequest()
	d := validDecision(r)
	revoked := Decision{ID: "decision-2", Actor: "second operator", Allowed: false, Time: d.Time}
	consumed := d.Time
	for _, record := range []Record{
		{Request: r, State: "pending"},
		{Request: r, State: "approved", Decisions: []Decision{d}},
		{Request: r, State: "denied", Decisions: []Decision{{ID: d.ID, Actor: d.Actor, Time: d.Time}}},
		{Request: r, State: "revoked", Decisions: []Decision{d, revoked}},
		{Request: r, State: "consumed", Decisions: []Decision{d}, ConsumedAt: &consumed},
	} {
		t.Run(record.State, func(t *testing.T) {
			before, _ := json.Marshal(record)
			if err := record.Validate(); err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(record)
			if string(before) != string(after) {
				t.Fatal("validation mutated record")
			}
		})
	}
}

func TestRecordRejectsInvalidStateAndDecisionBindings(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Record)
	}{
		{"invalid_request", func(r *Record) { r.Request.TaskID = "" }},
		{"unknown_state", func(r *Record) { r.State = "complete" }},
		{"empty_state", func(r *Record) { r.State = "" }},
		{"pending_with_decision", func(r *Record) { r.State = "pending" }},
		{"approved_without_decision", func(r *Record) { r.Decisions = nil }},
		{"approved_with_two", func(r *Record) { r.Decisions = append(r.Decisions, r.Decisions[0]) }},
		{"approved_with_denial", func(r *Record) { r.Decisions[0].Allowed = false }},
		{"denied_with_approval", func(r *Record) { r.State = "denied" }},
		{"decision_empty_id", func(r *Record) { r.Decisions[0].ID = "" }},
		{"decision_invalid_id", func(r *Record) { r.Decisions[0].ID = "decision/1" }},
		{"decision_empty_actor", func(r *Record) { r.Decisions[0].Actor = "" }},
		{"decision_untrimmed_actor", func(r *Record) { r.Decisions[0].Actor = " operator" }},
		{"decision_control_actor", func(r *Record) { r.Decisions[0].Actor = "op\nname" }},
		{"decision_invalid_utf8_actor", func(r *Record) { r.Decisions[0].Actor = string([]byte{0xff}) }},
		{"decision_long_actor", func(r *Record) { r.Decisions[0].Actor = strings.Repeat("x", 129) }},
		{"decision_before_request", func(r *Record) { r.Decisions[0].Time = r.Request.CreatedAt.Add(-time.Nanosecond) }},
		{"decision_at_expiry", func(r *Record) { r.Decisions[0].Time = r.Request.ExpiresAt }},
		{"decision_zero_time", func(r *Record) { r.Decisions[0].Time = time.Time{} }},
		{"decision_nonutc_time", func(r *Record) { r.Decisions[0].Time = r.Decisions[0].Time.In(time.FixedZone("east", 3600)) }},
		{"approved_with_consumption", func(r *Record) { at := r.Decisions[0].Time; r.ConsumedAt = &at }},
		{"consumed_without_time", func(r *Record) { r.State = "consumed" }},
		{"consumed_before_decision", func(r *Record) {
			r.State = "consumed"
			at := r.Decisions[0].Time.Add(-time.Nanosecond)
			r.ConsumedAt = &at
		}},
		{"consumed_at_expiry", func(r *Record) { r.State = "consumed"; at := r.Request.ExpiresAt; r.ConsumedAt = &at }},
		{"revoked_without_second", func(r *Record) { r.State = "revoked" }},
		{"revoked_duplicate_id", func(r *Record) {
			r.State = "revoked"
			d := r.Decisions[0]
			d.Allowed = false
			r.Decisions = append(r.Decisions, d)
		}},
		{"revoked_second_approval", func(r *Record) {
			r.State = "revoked"
			d := r.Decisions[0]
			d.ID = "decision-2"
			r.Decisions = append(r.Decisions, d)
		}},
		{"revoked_reverse_time", func(r *Record) {
			r.State = "revoked"
			d := r.Decisions[0]
			d.ID = "decision-2"
			d.Allowed = false
			d.Time = d.Time.Add(-time.Nanosecond)
			r.Decisions = append(r.Decisions, d)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validRequest()
			record := Record{Request: r, State: "approved", Decisions: []Decision{validDecision(r)}}
			tt.edit(&record)
			before, _ := json.Marshal(record)
			if err := record.Validate(); err == nil {
				t.Fatal("accepted invalid record")
			}
			after, _ := json.Marshal(record)
			if string(before) != string(after) {
				t.Fatal("failed validation mutated record")
			}
		})
	}
}
