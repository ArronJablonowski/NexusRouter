package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	run "github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestSteeringInputValidation(t *testing.T) {
	for _, body := range []string{"", " \n", "\xff", strings.Repeat("x", run.MaxSteeringBytes+1)} {
		if text, err := readSteeringInput(context.Background(), strings.NewReader(body)); err == nil || text != "" {
			t.Fatal("invalid input accepted", len(text), err)
		}
	}
	for _, body := range []string{"guidance\n", strings.Repeat("x", run.MaxSteeringBytes)} {
		if text, err := readSteeringInput(context.Background(), strings.NewReader(body)); err != nil || text != body {
			t.Fatal(len(text), err)
		}
	}
	if text, err := readSteeringInput(context.Background(), steeringFailReader{}); text != "" || !errors.Is(err, errSteeringInput) || strings.Contains(err.Error(), "private") {
		t.Fatal(text, err)
	}
	if text, err := readSteeringInput(context.Background(), steeringPanicReader{}); text != "" || !errors.Is(err, errSteeringInput) {
		t.Fatal(text, err)
	}
}

type steeringFailReader struct{}

func (steeringFailReader) Read([]byte) (int, error) { return 0, errors.New("private read error") }

type steeringPanicReader struct{}

func (steeringPanicReader) Read([]byte) (int, error) { panic("private read panic") }

type steeringCountingReader struct{ calls int }

func (r *steeringCountingReader) Read([]byte) (int, error) { r.calls++; return 0, io.EOF }

func TestSteeringInputCanceledBeforeConsumption(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	input := &steeringCountingReader{}
	text, err := readSteeringInput(ctx, input)
	if text != "" || !errors.Is(err, context.Canceled) || input.calls != 0 {
		t.Fatal(text, err, input.calls)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if _, err := writer.Write([]byte("untouched")); err != nil {
		t.Fatal(err)
	}
	if _, err := readSteeringInput(ctx, reader); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	body := make([]byte, 9)
	if _, err := io.ReadFull(reader, body); err != nil || string(body) != "untouched" {
		t.Fatal(string(body), err)
	}
}

func TestSteeringInputPipeCancellationRestoresFlagsAndOwnership(t *testing.T) {
	for _, nonblocking := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocking", true: "nonblocking"}[nonblocking], func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			flags := outputPipeFlags(t, reader)
			if nonblocking {
				flags |= syscall.O_NONBLOCK
			} else {
				flags &^= syscall.O_NONBLOCK
			}
			raw, err := reader.SyscallConn()
			if err != nil {
				t.Fatal(err)
			}
			var setupErr error
			if err := raw.Control(func(fd uintptr) { setupErr = setStreamFileFlags(int(fd), flags) }); err != nil || setupErr != nil {
				t.Fatal(err, setupErr)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			start := time.Now()
			text, err := readSteeringInput(ctx, reader)
			if text != "" || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
				t.Fatal("blocked pipe not canceled", text, err, time.Since(start))
			}
			if got := outputPipeFlags(t, reader); restoredOutputFlags(got) != restoredOutputFlags(flags) {
				t.Fatal("flags changed", flags, got)
			}
			if _, err := writer.Write([]byte("still usable")); err != nil {
				t.Fatal(err)
			}
			body := make([]byte, len("still usable"))
			if _, err := io.ReadFull(reader, body); err != nil || string(body) != "still usable" {
				t.Fatal("caller descriptor closed or poisoned", string(body), err)
			}
		})
	}
}

func TestSteeringInputPipeSuccessRestoresFlags(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	flags := outputPipeFlags(t, reader)
	if _, err := writer.Write([]byte("guidance")); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	text, err := readSteeringInput(context.Background(), reader)
	if err != nil || text != "guidance" {
		t.Fatal(text, err)
	}
	if got := outputPipeFlags(t, reader); restoredOutputFlags(got) != restoredOutputFlags(flags) {
		t.Fatal(flags, got)
	}
	if _, err := reader.Stat(); err != nil {
		t.Fatal("caller pipe closed", err)
	}
}

func TestSteeringInputRegularFileAndCharacterDevice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guidance.txt")
	if err := os.WriteFile(path, []byte("file guidance"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if text, err := readSteeringInput(context.Background(), file); err != nil || text != "file guidance" {
		t.Fatal(text, err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal("caller file closed", err)
	}
	device, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer device.Close()
	if _, err := readSteeringInput(context.Background(), device); !errors.Is(err, errSteeringInput) {
		t.Fatal("character device accepted", err)
	}
}
