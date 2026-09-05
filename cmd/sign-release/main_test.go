package main

import (
	"bytes"
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
		{[]string{"--dir", "x"}, 2},
		{[]string{"--key", "x"}, 2},
		{[]string{"--dir", "x", "--key", "y", "extra"}, 2},
		{[]string{"--dir", t.TempDir(), "--key", "nonexistent-release-key"}, 1},
	} {
		var out bytes.Buffer
		if got := run(tc.args, &out); got != tc.code {
			t.Fatalf("%q: code %d, want %d", tc.args, got, tc.code)
		}
	}
}
