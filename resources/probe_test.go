package resources

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestProbeChild(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "large":
		fmt.Print(strings.Repeat("x", maxProbeBytes+1))
	case "sleep":
		time.Sleep(10 * time.Second)
	case "fail":
		fmt.Fprintln(os.Stderr, "private error")
		os.Exit(7)
	default:
		return
	}
	os.Exit(0)
}

func TestRunProbeBoundaries(t *testing.T) {
	for _, mode := range []string{"small", "large", "fail", "sleep"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			start := time.Now()
			path, args := os.Args[0], []string{"-test.run=^TestProbeChild$", "--", mode}
			if mode == "small" {
				// A race-instrumented test child intentionally delays normal exit
				// for one second; use a system formatter for the success fixture.
				var err error
				path, err = exec.LookPath("printf")
				if err != nil {
					t.Skip("printf fixture unavailable")
				}
				args = []string{"observed\n"}
			}
			body, err := runProbe(ctx, path, args...)
			if mode == "small" {
				if err != nil || string(body) != "observed\n" {
					t.Fatalf("%q %v", body, err)
				}
			} else if err != ErrProfile || body != nil {
				t.Fatalf("leaked invalid result %q %v", body, err)
			}
			if time.Since(start) > 3*time.Second {
				t.Fatal("unbounded probe")
			}
		})
	}
}

func TestProbeBufferBound(t *testing.T) {
	var out probeBuffer
	if _, err := out.Write(make([]byte, maxProbeBytes)); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte("x")); err != ErrProfile || out.Len() != maxProbeBytes {
		t.Fatal("output ceiling bypassed")
	}
}

func TestProbeBufferCopyCannotBypassBound(t *testing.T) {
	var out probeBuffer
	_, err := io.Copy(&out, io.LimitReader(strings.NewReader(strings.Repeat("x", maxProbeBytes+1)), maxProbeBytes+1))
	if err != ErrProfile || out.Len() > maxProbeBytes {
		t.Fatalf("copy bypass: %d %v", out.Len(), err)
	}
	if _, ok := any(&out).(io.ReaderFrom); ok {
		t.Fatal("ReaderFrom bypasses bounded Write")
	}
}
