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

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/metrics"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestMetricsStrictArgumentsAndMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-missing.db")
	for _, args := range [][]string{nil, {"--db"}, {"--db", ""}, {"--db", path, "--db", path}, {"--db", path, "--state", "secret-state"}, {"--db", path, "extra-secret"}} {
		var out, stderr bytes.Buffer
		if code := Run(append([]string{"metrics"}, args...), &out, &stderr, "dev"); code != 2 || out.Len() != 0 || strings.Contains(stderr.String(), "private-") || strings.Contains(stderr.String(), "secret") {
			t.Fatal(code, stderr.String())
		}
	}
	var out, stderr bytes.Buffer
	if code := Run([]string{"metrics", "--db", path}, &out, &stderr, "dev"); code != 1 || out.Len() != 0 || strings.Contains(stderr.String(), path) {
		t.Fatal(code, stderr.String())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("metrics created database", err)
	}
}

func TestMetricsReadsExistingReopenedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.db")
	db, err := telemetry.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Append(context.Background(), 0, runtime.Event{Version: 1, ID: "private-event", TaskID: "private-task", SessionID: "private-task", CorrelationID: "private-task", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		var out, stderr bytes.Buffer
		if code := Run([]string{"metrics", "--db", path}, &out, &stderr, "dev"); code != 0 {
			t.Fatal(code, stderr.String())
		}
		var snapshot metrics.Snapshot
		if json.Unmarshal(out.Bytes(), &snapshot) != nil || snapshot.Validate() != nil {
			t.Fatal(out.String())
		}
		found := false
		for _, group := range snapshot.Groups {
			if group.Name == "tasks" && len(group.Counts) > 0 && group.Counts[0].Value == 1 {
				found = true
			}
		}
		if !found || strings.Contains(out.String(), "private-") {
			t.Fatal("missing count or leaked identity", out.String())
		}
		if strings.Contains(out.String(), path) {
			t.Fatal("path leaked")
		}
	}
	var stderr bytes.Buffer
	if code := Run([]string{"metrics", "--db", path}, submissionBrokenOutput{}, &stderr, "dev"); code != 1 || strings.Contains(stderr.String(), "private") {
		t.Fatal(code, stderr.String())
	}
	if code := Run([]string{"metrics", "--db", path}, submissionPanicOutput{}, &stderr, "dev"); code != 1 {
		t.Fatal(code)
	}
}

func TestMetricsCanceledBeforeRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "missing.db")
	var out, stderr bytes.Buffer
	if code := runMetricsContext(ctx, path, &out, &stderr); code != 1 || out.Len() != 0 {
		t.Fatal(code, out.String())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}
