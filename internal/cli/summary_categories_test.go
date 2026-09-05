package cli

import (
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestCLIAdditionalSummaryCategories(t *testing.T) {
	for _, category := range []string{"requirements", "activity"} {
		summary, err := readCompactionSummary(compactSummaryFile(t, `{"`+category+`":["retained detail"]}`))
		if err != nil || sessions.ValidateCompactionRequest(&sessions.CompactionRequest{Keep: 1, Summary: summary}) != nil {
			t.Fatalf("category %s rejected: %v", category, err)
		}
		got := summary.Requirements
		if category == "activity" {
			got = summary.Activity
		}
		if len(got) != 1 || got[0] != "retained detail" {
			t.Fatal("category not forwarded")
		}
		for _, body := range []string{`{"` + category + `":null}`, `{"` + category + `":[1]}`, `{"` + category + `":["a"],"` + category + `":["b"]}`} {
			if _, err := readCompactionSummary(compactSummaryFile(t, body)); err == nil {
				t.Fatal("invalid category accepted")
			}
		}
	}
}
