package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"darwinrouter/sessions"
)

func compactSummaryFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private-summary.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunCompactionFlagsForwardSummary(t *testing.T) {
	path := compactSummaryFile(t, `{"decisions":["use Go"],"pending_work":["test"],"failures":["prior check failed"],"artifacts":["main.go"]}`)
	_, got, err := parseRunArgs([]string{"--config", "project.yaml", "--model", "auto", "--continue-task", "previous", "--compact-keep", "3", "--compact-summary", path})
	want := &sessions.CompactionRequest{Keep: 3, Summary: sessions.Summary{Decisions: []string{"use Go"}, PendingWork: []string{"test"}, Failures: []string{"prior check failed"}, Artifacts: []string{"main.go"}}}
	if err != nil || !reflect.DeepEqual(got.Compaction, want) || got.ContinueTaskID != "previous" {
		t.Fatalf("compaction=%#v error=%v", got.Compaction, err)
	}
	_, got, err = parseRunArgs([]string{"--config", "project.yaml", "--model", "auto", "--continue-task", "previous"})
	if err != nil || got.Compaction != nil {
		t.Fatal("compaction must be opt-in", err)
	}
}

func TestRunCompactionFlagCombinations(t *testing.T) {
	path := compactSummaryFile(t, `{"decisions":["use Go"]}`)
	for _, flags := range [][]string{
		{"--compact-keep", "1", "--compact-summary", path},
		{"--continue-task", "previous", "--compact-keep", "1"},
		{"--continue-task", "previous", "--compact-summary", path},
		{"--continue-task", "previous", "--compact-keep", "0", "--compact-summary", path},
		{"--continue-task", "previous", "--compact-keep", "-1", "--compact-summary", path},
		{"--continue-task", "previous", "--compact-keep", "1.5", "--compact-summary", path},
		{"--continue-task", "previous", "--compact-keep", "999999999999999999999999", "--compact-summary", path},
		{"--continue-task", "previous", "--compact-keep", "1", "--compact-summary", ""},
	} {
		var stdout, stderr bytes.Buffer
		code := runTask(append([]string{"--config", "missing.yaml", "--model", "auto"}, flags...), &routingUnreadablePrompt{}, &stdout, &stderr)
		if code != 2 || strings.Contains(stderr.String(), path) {
			t.Fatalf("code=%d error=%q", code, stderr.String())
		}
	}
}

func TestCompactionSummaryRejectsInvalidFiles(t *testing.T) {
	for _, body := range []string{
		`{"decisions":["secret-value"],"unknown":[]}`, `{"Decisions":[]}`, `{"decisions":[],"decisions":[]}`,
		`{"decisions":null}`, `{"decisions":"secret-value"}`, `{"decisions":[null]}`, `{"decisions":[1]}`,
		`{"decisions":[" "]}`, `{"decisions":[]} {}`, `[]`, `null`, ``,
		"{\"decisions\":[\"\xff\"]}", strings.Repeat(" ", (64<<10)+1),
	} {
		path := compactSummaryFile(t, body)
		_, _, err := parseRunArgs([]string{"--config", "missing.yaml", "--model", "auto", "--continue-task", "previous", "--compact-keep", "1", "--compact-summary", path})
		if err == nil || strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("invalid file accepted or leaked: %v", err)
		}
	}
	for _, path := range []string{t.TempDir(), filepath.Join(t.TempDir(), "missing.json")} {
		if _, err := readCompactionSummary(path); err == nil || strings.Contains(err.Error(), path) {
			t.Fatalf("non-file accepted or leaked: %v", err)
		}
	}
	fifo := filepath.Join(t.TempDir(), "summary.fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCompactionSummary(fifo); err == nil {
		t.Fatal("FIFO accepted")
	}
}

func TestCompactionSummaryExactSizeLimit(t *testing.T) {
	body := `{"decisions":["use Go"]}`
	body += strings.Repeat(" ", (64<<10)-len(body))
	if _, err := readCompactionSummary(compactSummaryFile(t, body)); err != nil {
		t.Fatal("exactly 64 KiB rejected", err)
	}
}
