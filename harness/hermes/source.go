package hermes

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Read-only source checks use the host git installation. Dependencies are trusted
// installed code; this does not attest every imported package or prevent changes
// by another process during execution.
func verifySource(ctx context.Context, c Config, env []string, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, check := range []struct {
		args []string
		want string
	}{
		{[]string{"rev-parse", "HEAD"}, SupportedRevision},
		{[]string{"status", "--porcelain", "--untracked-files=no"}, ""},
	} {
		cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "-C", c.SourceDir}, check.args...)...)
		cmd.Env = append(append([]string(nil), env...), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		cmd.Dir = dir
		out := &boundedOutput{limit: 4096, cancel: cancel}
		cmd.Stdout = out
		if runProcess(cmd) != nil || strings.TrimSpace(string(out.data)) != check.want {
			return ErrProjection
		}
	}
	return nil
}
