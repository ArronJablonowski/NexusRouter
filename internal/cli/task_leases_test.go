package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func TestTaskLeasesMetadataOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "task.db")
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	event := runtime.Event{Version: 1, ID: "start", TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted}
	event.Data.Messages = []providers.Message{{Role: "user", Content: "private-prompt-marker"}}
	if err := db.Append(ctx, 0, event); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// No config/model input belongs to this inspection path.
	t.Setenv("DARWIN_MODE", "invalid-private-config-marker")
	var out, errout bytes.Buffer
	args := []string{"task", "leases", "--db", path, "--task", "task"}
	if code := Run(args, &out, &errout, "dev"); code != 0 {
		t.Fatal(code, errout.String())
	}
	var status workers.TaskLeaseStatus
	if json.Unmarshal(out.Bytes(), &status) != nil || status.Version != 1 || status.TaskID != "task" || status.Sequence != 1 || status.TaskState != "running" || status.Leases == nil || status.Recoveries == nil || status.ObservedAt.IsZero() {
		t.Fatal("wrong lease status", out.String())
	}
	if strings.Contains(out.String(), "private-") || errout.Len() != 0 {
		t.Fatal("inspection exposed private content")
	}
	if Run(args, brokenWriter{}, &errout, "dev") != 1 {
		t.Fatal("ignored output failure")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("inspection changed database", err)
	}
	ro, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	events, err := ro.Read(ctx, "task", 0, 10)
	if err != nil || !reflect.DeepEqual(events, []runtime.Event{event}) {
		t.Fatal("inspection changed journal", err)
	}
}

func TestTaskLeasesRejectsInvalidRequestsWithoutStorage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	for _, flags := range [][]string{{"--db", path}, {"--db", path, "--task", "task", "--task", "other"}, {"--db", path, "--task", "bad:id"}, {"--db", path, "--task", "task", "--extra", "private-value"}, {"--db", path, "--task", "task", "trailing-private-value"}, {"--db=", "--task=task"}} {
		var out, errout bytes.Buffer
		if Run(append([]string{"task", "leases"}, flags...), &out, &errout, "dev") != 2 || out.Len() != 0 || strings.Contains(errout.String(), "private-value") {
			t.Fatal("invalid arguments accepted")
		}
	}
	var out, errout bytes.Buffer
	if Run([]string{"task", "leases", "--db=" + path, "--task=task"}, &out, &errout, "dev") != 1 || out.Len() != 0 || strings.Contains(errout.String(), path) {
		t.Fatal("missing history accepted or private path exposed")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("inspection created storage", err)
	}
}
