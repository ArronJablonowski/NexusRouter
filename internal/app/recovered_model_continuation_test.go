package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func TestRecoveredModelExplicitContinuation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, db, unused := recoveryFixture(t)
	var calls atomic.Int32
	requests := make(chan []providers.Message, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []providers.Message `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid provider request")
			return
		}
		calls.Add(1)
		select {
		case requests <- body.Messages:
		case <-ctx.Done():
			return
		}
		fmt.Fprintln(w, `{"message":{"content":"continued answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer provider.Close()
	s.settings.Providers[0].Endpoint = provider.URL
	grandparentMessages := []providers.Message{{Role: "user", Content: "older question"}, {Role: "assistant", Content: "older answer"}, {Role: "user", Content: "historical question"}, {Role: "assistant", Content: "complete historical answer"}}
	grandparentStart := runtime.Event{Version: 1, ID: "model-grandparent-start", TaskID: "model-grandparent", SessionID: "model-source-session", CorrelationID: "model-grandparent", Sequence: 1, Time: time.Now().Add(-time.Minute).UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{ModelID: "fixture", ProviderID: "local", Privacy: "local_only", Messages: grandparentMessages}}
	grandparentDone := runtime.Event{Version: 1, ID: "model-grandparent-complete", TaskID: grandparentStart.TaskID, SessionID: grandparentStart.SessionID, CorrelationID: grandparentStart.CorrelationID, Sequence: 2, Time: grandparentStart.Time.Add(time.Second), Kind: runtime.TaskCompleted}
	if err := db.Append(ctx, 0, grandparentStart); err != nil {
		t.Fatal(err)
	}
	if err := db.Append(ctx, 1, grandparentDone); err != nil {
		t.Fatal(err)
	}
	grandparent, err := sessions.Replay(ctx, db, grandparentStart.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	compacted, checkpoint, err := sessions.PrepareContinuation(grandparent, sessions.CompactionRequest{Keep: 1, Summary: sessions.Summary{Requirements: []string{"preserve recovered history"}}})
	if err != nil {
		t.Fatal(err)
	}
	lineage, err := runtime.ExtendContextLineage(nil, "model-parent", 1, checkpoint, grandparent.Messages)
	if err != nil {
		t.Fatal(err)
	}
	parentStart := runtime.Event{Version: 1, ID: "model-parent-start", TaskID: "model-parent", SessionID: grandparent.SessionID, CorrelationID: "model-parent", Sequence: 1, Time: grandparentDone.Time.Add(time.Second), Kind: runtime.TaskStarted, Data: runtime.Data{ModelID: "fixture", ProviderID: "local", ParentTaskID: grandparent.TaskID, Privacy: "local_only", Compaction: checkpoint, ContextLineage: lineage, Messages: compacted}}
	parentDone := runtime.Event{Version: 1, ID: "model-parent-complete", TaskID: parentStart.TaskID, SessionID: parentStart.SessionID, CorrelationID: parentStart.CorrelationID, Sequence: 2, Time: parentStart.Time.Add(time.Second), Kind: runtime.TaskCompleted}
	if err = db.Append(ctx, 0, parentStart); err != nil {
		t.Fatal(err)
	}
	if err = db.Append(ctx, 1, parentDone); err != nil {
		t.Fatal(err)
	}
	parent, err := sessions.Replay(ctx, db, parentStart.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Submit(ctx, "0123456789abcdef", Request{ModelID: "chat", Prompt: "unfinished request", ContinueTaskID: parent.TaskID}); err != nil {
		t.Fatal(err)
	}
	claim, err := db.ClaimSubmission(ctx, s.submissionConfigDigest(), time.Now().UTC(), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	initial := append(append([]providers.Message(nil), parent.Messages...), providers.Message{Role: "user", Content: "unfinished request"})
	start := runtime.Event{Version: 1, ID: "model-source-start", TaskID: "model-source", SessionID: parent.SessionID, CorrelationID: "model-source", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{SubmissionID: claim.Status.ID, ModelID: "fixture", ProviderID: "local", ParentTaskID: parent.TaskID, Privacy: "local_only", ContextLineage: lineage, Messages: initial}}
	turn := start
	turn.ID = "model-source-turn"
	turn.Sequence = 2
	turn.Kind = runtime.TurnStarted
	turn.TurnID = "source-turn"
	turn.AttemptID = "source-attempt"
	turn.Data = runtime.Data{ModelID: "fixture", ProviderID: "local"}
	delta := turn
	delta.ID = "model-source-delta"
	delta.Sequence = 3
	delta.Kind = runtime.ModelDelta
	delta.Data = runtime.Data{Text: "partial output must never be imported"}
	for _, e := range []runtime.Event{start, turn, delta} {
		if err := db.AppendSubmission(ctx, e.Sequence-1, e, claim.Status.ID, claim.Token); err != nil {
			t.Fatal(err)
		}
	}
	expireRecoveryClaim(t, s, claim.Status.ID)
	d := &Dispatcher{db: db}
	if _, err := d.recoverPage(ctx, s.submissionConfigDigest(), ""); err != nil {
		t.Fatal(err)
	}
	before, err := db.Read(ctx, start.TaskID, 0, 100)
	if err != nil || len(before) != 4 || before[3].Kind != runtime.TaskFailed || before[3].Data.Code != "interrupted_model" {
		t.Fatal("source not recovered", before, err)
	}
	raw, err := sessions.Replay(ctx, db, start.TaskID)
	if err != nil || !raw.InterruptedTurn || raw.State != "failed" {
		t.Fatal("raw recovery history changed", err)
	}
	eligibility, err := db.TaskContinuation(ctx, start.TaskID)
	if err != nil || !eligibility.HistoryEligible {
		t.Fatal("recovered source not selectable", err)
	}
	if calls.Load() != 0 || unused.Load() != 0 {
		t.Fatal("recovery repeated inference")
	}
	compaction := &sessions.CompactionRequest{Keep: 1, Summary: sessions.Summary{Requirements: []string{"preserve history"}}}
	if sessions.ValidateCompactionRequest(compaction) != nil {
		t.Fatal("invalid denial fixture")
	}
	for _, request := range []Request{{ModelID: "chat", Prompt: "next", ContinueTaskID: start.TaskID, Compaction: compaction}, {ModelID: "chat", Prompt: "next", ContinueTaskID: start.TaskID, SummaryAttemptID: "summary"}} {
		if _, err := s.Run(ctx, request); err == nil {
			t.Fatal("recovered compaction admitted")
		}
	}
	cloud := s.settings
	cloud.Mode = "hybrid"
	cloud.Models = append([]config.Model(nil), s.settings.Models...)
	cloud.Models[0].Locality = "cloud"
	cloudService, err := NewService(cloud, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cloudService.Run(ctx, Request{ModelID: "chat", Prompt: "send to cloud", ContinueTaskID: start.TaskID}); err == nil {
		t.Fatal("local recovered history sent to cloud")
	}
	if calls.Load() != 0 {
		t.Fatal("denied continuation dispatched")
	}
	restarted, err := NewService(s.settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	restarted.profile = healthProfile
	result, err := restarted.Run(ctx, Request{ModelID: "chat", Prompt: "explicit follow-up", ContinueTaskID: start.TaskID})
	if err != nil {
		t.Fatal("explicit continuation failed", err)
	}
	var messages []providers.Message
	select {
	case messages = <-requests:
	case <-ctx.Done():
		t.Fatal("provider did not receive continuation")
	}
	want := append(append([]providers.Message(nil), initial...), providers.Message{Role: "user", Content: "explicit follow-up"})
	if calls.Load() != 1 || !reflect.DeepEqual(messages, want) {
		t.Fatal("partial output imported or complete history lost")
	}
	child, err := sessions.Replay(ctx, db, result.TaskID)
	if err != nil || child.ParentTaskID != start.TaskID || child.SessionID != start.SessionID || child.State != "completed" || child.Privacy != "local_only" || child.ContextLineage == nil || child.ContextLineage.Digest != lineage.Digest {
		t.Fatal("new task lineage", err)
	}
	after, err := db.Read(ctx, start.TaskID, 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("source recovery journal mutated")
	}
	status, err := s.SubmissionStatus(ctx, claim.Status.ID)
	if err != nil || status.State != "failed" {
		t.Fatal("source submission overwritten", err)
	}
}
