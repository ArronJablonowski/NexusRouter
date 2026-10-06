package codexbridge

import (
	"bytes"
	"context"
	"github.com/ArronJablonowski/NexusRouter/internal/processaudit"
	"os/exec"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/codexrpc"
)

// Only trusted, version-pinned CLI metadata is captured, never stderr or auth.
// WaitDelay bounds a descendant retaining the metadata stdout pipe; it is not
// descendant supervision. The task-owned stdio transport handles model traffic.
func launchMetadata(ctx context.Context, base codexrpc.ProcessSpec, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, base.Executable, args...)
	cmd.Env = append([]string{}, base.Env...)
	cmd.Dir = base.Dir
	cmd.WaitDelay = time.Second
	out := &launchMetadataBuffer{}
	cmd.Stdout = out
	if processaudit.Run(cmd) != nil {
		return nil, ErrLaunchObservation
	}
	return out.buf.Bytes(), nil
}

type launchMetadataBuffer struct{ buf bytes.Buffer }

func (b *launchMetadataBuffer) Write(p []byte) (int, error) {
	if len(p) > (64<<10)-b.buf.Len() {
		return 0, ErrLaunchObservation
	}
	return b.buf.Write(p)
}
