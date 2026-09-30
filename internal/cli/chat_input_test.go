package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestChatLine(t *testing.T) {
	for _, input := range []string{"hello\n", "hello\r\n", "hello"} {
		reader := bufio.NewReaderSize(strings.NewReader(input), 16)
		if got, err := readChatLine(reader); got != "hello" || err != nil {
			t.Fatal(got, err)
		}
		if _, err := readChatLine(reader); err != io.EOF {
			t.Fatal(err)
		}
	}
	for _, input := range []string{"\n", " \t\r\n"} {
		if got, err := readChatLine(bufio.NewReader(strings.NewReader(input))); got != "" || err != nil {
			t.Fatal(got, err)
		}
	}
	for _, suffix := range []string{"", "\n", "\r\n"} {
		input := strings.Repeat("a", runtime.MaxSteeringBytes)
		if got, err := readChatLine(bufio.NewReaderSize(strings.NewReader(input+suffix), 16)); got != input || err != nil {
			t.Fatal(len(got), err)
		}
	}
	for _, input := range []string{"\xff\n", strings.Repeat("a", runtime.MaxSteeringBytes+1) + "\n"} {
		if got, err := readChatLine(bufio.NewReader(strings.NewReader(input))); got != "" || !errors.Is(err, errChatInput) {
			t.Fatal(len(got), err)
		}
	}
	if got, err := readChatLine(bufio.NewReader(steeringFailReader{})); got != "" || err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal(got, err)
	}
	if _, err := readChatLine(nil); err == nil {
		t.Fatal("nil accepted")
	}
}

func TestChatInputPipeCancellation(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	flags := outputPipeFlags(t, r)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	input, cleanup, err := prepareChatInput(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, ok := input.(chatPipeReader); !ok {
		t.Fatal("pipe must have no idle timeout")
	}
	if _, err = input.Read(make([]byte, 1)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err = cleanup(); err != nil {
		t.Fatal(err)
	}
	if got := outputPipeFlags(t, r); restoredOutputFlags(got) != restoredOutputFlags(flags) {
		t.Fatal(flags, got)
	}
	if _, err = w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 1)
	if _, err = io.ReadFull(r, body); err != nil || string(body) != "x" {
		t.Fatal(err)
	}
}

func TestChatInputRejectsDeviceAndCanceled(t *testing.T) {
	file, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, _, err = prepareChatInput(context.Background(), file); err == nil {
		t.Fatal("nonterminal accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := &steeringCountingReader{}
	if _, _, err = prepareChatInput(ctx, reader); !errors.Is(err, context.Canceled) || reader.calls != 0 {
		t.Fatal(err, reader.calls)
	}
}

func TestChatInputRegularAndCustom(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err = file.WriteString("file line\n"); err != nil {
		t.Fatal(err)
	}
	if _, err = file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	for _, source := range []io.Reader{file, strings.NewReader("file line\n")} {
		reader, cleanup, err := prepareChatInput(context.Background(), source)
		if err != nil {
			t.Fatal(err)
		}
		line, err := readChatLine(bufio.NewReader(reader))
		if err != nil || line != "file line" {
			t.Fatal(line, err)
		}
		if err = cleanup(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = file.Seek(0, 0); err != nil {
		t.Fatal("caller file closed", err)
	}
}

func TestChatInputPTY(t *testing.T) {
	if goruntime.GOOS != "darwin" {
		t.Skip("Darwin script fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	cmd := exec.CommandContext(ctx, "/usr/bin/script", "-q", "/dev/null", os.Args[0], "-test.run=^TestChatInputPTYChild$")
	cmd.Stdin = r
	cmd.WaitDelay = 100 * time.Millisecond
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GORACE=") && !strings.HasPrefix(entry, "DARWIN_CHAT_INPUT_PTY_CHILD=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GORACE=atexit_sleep_ms=0", "DARWIN_CHAT_INPUT_PTY_CHILD=1")
	body, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(body), "CHAT_PTY_OK") {
		t.Fatalf("pty fixture: %v %s", err, body)
	}
}

func TestChatInputPTYChild(t *testing.T) {
	if os.Getenv("DARWIN_CHAT_INPUT_PTY_CHILD") != "1" {
		t.Skip("child only")
	}
	terminalMode := func() string {
		cmd := exec.Command("/bin/stty", "-g")
		cmd.Stdin = os.Stdin
		body, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	before := terminalMode()
	flags := outputPipeFlags(t, os.Stdin)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	input, cleanup, err := prepareChatInput(ctx, os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, ok := input.(chatPipeReader); !ok {
		t.Fatal("not terminal poller")
	}
	if _, err = input.Read(make([]byte, 1)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err = cleanup(); err != nil {
		t.Fatal(err)
	}
	if got := outputPipeFlags(t, os.Stdin); restoredOutputFlags(got) != restoredOutputFlags(flags) {
		t.Fatal(flags, got)
	}
	if after := terminalMode(); before != after {
		t.Fatal("terminal mode changed")
	}
	fmt.Println("CHAT_PTY_OK")
}
