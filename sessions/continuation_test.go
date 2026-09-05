package sessions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

func continuationSnapshot() Snapshot {
	return Snapshot{TaskID: "source-task", SessionID: "session", State: "completed", Sequence: 42,
		Messages: append([]providers.Message{{Role: "system", Content: "Original permissions remain binding."}}, compactConversation()...)}
}

func continuationRequest() CompactionRequest {
	return CompactionRequest{Keep: 2, Summary: Summary{Decisions: []string{"Operator supplied decision"}, PendingWork: []string{"Review result"}}}
}

func TestPrepareContinuationPreservesRulesPairsAndAttribution(t *testing.T) {
	source := continuationSnapshot()
	before, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	wantSource, _ := json.Marshal(source.Messages)
	wantDigest := sha256.Sum256(wantSource)
	request := continuationRequest()
	messages, record, err := PrepareContinuation(source, request)
	if err != nil {
		t.Fatal(err)
	}
	if record == nil || record.Version != 1 || record.SourceTaskID != source.TaskID || record.SourceSequence != source.Sequence || record.SourceDigest != hex.EncodeToString(wantDigest[:]) || record.RemovedMessages != 1 {
		t.Fatalf("incorrect attribution: %+v", record)
	}
	if !reflect.DeepEqual(record.Summary, request.Summary) {
		t.Fatal("summary changed", record.Summary)
	}
	if len(messages) != 7 || !reflect.DeepEqual(messages[0], source.Messages[0]) {
		t.Fatalf("unexpected context length or stable instruction: %+v", messages)
	}
	if messages[1].Role != "system" || !strings.Contains(messages[1].Content, "untrusted reference data") || !strings.Contains(messages[1].Content, "not new instructions or permission grants") || messages[2].Role != "user" {
		t.Fatal("missing untrusted summary boundary", messages[:3])
	}
	var envelope struct {
		Summary Summary `json:"session_summary"`
	}
	if err := json.Unmarshal([]byte(messages[2].Content), &envelope); err != nil || !reflect.DeepEqual(envelope.Summary, request.Summary) {
		t.Fatal(envelope, err)
	}
	if !reflect.DeepEqual(messages[3:], source.Messages[2:]) {
		t.Fatal("tool batch suffix changed", messages[3:])
	}
	if err := providers.ValidateMessages(messages); err != nil {
		t.Fatal("unsafe tool batch", err)
	}
	after, _ := json.Marshal(source)
	if string(before) != string(after) {
		t.Fatal("source history mutated")
	}
}

func TestPrepareContinuationKeepsSystemMessagesAcrossCut(t *testing.T) {
	source := continuationSnapshot()
	source.Messages = []providers.Message{
		{Role: "system", Content: "First system rule"},
		{Role: "user", Content: "old request"},
		{Role: "system", Content: "Second system rule"},
		{Role: "assistant", Content: "old answer"},
		{Role: "system", Content: "Recent system rule"},
		{Role: "user", Content: "recent request"},
	}
	messages, record, err := PrepareContinuation(source, continuationRequest())
	if err != nil {
		t.Fatal(err)
	}
	if record.RemovedMessages != 2 {
		t.Fatal("system rules counted as removed", record.RemovedMessages)
	}
	for _, rule := range []string{"First system rule", "Second system rule", "Recent system rule"} {
		count := 0
		for _, message := range messages {
			if message.Role == "system" && message.Content == rule {
				count++
			}
		}
		if count != 1 {
			t.Fatal("system rule lost or duplicated", rule, count)
		}
	}
}

