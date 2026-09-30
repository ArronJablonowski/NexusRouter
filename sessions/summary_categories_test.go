package sessions

import (
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestAdditionalSummaryCategoriesValidationAndOwnership(t *testing.T) {
	for _, category := range []string{"requirements", "activity"} {
		t.Run(category, func(t *testing.T) {
			summary := func(items []string) Summary {
				if category == "requirements" {
					return Summary{Requirements: items}
				}
				return Summary{Activity: items}
			}
			check := func(s Summary) (error, error) {
				requestErr := ValidateCompactionRequest(&CompactionRequest{Keep: 1, Summary: s})
				c := runtime.ContextCompaction{Version: 1, SourceTaskID: "source", SourceSequence: 4, SourceDigest: strings.Repeat("a", 64), RemovedMessages: 1, Summary: s}
				return requestErr, c.Validate("source")
			}
			items := []string{"retained detail"}
			if a, b := check(summary(items)); a != nil || b != nil {
				t.Fatalf("single category rejected: %v %v", a, b)
			}
			messages := []providers.Message{{Role: "user", Content: "old"}, {Role: "assistant", Content: "recent"}}
			out, err := Compact(messages, 1, summary(items))
			if err != nil {
				t.Fatal(err)
			}
			items[0] = "mutated"
			got := out.Summary.Requirements
			if category == "activity" {
				got = out.Summary.Activity
			}
			if len(got) != 1 || got[0] != "retained detail" {
				t.Fatal("summary aliases caller slice")
			}
			maxItems := make([]string, 128)
			for i := range maxItems {
				maxItems[i] = "detail"
			}
			if a, b := check(summary(maxItems)); a != nil || b != nil {
				t.Fatalf("128 items rejected: %v %v", a, b)
			}
			for _, invalid := range [][]string{{" "}, {"\xff"}, {strings.Repeat("x", 64<<10)}, append(maxItems, "excess")} {
				if a, b := check(summary(invalid)); a == nil || b == nil {
					t.Fatalf("invalid category accepted: %v %v", a, b)
				}
				if _, err := Compact(messages, 1, summary(invalid)); err == nil {
					t.Fatal("compact accepted invalid category")
				}
			}
		})
	}
	combined := Summary{Requirements: []string{strings.Repeat("a", 33<<10)}, Activity: []string{strings.Repeat("b", 33<<10)}}
	if ValidateCompactionRequest(&CompactionRequest{Keep: 1, Summary: combined}) == nil {
		t.Fatal("combined category budget bypassed")
	}
	c := runtime.ContextCompaction{Version: 1, SourceTaskID: "source", SourceSequence: 4, SourceDigest: strings.Repeat("a", 64), RemovedMessages: 1, Summary: combined}
	if c.Validate("source") == nil {
		t.Fatal("runtime combined category budget bypassed")
	}
}
