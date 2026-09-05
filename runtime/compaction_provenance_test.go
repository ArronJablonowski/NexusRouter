package runtime

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompactionOptionalProvenanceValidation(t *testing.T) {
	legacy := ContextCompaction{Version: 1, SourceTaskID: "source", SourceSequence: 9, SourceDigest: strings.Repeat("a", 64), RemovedMessages: 1, Summary: ContextSummary{Requirements: []string{"retain"}}}
	if err := legacy.Validate("source"); err != nil {
		t.Fatal("legacy record rejected", err)
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"first_retained_message", "first_retained_sequence", "before_context_tokens", "after_context_tokens"} {
		if strings.Contains(string(encoded), key) {
			t.Fatalf("legacy optional field emitted: %s", key)
		}
	}
	valid := legacy
	valid.FirstRetainedMessage, valid.FirstRetainedSequence, valid.BeforeContextTokens, valid.AfterContextTokens = 2, 4, 100, 120
	if err := valid.Validate("source"); err != nil {
		t.Fatal("positive estimates may increase for short contexts", err)
	}
	for _, mutate := range []func(*ContextCompaction){
		func(c *ContextCompaction) { c.FirstRetainedMessage = -1 },
		func(c *ContextCompaction) { c.FirstRetainedSequence = -1 },
		func(c *ContextCompaction) { c.FirstRetainedSequence = 10 },
		func(c *ContextCompaction) { c.BeforeContextTokens = -1 },
		func(c *ContextCompaction) { c.AfterContextTokens = -1 },
		func(c *ContextCompaction) { c.BeforeContextTokens = 0 },
		func(c *ContextCompaction) { c.AfterContextTokens = 0 },
	} {
		invalid := valid
		mutate(&invalid)
		if invalid.Validate("source") == nil {
			t.Fatalf("invalid provenance accepted: %+v", invalid)
		}
	}
}
