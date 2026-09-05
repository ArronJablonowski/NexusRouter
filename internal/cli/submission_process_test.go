package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"darwinrouter/submissions"
)

func TestSubmissionCommandsAcrossProcesses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dir := t.TempDir()
	binary := filepath.Join(dir, "darwin")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../../cmd/darwin")
	if body, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, body)
	}
	database, configuration := filepath.Join(dir, "tasks.db"), filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configuration, []byte(fmt.Sprintf("telemetry:\n  database: %q\n", database)), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(input string, args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Stdin = strings.NewReader(input)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatal("command failed", err, stderr.String())
		}
		if strings.Contains(string(out), "private-process-prompt") || strings.Contains(string(out), "process-request-key") {
			t.Fatal("private intake fields printed")
		}
		return out
	}
	args := []string{"submit", "--config", configuration, "--key", "process-request-key", "--model", "fixture"}
	var first, retry submissions.Status
	if json.Unmarshal(run("private-process-prompt", args...), &first) != nil || first.ID == "" || first.State != "queued" {
		t.Fatal(first)
	}
	if json.Unmarshal(run("private-process-prompt", args...), &retry) != nil || retry.ID != first.ID {
		t.Fatal(retry)
	}
	var page submissions.Page
	if json.Unmarshal(run("", "submissions", "list", "--db", database, "--state", "queued"), &page) != nil || len(page.Items) != 1 || page.Items[0].ID != first.ID {
		t.Fatal(page)
	}
	var canceled submissions.Status
	if json.Unmarshal(run("", "submissions", "cancel", "--db", database, "--id", first.ID), &canceled) != nil || canceled.State != "canceled" {
		t.Fatal(canceled)
	}
	var inspected submissions.Status
	if json.Unmarshal(run("", "submissions", "show", "--db", database, "--id", first.ID), &inspected) != nil || inspected.State != "canceled" || len(inspected.TaskIDs) != 0 {
		t.Fatal(inspected)
	}
}
