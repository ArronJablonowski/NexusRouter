package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/codexbridge"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

// A completed fixture journal contains historical tools, not currently
// executable calls. All subsequent admission, persistence and protocol work is
// performed by the application and real SQLite/session bridge implementations.
func codexCompactionFixture(t *testing.T, toolArguments ...string) (*Service, *telemetry.Store, string) {
	t.Helper()
	cfg := codexTaskConfig(t)
	ctx := context.Background()
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	const task = "compaction-source"
	messages := []providers.Message{{Role: "system", Content: "Keep fixture system authority."}, {Role: "user", Content: "old removable request"}, {Role: "assistant", Content: "old removable answer"}, {Role: "user", Content: "inspect recent file"}, {Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "historical-call", Name: "read_file", Arguments: json.RawMessage(`{"path":"saved.go"}`)}}}, {Role: "tool", ToolCallID: "historical-call", Content: `{"untrusted_output":"package saved"}`}}
	if len(toolArguments) > 0 {
		messages[4].ToolCalls[0].Arguments = json.RawMessage(toolArguments[0])
	}
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.TaskCompleted} {
		e := runtime.Event{Version: 1, ID: fmt.Sprintf("compact-source-%d", i), TaskID: task, SessionID: "compact-session", CorrelationID: task, Sequence: int64(i + 1), Time: time.Now().UTC(), Kind: kind}
		if i == 0 {
			e.Data.Privacy = "cloud_allowed"
			e.Data.Messages = messages
		} else {
			e.TurnID = "source-turn"
			e.AttemptID = "source-attempt"
		}
		if kind == runtime.TurnCompleted {
			e.Data.Text = "saved result reviewed"
			e.Data.FinishReason = "stop"
		}
		if err = db.Append(ctx, int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return svc, db, task
}

func TestCodexCompactionRejectsAmbiguousRetainedArgumentsBeforeEngine(t *testing.T) {
	for name, args := range map[string]string{
		"duplicate":         `{"path":"one","path":"two"}`,
		"escaped_duplicate": `{"path":"one","\u0070ath":"two"}`,
		"nested_duplicate":  `{"nested":{"key":1,"key":2}}`,
		"deep":              `{"nested":` + strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65) + `}`,
	} {
		for _, withEngine := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/engine=%t", name, withEngine), func(t *testing.T) {
				svc, db, task := codexCompactionFixture(t, args)
				if withEngine {
					svc.contextEngine = applicationContextEngine{}
				}
				ctx := context.Background()
				before, err := db.Read(ctx, task, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) {
					t.Fatal("ambiguous retained arguments launched")
					return nil, nil
				}
				_, err = svc.Run(ctx, Request{ModelID: "brain", ContinueTaskID: task, Prompt: "continue", Compaction: &sessions.CompactionRequest{Keep: 3, Summary: sessions.Summary{Requirements: []string{"Preserve prior intent"}}}})
				if err == nil {
					t.Fatal("ambiguous retained arguments admitted")
				}
				after, err := db.Read(ctx, task, 0, 100)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("rejection changed original source", err)
				}
			})
		}
	}
}

func codexCompactionDraft(t *testing.T, svc *Service, task string) sessions.SummaryAttempt {
	t.Helper()
	p := &codexAuditProviderFixture{stream: func(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
		return emit(providers.Chunk{Text: `{"version":1,"summary":{"requirements":["Preserve approved-marker requirement"]}}`, Done: true, FinishReason: "stop"})
	}}
	svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) { return p, nil }
	a, err := svc.SummarizeTask(context.Background(), task, "brain", 3, 0)
	if err != nil || a.Draft == nil {
		t.Fatal("draft fixture", err)
	}
	return a
}

