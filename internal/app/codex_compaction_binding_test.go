package app

import (
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func TestCodexCompactionRequiresResolvedCheckpoint(t *testing.T) {
	for _, kind := range []string{"manual", "reviewed"} {
		for _, mutation := range []string{"valid", "unresolved", "no_checkpoint", "foreign", "invalid", "private", "mixed", "wrong_attempt", "missing_review", "unrequested"} {
			t.Run(kind+"/"+mutation, func(t *testing.T) {
				summary := sessions.Summary{Decisions: []string{"Retain plan"}}
				checkpoint := &runtime.ContextCompaction{Version: 1, SourceTaskID: "source", SourceSequence: 6, SourceDigest: strings.Repeat("a", 64), RemovedMessages: 1, Summary: summary}
				r := Request{ContinueTaskID: "source", Compaction: &sessions.CompactionRequest{Keep: 1, Summary: summary}, continuation: &continuationContext{Privacy: "cloud_allowed", Compaction: checkpoint}}
				if kind == "reviewed" {
					r.Compaction, r.SummaryAttemptID = nil, "draft"
					checkpoint.SummaryAttemptID, checkpoint.SummaryReviewID = "draft", "review"
				}
				switch mutation {
				case "unresolved":
					r.continuation = nil
				case "no_checkpoint":
					r.continuation.Compaction = nil
				case "foreign":
					checkpoint.SourceTaskID = "other"
				case "invalid":
					checkpoint.SourceDigest = "invalid"
				case "private":
					r.continuation.Privacy = "local_only"
				case "mixed":
					r.Compaction, r.SummaryAttemptID = &sessions.CompactionRequest{Keep: 1, Summary: summary}, "draft"
				case "wrong_attempt":
					checkpoint.SummaryAttemptID, checkpoint.SummaryReviewID = "other", "review"
				case "missing_review":
					checkpoint.SummaryAttemptID, checkpoint.SummaryReviewID = "draft", ""
				case "unrequested":
					r.Compaction, r.SummaryAttemptID = nil, ""
				}
				if got := codexCompactionReady(r); got != (mutation == "valid") {
					t.Fatal("incorrect checkpoint binding", got)
				}
			})
		}
	}
	if !codexCompactionReady(Request{}) || !codexCompactionReady(Request{ContinueTaskID: "source", continuation: &continuationContext{Privacy: "cloud_allowed"}}) {
		t.Fatal("ordinary task/history changed")
	}
}
