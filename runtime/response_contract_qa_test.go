package runtime_test

import (
	"context"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/routing"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func TestResponseContractPreDispatchSteeringSupersedesFormat(t *testing.T) {
	journal := &steeringFixture{}
	journal.queue("Change the requested format. Explain in plain text instead of JSON.")
	calls := 0
	loop := runtime.Loop{Journal: journal, Steering: journal, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		calls++
		return emit(providers.Chunk{Text: "An explanation in plain text.", Done: true, FinishReason: "stop"})
	})}
	request := runRequest()
	request.ResponseInstructions = "Return only JSON."
	request.RequireText = true
	result, err := loop.Run(context.Background(), request)
	if err != nil || calls != 1 || result.Text != "An explanation in plain text." {
		t.Fatalf("obsolete format survived steering: calls=%d result=%+v err=%v", calls, result, err)
	}
	for _, event := range journal.events {
		if event.Kind == runtime.ResponseRevision || event.Data.Code == "deterministic.response_contract.v1" {
			t.Fatalf("enforced the obsolete format after steering: %+v", event)
		}
	}
}

func TestResponseContractRepairsCreateOneFinalQualityObservation(t *testing.T) {
	db, _ := store(t)
	calls := 0
	loop := runtime.Loop{Journal: db, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		calls++
		answer := "JSON needs a repair"
		if calls == 3 {
			answer = `{"answer":true}`
		}
		return emit(providers.Chunk{Text: answer, Done: true, FinishReason: "stop", Usage: &providers.Usage{InputTokens: 4, OutputTokens: 3}})
	})}
	request := runRequest()
	request.ResponseInstructions = "Return only JSON."
	request.RequireText = true
	request.Domain, request.Profile = "structured_json", "default"
	request.Inference.MaxOutputTokens = 30
	result, err := loop.Run(context.Background(), request)
	if err != nil || calls != 3 || result.Text != `{"answer":true}` {
		t.Fatalf("repair failed: calls=%d result=%+v err=%v", calls, result, err)
	}
	snapshot, err := sessions.Replay(context.Background(), db, request.TaskID)
	if err != nil || snapshot.State != "completed" || len(snapshot.Messages) != 6 {
		t.Fatalf("repair transcript did not replay: snapshot=%+v err=%v", snapshot, err)
	}
	key := routing.Key{Model: "fixture", Provider: "fixture", Domain: request.Domain, Profile: request.Profile}
	before, err := db.ObservationSet(context.Background(), key, false)
	if err != nil || len(before.Fitness) != 0 {
		t.Fatalf("format success manufactured model-quality feedback: %+v %v", before, err)
	}
	for i := 0; i < 2; i++ {
		if err := app.RecordFeedbackStore(context.Background(), db, request.TaskID, true, 0); err != nil {
			t.Fatal(err)
		}
	}
	after, err := db.ObservationSet(context.Background(), key, false)
	if err != nil || len(after.Fitness) != 1 || after.Fitness[0].Quality != 1 {
		t.Fatalf("final feedback was lost or duplicated by repair turns: %+v %v", after, err)
	}
	events, err := db.Read(context.Background(), request.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var end runtime.Event
	for _, event := range events {
		if event.Kind == runtime.TurnCompleted {
			end = event
		}
	}
	if after.Fitness[0].Latency != end.Time.Sub(events[0].Time) {
		t.Fatal("routing latency omitted prior repair turns")
	}
}

func TestResponseContractKeepsGradeableDraftWhenNoRepairTokenBudget(t *testing.T) {
	for _, knownUsage := range []bool{false, true} {
		name := "unknown_usage"
		if knownUsage {
			name = "budget_consumed"
		}
		t.Run(name, func(t *testing.T) {
			journal := &steeringFixture{}
			calls := 0
			loop := runtime.Loop{Journal: journal, Provider: model(func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
				calls++
				chunk := providers.Chunk{Text: "malformed JSON", Done: true, FinishReason: "stop"}
				if knownUsage {
					chunk.Usage = &providers.Usage{InputTokens: 1, OutputTokens: 1}
				}
				return emit(chunk)
			})}
			request := runRequest()
			request.ResponseInstructions = "Return only JSON."
			request.RequireText = true
			request.Inference.MaxOutputTokens = 1
			result, err := loop.Run(context.Background(), request)
			if err != nil || calls != 1 || result.Text != "malformed JSON" {
				t.Fatalf("lost completed draft to impossible correction: calls=%d result=%+v err=%v", calls, result, err)
			}
			rejected := false
			for _, event := range journal.events {
				if event.Kind == runtime.ResponseRevision {
					t.Fatal("recorded a correction with no provable output budget")
				}
				if event.Kind == runtime.EvaluationRecorded && event.Data.Code == "deterministic.response_contract.v1" {
					rejected = event.Data.Accepted != nil && !*event.Data.Accepted
				}
			}
			if !rejected {
				t.Fatal("missing rejected presentation evidence for the completed draft")
			}
		})
	}
}
