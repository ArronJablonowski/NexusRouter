package cli

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/mattn/go-isatty"
)

var errChatInput = errors.New("chat input unavailable or invalid")

// prepareChatInput borrows input exclusively without changing canonical/echo
// terminal settings. The owner must join pending reads before cleanup. Custom
// readers and regular-file kernel reads remain cooperatively cancellable.
func prepareChatInput(ctx context.Context, input io.Reader) (io.Reader, func() error, error) {
	noop := func() error { return nil }
	if ctx.Err() != nil {
		return nil, noop, errors.Join(errChatInput, ctx.Err())
	}
	if input == nil {
		return nil, noop, errChatInput
	}
	file, ok := input.(*os.File)
	if !ok {
		return prepareSteeringInput(ctx, input)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, noop, errChatInput
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		reader, cleanup, err := prepareSteeringInput(ctx, input)
		if pipe, ok := reader.(steeringPipeReader); ok {
			reader = chatPipeReader{ctx: ctx, file: pipe.file}
		}
		return reader, cleanup, err
	}
	raw, err := file.SyscallConn()
	if err != nil {
		return nil, noop, errChatInput
	}
	duplicate, flags := -1, 0
	var setupErr error
	err = raw.Control(func(fd uintptr) {
		if !isatty.IsTerminal(fd) {
			setupErr = errChatInput
			return
		}
		flags, setupErr = streamFileFlags(int(fd))
		if setupErr != nil {
			return
		}
		syscall.ForkLock.RLock()
		duplicate, setupErr = syscall.Dup(int(fd))
		if setupErr == nil {
			syscall.CloseOnExec(duplicate)
		}
		syscall.ForkLock.RUnlock()
		if setupErr == nil {
			setupErr = setStreamFileFlags(duplicate, flags|syscall.O_NONBLOCK)
		}
	})
	if err != nil || setupErr != nil {
		if duplicate >= 0 {
			_ = setStreamFileFlags(duplicate, flags)
			_ = syscall.Close(duplicate)
		}
		return nil, noop, errChatInput
	}
	borrowed := os.NewFile(uintptr(duplicate), "chat-terminal-input")
	if borrowed == nil {
		_ = setStreamFileFlags(duplicate, flags)
		_ = syscall.Close(duplicate)
		return nil, noop, errChatInput
	}
	closed := false
	cleanup := func() error {
		if closed {
			return nil
		}
		closed = true
		restoreErr := setStreamFileFlags(duplicate, flags)
		closeErr := borrowed.Close()
		if restoreErr != nil || closeErr != nil {
			return errChatInput
		}
		return nil
	}
	if borrowed.SetReadDeadline(time.Time{}) != nil {
		_ = cleanup()
		return nil, noop, errChatInput
	}
	return chatPipeReader{ctx: ctx, file: borrowed}, cleanup, nil
}

// Chat input has no idle timeout. Cancellation interrupts pollable reads, and
// the callback is joined before clearing its deadline or releasing the file.
type chatPipeReader struct {
	ctx  context.Context
	file *os.File
}

func (r chatPipeReader) Read(body []byte) (n int, err error) {
	if err = r.ctx.Err(); err != nil {
		return 0, err
	}
	if err = r.file.SetReadDeadline(time.Time{}); err != nil {
		return 0, err
	}
	finished := make(chan struct{})
	stop := context.AfterFunc(r.ctx, func() { defer close(finished); _ = r.file.SetReadDeadline(time.Now()) })
	defer func() {
		if !stop() {
			<-finished
		}
		clearErr := r.file.SetReadDeadline(time.Time{})
		if err == nil {
			err = clearErr
		}
	}()
	n, err = r.file.Read(body)
	if r.ctx.Err() != nil {
		err = r.ctx.Err()
	}
	return n, err
}

// readChatLine does not echo invalid or oversized input. EOF with a final
// unterminated line returns that line once; the next call returns io.EOF.
func readChatLine(reader *bufio.Reader) (string, error) {
	if reader == nil {
		return "", errChatInput
	}
	line := make([]byte, 0, 256)
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > runtime.MaxSteeringBytes+2-len(line) {
			return "", errChatInput
		}
		line = append(line, fragment...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil && err != io.EOF {
			return "", errChatInput
		}
		if err == io.EOF && len(line) == 0 {
			return "", io.EOF
		}
		if len(line) > 0 && line[len(line)-1] == '\n' {
			line = line[:len(line)-1]
		}
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if len(line) > runtime.MaxSteeringBytes || !utf8.Valid(line) {
			return "", errChatInput
		}
		text := string(line)
		if strings.TrimSpace(text) == "" {
			return "", nil
		}
		return text, nil
	}
}
