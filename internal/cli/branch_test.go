package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

func branchCLIArgs(config, key string) []string {
	return []string{"branch", "--config", config, "--key", key, "--task", "source", "--session", "session", "--sequence", "2", "--event", "source-terminal", "--model", "model"}
}

func TestBranchParserReusesSubmitAndRunOptions(t *testing.T) {
	args := branchCLIArgs("config.yaml", "fixture-branch-key")[1:]
	options, request, key, source, err := parseBranchArgs(append(args, "--capability", "chat", "--max-cost", "1", "--local-required"))
	if err != nil || options.ProjectFile != "config.yaml" || key != "fixture-branch-key" || request.ModelID != "model" || !request.LocalRequired || request.MaxCost != 1 || len(request.Capabilities) != 1 || source.TaskID != "source" || source.SessionID != "session" || source.HeadSequence != 2 || source.HeadEventID != "source-terminal" {
		t.Fatal(options, request, key, source, err)
	}
	for _, extra := range [][]string{
		{"--task", "again"}, {"--session", "bad:session"}, {"--sequence", "02"}, {"--sequence", "0"}, {"--event", "bad:event"}, {"--continue-task", "source"}, {"--summary-attempt", "private"}, {"--json"}, {"extra"},
	} {
		var out, diagnostic bytes.Buffer
		full := append(append([]string{}, branchCLIArgs("config.yaml", "fixture-branch-key")...), extra...)
		if code := RunWithInput(full, strings.NewReader("prompt"), &out, &diagnostic, "dev"); code != 2 || out.Len() != 0 || strings.Contains(diagnostic.String(), "private") {
			t.Fatal(extra, code, diagnostic.String())
		}
	}
}

func TestBranchCLIQueuesIdempotentlyWithoutMutatingSource(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	database := filepath.Join(dir, "tasks.db")
	configuration := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configuration, []byte(fmt.Sprintf("telemetry:\n  database: %q\n", database)), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	start := runtime.Event{Version: 1, ID: "source-start", TaskID: "source", SessionID: "session", CorrelationID: "source", Sequence: 1, Time: time.Unix(100, 0).UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{Messages: []providers.Message{{Role: "user", Content: "private source"}}}}
	terminal := runtime.Event{Version: 1, ID: "source-terminal", TaskID: "source", SessionID: "session", CorrelationID: "source", Sequence: 2, Time: time.Unix(101, 0).UTC(), Kind: runtime.TaskCompleted, Data: runtime.Data{Text: "private output"}}
	if err = db.Append(ctx, 0, start); err != nil {
		t.Fatal(err)
	}
	if err = db.Append(ctx, 1, terminal); err != nil {
		t.Fatal(err)
	}
	before, err := db.Read(ctx, "source", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	args := branchCLIArgs(configuration, "fixture-branch-key")
	var firstID string
	for i := 0; i < 2; i++ {
		var out, diagnostic bytes.Buffer
		if code := RunWithInput(args, strings.NewReader("private branch prompt"), &out, &diagnostic, "dev"); code != 0 {
			t.Fatal(code, diagnostic.String())
		}
		var status submissions.Status
		if json.Unmarshal(out.Bytes(), &status) != nil || status.State != "queued" || status.ID == "" || len(status.TaskIDs) != 0 {
			t.Fatal(out.String())
		}
		if i == 0 {
			firstID = status.ID
		} else if status.ID != firstID {
			t.Fatal("branch intake was not idempotent", firstID, status.ID)
		}
	}

	var out, diagnostic bytes.Buffer
	stale := branchCLIArgs(configuration, "different-branch-key")
	for i := range stale {
		if stale[i] == "source-terminal" {
			stale[i] = "private-stale-event"
		}
	}
	if code := RunWithInput(stale, strings.NewReader("private branch prompt"), &out, &diagnostic, "dev"); code != 1 || out.Len() != 0 || strings.Contains(diagnostic.String(), "private") {
		t.Fatal(code, diagnostic.String())
	}

	db, err = telemetry.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	after, err := db.Read(ctx, "source", 0, 10)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatal("branch admission mutated source", len(before), len(after), err)
	}
	page, err := db.ListSubmissions(ctx, submissions.ListOptions{Limit: 25})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != firstID {
		t.Fatal("unexpected queued branches", page, err)
	}
}

func TestBranchCLIRejectsInvalidPromptBeforeStorage(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "absent.db")
	configuration := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configuration, []byte(fmt.Sprintf("telemetry:\n  database: %q\n", database)), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	if code := RunWithInput(branchCLIArgs(configuration, "fixture-branch-key"), strings.NewReader(" \n\t"), &out, &diagnostic, "dev"); code != 1 || out.Len() != 0 {
		t.Fatal(code, out.String(), diagnostic.String())
	}
	if _, err := os.Stat(database); !os.IsNotExist(err) {
		t.Fatal("invalid prompt created storage", err)
	}
}