func TestCodexCompactionImportsCheckpointAndPairedHistory(t *testing.T) {
	for _, mode := range []string{"manual", "approved"} {
		t.Run(mode, func(t *testing.T) {
			svc, db, task := codexCompactionFixture(t)
			ctx := context.Background()
			before, err := db.Read(ctx, task, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			request := Request{ModelID: "brain", ContinueTaskID: task, Prompt: "fresh continuation prompt"}
			reviewID := ""
			if mode == "manual" {
				request.Compaction = &sessions.CompactionRequest{Keep: 3, Summary: sessions.Summary{Requirements: []string{"Preserve approved-marker requirement"}}}
			} else {
				a := codexCompactionDraft(t, svc, task)
				review, err := svc.ReviewSummary(ctx, a.ID, "", "approved", "Compared with original source")
				if err != nil {
					t.Fatal(err)
				}
				request.SummaryAttemptID = a.ID
				reviewID = review.ID
			}
			wire := &codexHistoryWire{}
			svc.codexLauncher = func(ctx context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
				return codexbridge.NewSession(ctx, wire, codexbridge.Options{Model: spec.Model, CWD: spec.CWD})
			}
			out, err := svc.Run(ctx, request)
			if err != nil {
				t.Fatal("compacted continuation", err)
			}
			replayed, err := sessions.Replay(ctx, db, out.TaskID)
			if err != nil || replayed.Compaction == nil || replayed.Compaction.SourceTaskID != task || replayed.Compaction.SummaryAttemptID != request.SummaryAttemptID || replayed.Compaction.SummaryReviewID != reviewID {
				t.Fatal("missing bound checkpoint", err)
			}
			after, err := db.Read(ctx, task, 0, 100)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("source mutated", err)
			}
			events, err := db.Read(ctx, out.TaskID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range events {
				if e.Kind == runtime.ToolStarted || e.Kind == runtime.ToolCompleted {
					t.Fatal("historical tool redispatched")
				}
			}
			imports, turns := 0, 0
			for _, sent := range wire.writes {
				switch sent.Method {
				case "thread/inject_items":
					imports++
					body := string(sent.Params)
					for _, marker := range []string{"Keep fixture system authority.", "session_summary", "untrusted reference data", "approved-marker", "function_call", "function_call_output", "historical-call", "package saved"} {
						if !strings.Contains(body, marker) {
							t.Fatalf("import missing %s", marker)
						}
					}
					if strings.Contains(body, "old removable") || strings.Contains(body, request.Prompt) {
						t.Fatal("incorrect compaction/import boundary")
					}
				case "turn/start":
					turns++
					if !strings.Contains(string(sent.Params), request.Prompt) || strings.Contains(string(sent.Params), "historical-call") {
						t.Fatal("history flattened into fresh turn")
					}
				}
			}
			if imports != 1 || turns != 1 || !wire.closed {
				t.Fatal(imports, turns, wire.closed)
			}
		})
	}
}

func TestCodexCompactionSummaryAdmissionDenials(t *testing.T) {
	for _, mode := range []string{"unapproved", "rejected", "wrong_source", "stale", "new_secret"} {
		t.Run(mode, func(t *testing.T) {
			svc, db, task := codexCompactionFixture(t)
			ctx := context.Background()
			a := codexCompactionDraft(t, svc, task)
			if mode != "unapproved" {
				decision := "approved"
				if mode == "rejected" {
					decision = "rejected"
				}
				review, err := svc.ReviewSummary(ctx, a.ID, "", decision, "Fixture review")
				if err != nil {
					t.Fatal(err)
				}
				if mode == "stale" {
					if _, err = svc.ReviewSummary(ctx, a.ID, review.ID, "rejected", "Revoked"); err != nil {
						t.Fatal(err)
					}
				}
			}
			req := Request{ModelID: "brain", ContinueTaskID: task, SummaryAttemptID: a.ID, Prompt: "continue"}
			if mode == "wrong_source" {
				otherWire := &codexHistoryWire{}
				svc.codexLauncher = func(ctx context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
					return codexbridge.NewSession(ctx, otherWire, codexbridge.Options{Model: spec.Model, CWD: spec.CWD})
				}
				other, err := svc.Run(ctx, Request{ModelID: "brain", Prompt: "different completed source"})
				if err != nil {
					t.Fatal(err)
				}
				req.ContinueTaskID = other.TaskID
			}
			if mode == "new_secret" {
				svc.secret = func(name string) string {
					if name == "DARWIN_API_TOKEN" {
						return "approved-marker"
					}
					return ""
				}
			}
			before, _ := db.Read(ctx, task, 0, 100)
			svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) {
				t.Fatal("denied import launched")
				return nil, nil
			}
			if _, err := svc.Run(ctx, req); err == nil {
				t.Fatal("invalid approved summary admitted")
			}
			after, err := db.Read(ctx, task, 0, 100)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("denial changed source")
			}
		})
	}
}

