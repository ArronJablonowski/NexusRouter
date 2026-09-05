package app

import (
	"reflect"
	"strings"
	"testing"
)

func TestTextRedactorEveryChunkPartition(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		secrets []string
	}{
		{"empty", "", nil},
		{"no secrets", "plain text", nil},
		{"empty secret", "abcabc", []string{"", "abc", ""}},
		{"partial end", "xabcab", []string{"abc"}},
		{"overlapping prefixes", "ababababac", []string{"ababac", "aba", "ab"}},
		{"repeated prefix", "aaaaaabaa", []string{"aaaab", "aaa"}},
		{"overlapping matches", "ababa", []string{"aba"}},
		{"equal length order", "abcab", []string{"bc", "ab"}},
		{"unicode bytes", "x秘密y密", []string{"秘密", "密"}},
		{"replacement contents", "secret!", []string{"secret", "ACT", "["}},
		{"replacement suffix", "secretx", []string{"secret", "]x"}},
		{"replacement prefix", "xsecret", []string{"secret", "x["}},
		{"replacement itself", "[REDACTED]", []string{"[REDACTED]", "ACT"}},
		{"duplicate stages", "xx", []string{"x", "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := redact(tc.input, tc.secrets)
			partitions := 1
			if len(tc.input) > 0 {
				partitions <<= len(tc.input) - 1
			}
			for mask := 0; mask < partitions; mask++ {
				r := newTextRedactor(tc.secrets)
				var got strings.Builder
				start := 0
				for end := 1; end <= len(tc.input); end++ {
					if end != len(tc.input) && mask&(1<<(end-1)) == 0 {
						continue
					}
					got.WriteString(r.Write(tc.input[start:end]))
					if !strings.HasPrefix(want, got.String()) {
						t.Fatalf("partition %d released unsafe prefix %q; final %q", mask, got.String(), want)
					}
					if r.Write("") != "" {
						t.Fatal("empty write released pending text")
					}
					start = end
				}
				got.WriteString(r.Flush())
				if got.String() != want {
					t.Fatalf("partition %d: got %q, want %q", mask, got.String(), want)
				}
				if r.Flush() != "" {
					t.Fatal("second flush was not empty")
				}
			}
		})
	}
}

func TestTextRedactorReuseAndInputOwnership(t *testing.T) {
	secrets := []string{"b", "abcdef", "abc"}
	original := append([]string(nil), secrets...)
	r := newTextRedactor(secrets)
	if !reflect.DeepEqual(secrets, original) {
		t.Fatal("constructor reordered caller's secrets")
	}
	secrets[1] = "changed"
	for _, input := range []string{"ab", "cdef", "abcdef", "abc"} {
		got := r.Write(input) + r.Flush()
		if want := redact(input, original); got != want {
			t.Fatalf("independent text %q: got %q, want %q", input, got, want)
		}
	}
}

func TestTextRedactorLongOverlappingPrefix(t *testing.T) {
	secret := strings.Repeat("a", 4096) + "b"
	input := strings.Repeat("a", 65536) + "b-tail"
	r := newTextRedactor([]string{secret})
	var got strings.Builder
	for i := 0; i < len(input); i += 17 {
		got.WriteString(r.Write(input[i:min(i+17, len(input))]))
		if r.stages[0].matched >= len(secret) {
			t.Fatal("pending prefix exceeded secret bound")
		}
	}
	got.WriteString(r.Flush())
	if got.String() != redact(input, []string{secret}) {
		t.Fatal("long overlapping prefix differs from full literal replacement")
	}
}

func FuzzTextRedactor(f *testing.F) {
	f.Add("ababababac", "ababac", "aba", uint8(3))
	f.Add("secretxsecret", "secret", "]x", uint8(1))
	f.Add("秘密hello秘密", "秘密", "ACT", uint8(2))
	f.Fuzz(func(t *testing.T, input, first, second string, width uint8) {
		if len(input) > 65536 || len(first)+len(second) > 8192 {
			t.Skip()
		}
		secrets := []string{first, second}
		r := newTextRedactor(secrets)
		var got strings.Builder
		step := int(width) + 1
		for i := 0; i < len(input); i += step {
			got.WriteString(r.Write(input[i:min(i+step, len(input))]))
		}
		got.WriteString(r.Flush())
		if want := redact(input, secrets); got.String() != want {
			t.Fatalf("got %q, want %q", got.String(), want)
		}
	})
}
