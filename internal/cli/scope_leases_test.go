package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func TestScopeLeasesCLIReadOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "leases.db")
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	event := runtime.Event{Version: 1, ID: "start", TaskID: "holder", SessionID: "session", CorrelationID: "holder", Sequence: 1, Time: time.Now().UTC(), Kind: runtime.TaskStarted}
	if err := db.Append(ctx, 0, event); err != nil {
		t.Fatal(err)
	}
	lease, err := db.AcquireLease(ctx, "holder", "private-owner", "create_private-alias", false, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DARWIN_MODE", "invalid-private-config")
	var out, errout bytes.Buffer
	args := []string{"resources", "leases", "--db", path, "--scope=workspace"}
	if code := Run(args, &out, &errout, "dev"); code != 0 {
		t.Fatal(code, errout.String())
	}
	var status workers.ScopeLeaseStatus
	if json.Unmarshal(out.Bytes(), &status) != nil || status.Validate() != nil || status.Scope != "workspace" || !status.Available || len(status.Holders) != 1 || status.Holders[0].TaskID != "holder" || status.Holders[0].LiveReaders != 1 {
		t.Fatal(out.String())
	}
	for _, forbidden := range []string{"private-", lease.Token, "owner", "process_id", path} {
		if strings.Contains(out.String(), forbidden) {
			t.Fatalf("exposed %s", forbidden)
		}
	}
	if errout.Len() != 0 {
		t.Fatal(errout.String())
	}
	if Run(args, brokenWriter{}, &errout, "dev") != 1 {
		t.Fatal("ignored output error")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	out.Reset()
	if runScopeLeasesContext(canceled, args[2:], &out, &errout) != 1 || out.Len() != 0 {
		t.Fatal("canceled request returned output")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("inspection changed database", err)
	}
}

func TestScopeLeasesCLIRejectsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	for _, flags := range [][]string{
		{"--db", path}, {"--db", path, "--scope", ""},
		{"--db", path, "--scope", "workspace", "--scope", "other"},
		{"--db", path, "--scope", "workspace", "--db", path},
		{"--db", path, "--scope", "workspace", "--extra", "private-value"},
		{"--db", path, "--scope", "workspace", "private-value"},
		{"--db", path, "--scope", "private\nvalue"},
		{"--db", path, "--scope", strings.Repeat("a", 513)},
		{"--db", "--scope", "workspace"},
	} {
		var out, errout bytes.Buffer
		if Run(append([]string{"resources", "leases"}, flags...), &out, &errout, "dev") != 2 || out.Len() != 0 || strings.Contains(errout.String(), "private") || strings.Contains(errout.String(), path) {
			t.Fatal("invalid arguments accepted or echoed")
		}
	}
	var out, errout bytes.Buffer
	if Run([]string{"resources", "leases", "--db=" + path, "--scope=workspace"}, &out, &errout, "dev") != 1 || out.Len() != 0 || strings.Contains(errout.String(), path) {
		t.Fatal("missing storage accepted or echoed")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("created storage", err)
	}
}