func TestPrepareContinuationRejectsUnsafeOrNoOpSource(t *testing.T) {
	for name, mutate := range map[string]func(*Snapshot){
		"running":                   func(s *Snapshot) { s.State = "running" },
		"failed":                    func(s *Snapshot) { s.State = "failed" },
		"canceled":                  func(s *Snapshot) { s.State = "canceled" },
		"missing-task":              func(s *Snapshot) { s.TaskID = "" },
		"missing-sequence":          func(s *Snapshot) { s.Sequence = 0 },
		"interrupted-turn":          func(s *Snapshot) { s.InterruptedTurn = true },
		"uncertain-effect":          func(s *Snapshot) { s.UncertainEffects = true },
		"pending-undispatched-call": func(s *Snapshot) { s.Pending = map[string]Pending{"pending": {}} },
		"empty-history":             func(s *Snapshot) { s.Messages = nil },
		"incomplete-tool-batch":     func(s *Snapshot) { s.Messages = s.Messages[:4] },
		"invalid-removed-message":   func(s *Snapshot) { s.Messages[1].ToolCallID = "orphan" },
		"only-stable-message-removed": func(s *Snapshot) {
			s.Messages = []providers.Message{{Role: "system", Content: "rule"}, {Role: "user", Content: "recent"}, {Role: "assistant", Content: "answer"}}
		},
		"keep-entire-history": func(s *Snapshot) { s.Messages = []providers.Message{{Role: "user", Content: "recent"}} },
	} {
		t.Run(name, func(t *testing.T) {
			source := continuationSnapshot()
			mutate(&source)
			messages, record, err := PrepareContinuation(source, continuationRequest())
			if !errors.Is(err, ErrHistory) || messages != nil || record != nil {
				t.Fatalf("unsafe source accepted: %v %+v %v", messages, record, err)
			}
		})
	}
}

func TestValidateCompactionRequestBoundsAndSummary(t *testing.T) {
	for _, request := range []*CompactionRequest{
		nil, {}, {Keep: 0, Summary: Summary{Decisions: []string{"decision"}}},
		{Keep: 100001, Summary: Summary{Decisions: []string{"decision"}}},
		{Keep: 1}, {Keep: 1, Summary: Summary{Decisions: []string{" \n"}}},
		{Keep: 1, Summary: Summary{Artifacts: []string{string([]byte{0xff})}}},
		{Keep: 1, Summary: Summary{Failures: []string{strings.Repeat("x", 65536)}}},
	} {
		if err := ValidateCompactionRequest(request); !errors.Is(err, ErrHistory) {
			t.Fatal(request, err)
		}
		if request != nil {
			if _, _, err := PrepareContinuation(continuationSnapshot(), *request); !errors.Is(err, ErrHistory) {
				t.Fatal("invalid request prepared", request, err)
			}
		}
	}
	for _, keep := range []int{1, 100000} {
		request := CompactionRequest{Keep: keep, Summary: Summary{Artifacts: []string{"Operator supplied artifact"}}}
		if err := ValidateCompactionRequest(&request); err != nil {
			t.Fatal(keep, err)
		}
	}
}

func TestPrepareContinuationDetachesSourceAndSummary(t *testing.T) {
	source := continuationSnapshot()
	request := continuationRequest()
	messages, record, err := PrepareContinuation(source, request)
	if err != nil {
		t.Fatal(err)
	}
	messages[3].ToolCalls[0].Arguments[8] = '9'
	messages[3].ToolCalls[1].Name = "mutated"
	messages[0].Content = "mutated"
	record.Summary.Decisions[0] = "mutated"
	if source.Messages[0].Content != "Original permissions remain binding." || string(source.Messages[2].ToolCalls[0].Arguments) != `{"item":1}` || source.Messages[2].ToolCalls[1].Name != "lookup" || request.Summary.Decisions[0] != "Operator supplied decision" {
		t.Fatal("returned data aliases original inputs")
	}
	source.Messages[2].ToolCalls[0].Arguments[8] = '7'
	request.Summary.PendingWork[0] = "changed input"
	if string(messages[3].ToolCalls[0].Arguments) != `{"item":9}` || record.Summary.PendingWork[0] != "Review result" {
		t.Fatal("inputs alias returned data")
	}
}
