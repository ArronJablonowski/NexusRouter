package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type resumeAPIFactory func(context.Context, providers.Connection) (providers.Provider, error)

func (f resumeAPIFactory) Build(ctx context.Context, connection providers.Connection) (providers.Provider, error) {
	return f(ctx, connection)
}

func apiRecoveredResumeSource(t *testing.T, database string) sessions.TaskHeadFence {
	t.Helper()
	ctx := context.Background()
	db, err := telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	digest := func(value string) string {
		sum := sha256.Sum256([]byte(value))
		return hex.EncodeToString(sum[:])
	}
	body := []byte(`{"prompt":"private source request"}`)
	job, err := db.CreateSubmission(ctx, digest("source-key"), digest(string(body)), digest("source-config"), body)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	claim, err := db.ClaimSubmission(ctx, digest("source-config"), now, time.Minute)
	if err != nil || claim.Status.ID != job.ID {
		t.Fatal(claim.Status, err)
	}
	events := []runtime.Event{
		{Version: 1, ID: "source-start", TaskID: "source", SessionID: "session", CorrelationID: "source", Sequence: 1, Time: now, Kind: runtime.TaskStarted, Data: runtime.Data{SubmissionID: job.ID, Privacy: "local_only", Messages: []providers.Message{{Role: "user", Content: "private history"}}}},
		{Version: 1, ID: "source-turn", TaskID: "source", SessionID: "session", CorrelationID: "source", Sequence: 2, Time: now.Add(time.Second), Kind: runtime.TurnStarted, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{ProviderID: "local", ModelID: "chat"}},
		{Version: 1, ID: "source-delta", TaskID: "source", SessionID: "session", CorrelationID: "source", Sequence: 3, Time: now.Add(2 * time.Second), Kind: runtime.ModelDelta, TurnID: "turn", AttemptID: "attempt", Data: runtime.Data{Text: "private partial"}},
	}
	for _, event := range events {
		if err = db.AppendSubmission(ctx, event.Sequence-1, event, job.ID, claim.Token); err != nil {
			t.Fatal(err)
		}
	}
	if ok, recoverErr := db.RecoverInterruptedModel(ctx, job.ID, digest("source-config"), now.Add(2*time.Minute)); recoverErr != nil || !ok {
		t.Fatal(ok, recoverErr)
	}
	fence, err := db.ResumeSource(ctx, "source")
	if err != nil {
		t.Fatal(err)
	}
	return sessions.TaskHeadFence{Version: 1, TaskID: fence.TaskID, SessionID: fence.SessionID, HeadSequence: fence.HeadSequence, HeadEventID: fence.HeadEventID}
}

func resumeRequest(body string) *http.Request {
	r := request(http.MethodPost, "/v1/tasks/source/resumes", body)
	r.Header.Set("Idempotency-Key", "fixture-resume-key")
	return r
}

func TestTaskResumeStrictAdmissionAndBinding(t *testing.T) {
	var calls atomic.Int32
	s := services()
	s.SubmitResume = func(_ context.Context, key string, source sessions.TaskHeadFence, request app.Request) (submissions.Status, error) {
		calls.Add(1)
		if key != "fixture-resume-key" || source.TaskID != "source" || source.SessionID != "session" || source.HeadSequence != 2 || source.HeadEventID != "source-terminal" || request.ModelID != "model" || request.Prompt != "branch prompt" || request.ContinueTaskID != "" {
			t.Fatal("adapter changed resume admission", key, source, request)
		}
		return branchStatus(), nil
	}
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, resumeRequest(branchBody))
	if w.Code != http.StatusAccepted || calls.Load() != 1 {
		t.Fatal(w.Code, calls.Load(), w.Body.String())
	}
	for _, body := range []string{
		`{}`,
		strings.Replace(branchBody, `"task_id":"source"`, `"task_id":"other"`, 1),
		strings.Replace(branchBody, `"head_event_id":"source-terminal"`, `"head_event_id":"source-terminal","head_event_id":"other"`, 1),
		strings.Replace(branchBody, `"prompt":"branch prompt"`, `"prompt":"branch prompt","continue_task_id":"source"`, 1),
		branchBody + `{}`,
	} {
		before := calls.Load()
		w = httptest.NewRecorder()
		h.ServeHTTP(w, resumeRequest(body))
		if w.Code != http.StatusBadRequest || calls.Load() != before {
			t.Fatal("invalid resume dispatched", w.Code, calls.Load(), w.Body.String())
		}
	}
}

