package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

// Shared rejection assertion for tests whose primary concern is isolation or
// admission, not the particular diagnostic reason. Unknown/output fields fail.
func validDelegateRejection(content string) bool {
	content = strings.TrimPrefix(content, "Tool execution failed.\n")
	if content == `{"error":"delegate_unavailable_or_rejected"}` {
		return true
	}
	var report delegateFailure
	d := json.NewDecoder(bytes.NewBufferString(content))
	d.DisallowUnknownFields()
	return len(content) <= 2048 && json.Valid([]byte(content)) && d.Decode(&report) == nil && report.Version == 1 && report.Error == "delegate_unavailable_or_rejected" && report.Reason != "" && report.WorkID != "" && len(report.Evidence) > 0 && len(report.Evidence) <= 2
}

func TestDelegateRejectionUsesOnlyAttributedDurableMetadata(t *testing.T) {
	for _, mode := range []string{"invalid", "canceled", "work_only", "completed_execution", "completed_work", "wrong_parent", "wrong_session", "wrong_child", "missing_child", "missing_work", "unknown_code", "unknown_work_code", "running_work", "running_execution"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			db, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "rejection.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			seed := func(id, parent, session string, kind runtime.Kind, code string) {
				start := runtime.Event{Version: 1, ID: id + "-start", TaskID: id, SessionID: session, CorrelationID: id, Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{ParentTaskID: parent, Messages: nil}}
				if err := db.Append(ctx, 0, start); err != nil {
					t.Fatal(err)
				}
				if kind == "" {
					return
				}
				end := start
				end.ID = id + "-end"
				end.Sequence = 2
				end.Kind = kind
				end.Data = runtime.Data{Code: code, Text: "PRIVATE_FAILURE_PAYLOAD"}
				if err := db.Append(ctx, 1, end); err != nil {
					t.Fatal(err)
				}
			}
			workKind, childKind := runtime.TaskFailed, runtime.TaskFailed
			workCode := "worker_failed"
			childCode := "invalid_output"
			if mode == "canceled" {
				workKind = runtime.TaskCanceled
				childKind = runtime.TaskCanceled
				childCode = "canceled"
			}
			if mode == "running_work" {
				workKind = ""
			}
			if mode == "running_execution" {
				childKind = ""
			}
			if mode == "completed_execution" {
				childKind = runtime.TaskCompleted
				childCode = ""
			}
			if mode == "completed_work" {
				workKind = runtime.TaskCompleted
				workCode = ""
			}
			if mode == "unknown_work_code" {
				workCode = "PRIVATE_FAILURE_PAYLOAD"
			}
			if mode == "unknown_code" {
				childCode = "PRIVATE_FAILURE_PAYLOAD"
			}
			if mode != "missing_work" {
				seed("work", "parent", "session", workKind, workCode)
			}
			childParent := "work"
			if mode == "wrong_child" {
				childParent = "other-work"
			}
			if mode != "missing_child" {
				seed("execution", childParent, "child-session", childKind, childCode)
			}
			parent, session, execution := "parent", "session", "execution"
			if mode == "wrong_parent" {
				parent = "other-parent"
			}
			if mode == "wrong_session" {
				session = "other-session"
			}
			if mode == "work_only" {
				execution = ""
			}
			out := delegateRejection(ctx, db, parent, session, "work", execution)
			wantEffect := runtime.NoEffect
			if mode == "wrong_child" || mode == "missing_child" {
				wantEffect = runtime.UncertainEffect
			}
			if out.Effect != wantEffect || !out.Failed || out.Recoverable != (wantEffect == runtime.NoEffect) || !validDelegateRejection(out.Content) || strings.Contains(out.Content, "PRIVATE_FAILURE_PAYLOAD") {
				t.Fatal("unsafe rejection", out)
			}
			if mode != "invalid" && mode != "canceled" && mode != "work_only" && mode != "completed_execution" {
				if out.Content != `{"error":"delegate_unavailable_or_rejected"}` {
					t.Fatal("unverified metadata escaped", out.Content)
				}
				return
			}
			var report delegateFailure
			if json.Unmarshal([]byte(out.Content), &report) != nil || report.WorkID != "work" || report.ExecutionID != execution {
				t.Fatal("missing attribution", out.Content)
			}
			want := "invalid_output"
			count := 2
			if mode == "canceled" {
				want = "canceled"
			}
			if mode == "work_only" || mode == "completed_execution" {
				want = "worker_failed"
				count = 1
			}
			if report.Reason != want || len(report.Evidence) != count {
				t.Fatal("wrong diagnostic", report)
			}
			for i, e := range report.Evidence {
				id := "work"
				if i == 1 {
					id = "execution"
				}
				page, err := db.ReadEventPage(ctx, id, 1, 1)
				if err != nil || e.TaskID != id || e.Sequence != 2 || e.Kind != page.Events[0].Kind || e.Code != page.Events[0].Data.Code {
					t.Fatal(fmt.Sprintf("unbound evidence %d", i), err)
				}
			}
		})
	}
}
