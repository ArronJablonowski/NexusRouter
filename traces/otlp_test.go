package traces

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func traceFixture() Snapshot {
	base := time.Unix(1_700_000_000, 0).UTC()
	return Snapshot{Version: SnapshotVersion, ObservedAt: base.Add(3 * time.Second), Traces: []Trace{{Spans: []Span{
		{Name: "task", Outcome: "completed", Parent: -1, StartedAt: base, EndedAt: base.Add(2 * time.Second)},
		{Name: "provider", Outcome: "completed", Parent: 0, StartedAt: base.Add(time.Second), EndedAt: base.Add(1200 * time.Millisecond)},
		{Name: "tool", Outcome: "completed", Parent: 0, StartedAt: base.Add(1300 * time.Millisecond), EndedAt: base.Add(1400 * time.Millisecond)},
		{Name: "route", Outcome: "explored", Parent: 0, StartedAt: base.Add(1500 * time.Millisecond), EndedAt: base.Add(1500 * time.Millisecond)},
	}}}}
}

func TestMarshalOTLPContentFreeShapeAndFreshIDs(t *testing.T) {
	snapshot := traceFixture()
	first, err := MarshalOTLP(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := MarshalOTLP(snapshot)
	if err != nil || bytes.Equal(first, second) {
		t.Fatal("wire identities were not refreshed", err)
	}
	for _, private := range [][]byte{[]byte("task-id"), []byte("model-id"), []byte("tool-name"), []byte("prompt")} {
		if bytes.Contains(first, private) {
			t.Fatal("private content escaped")
		}
	}
	var body struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Scope struct{ Version string }
				Spans []struct {
					TraceID, SpanID, ParentSpanID, Name string
				}
			}
		}
	}
	if json.Unmarshal(first, &body) != nil || len(body.ResourceSpans) != 1 || len(body.ResourceSpans[0].ScopeSpans) != 1 || len(body.ResourceSpans[0].ScopeSpans[0].Spans) != 4 {
		t.Fatal(string(first))
	}
	if body.ResourceSpans[0].ScopeSpans[0].Scope.Version != "3" {
		t.Fatal("wrong schema version", body.ResourceSpans[0].ScopeSpans[0].Scope.Version)
	}
	spans := body.ResourceSpans[0].ScopeSpans[0].Spans
	if len(spans[0].TraceID) != 32 || len(spans[0].SpanID) != 16 || spans[0].ParentSpanID != "" || spans[1].ParentSpanID != spans[0].SpanID || spans[2].ParentSpanID != spans[0].SpanID || spans[3].ParentSpanID != spans[0].SpanID {
		t.Fatal(spans)
	}
}

func TestSnapshotRejectsInvalidGraphsAndBounds(t *testing.T) {
	base := traceFixture()
	mutations := []func(*Snapshot){
		func(s *Snapshot) { s.Version++ },
		func(s *Snapshot) { s.Traces[0].Spans[0].Outcome = "private" },
		func(s *Snapshot) { s.Traces[0].Spans[1].Outcome = "private" },
		func(s *Snapshot) { s.Traces[0].Spans[1].Parent = -1 },
		func(s *Snapshot) { s.Traces[0].Spans[1].StartedAt = s.Traces[0].Spans[0].StartedAt.Add(-time.Second) },
		func(s *Snapshot) {
			s.Traces[0].Spans[2].StartedAt = s.Traces[0].Spans[1].StartedAt.Add(-time.Millisecond)
		},
		func(s *Snapshot) { s.ObservedAt = s.ObservedAt.Local() },
	}
	encoded, _ := json.Marshal(base)
	for _, mutate := range mutations {
		var candidate Snapshot
		_ = json.Unmarshal(encoded, &candidate)
		mutate(&candidate)
		if candidate.Validate() == nil {
			t.Fatal(candidate)
		}
	}
}

func TestExportOptions(t *testing.T) {
	for _, valid := range []ExportOptions{{Endpoint: "https://collector.example/v1/traces"}, {Endpoint: "http://127.0.0.1:4318/v1/traces", APIKeyEnv: "TRACE_KEY", Limit: MaxTraces}} {
		if valid.Validate() != nil || valid.TraceLimit() < 1 {
			t.Fatal(valid)
		}
	}
	for _, invalid := range []ExportOptions{{}, {Endpoint: "http://example.com/v1/traces"}, {Endpoint: "https://collector.example/v1/traces", Limit: MaxTraces + 1}, {Endpoint: "https://collector.example/v1/traces", APIKeyEnv: "bad-key"}} {
		if invalid.Validate() == nil {
			t.Fatal(invalid)
		}
	}
}

func TestSpanVocabulary(t *testing.T) {
	valid := map[string][]string{
		"provider": {"completed"}, "tool": {"completed"}, "worker": {"completed"},
		"tool_effect": {"none", "confirmed", "uncertain"},
		"route":       {"selected", "explored"}, "evaluation": {"accepted", "rejected"},
		"fallback": {"selected"}, "compaction": {"applied"}, "skill_context": {"loaded"},
		"steering": {"applied"}, "error": {"recorded"},
		"queue_residency":  {"lt_1s", "lt_10s", "lt_1m", "lt_5m", "lt_30m", "lt_1h", "gte_1h"},
		"route_constraint": {"mode", "privacy", "health", "policy", "credential", "capacity", "context", "budget", "capability"},
	}
	for name, outcomes := range valid {
		for _, outcome := range outcomes {
			if !spanVocabulary(name, outcome) {
				t.Fatal(name, outcome)
			}
		}
		if spanVocabulary(name, "private") {
			t.Fatal("open outcome vocabulary", name)
		}
	}
	if spanVocabulary("private", "completed") {
		t.Fatal("open span vocabulary")
	}
}
