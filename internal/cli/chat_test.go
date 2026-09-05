package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"darwinrouter/internal/app"
)

func TestChatRejectsArgumentsBeforeInput(t *testing.T) {
	for _, args := range [][]string{{"chat"}, {"chat", "--config", "absent", "--model", "auto", "--json"}} {
		var out, diagnostic bytes.Buffer
		if code := RunWithInput(args, nil, &out, &diagnostic, "test"); code != 2 || out.Len() != 0 {
			t.Fatalf("code=%d out=%q", code, out.String())
		}
	}
}

func TestChatQuitJoinsBlockedInputAndKeepsCallerPipe(t *testing.T) {
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := io.WriteString(writer, "/quit\n"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := runChatIO(ctx, app.Request{}, chatHooks{}, input, &out, nil); code != 0 {
		t.Fatalf("code=%d output=%q", code, out.String())
	}
	if _, err := io.WriteString(writer, "x"); err != nil {
		t.Fatal(err)
	}
	var got [1]byte
	if _, err := input.Read(got[:]); err != nil || got[0] != 'x' {
		t.Fatalf("caller pipe: %q %v", got, err)
	}
}

func TestChatIOEmptyInput(t *testing.T) {
	var out bytes.Buffer
	if code := runChatIO(context.Background(), app.Request{}, chatHooks{}, strings.NewReader(""), &out, nil); code != 0 {
		t.Fatalf("code=%d output=%q", code, out.String())
	}
}
