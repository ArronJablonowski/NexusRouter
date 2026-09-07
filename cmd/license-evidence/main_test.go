package main

import (
	"bytes"
	"context"
	"io"
	"testing"
)

func TestArguments(t *testing.T) {
	for _, test := range []struct {
		args []string
		code int
	}{
		{nil, 2}, {[]string{"unknown"}, 2}, {[]string{"freeze", "--help"}, 0},
		{[]string{"freeze"}, 2}, {[]string{"freeze", "--commit", "bad", "--out", "x"}, 1},
		{[]string{"verify", "--help"}, 0}, {[]string{"verify"}, 2},
		{[]string{"verify", "--record", "missing", "--record-sha256", "bad"}, 1},
		{[]string{"verify", "--record", "x", "--record-sha256", "bad", "extra"}, 2},
	} {
		var stderr bytes.Buffer
		if got := run(context.Background(), test.args, io.Discard, &stderr); got != test.code {
			t.Fatalf("%q: code %d want %d (%s)", test.args, got, test.code, stderr.String())
		}
	}
}
