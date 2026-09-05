package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

var errSteeringInput = errors.New("steering input unavailable or invalid")

// readSteeringInput borrows a file/pipe exclusively for this read, never closes
// the caller's descriptor, and never starts an unjoinable reader goroutine.
// Terminals are intentionally unsupported. Regular files and custom readers
// are cooperative: an in-progress kernel/custom Read cannot be interrupted.
func readSteeringInput(ctx context.Context, input io.Reader) (text string, err error) {
	defer func() {
		if recover() != nil {
			text = ""
			err = errSteeringInput
		}
	}()
	if ctx.Err() != nil {
		return "", errors.Join(errSteeringInput, ctx.Err())
	}
	if input == nil {
		return "", errSteeringInput
	}
	reader, cleanup, err := prepareSteeringInput(ctx, input)
	if err != nil {
		return "", errSteeringInput
	}
	defer func() {
		if cleanup() != nil {
			text = ""
			err = errSteeringInput
		}
	}()
	body, readErr := io.ReadAll(io.LimitReader(reader, runtime.MaxSteeringBytes+1))
	if ctx.Err() != nil {
		return "", errors.Join(errSteeringInput, ctx.Err())
	}
	if readErr != nil || !runtime.ValidSteeringText(string(body)) {
		return "", errSteeringInput
	}
	return string(body), nil
}

func prepareSteeringInput(ctx context.Context, input io.Reader) (io.Reader, func() error, error) {
	noop := func() error { return nil }
	file, ok := input.(*os.File)
	if !ok {
		return steeringContextReader{ctx: ctx, input: input}, noop, nil
	}
	info, err := file.Stat()
	if err != nil {
		return nil, noop, errSteeringInput
	}
	if info.Mode().IsRegular() {
		return steeringContextReader{ctx: ctx, input: file}, noop, nil
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		return nil, noop, errSteeringInput
	}
	raw, err := file.SyscallConn()
	if err != nil {
		return nil, noop, errSteeringInput
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
		return nil, noop, errSteeringInput
	}
	borrowed := os.NewFile(uintptr(duplicate), "steering-input")
	if borrowed == nil {
		_ = setStreamFileFlags(duplicate, flags)
		_ = syscall.Close(duplicate)
		return nil, noop, errSteeringInput
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
			return errSteeringInput
		}
		return nil
	}
	if borrowed.SetReadDeadline(time.Time{}) != nil {
		_ = cleanup()
		return nil, noop, errSteeringInput
	}
	return steeringPipeReader{ctx: ctx, file: borrowed}, cleanup, nil
}

type steeringContextReader struct {
	ctx   context.Context
	input io.Reader
}

func (r steeringContextReader) Read(body []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.input.Read(body)
	if r.ctx.Err() != nil {
		return n, r.ctx.Err()
	}
	return n, err
}

type steeringPipeReader struct {
	ctx  context.Context
	file *os.File
}

func (r steeringPipeReader) Read(body []byte) (n int, err error) {
	if err = r.ctx.Err(); err != nil {
		return 0, err
	}
	if err = r.file.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
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
