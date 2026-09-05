package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoredSummaryContinuationFlag(t *testing.T) {
	base := []string{"--config", "project.yaml", "--model", "auto"}
	_, request, err := parseRunArgs(append(base, "--continue-task", "source", "--summary-attempt", "draft"))
	if err != nil || request.SummaryAttemptID != "draft" || request.ContinueTaskID != "source" || request.Compaction != nil {
		t.Fatalf("request=%+v error=%v", request, err)
	}
	for _, extra := range [][]string{
		{"--summary-attempt", "draft"}, {"--continue-task", "source", "--summary-attempt", ""},
		{"--continue-task", "source", "--summary-attempt", "draft", "--compact-keep", "1"},
		{"--continue-task", "source", "--summary-attempt", "draft", "--compact-summary", ""},
		{"--continue-task", "source", "--summary-attempt", "draft", "--compact-keep", "1", "--compact-summary", "missing.json"},
	} {
		if _, _, err := parseRunArgs(append(append([]string{}, base...), extra...)); err == nil {
			t.Fatal("invalid summary combination accepted", extra)
		}
	}
}

func TestSummaryReviewArguments(t *testing.T) {
	base := []string{"--config", "missing.yaml", "--attempt", "draft", "--decision", "approved", "--note", "Checked against source"}
	got, err := parseSummaryReviewArgs(append(base, "--expected", "previous"))
	want := summaryReviewArgs{config: "missing.yaml", attempt: "draft", expected: "previous", decision: "approved", note: "Checked against source"}
	if err != nil || got != want {
		t.Fatalf("arguments=%+v error=%v", got, err)
	}
	for _, extra := range [][]string{{"--decision", "approve"}, {"--decision", "Approved"}, {"--attempt", ""}, {"--note", " "}, {"--config", ""}, {"secret-value"}} {
		var out, stderr bytes.Buffer
		args := append(append([]string{"summary-review"}, base...), extra...)
		if code := Run(args, &out, &stderr, "dev"); code != 2 || strings.Contains(stderr.String(), "secret-value") {
			t.Fatalf("code=%d error=%q", code, stderr.String())
		}
	}
	if _, err := parseSummaryReviewArgs([]string{"--config", "c", "--attempt", "a", "--decision", "rejected"}); err == nil {
		t.Fatal("missing note accepted")
	}
}

func TestSummaryReviewHistoryMissingDatabaseIsReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-missing.db")
	var out, stderr bytes.Buffer
	if code := Run([]string{"summary-reviews", "--db", path, "--attempt", "draft"}, &out, &stderr, "dev"); code != 1 || strings.Contains(stderr.String(), path) {
		t.Fatalf("code=%d error=%q", code, stderr.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("history created database")
	}
	if code := Run([]string{"summary-reviews", "--db", path}, &out, &stderr, "dev"); code != 2 {
		t.Fatal("missing attempt accepted")
	}
}