func TestTaskResumeTransportCapacityCancellationAndErrors(t *testing.T) {
	for _, mode := range []string{"auth", "origin", "query", "method", "key", "media", "missing", "capacity", "canceled", "conflict", "admission", "panic"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			s := services()
			s.SubmitResume = func(context.Context, string, sessions.TaskHeadFence, app.Request) (submissions.Status, error) {
				calls.Add(1)
				switch mode {
				case "conflict":
					return submissions.Status{}, submissions.ErrConflict
				case "admission":
					return submissions.Status{}, app.ErrAdmission
				case "panic":
					panic("private resume secret")
				}
				return branchStatus(), nil
			}
			if mode == "missing" {
				s.SubmitResume = nil
			}
			h, _ := New(token, 1, s)
			if mode == "capacity" {
				h.intake <- struct{}{}
				h.intake <- struct{}{}
			}
			r := resumeRequest(branchBody)
			want := http.StatusAccepted
			switch mode {
			case "auth":
				r.Header.Del("Authorization")
				want = http.StatusUnauthorized
			case "origin":
				r.Header.Set("Origin", "https://example.com")
				want = http.StatusForbidden
			case "query":
				r.URL.RawQuery = "secret=value"
				want = http.StatusBadRequest
			case "method":
				r.Method = http.MethodPut
				want = http.StatusNotFound
			case "key":
				r.Header.Del("Idempotency-Key")
				want = http.StatusBadRequest
			case "media":
				r.Header.Set("Content-Type", "text/plain")
				want = http.StatusUnsupportedMediaType
			case "missing", "capacity":
				want = http.StatusServiceUnavailable
			case "canceled":
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
				want = http.StatusOK
			case "conflict":
				want = http.StatusConflict
			case "admission":
				want = http.StatusUnprocessableEntity
			case "panic":
				want = http.StatusInternalServerError
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if mode != "canceled" && (w.Code != want || strings.Contains(w.Body.String(), "private")) {
				t.Fatal(w.Code, w.Body.String())
			}
			if mode != "conflict" && mode != "admission" && mode != "panic" && calls.Load() != 0 {
				t.Fatal("rejected resume dispatched", calls.Load())
			}
		})
	}
}

func TestTaskResumeAuthenticatedDurableAdmission(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "resume.db")
	fence := apiRecoveredResumeSource(t, database)
	db, err := telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.Read(ctx, fence.TaskID, 0, 100)
	if err != nil || db.Close() != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Mode = "local_only"
	cfg.Hardware.AutoProfile = false
	cfg.Hardware.Concurrent = "1"
	cfg.Telemetry.Database = database
	cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:1"}}
	zero := 0.0
	cfg.Models = []config.Model{{ID: "chat", Provider: "local", Model: "fixture", Locality: "local", RAMBytes: 1, ContextTokens: 8192, Capabilities: []string{"chat"}, EstimatedCost: &zero}}
	var builds atomic.Int32
	svc, err := app.NewServiceWithProviderFactory(cfg, nil, nil, nil, nil, resumeAPIFactory(func(context.Context, providers.Connection) (providers.Provider, error) {
		builds.Add(1)
		return nil, errors.New("must not construct provider during intake")
	}))
	if err != nil {
		t.Fatal(err)
	}
	s := services()
	s.SubmitResume = svc.SubmitResume
	h, err := New(token, 1, s)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(struct {
		Version int                    `json:"version"`
		Source  sessions.TaskHeadFence `json:"source"`
		Request struct {
			ModelID string `json:"model_id"`
			Prompt  string `json:"prompt"`
		} `json:"request"`
	}{
		Version: 1,
		Source:  fence,
		Request: struct {
			ModelID string `json:"model_id"`
			Prompt  string `json:"prompt"`
		}{ModelID: "chat", Prompt: "fresh resume intent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var first submissions.Status
	for i := 0; i < 2; i++ {
		r := request(http.MethodPost, "/v1/tasks/source/resumes", string(payload))
		r.Header.Set("Idempotency-Key", "authenticated-resume-key")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var status submissions.Status
		if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &status) != nil || status.State != "queued" || status.ID == "" {
			t.Fatal(w.Code, w.Body.String())
		}
		if i == 0 {
			first = status
		} else if status.ID != first.ID {
			t.Fatal("HTTP retry was not idempotent", first.ID, status.ID)
		}
	}
	db, err = telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	after, err := db.Read(ctx, fence.TaskID, 0, 100)
	if err != nil || !reflect.DeepEqual(before, after) || builds.Load() != 0 {
		t.Fatal("HTTP resume mutated source or constructed provider", err, builds.Load())
	}
}
