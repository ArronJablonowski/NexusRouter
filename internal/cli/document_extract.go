package cli

import (
	"github.com/ArronJablonowski/NexusRouter/attachments"
	"github.com/ArronJablonowski/NexusRouter/internal/documenttext"
	"io"
	"runtime/debug"
)

// Owned parser subprocess: only bytes on stdin, no filename, network, scripts,
// external document resources or model/tool dispatch. Parent enforces deadline.
func runDocumentExtract(args []string, in io.Reader, out io.Writer) int {
	oldLimit := debug.SetMemoryLimit(64 << 20)
	defer debug.SetMemoryLimit(oldLimit)
	if len(args) != 1 || args[0] != "pdf" {
		return 1
	}
	b, e := io.ReadAll(io.LimitReader(in, attachments.MaxFileBytes+1))
	if e != nil || len(b) > attachments.MaxFileBytes {
		return 1
	}
	text, e := documenttext.PDF(b)
	if e != nil {
		return 1
	}
	if _, e = io.WriteString(out, text); e != nil {
		return 1
	}
	return 0
}