func TestCodexCompactionRevocationRespectsDurableStartBoundary(t *testing.T) {
	for _, phase := range []string{"launcher", "estimator"} {
		t.Run(phase, func(t *testing.T) {
			svc, db, task := codexCompactionFixture(t)
			ctx := context.Background()
			a := codexCompactionDraft(t, svc, task)
			review, err := svc.ReviewSummary(ctx, a.ID, "", "approved", "Fixture approval")
			if err != nil {
				t.Fatal(err)
			}
			revoked := false
			revoke := func() {
				if revoked {
					return
				}
				revoked = true
				if _, err := svc.ReviewSummary(ctx, a.ID, review.ID, "rejected", "Revoked during admission"); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "estimator" {
				svc.contextEstimator = auxiliaryContextEstimator(func(context.Context, providers.Request) (int, error) {
					raw, err := sql.Open("sqlite", svc.settings.Telemetry.Database)
					if err != nil {
						t.Fatal(err)
					}
					defer raw.Close()
					var body []byte
					if err = raw.QueryRow(`SELECT body FROM events WHERE task_id<>? AND json_extract(body,'$.kind')='task.started'`, task).Scan(&body); err != nil {
						t.Fatal("estimation preceded durable start", err)
					}
					var started runtime.Event
					if json.Unmarshal(body, &started) != nil || started.Data.Compaction == nil || started.Data.Compaction.SummaryReviewID != review.ID {
						t.Fatal("missing frozen approved start")
					}
					revoke()
					return 1, nil
				})
			}
			wire := &codexHistoryWire{}
			svc.codexLauncher = func(ctx context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
				if phase == "launcher" {
					revoke()
				}
				return codexbridge.NewSession(ctx, wire, codexbridge.Options{Model: spec.Model, CWD: spec.CWD})
			}
			out, err := svc.Run(ctx, Request{ModelID: "brain", ContinueTaskID: task, SummaryAttemptID: a.ID, Prompt: "continue"})
			if phase == "estimator" || phase == "launcher" {
				if err != nil || !revoked {
					t.Fatal("post-start revocation canceled admitted task", err)
				}
				replayed, err := sessions.Replay(ctx, db, out.TaskID)
				if err != nil || replayed.Compaction == nil || replayed.Compaction.SummaryReviewID != review.ID {
					t.Fatal("historical approval changed", err)
				}
				svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) {
					t.Fatal("future rejected import launched")
					return nil, nil
				}
				if _, err := svc.Run(ctx, Request{ModelID: "brain", ContinueTaskID: task, SummaryAttemptID: a.ID, Prompt: "new import"}); err == nil {
					t.Fatal("revoked summary admitted future import")
				}
				return
			}
			if err == nil || !revoked {
				t.Fatal("revocation did not prevent dispatch", err, revoked)
			}
			for _, sent := range wire.writes {
				if sent.Method == "thread/start" || sent.Method == "thread/inject_items" || sent.Method == "turn/start" {
					t.Fatal("revoked import reached protocol", sent.Method)
				}
			}
			if out.TaskID != "" {
				events, err := db.Read(ctx, out.TaskID, 0, 100)
				if err != nil || len(events) != 0 {
					t.Fatal("revoked start persisted", err)
				}
			}
		})
	}
}
