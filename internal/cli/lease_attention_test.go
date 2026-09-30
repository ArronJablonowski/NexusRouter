package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/stateschema"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/workers"
)

func TestLeaseAttentionCLIReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attention.db")
	db, err := telemetry.Open(context.Background(), path)
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
	t.Setenv("DARWIN_PROCESS_OWNER_DIR", "") // Inspection must never resolve/probe ownership.
	args := []string{"resources", "attention", "--db", path}
	var out, errout bytes.Buffer
	if code := Run(args, &out, &errout, "dev"); code != 0 {
		t.Fatal(code, errout.String())
	}
	var page workers.LeaseAttentionPage
	if json.Unmarshal(out.Bytes(), &page) != nil || page.Version != 1 || !page.Available || page.StorageSchema != stateschema.Current || page.Items == nil || len(page.Items) != 0 || page.HasMore {
		t.Fatal(out.String())
	}
	if errout.Len() != 0 || strings.Contains(out.String(), "private") || strings.Contains(out.String(), path) {
		t.Fatal("inspection leaked private input")
	}
	if Run(args, brokenWriter{}, &errout, "dev") != 1 {
		t.Fatal("ignored output failure")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	out.Reset()
	if runLeaseAttentionContext(canceled, args[2:], &out, &errout) != 1 || out.Len() != 0 {
		t.Fatal("canceled inspection returned output")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("inspection mutated storage", err)
	}
}

func TestLeaseAttentionCLIFlags(t *testing.T) {
	for _, args := range [][]string{{"--db", "path"}, {"--db=path", "--state=resolved", "--after=opaque-id", "--limit=100"}} {
		path, options, ok := leaseAttentionFlags(args)
		if !ok || path != "path" || options.Validate() != nil {
			t.Fatal("valid flags rejected")
		}
		if len(args) == 2 && (options.State != "open" || options.Limit != 25) {
			t.Fatal("defaults changed")
		}
	}
	path := filepath.Join(t.TempDir(), "missing.db")
	for _, extra := range [][]string{{"--db", path}, {"--state="}, {"--state", "private-invalid"}, {"--after="}, {"--after", "private:cursor"}, {"--limit", "0"}, {"--limit", "101"}, {"--limit", "+1"}, {"--limit", "01"}, {"--limit", "1", "--limit", "2"}, {"--state", "open", "--state", "resolved"}, {"--after", "first", "--after", "second"}, {"--unknown", "private-value"}, {"private-tail"}} {
		var out, errout bytes.Buffer
		args := append([]string{"resources", "attention", "--db", path}, extra...)
		if Run(args, &out, &errout, "dev") != 2 || out.Len() != 0 || strings.Contains(errout.String(), "private") || strings.Contains(errout.String(), path) {
			t.Fatal("invalid flags accepted or echoed", extra)
		}
	}
	var out, errout bytes.Buffer
	if Run([]string{"resources", "attention", "--db", path}, &out, &errout, "dev") != 1 || out.Len() != 0 || strings.Contains(errout.String(), path) {
		t.Fatal("missing database accepted or echoed")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("created database", err)
	}
}
