package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

func TestSubmittedBranchUsesExactCompletedSourceWithoutMutation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	requests := make(chan []providers.Message, 2)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []providers.Message `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid provider request")
			return
		}
		requests <- body.Messages
		fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer provider.Close()
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Workers.Max = 1
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "branch.db")
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: provider.URL}}
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, ContextTokens: 8192, Capabilities: []string{"chat"}}}
	s, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.profile = healthProfile
	source, err := s.Run(ctx, Request{ModelID: "chat", Prompt: "root question"})
	if err != nil {
		t.Fatal(err)
	}
	if got := <-requests; len(got) != 1 || got[0].Content != "root question" {
		t.Fatal(got)
	}
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.Read(ctx, source.TaskID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ListSessionTasks(ctx, source.TaskID, sessions.SessionTaskListOptions{Limit: 25})
	if err != nil || len(page.Items) != 1 || page.Items[0].Fence.Validate() != nil {
		t.Fatal(page, err)
	}
	fence := page.Items[0].Fence
	stale := fence
	stale.HeadSequence++
	if _, err = s.SubmitBranch(ctx, "stale-branch-key-01", stale, Request{ModelID: "chat", Prompt: "must not run"}); !errors.Is(err, ErrAdmission) {
		t.Fatal("stale branch fence accepted", err)
	}
	request := Request{ModelID: "chat", Prompt: "branch question"}
	queued, err := s.SubmitBranch(ctx, "exact-branch-key-01", fence, request)
	if err != nil || queued.State != "queued" {
		t.Fatal(queued, err)
	}
	again, err := s.SubmitBranch(ctx, "exact-branch-key-01", fence, request)
	if err != nil || again.ID != queued.ID {
		t.Fatal(again, err)
	}
	changed := request
	changed.Prompt = "different branch"
	if _, err = s.SubmitBranch(ctx, "exact-branch-key-01", fence, changed); !errors.Is(err, submissions.ErrConflict) {
		t.Fatal("changed idempotent branch did not conflict", err)
	}
	dispatcher, err := StartDispatcher(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	status := awaitSubmission(t, ctx, s, queued.ID, "succeeded")
	if status.Result == nil || status.Result.Text != "answer" {
		t.Fatal(status)
	}
	got := <-requests
	if len(got) != 3 || got[0].Content != "root question" || got[1].Content != "answer" || got[2].Content != "branch question" {
		t.Fatal("branch context was not isolated", got)
	}
	child, err := sessions.Replay(ctx, db, status.Result.TaskID)
	if err != nil || child.ParentTaskID != source.TaskID || child.SessionID != source.TaskID || child.State != "completed" {
		t.Fatal(child, err)
	}
	childEvents, err := db.Read(ctx, status.Result.TaskID, 0, 100)
	if err != nil || len(childEvents) == 0 || childEvents[0].Data.Domain != "general" || childEvents[0].Data.Profile != "default" {
		t.Fatal("branch intent was not classified before durable execution", childEvents, err)
	}
	after, err := db.Read(ctx, source.TaskID, 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("branch mutated its source", err)
	}
}

func TestSubmittedBranchRejectsInvalidIntentBeforeStorage(t *testing.T) {
	for _, test := range []struct {
		name, key, secret string
		fence             sessions.TaskHeadFence
		request           Request
	}{
		{"invalid key", "short", "", sessions.TaskHeadFence{Version: 1, TaskID: "source", SessionID: "session", HeadSequence: 2, HeadEventID: "terminal"}, Request{ModelID: "fixture", Prompt: "branch"}},
		{"invalid request", "exact-branch-key-01", "", sessions.TaskHeadFence{Version: 1, TaskID: "source", SessionID: "session", HeadSequence: 2, HeadEventID: "terminal"}, Request{ModelID: "fixture", Prompt: " \n"}},
		{"messages continuation", "exact-branch-key-01", "", sessions.TaskHeadFence{Version: 1, TaskID: "source", SessionID: "session", HeadSequence: 2, HeadEventID: "terminal"}, Request{ModelID: "fixture", Messages: []providers.Message{{Role: "user", Content: "branch"}}}},
		{"secret source", "exact-branch-key-01", "private-source", sessions.TaskHeadFence{Version: 1, TaskID: "private-source", SessionID: "session", HeadSequence: 2, HeadEventID: "terminal"}, Request{ModelID: "fixture", Prompt: "branch"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := submissionService(t)
			if test.secret != "" {
				s.secret = func(string) string { return test.secret }
			}
			if _, err := s.SubmitBranch(context.Background(), test.key, test.fence, test.request); !errors.Is(err, ErrAdmission) {
				t.Fatal(err)
			}
			if _, err := os.Stat(s.settings.Telemetry.Database); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid branch intent created storage", err)
			}
		})
	}
}

func TestSubmittedBranchSourceDriftFailsAsAdmissionBeforeExecution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := submissionService(t)
	db, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	start := runtime.Event{Version: 1, ID: "source-start", TaskID: "source", SessionID: "session", CorrelationID: "source", Sequence: 1, Time: now, Kind: runtime.TaskStarted, Data: runtime.Data{Privacy: "local_only"}}
	end := runtime.Event{Version: 1, ID: "source-end", TaskID: "source", SessionID: "session", CorrelationID: "source", Sequence: 2, Time: now.Add(time.Second), Kind: runtime.TaskCompleted, Data: runtime.Data{Text: "answer"}}
	if err = db.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	if err = db.Append(ctx, 1, end); err != nil {
		t.Fatal(err)
	}
	queued, err := s.SubmitBranch(ctx, "exact-branch-key-01", sessions.TaskHeadFence{Version: 1, TaskID: "source", SessionID: "session", HeadSequence: 2, HeadEventID: "source-end"}, Request{ModelID: "fixture", Prompt: "branch"})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", s.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.ExecContext(ctx, `UPDATE events SET body=CAST(body AS TEXT)||' ' WHERE task_id='source' AND sequence=2`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	dispatcher, err := StartDispatcher(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	status := awaitSubmission(t, ctx, s, queued.ID, "failed")
	if status.ErrorCode != "admission_denied" || len(status.TaskIDs) != 0 || status.Result != nil {
		t.Fatal(status)
	}
}
