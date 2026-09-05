package codexbridge

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Explicitly authorized diagnostic only; never starts a model turn. Default CI
// does not touch Codex or its login. Startup may refresh auth or write CLI state.
func TestLiveCodexCheckedLauncher(t *testing.T) {
	if os.Getenv("DARWIN_CODEX_LIVE_PROBE") != "1" {
		t.Skip("explicit no-inference launcher probe only")
	}
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("Codex executable unavailable")
	}
	env := []string{}
	for _, key := range []string{"HOME", "PATH", "TMPDIR", "CODEX_HOME"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := LaunchChecked(ctx, LaunchSpec{Executable: bin, CWD: t.TempDir(), Model: "gpt-5.6-sol", Mode: "hybrid", Privacy: "cloud_allowed", Env: env})
	if err != nil {
		t.Fatal("checked launcher failed")
	}
	defer s.Close()
	// The shorter internal setup context has ended. The returned task-owned
	// process must remain alive and must not redo initialization on recheck.
	if s.Prepare(ctx) != nil {
		t.Fatal("returned session could not recheck")
	}
	if s.started || s.thread != "" || s.turn != "" {
		t.Fatal("launcher submitted task traffic")
	}
	t.Log("task-owned checked launcher and recheck passed; no thread/turn/input submitted")
}
