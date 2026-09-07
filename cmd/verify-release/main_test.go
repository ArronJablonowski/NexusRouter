package main

import (
	"bytes"
	"strings"
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
		{[]string{"--public-key", "x"}, 2},
		{[]string{"--dir", "x", "--trust-record", "y"}, 2},
		{[]string{"--dir", "x", "--key-id", "release-2026-01"}, 2},
		{[]string{"--dir", "x", "--trust-record", "y", "--key-id", "release-2026-01"}, 2},
		{[]string{"--dir", "x", "--trust-record", "y", "--key-id", "release-2026-01", "--key-fingerprint", "sha256:x"}, 2},
		{[]string{"--dir", "x", "--public-key", "y", "--key-fingerprint", "sha256:x"}, 2},
		{[]string{"--dir", "x", "--public-key", "y", "--trust-record", "z", "--trust-record-sha256", "sha256:x", "--key-id", "release-2026-01", "--key-fingerprint", "sha256:x"}, 2},
		{[]string{"--dir", "x", "--public-key", "y", "extra"}, 2},
		{[]string{"--dir", t.TempDir(), "--public-key", "nonexistent-trusted-key"}, 1},
		{[]string{"--dir", t.TempDir(), "--trust-record", "nonexistent-trusted-record", "--trust-record-sha256", "sha256:" + strings.Repeat("0", 64), "--key-id", "release-2026-01", "--key-fingerprint", "sha256:" + strings.Repeat("0", 64)}, 1},
	} {
		var out bytes.Buffer
		if got := run(tc.args, &out); got != tc.code {
			t.Fatalf("%q: code %d, want %d", tc.args, got, tc.code)
		}
	}
}
