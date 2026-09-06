package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/memory"
)

func TestConfiguredMemoryProcessChild(t *testing.T) {
	if os.Getenv("DARWIN_TEST_MEMORY_PROCESS") != "1" {
		t.Skip("owned subprocess helper")
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Exit(RunWithInput(os.Args[i+1:], os.Stdin, os.Stdout, os.Stderr, "test"))
		}
	}
	os.Exit(2)
}

func TestConfiguredMemoryCLIProcessRoundTrip(t *testing.T) {
	const secret = "owned-process-synthetic-credential"
	file, path := configuredMemoryFixture(t, true)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run := func(input []byte, action string, flags ...string) (string, string, error) {
		t.Helper()
		args := []string{"-test.run=^TestConfiguredMemoryProcessChild$", "--", "memory", action, "--config", file}
		args = append(args, flags...)
		cmd := exec.CommandContext(ctx, executable, args...)
		// Only synthetic credentials and the owned temp directory reach this
		// child; host DARWIN_* overrides cannot redirect its database or scope.
		cmd.Env = []string{"DARWIN_TEST_MEMORY_PROCESS=1", "DARWIN_API_TOKEN=" + secret, "HOME=" + t.TempDir()}
		cmd.Stdin = bytes.NewReader(input)
		var out, errout bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errout
		err := cmd.Run() // joins the exact owned child, including cancellation.
		if strings.Contains(out.String()+errout.String(), secret) {
			t.Fatal("credential escaped subprocess")
		}
		return out.String(), errout.String(), err
	}
	f := configuredMemoryFact("process-fact")
	f.Content = "preference " + secret
	f.Provenance = "operator " + secret
	body, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if out, errout, err := run(body, "put", "--expected", "0"); err != nil || !strings.Contains(out, `"revision": 1`) || errout != "" {
		t.Fatal("process put failed", err, errout)
	}
	stored := configuredStoredFact(t, path, f.ID)
	if stored.Content != "preference [REDACTED]" || stored.Provenance != "operator [REDACTED]" {
		t.Fatal("raw secret persisted")
	}
	out, errout, err := run(nil, "show", "--id", f.ID)
	var got memory.Fact
	if err != nil || errout != "" || json.Unmarshal([]byte(out), &got) != nil || got != stored {
		t.Fatal("process inspection mismatch", err)
	}
	if out, _, err := run(nil, "delete", "--id", f.ID, "--expected", "2"); err == nil || out != "" {
		t.Fatal("stale process mutation accepted")
	}
	if current := configuredStoredFact(t, path, f.ID); current != stored {
		t.Fatal("stale mutation changed fact")
	}
	if _, errout, err := run(nil, "delete", "--id", f.ID, "--expected", "1"); err != nil || errout != "" {
		t.Fatal("process deletion failed", err)
	}
	if out, _, err := run(nil, "show", "--id", f.ID); err == nil || out != "" {
		t.Fatal("deleted process fact returned")
	}
	db, err := telemetry.OpenMemoryControl(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.GetMemory(ctx, f.Scope, f.ID); !errors.Is(err, memory.ErrConflict) {
		t.Fatal("subprocess deletion not proven by storage", err)
	}
	if err := db.PutMemory(ctx, stored, 0); !errors.Is(err, memory.ErrConflict) {
		t.Fatal("subprocess deletion did not retire the identity", err)
	}
}
