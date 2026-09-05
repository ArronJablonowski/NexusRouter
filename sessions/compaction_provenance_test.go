package sessions

import (
	"context"
	"reflect"
	"testing"

	"darwinrouter/providers"
)

func TestReplayMessageOriginsAndCompactionEstimates(t *testing.T) {
	events := fixture()
	events[0].Data.Messages = append([]providers.Message{{Role: "system", Content: "stable policy"}}, events[0].Data.Messages...)
	source, err := Replay(context.Background(), events, "task")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(source.MessageSequences, []int64{1, 1, 4, 6}) {
		t.Fatalf("message origins=%v", source.MessageSequences)
	}
	request := CompactionRequest{Keep: 1, Summary: Summary{Requirements: []string{"retain requirement"}}}
	messages, record, err := PrepareContinuation(source, request)
	if err != nil {
		t.Fatal(err)
	}
	// Keep=1 cuts into a tool batch, so the first retained message expands
	// backward to the assistant call at index 2, event sequence 4.
	if record.FirstRetainedMessage != 2 || record.FirstRetainedSequence != 4 || record.RemovedMessages != 1 {
		t.Fatalf("incorrect first-retained provenance: %+v", record)
	}
	before, err := providers.EstimateContext(providers.Request{Messages: source.Messages})
	if err != nil {
		t.Fatal(err)
	}
	after, err := providers.EstimateContext(providers.Request{Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	if record.BeforeContextTokens != before || record.AfterContextTokens != after || before < 1 || after < 1 {
		t.Fatalf("incorrect estimates: %+v", record)
	}
	if err := record.Validate(source.TaskID); err != nil {
		t.Fatal(err)
	}
	source.MessageSequences[2] = 1
	if record.FirstRetainedSequence != 4 {
		t.Fatal("record aliases message origins")
	}
}

func TestCompactionOriginValidationAndLegacySnapshot(t *testing.T) {
	source := continuationSnapshot()
	_, record, err := PrepareContinuation(source, continuationRequest())
	if err != nil || record.FirstRetainedSequence != 0 || record.FirstRetainedMessage != 2 || record.BeforeContextTokens < 1 || record.AfterContextTokens < 1 {
		t.Fatalf("legacy snapshot rejected: %+v %v", record, err)
	}
	for _, kind := range []string{"empty", "short", "zero", "negative", "future", "reordered"} {
		t.Run(kind, func(t *testing.T) {
			s := continuationSnapshot()
			s.MessageSequences = make([]int64, len(s.Messages))
			for i := range s.MessageSequences {
				s.MessageSequences[i] = 1
			}
			switch kind {
			case "empty":
				s.MessageSequences = []int64{}
			case "short":
				s.MessageSequences = s.MessageSequences[:1]
			case "zero":
				s.MessageSequences[0] = 0
			case "negative":
				s.MessageSequences[0] = -1
			case "future":
				s.MessageSequences[0] = s.Sequence + 1
			case "reordered":
				s.MessageSequences[0] = 2
			}
			if _, _, err := PrepareContinuation(s, continuationRequest()); err == nil {
				t.Fatal("invalid origin metadata accepted")
			}
		})
	}
}
