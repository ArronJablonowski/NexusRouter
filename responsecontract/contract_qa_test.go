package responsecontract_test

import (
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/responsecontract"
)

func TestEndingMarkerDoesNotInventANewLineRequirement(t *testing.T) {
	contract := responsecontract.Infer("Explain briefly. End your answer with STATUS: <status>.")
	if !contract.Active() {
		t.Fatal("explicit marker contract was lost")
	}
	for _, answer := range []string{
		"The service is ready. STATUS: ready",
		"The service is ready.\nSTATUS: ready",
	} {
		if got := contract.Validate(answer); len(got) != 0 {
			t.Fatalf("valid requested suffix was forced onto a new line: %q %v", answer, got)
		}
	}
	if strings.Contains(contract.Instructions(), "final line must start") {
		t.Fatal("reminder invented a line-start requirement")
	}
	if got := contract.Validate("STATUS: ready\nUnrelated text after the required ending."); len(got) == 0 {
		t.Fatal("the marker was not at the ending of the answer")
	}
}

func TestExplicitCodeFenceRequestCannotCreateConflictingRepair(t *testing.T) {
	for _, prompt := range []string{
		"Write only Python code in a Markdown code block.",
		"Return only Python code. Wrap the code in a fenced code block.",
	} {
		contract := responsecontract.Infer(prompt)
		if got := contract.Validate("```python\nanswer = 42\n```"); len(got) != 0 {
			t.Fatalf("explicitly requested code fence rejected: %q %v", prompt, got)
		}
		if strings.Contains(contract.Instructions(), "without Markdown fences") {
			t.Fatalf("host reminder contradicted user fence requirement: %q", prompt)
		}
	}
}

func TestPythonMultilineLiteralCanContainMarkdownFences(t *testing.T) {
	contract := responsecontract.Infer("Return only Python code for a fixture.")
	answer := "payload = \"\"\"\n```json\n{}\n```\n\"\"\"\nprint(payload)"
	if got := contract.Validate(answer); len(got) != 0 {
		t.Fatalf("string-literal data was mistaken for a response wrapper: %v", got)
	}
}

func TestPythonExpressionsAreCodePresentation(t *testing.T) {
	contract := responsecontract.Infer("Return only Python code for the expression.")
	for _, answer := range []string{"-1", "~mask", "not flag", "result", "here = 1", "sure()", "below.value"} {
		if got := contract.Validate(answer); len(got) != 0 {
			t.Fatalf("valid Python expression rejected as presentation: %q %v", answer, got)
		}
	}
}

func TestCodeFenceBoundariesAndExplicitProhibition(t *testing.T) {
	contract := responsecontract.Infer("Return only Python code. Keep under 3 lines.")
	for _, answer := range []string{"```python\nx = 1\ny = 2\n```", "~~~python\nx = 1\n~~~"} {
		if got := contract.Validate(answer); len(got) != 0 {
			t.Fatalf("valid code wrapper rejected or counted as source lines: %q %v", answer, got)
		}
	}
	for _, answer := range []string{"```python\nx = 1", "```python\nx = 1\n```\nExplanation", "```python\nx = 1\n```\n```python\ny = 2\n```"} {
		if got := contract.Validate(answer); len(got) == 0 {
			t.Fatalf("unclosed/extra wrapper or surrounding prose accepted: %q", answer)
		}
	}
	for _, restriction := range []string{"No Markdown.", "Do not use code fences.", "Return the answer without Markdown fences."} {
		contract := responsecontract.Infer("Return only Python code. " + restriction)
		if got := contract.Validate("```python\nx = 1\n```"); !has(got, responsecontract.CodePresentation) {
			t.Fatalf("explicit no-fence instruction ignored: %q %v", restriction, got)
		}
		if got := contract.Validate("x = 1"); len(got) != 0 {
			t.Fatal("raw code rejected", got)
		}
	}
}

func TestEndingMarkerRejectsAmbiguousOrDecoratedEndings(t *testing.T) {
	contract := responsecontract.Infer("End with STATUS: <status>.")
	for _, answer := range []string{"STATUS: pending STATUS: ready", "STATUS: <status>", "**STATUS: ready**", "The service is ready. `STATUS: ready`", "The service is ready. STATUS:", "The service is ready. STATUS: ready\nMore prose."} {
		if got := contract.Validate(answer); !has(got, responsecontract.MissingFinalLine) {
			t.Fatalf("invalid ending accepted: %q %v", answer, got)
		}
	}
}
