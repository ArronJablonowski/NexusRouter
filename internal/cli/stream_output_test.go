package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func outputPipeFlags(t *testing.T, file *os.File) int {
	t.Helper()
	raw, err := file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var flags int
	var opErr error
	if err := raw.Control(func(fd uintptr) { flags, opErr = streamFileFlags(int(fd)) }); err != nil || opErr != nil {
		t.Fatal(err, opErr)
	}
	return flags
}

func restoredOutputFlags(flags int) int {
	if runtime.GOOS == "darwin" {
		// XNU sets the kernel-only FWASWRITTEN (0x10000) after actual I/O.
		// It is excluded from FCNTLFLAGS and cannot be cleared by F_SETFL:
		// https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/fcntl.h
		flags &^= 0x10000
	}
	return flags
}

func TestStreamOutputPipeCancellationAndFlagRestore(t *testing.T) {
	for _, nonblocking := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocking", true: "nonblocking"}[nonblocking], func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			raw, err := writer.SyscallConn()
			if err != nil {
				t.Fatal(err)
			}
			flags := outputPipeFlags(t, writer)
			if nonblocking {
				flags |= syscall.O_NONBLOCK
			} else {
				flags &^= syscall.O_NONBLOCK
			}
			var opErr error
			if err := raw.Control(func(fd uintptr) { opErr = setStreamFileFlags(int(fd), flags) }); err != nil || opErr != nil {
				t.Fatal(err, opErr)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			output, cleanup, err := prepareStreamOutput(ctx, writer)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if outputPipeFlags(t, writer)&syscall.O_NONBLOCK == 0 {
				t.Fatal("duplicate did not enable nonblocking pipe")
			}
			start := time.Now()
			n, err := output.Write(bytes.Repeat([]byte("x"), 1<<20))
			if !errors.Is(err, context.DeadlineExceeded) || n >= 1<<20 || time.Since(start) > time.Second {
				t.Fatal("stalled pipe did not cancel", n, err, time.Since(start))
			}
			if err := cleanup(); err != nil {
				t.Fatal(err)
			}
			if got := outputPipeFlags(t, writer); restoredOutputFlags(got) != restoredOutputFlags(flags) {
				t.Fatal("original flags not restored", flags, got)
			}
			if _, err := writer.Stat(); err != nil {
				t.Fatal("caller descriptor closed", err)
			}
			if _, err := output.Write([]byte("late")); err == nil {
				t.Fatal("wrapper stayed writable after cleanup")
			}
		})
	}
}

func TestStreamOutputSuccessfulWriteThenCancellation(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	flags := outputPipeFlags(t, writer)
	ctx, cancel := context.WithCancel(context.Background())
	output, cleanup, err := prepareStreamOutput(ctx, writer)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := output.Write([]byte("ok")); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	data := make([]byte, 2)
	if _, err := io.ReadFull(reader, data); err != nil || string(data) != "ok" {
		t.Fatal(data, err)
	}
	// The callback from the completed write must already be stopped. Canceling
	// now must not race cleanup or install a deadline on a recycled descriptor.
	cancel()
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if got := outputPipeFlags(t, writer); restoredOutputFlags(got) != restoredOutputFlags(flags) {
		t.Fatal(flags, got)
	}
	if n, err := writer.Write([]byte("live")); err != nil || n != 4 {
		t.Fatal("caller stdout unusable", n, err)
	}
}

func TestStreamOutputOtherWritersAndSetupFailure(t *testing.T) {
	var buffer bytes.Buffer
	output, cleanup, err := prepareStreamOutput(context.Background(), &buffer)
	if err != nil || output != &buffer {
		t.Fatal("custom writer semantics changed", err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	output, cleanup, err = prepareStreamOutput(context.Background(), file)
	if err != nil || output != file {
		t.Fatal("regular file wrapped", err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, cleanup, err := prepareStreamOutput(context.Background(), file); err == nil {
		t.Fatal("closed output admitted")
	} else if err := cleanup(); err != nil {
		t.Fatal(err)
	}
}
