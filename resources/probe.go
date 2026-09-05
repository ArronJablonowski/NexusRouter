package resources

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"time"
)

const maxProbeBytes = 64 << 10

// A named field avoids promoting bytes.Buffer.ReadFrom, which would let
// io.Copy bypass Write and its output ceiling.
type probeBuffer struct{ buffer bytes.Buffer }

func (b *probeBuffer) Len() int      { return b.buffer.Len() }
func (b *probeBuffer) Bytes() []byte { return b.buffer.Bytes() }

func (b *probeBuffer) Write(p []byte) (int, error) {
	if len(p) > maxProbeBytes-b.Len() {
		return 0, ErrProfile
	}
	return b.buffer.Write(p)
}

// runProbe has a fixed output ceiling, a process deadline and a pipe-drain
// deadline. Only trusted fixed executable paths/arguments may reach this helper.
func runProbe(ctx context.Context, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = 100 * time.Millisecond
	cmd.Env = []string{"LANG=C", "LC_ALL=C", "PATH=/usr/bin:/bin"}
	var output probeBuffer
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	if err := cmd.Run(); err != nil || ctx.Err() != nil {
		return nil, ErrProfile
	}
	return output.Bytes(), nil
}
