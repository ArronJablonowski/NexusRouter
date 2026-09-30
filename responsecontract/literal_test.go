package responsecontract_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/responsecontract"
)

func TestExplicitLiteralResponsePreservesTextWithoutWrappers(t *testing.T) {
	for _, prefix := range []string{"Reply with exactly: ", "Respond with exactly: ", "Return exactly: ", "Output exactly: ", "Emit exactly: ", "Request label. Reply with exactly: ", "Reply with exactly this token and nothing else: ", "Respond with exactly the following text: "} {
		for _, literal := range []string{"ACK_37", "Hello!", "A. B; C?", "mañana ✓", "two  spaces", "don't change this"} {
			contract := responsecontract.Infer(prefix + literal)
			if !contract.Active() {
				t.Fatalf("explicit literal was not recognized: %q", prefix+literal)
			}
			for _, accepted := range []string{literal, literal + "\n", " \n" + literal + "\r\n"} {
				if got := contract.Validate(accepted); len(got) != 0 {
					t.Fatalf("literal spelling/punctuation changed: %q %v", accepted, got)
				}
			}
			for _, rejected := range []string{"", "Summary\n\nFinal\n```\n" + literal + "\n```", literal + ".", "Answer: " + literal, literal + "\nMore text"} {
				if got := contract.Validate(rejected); !has(got, responsecontract.LiteralMismatch) {
					t.Fatalf("literal wrapper or different answer accepted: %q %v", rejected, got)
				}
			}
		}
	}
}

func TestLiteralInferenceAbstainsOnConflictsAndAmbiguousTargets(t *testing.T) {
	for _, prompt := range []string{
		"Reply with exactly:",
		"Reply with exactly: <value>",
		"Reply with exactly: \"hello\"",
		"Reply with exactly: `hello`",
		"Reply with exactly: 'hello'",
		"Reply with exactly: hello\tworld",
		"Reply with exactly: hello\x00world",
		"Reply with exactly: " + strings.Repeat("x", 257),
		"Reply with exactly: ready if the check passes",
		"Reply with exactly: ready. Explain why.",
		"Reply with exactly: ready\nExplain why.",
		"Reply with exactly: first\nReply with exactly: second",
		"Reply with exactly: first. Reply with exactly: second",
		"Return only JSON. Reply with exactly: ready",
		"Write only Python code. Reply with exactly: ready",
		"End with RESULT: <value>. Reply with exactly: ready",
		"Use at most 2 lines. Reply with exactly: ready",
	} {
		if got := responsecontract.Infer(prompt); got.Active() {
			t.Fatalf("ambiguous/conflicting literal became an enforced target: %q", prompt)
		}
	}
}

func TestLiteralDiagnosticsNeverEchoPrivateTargetsOrCandidates(t *testing.T) {
	contract := responsecontract.Infer("Reply with exactly: PRIVATE_CANARY_481")
	violations := contract.Validate("PRIVATE_CANDIDATE_913")
	text := contract.Instructions() + contract.Guidance(violations) + fmt.Sprint(violations)
	if !has(violations, responsecontract.LiteralMismatch) || strings.Contains(text, "PRIVATE_") || len(text) > 2048 {
		t.Fatal("literal validation leaked target/candidate or unbounded instructions", text)
	}
}
