package documenttext

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/internal/processaudit"
)

var ErrDependency = errors.New("local document reader unavailable")

// Local parsers are invoked only inside the owned document worker. The parent
// owns its process group and temporary directory, including on cancellation.
// No document-supplied executable, argument, original filename or URL is used.
func Local(ctx context.Context, kind string, data []byte) (string, error) {
	if kind == "pdf" {
		var dir string
		defer func() {
			if dir != "" {
				os.RemoveAll(dir)
			}
		}()
		pages := 0
		return pdfText(data, func(page int) (string, error) {
			pages++
			if pages > 10 || ctx.Err() != nil {
				return "", ErrDocument
			}
			if dir == "" {
				var err error
				dir, err = os.MkdirTemp("", "nexus-ocr-")
				if err != nil || os.WriteFile(filepath.Join(dir, "input.pdf"), data, 0600) != nil {
					return "", ErrDocument
				}
			}
			raster, err := runLocal(ctx, "pdftoppm", nil, 6<<20, "-f", fmt.Sprint(page), "-l", fmt.Sprint(page), "-singlefile", "-scale-to", "2400", "-gray", filepath.Join(dir, "input.pdf"))
			if err != nil {
				return "", err
			}
			text, err := runLocal(ctx, "tesseract", raster, MaxText, "stdin", "stdout", "-l", "eng")
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(string(text)) == "" {
				return "", nil
			}
			return fmt.Sprintf("[Page %d: OCR; recognition may contain errors]\n%s", page, text), nil
		})
	}
	if kind != "doc" || len(data) < 512 || !bytes.Equal(data[:8], []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}) {
		return "", ErrDocument
	}
	var out []byte
	var err error
	if runtime.GOOS == "darwin" {
		out, err = runLocal(ctx, "textutil", data, MaxText, "-convert", "txt", "-format", "doc", "-stdin", "-stdout", "-encoding", "UTF-8", "-noload", "-nostore", "-timeout", "1")
	} else {
		out, err = runLocal(ctx, "antiword", data, MaxText, "-m", "UTF-8.txt", "-")
	}
	if errors.Is(err, ErrDependency) {
		return "", err
	}
	if err != nil || !utf8.Valid(out) || bytes.IndexByte(out, 0) >= 0 || strings.TrimSpace(string(out)) == "" {
		return "", ErrDocument
	}
	return string(out), nil
}

func localTool(name string) (string, error) {
	for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin"} {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return path, nil
		}
	}
	return "", ErrDependency
}

func runLocal(ctx context.Context, tool string, input []byte, limit int, args ...string) ([]byte, error) {
	path, err := localTool(tool)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = []string{"PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin", "LANG=en_US.UTF-8", "OMP_THREAD_LIMIT=1", "TMPDIR=" + os.TempDir()}
	cmd.Stdin = bytes.NewReader(input)
	var out boundedOutput
	out.limit = limit
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	if err = processaudit.Run(cmd); err != nil {
		return nil, ErrDocument
	}
	if tool == "tesseract" && (!utf8.Valid(out.Bytes()) || bytes.IndexByte(out.Bytes(), 0) >= 0) {
		return nil, ErrDocument
	}
	return out.Bytes(), nil
}

type boundedOutput struct {
	buffer bytes.Buffer
	limit  int
}

func (w *boundedOutput) Bytes() []byte { return w.buffer.Bytes() }
func (w *boundedOutput) Len() int      { return w.buffer.Len() }
func (w *boundedOutput) Write(b []byte) (int, error) {
	if len(b) > w.limit-w.Len() {
		return 0, ErrDocument
	}
	return w.buffer.Write(b)
}
