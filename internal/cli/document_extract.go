package cli

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/attachments"
	"github.com/ArronJablonowski/NexusRouter/internal/documenttext"
	"io"
	"runtime/debug"
	"time"
)

// Owned parser subprocess: only bytes on stdin, no filename, network, scripts,
// external document resources or model/tool dispatch. Parent enforces deadline.
func runDocumentExtract(args []string, in io.Reader, out io.Writer) int {
	oldLimit := debug.SetMemoryLimit(64 << 20)
	defer debug.SetMemoryLimit(oldLimit)
	if len(args) != 1 || (args[0] != "pdf" && args[0] != "doc") {
		return 1
	}
	b, e := io.ReadAll(io.LimitReader(in, attachments.MaxFileBytes+1))
	if e != nil || len(b) > attachments.MaxFileBytes {
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	text, e := documenttext.Local(ctx, args[0], b)
	if errors.Is(e, documenttext.ErrDependency) {
		return 2
	}
	if e != nil {
		return 1
	}
	if _, e = io.WriteString(out, text); e != nil {
		return 1
	}
	return 0
}
