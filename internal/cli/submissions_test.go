package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"darwinrouter/submissions"
)

func TestSubmitParserReusesRunOptions(t *testing.T) {
	base := []string{"--config", "config.yaml", "--key", "0123456789abcdef", "--model", "auto"}
	options, request, key, err := parseSubmitArgs(append(append([]string{}, base...), "--capability", "chat", "--max-cost", "1", "--local-required"))
	if err != nil || options.ProjectFile != "config.yaml" || request.ModelID != "auto" || !request.LocalRequired || request.MaxCost != 1 || key != "0123456789abcdef" {
		t.Fatal(options, request, key, err)
	}
	for _, extra := range [][]string{{"--json"}, {"--json=false"}, {"--key", "private-key-value"}, {"--unexpected", "private-value"}, {"extra"}} {
		var out, stderr bytes.Buffer
		args := append(append([]string{"submit"}, base...), extra...)
		if code := RunWithInput(args, strings.NewReader("hello"), &out, &stderr, "dev"); code != 2 || out.Len() != 0 || strings.Contains(stderr.String(), "private") {
			t.Fatal(code, stderr.String())
		}
	}
}

func TestSubmissionsMissingDatabaseAndStrictControlFlags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-missing.db")
	for _, verb := range []string{"list", "show", "cancel"} {
		args := []string{"submissions", verb, "--db", path}
		if verb != "list" {
			args = append(args, "--id", "missing")
		}
		var out, stderr bytes.Buffer
		if code := Run(args, &out, &stderr, "dev"); code != 1 || out.Len() != 0 || strings.Contains(stderr.String(), path) {
			t.Fatal(code, stderr.String())
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"list", "--id", "x"}, {"show", "--id", "x", "--after", ""}, {"cancel", "--id", "x", "--limit", "25"}, {"list", "--limit", "101"}, {"list", "--state", "private-unknown"}, {"list", "--after", "private-invalid-cursor"}, {"show", "--id", "x", "--id", "y"},
	} {
		var out, stderr bytes.Buffer
		full := append([]string{"submissions"}, args...)
		full = append(full, "--db", path)
		if code := Run(full, &out, &stderr, "dev"); code != 2 || out.Len() != 0 || strings.Contains(stderr.String(), "private-") {
			t.Fatal(code, stderr.String())
		}
	}
}

type submissionBrokenOutput struct{}

func (submissionBrokenOutput) Write([]byte) (int, error) {
	return 0, errors.New("private-output-detail")
}

type submissionPanicOutput struct{}

func (submissionPanicOutput) Write([]byte) (int, error) { panic("private writer panic") }

func TestSubmissionOutputPanicIsGenericFailure(t *testing.T) {
	if code := writeSubmissionJSON(context.Background(), submissionPanicOutput{}, map[string]string{"state": "queued"}); code != 1 {
		t.Fatal(code)
	}
}

func TestSubmitAndControlIntakeOnly(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "tasks.db")
	configuration := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configuration, []byte(fmt.Sprintf("telemetry:\n  database: %q\n", database)), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"submit", "--config", configuration, "--key", "0123456789abcdef", "--model", "fixture"}
	var firstID string
	for i := 0; i < 2; i++ {
		var out, stderr bytes.Buffer
		if code := RunWithInput(args, strings.NewReader("hello"), &out, &stderr, "dev"); code != 0 {
			t.Fatal(code, stderr.String())
		}
		var status submissions.Status
		if json.Unmarshal(out.Bytes(), &status) != nil || status.State != "queued" || len(status.TaskIDs) != 0 {
			t.Fatal(out.String())
		}
		if i == 0 {
			firstID = status.ID
		} else if status.ID != firstID {
			t.Fatal("non-idempotent intake")
		}
	}
	for _, verb := range []string{"show", "cancel"} {
		var out, stderr bytes.Buffer
		if code := Run([]string{"submissions", verb, "--db", database, "--id", firstID}, &out, &stderr, "dev"); code != 0 {
			t.Fatal(code, stderr.String())
		}
		var status submissions.Status
		if json.Unmarshal(out.Bytes(), &status) != nil || status.ID != firstID || verb == "cancel" && status.State != "canceled" {
			t.Fatal(out.String())
		}
	}
	var out, stderr bytes.Buffer
	if code := Run([]string{"submissions", "list", "--db", database}, &out, &stderr, "dev"); code != 0 || !strings.Contains(out.String(), firstID) {
		t.Fatal(code, out.String(), stderr.String())
	}
	if code := Run([]string{"submissions", "show", "--db", database, "--id", firstID}, submissionBrokenOutput{}, &stderr, "dev"); code != 1 || strings.Contains(stderr.String(), "private-output-detail") {
		t.Fatal(code, stderr.String())
	}
	if code := RunWithInput(args, strings.NewReader("hello"), submissionBrokenOutput{}, &stderr, "dev"); code != 1 {
		t.Fatal(code)
	}
}
