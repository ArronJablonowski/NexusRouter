package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

type promptPanicReader struct{}

func (promptPanicReader) Read([]byte) (int, error) { panic("private input detail") }

type promptErrorReader struct{ err error }

func (r promptErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestReadTaskPromptValidationAndPreservation(t *testing.T) {
	for _, input := range []string{" hello \n\t", "世界", strings.Repeat("x", 1<<20)} {
		text, err := readTaskPrompt(context.Background(), strings.NewReader(input))
		if err != nil || text != input {
			t.Fatal(len(text), err)
		}
	}
	for _, input := range []string{"", " \n\t", string([]byte{255}), strings.Repeat("x", (1<<20)+1)} {
		text, err := readTaskPrompt(context.Background(), strings.NewReader(input))
		if text != "" || !errors.Is(err, errTaskPrompt) {
			t.Fatal(len(text), err)
		}
	}
	for _, reader := range []io.Reader{nil, promptPanicReader{}, promptErrorReader{errors.New("private input error")}} {
		text, err := readTaskPrompt(context.Background(), reader)
		if text != "" || err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal(text, err)
		}
	}
	if text, err := readTaskPrompt(nil, strings.NewReader("hello")); text != "" || err == nil {
		t.Fatal(text, err)
	}
}

func TestReadTaskPromptCancellationIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if text, err := readTaskPrompt(ctx, strings.NewReader("hello")); text != "" || !errors.Is(err, context.Canceled) {
		t.Fatal(text, err)
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		text, err := readTaskPrompt(context.Background(), promptErrorReader{errors.Join(errors.New("private cancellation detail"), cause)})
		if text != "" || !errors.Is(err, cause) || strings.Contains(err.Error(), "private") {
			t.Fatal(text, err)
		}
	}
}

func TestReadTaskPromptBlockedPipeRestoresCaller(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	raw, err := reader.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	before := 0
	var flagErr error
	if err = raw.Control(func(fd uintptr) { before, flagErr = streamFileFlags(int(fd)) }); err != nil || flagErr != nil {
		t.Fatal(err, flagErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	text, err := readTaskPrompt(ctx, reader)
	if text != "" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(text, err)
	}
	after := 0
	if err = raw.Control(func(fd uintptr) { after, flagErr = streamFileFlags(int(fd)) }); err != nil || flagErr != nil || after != before {
		t.Fatal("caller flags changed", before, after, err, flagErr)
	}
	if _, err = writer.Write([]byte("usable")); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 6)
	if _, err = io.ReadFull(reader, body); err != nil || string(body) != "usable" {
		t.Fatal("caller closed or unusable", string(body), err)
	}
}
