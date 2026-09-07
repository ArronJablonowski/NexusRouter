package main

import (
	"bytes"
	"context"
	"io"
	"testing"
)

func TestArguments(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
	}{
		{nil, 2},
		{[]string{"--help"}, 0},
		{[]string{"--unknown"}, 2},
		{[]string{"--version", "1.0.0"}, 2},
		{[]string{"--version", "1.0.0", "--commit", "bad", "--out", "x"}, 1},
		{[]string{"--version", "1.0.0", "--commit", "0123456789abcdef0123456789abcdef01234567", "--out", "x", "extra"}, 2},
	} {
		var output bytes.Buffer
		if got := run(context.Background(), tc.args, io.Discard, &output); got != tc.code {
			t.Fatalf("%q: code %d, want %d (%s)", tc.args, got, tc.code, output.String())
		}
	}
}
