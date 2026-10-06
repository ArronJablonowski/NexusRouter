package processaudit

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLifecycleAndFailures(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Enable(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Lock(); state.directory = ""; state.commands = nil; state.Unlock() })
	c := exec.Command("sh", "-c", "exit 7")
	if err := Run(c); err == nil {
		t.Fatal("exit status lost")
	} else {
		var e *exec.ExitError
		if !errors.As(err, &e) || e.ExitCode() != 7 {
			t.Fatal(err)
		}
	}
	if err := Run(exec.Command(filepath.Join(dir, "missing"))); err == nil {
		t.Fatal("missing executable succeeded")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, e := Output(exec.Command("printf", "safe"))
			if e != nil || string(b) != "safe" {
				t.Errorf("output changed: %v", e)
			}
		}()
	}
	wg.Wait()
	files, _ := filepath.Glob(filepath.Join(dir, "process-*.jsonl"))
	if len(files) != 1 {
		t.Fatal(files)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "exit 7") {
		t.Fatal("inline script leaked")
	}
	events := map[string][]record{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var r record
		if json.Unmarshal([]byte(line), &r) != nil {
			t.Fatal("invalid json")
		}
		events[r.LaunchID] = append(events[r.LaunchID], r)
	}
	if len(events) != 10 {
		t.Fatal(len(events))
	}
	for _, rs := range events {
		if rs[0].Event != "launch_requested" {
			t.Fatal("missing intent")
		}
		last := rs[len(rs)-1]
		if last.Event != "exited" && last.Event != "launch_failed" {
			t.Fatal("missing terminal")
		}
		if last.Event == "exited" && (last.PID == 0 || last.ExitCode == nil) {
			t.Fatal("missing exit metadata")
		}
	}
	if err := os.Remove(files[0]); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("untouched"), 0600)
	os.Symlink(outside, files[0])
	marker := filepath.Join(dir, "must-not-exist")
	if !errors.Is(Run(exec.Command("touch", marker)), ErrAudit) {
		t.Fatal("audit failure did not prevent launch")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("launched despite audit failure")
	}
}
func TestRedaction(t *testing.T) {
	args := []string{"tool", "--api-key", "private1", "--password=private2", "https://user:private3@example.com/path?token=private4", "--prompt", "private5", "-H", "Authorization: private6", "--model", "model-a"}
	out := strings.Join(Redact(args), " ")
	if strings.Contains(out, "private") {
		t.Fatal("credential leaked")
	}
	if !strings.Contains(out, "model-a") {
		t.Fatal("ordinary argument lost")
	}
}
