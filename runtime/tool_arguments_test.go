package runtime

import (
	"encoding/json"
	"testing"
)

func TestCanonicalToolArgumentsStableAndPrecise(t *testing.T) {
	raw := json.RawMessage(" { \"z\" : {\"n\":9007199254740993,\"a\":1}, \"a\" : true } ")
	got, err := canonicalToolArguments(raw)
	if err != nil || string(got) != `{"a":true,"z":{"a":1,"n":9007199254740993}}` {
		t.Fatalf("canonical arguments = %q, %v", got, err)
	}
	again, err := canonicalToolArguments(got)
	if err != nil || string(again) != string(got) {
		t.Fatalf("canonicalization is not idempotent: %q, %v", again, err)
	}
}

func TestCanonicalToolArgumentsRejectsAmbiguity(t *testing.T) {
	for _, raw := range []string{
		`{"a":1,"a":2}`,
		`{"outer":{"a":1,"a":2}}`,
		`{"items":[{"a":1,"a":2}]}`,
		`[]`,
		`{"a":1} trailing`,
	} {
		if got, err := canonicalToolArguments(json.RawMessage(raw)); err == nil || got != nil {
			t.Fatalf("accepted ambiguous arguments %q as %q", raw, got)
		}
	}
}
