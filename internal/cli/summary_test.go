package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestSummaryArguments(t *testing.T) {
	got, err := parseSummaryArgs([]string{"--config", "config.yaml", "--task", "source", "--model", "summarizer", "--keep", "3", "--max-cost", "0.25"})
	want := summaryArgs{config: "config.yaml", task: "source", model: "summarizer", keep: 3, maxCost: .25}
	if err != nil || got != want {
		t.Fatalf("arguments=%+v error=%v", got, err)
	}
	base := []string{"--config", "config.yaml", "--task", "source", "--model", "summarizer", "--keep", "1"}
	got, err = parseSummaryArgs(base)
	if err != nil || got.maxCost != 0 {
		t.Fatal("zero-cost default lost", err)
	}
	keyed, err := parseSummaryArgs(append(base, "--idempotency-key", "summary-preparation-key-0001"))
	if err != nil || keyed.idempotencyKey != "summary-preparation-key-0001" {
		t.Fatal("idempotent preparation key rejected", err)
	}
	for _, flags := range [][]string{{"--keep", "0"}, {"--keep", "-1"}, {"--keep", "100001"}, {"--keep", "1.5"}, {"--max-cost", "NaN"}, {"--max-cost", "Inf"}, {"--max-cost", "-1"}, {"--max-cost", "1e999"}, {"--task", ""}, {"--model", ""}, {"--config", ""}, {"secret-value"}} {
		var out, stderr bytes.Buffer
		code := RunWithInput(append(append([]string{"summary"}, base...), flags...), strings.NewReader(""), &out, &stderr, "dev")
		if code != 2 || out.Len() != 0 || strings.Contains(stderr.String(), "secret-value") {
			t.Fatalf("code=%d error=%q", code, stderr.String())
		}
	}
	for _, key := range []string{"short", "summary key with spaces", strings.Repeat("x", 129)} {
		if _, err := parseSummaryArgs(append(base, "--idempotency-key", key)); err == nil {
			t.Fatal("malformed preparation key accepted")
		}
	}
	if _, err := parseSummaryArgs(append(base, "--idempotency-key", "summary-preparation-key-0001", "--idempotency-key", "summary-preparation-key-0001")); err == nil {
		t.Fatal("duplicate preparation key flag accepted")
	}
	if _, err := parseSummaryArgs([]string{"--config", "c", "--task", "t", "--model", "m"}); err == nil {
		t.Fatal("missing keep accepted")
	}
}

func TestSummaryInspectionArgumentsAndMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-missing.db")
	for _, args := range [][]string{{"summaries", "list", "--db", path}, {"summaries", "show", "--db", path, "--id", "attempt"}} {
		var out, stderr bytes.Buffer
		if code := Run(args, &out, &stderr, "dev"); code != 1 || strings.Contains(stderr.String(), path) {
			t.Fatalf("code=%d error=%q", code, stderr.String())
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("inspection created database")
		}
	}
	for _, args := range [][]string{
		{}, {"other"}, {"list"}, {"show", "--db", path}, {"list", "--db", path, "--id", "attempt"},
		{"list", "--db", path, "--limit", "0"}, {"list", "--db", path, "--limit", "101"},
		{"show", "--db", path, "--id", "attempt", "--task", "source"},
		{"show", "--db", path, "--id", "attempt", "--after", "previous"},
		{"show", "--db", path, "--id", "attempt", "--limit", "1"},
	} {
		var out, stderr bytes.Buffer
		if code := runSummaries(args, &out, &stderr); code != 2 {
			t.Fatalf("args=%v code=%d", args, code)
		}
	}
}

func TestSummaryHelpDescribesDraftOnly(t *testing.T) {
	var out, stderr bytes.Buffer
	if Run([]string{"help"}, &out, &stderr, "dev") != 0 || !strings.Contains(out.String(), "does not activate compaction") {
		t.Fatal("missing draft-only help")
	}
}

func TestSummaryInspectionEmptyStoreAndOutputFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summaries.db")
	db, err := telemetry.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	args := []string{"summaries", "list", "--db", path, "--task", "source"}
	if code := Run(args, &out, &stderr, "dev"); code != 0 {
		t.Fatalf("code=%d error=%q", code, stderr.String())
	}
	var attempts []sessions.SummaryAttempt
	if err := json.Unmarshal(out.Bytes(), &attempts); err != nil || len(attempts) != 0 {
		t.Fatalf("unexpected empty list %q: %v", out.String(), err)
	}
	if code := Run(args, brokenWriter{}, &stderr, "dev"); code != 1 {
		t.Fatal("output failure ignored", code)
	}
}
