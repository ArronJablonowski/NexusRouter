package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/diagnostics"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func logsFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	database := filepath.Join(root, "state.db")
	db, err := telemetry.Open(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	event := runtime.Event{Version: 1, ID: "log-event", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted,
		Data: runtime.Data{ModelID: "actual:model", ProviderID: "local", ContextTokens: 4096, Messages: []providers.Message{{Role: "user", Content: "private prompt with current-credential"}}}}
	if err = db.Append(context.Background(), 0, event); err != nil {
		t.Fatal(err)
	}
	db.Close()
	path := filepath.Join(root, "config.yaml")
	if err = os.WriteFile(path, []byte(fmt.Sprintf("version: 1\ntelemetry:\n  database: %s\nsecurity:\n  redact_env: [LOG_TEST_SECRET]\n", database)), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLogsCLIContentOptInAndCredentialRedaction(t *testing.T) {
	path := logsFixture(t)
	t.Setenv("LOG_TEST_SECRET", "current-credential")
	for _, include := range []bool{false, true} {
		args := []string{"logs", "--config", path, "--task", "task", "--include-content=" + fmt.Sprint(include)}
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr, "test"); code != 0 || stderr.Len() != 0 {
			t.Fatal(code, stderr.String())
		}
		text := stdout.String()
		if strings.Contains(text, "current-credential") || strings.Contains(text, "private prompt") != include || !strings.Contains(text, `"next_after":1`) || !strings.Contains(text, `"context_tokens":4096`) {
			t.Fatal("invalid log output", text)
		}
	}
	if code := Run([]string{"logs", "--config", path}, diagnosticShortWriter{}, &bytes.Buffer{}, "test"); code != 1 {
		t.Fatal("short write accepted", code)
	}
}

func TestLogsCLIInvalidArgumentsAndMissingDatabaseDoNotLeak(t *testing.T) {
	for _, args := range [][]string{{}, {"--config", "private-path", "--config", "second"}, {"--config", "private-path", "--limit", "101"}, {"--config", "private-path", "--follow=private-value"}, {"--config", "private-path", "--after", "-1"}} {
		var stdout, stderr bytes.Buffer
		if code := runLogs(args, &stdout, &stderr); code != 2 || stdout.Len() != 0 || strings.Contains(stderr.String(), "private-") {
			t.Fatal(code, stdout.String(), stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := runLogs([]string{"--config", filepath.Join(t.TempDir(), "absent")}, &stdout, &stderr); code != 1 || stdout.Len() != 0 {
		t.Fatal(code, stdout.String(), stderr.String())
	}
}

func TestLogsFollowStopsOnCancellation(t *testing.T) {
	path := logsFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stdout := cancelCheckpointWriter{cancel: cancel}
	var stderr bytes.Buffer
	if code := runLogsContext(ctx, path, diagnostics.Options{Limit: 100}, true, true, &stdout, &stderr); code != 0 || ctx.Err() != context.Canceled || !strings.Contains(stdout.String(), "diagnostic.checkpoint") || strings.Contains(stdout.String(), "log-event") {
		t.Fatal(code, stdout.String(), stderr.String())
	}
}

func TestLogsCancellationBeforeStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, follow := range []bool{false, true} {
		var stdout, stderr bytes.Buffer
		code := runLogsContext(ctx, "unused", diagnostics.Options{Limit: 100}, follow, follow, &stdout, &stderr)
		if follow && (code != 0 || stderr.Len() != 0) || !follow && code != 1 || stdout.Len() != 0 {
			t.Fatal(follow, code, stdout.String(), stderr.String())
		}
	}
}

type cancelCheckpointWriter struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelCheckpointWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	if bytes.Contains(p, []byte(`"kind":"diagnostic.checkpoint"`)) {
		w.cancel()
	}
	return n, err
}
