package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestCommands(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"default", nil, 0, "Usage:"},
		{"help", []string{"--help"}, 0, "separate-command steering are available"},
		{"version", []string{"version"}, 0, "nexus test-build\n"},
		{"version flag", []string{"--version"}, 0, "nexus test-build\n"},
		{"unknown", []string{"api-key-secret"}, 2, ""},
		{"extra args", []string{"version", "secret"}, 2, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errout bytes.Buffer
			if code := Run(tc.args, &out, &errout, "test-build"); code != tc.code {
				t.Fatalf("exit=%d, want %d", code, tc.code)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("unexpected stdout: %q", out.String())
			}
			if tc.code == 2 && (out.Len() != 0 || errout.Len() == 0) {
				t.Fatal("usage errors must go only to stderr")
			}
			if strings.Contains(errout.String(), "secret") {
				t.Fatal("argument leaked into diagnostic")
			}
		})
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestOutputFailure(t *testing.T) {
	for _, args := range [][]string{nil, {"version"}} {
		if Run(args, brokenWriter{}, &bytes.Buffer{}, "dev") != 1 {
			t.Fatal("output failure must return nonzero")
		}
	}
}
