package cli

import (
	"context"
	"io"
	"os"
	"syscall"
	"time"

	"darwinrouter/internal/app"
)

// prepareStreamOutput borrows an actual stdout pipe exclusively for the run.
// Duplicate descriptors share status flags, so original flags are restored
// before closing our duplicate. The caller's descriptor is never closed.
// Custom io.Writers and regular files retain their own blocking semantics.
func prepareStreamOutput(ctx context.Context, output io.Writer) (io.Writer, func() error, error) {
	noop := func() error { return nil }
	file, ok := output.(*os.File)
	if !ok {
		return output, noop, nil
	}
	info, err := file.Stat()
	if err != nil {
		return nil, noop, app.ErrEventDelivery
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		return output, noop, nil
	}
	raw, err := file.SyscallConn()
	if err != nil {
		return nil, noop, app.ErrEventDelivery
	}
	duplicate, flags := -1, 0
	var setupErr error
	err = raw.Control(func(fd uintptr) {
		flags, setupErr = streamFileFlags(int(fd))
		if setupErr != nil {
			return
		}
		syscall.ForkLock.RLock()
		duplicate, setupErr = syscall.Dup(int(fd))
		if setupErr != nil {
			syscall.ForkLock.RUnlock()
			return
		}
		syscall.CloseOnExec(duplicate)
		syscall.ForkLock.RUnlock()
		setupErr = setStreamFileFlags(duplicate, flags|syscall.O_NONBLOCK)
	})
	if err != nil || setupErr != nil {
		if duplicate >= 0 {
			_ = setStreamFileFlags(duplicate, flags)
			_ = syscall.Close(duplicate)
		}
		return nil, noop, app.ErrEventDelivery
	}
	// NewFile sees O_NONBLOCK and registers the duplicate with Go's poller.
	// Calling file.Fd() instead would force an existing pollable file blocking.
	borrowed := os.NewFile(uintptr(duplicate), "task-json-output")
	if borrowed == nil {
		_ = setStreamFileFlags(duplicate, flags)
		_ = syscall.Close(duplicate)
		return nil, noop, app.ErrEventDelivery
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
			return app.ErrEventDelivery
		}
		return nil
	}
	if err := borrowed.SetWriteDeadline(time.Time{}); err != nil {
		_ = cleanup()
		return nil, noop, app.ErrEventDelivery
	}
	return &streamPipeWriter{ctx: ctx, file: borrowed}, cleanup, nil
}

type streamPipeWriter struct {
	ctx  context.Context
	file *os.File
}

func (w *streamPipeWriter) Write(body []byte) (n int, err error) {
	if err = w.ctx.Err(); err != nil {
		return 0, err
	}
	if err = w.file.SetWriteDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return 0, err
	}
	finished := make(chan struct{})
	stop := context.AfterFunc(w.ctx, func() { defer close(finished); _ = w.file.SetWriteDeadline(time.Now()) })
	defer func() {
		// A cancellation callback must finish before clearing the deadline; it
		// must never poison the next write or race cleanup's descriptor close.
		if !stop() {
			<-finished
		}
		clearErr := w.file.SetWriteDeadline(time.Time{})
		if err == nil {
			err = clearErr
		}
	}()
	n, err = w.file.Write(body)
	if w.ctx.Err() != nil {
		err = w.ctx.Err()
	}
	return n, err
}

func streamFileFlags(fd int) (int, error) {
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_GETFL), 0)
	if errno != 0 {
		return 0, errno
	}
	return int(flags), nil
}
func setStreamFileFlags(fd, flags int) error {
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_SETFL), uintptr(flags))
	if errno != 0 {
		return errno
	}
	return nil
}
