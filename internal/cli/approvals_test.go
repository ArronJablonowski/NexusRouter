package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func cliApprovalFixture(t *testing.T) (string, approvals.Request) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "approvals.db")
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, kind := range []runtime.Kind{runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.ToolStarted} {
		e := runtime.Event{Version: 1, ID: string(kind), TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: int64(i + 1), Time: time.Now().UTC(), Kind: kind}
		if i > 0 {
			e.TurnID = "turn"
			e.AttemptID = "attempt"
		}
		if kind == runtime.TurnCompleted {
			e.Data.ToolCalls = []providers.ToolCall{{ID: "call", Name: "write", Arguments: json.RawMessage(`{"private":"sensitive-arguments"}`)}}
		}
		if kind == runtime.ToolStarted {
			e.Data = runtime.Data{ToolCallID: "call", ToolName: "write", Effect: runtime.UncertainEffect}
		}
		if err = db.Append(ctx, int64(i), e); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	r := approvals.Request{Version: 1, ID: "approval", TaskID: "task", TurnID: "turn", ToolCallID: "call", ToolName: "write", Scope: "workspace", ArgumentsDigest: strings.Repeat("a", 64), SchemaDigest: strings.Repeat("b", 64), PolicyDigest: strings.Repeat("c", 64), CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if _, err = db.RequestApproval(ctx, r); err != nil {
		t.Fatal(err)
	}
	return path, r
}

func TestApprovalCLIReadOnlyMetadata(t *testing.T) {
	path, request := cliApprovalFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"list", "show"} {
		args := []string{"approvals", command, "--db", path, "--task", "task"}
		if command == "show" {
			args = append(args, "--id", request.ID)
		} else {
			args = append(args, "--limit=1")
		}
		var out, errout bytes.Buffer
		if code := Run(args, &out, &errout, "test"); code != 0 || errout.Len() != 0 {
			t.Fatal(code, errout.String())
		}
		if strings.Contains(out.String(), "sensitive-arguments") {
			t.Fatal("raw arguments exposed")
		}
		if command == "show" {
			var r approvals.Record
			if json.Unmarshal(out.Bytes(), &r) != nil || r.Validate() != nil || r.Request.ID != request.ID || r.State != approvals.Pending {
				t.Fatal(out.String())
			}
		} else {
			var p approvals.Page
			if json.Unmarshal(out.Bytes(), &p) != nil || p.Validate() != nil || len(p.Records) != 1 || p.Query.Limit != 1 {
				t.Fatal(out.String())
			}
		}
	}
	var out, errout bytes.Buffer
	if code := runApprovals([]string{"list", "--db", path, "--task", "task", "--after", "call"}, &out, &errout); code != 0 {
		t.Fatal(code, errout.String())
	}
	var page approvals.Page
	if json.Unmarshal(out.Bytes(), &page) != nil || page.Validate() != nil || len(page.Records) != 0 {
		t.Fatal(out.String())
	}
	if code := runApprovals([]string{"show", "--db", path, "--task", "other", "--id", request.ID}, &bytes.Buffer{}, &errout); code != 1 {
		t.Fatal("cross-task record exposed", code)
	}
	if code := runApprovals([]string{"list", "--db", path, "--task", "task"}, steeringFailedWriter{}, &errout); code != 1 {
		t.Fatal(code)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("inspection mutated database", err)
	}
}

func TestApprovalCLIRejectsArgumentsAndMissingStorage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-missing.db")
	for _, args := range [][]string{nil, {"delete"}, {"show", "--db", path, "--task", "task"}, {"list", "--db", path, "--task", "task", "--task", "other"}, {"list", "--db", path, "--task", "task", "--limit", "0"}, {"list", "--db", path, "--task", "task", "--limit", "101"}, {"list", "--db", path, "--task", "task", "--limit", "+1"}, {"list", "--db", path, "--task", "task", "--id", "id"}, {"list", "--db", path, "--task", "task", "--after", "../bad"}} {
		var out, errout bytes.Buffer
		if code := runApprovals(args, &out, &errout); code != 2 || out.Len() != 0 || strings.Contains(errout.String(), path) {
			t.Fatal(code, out.String(), errout.String())
		}
	}
	var out, errout bytes.Buffer
	if code := runApprovals([]string{"list", "--db", path, "--task", "task"}, &out, &errout); code != 1 || out.Len() != 0 || strings.Contains(errout.String(), path) {
		t.Fatal(code, out.String(), errout.String())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created missing database", err)
	}
}
