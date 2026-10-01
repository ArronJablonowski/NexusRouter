package harness

import "time"

// ReviewState is a copy from a fully validated ledger snapshot. Head.ID is the
// expected-current-head token for revisions, not permission to change feedback.
type ReviewState struct {
	Version         int
	ExecutionDigest string
	Execution       Execution
	Head            *Review
	Classification  string // pending, unverified, withdrawn, advisory, confirmed, non_quality
	AsOf            time.Time
}

func (s *Snapshot) ReviewState(executionDigest string) (ReviewState, bool, error) {
	if s == nil || s.entries == nil || !digest(executionDigest) {
		return ReviewState{}, false, ErrInvalid
	}
	entry, ok := s.entries[executionDigest]
	if !ok {
		return ReviewState{}, false, nil
	}
	out := ReviewState{Version: Version, ExecutionDigest: executionDigest, Execution: entry.execution, AsOf: s.asOf, Classification: "pending"}
	if entry.execution.Status != "completed" {
		out.Classification = "non_quality"
	}
	if entry.head != nil {
		copy := *entry.head
		out.Head = &copy
		switch copy.Verdict {
		case "unverified", "withdrawn":
			out.Classification = copy.Verdict
		default:
			out.Classification = "confirmed"
			if copy.Method == "automated_ai" {
				out.Classification = "advisory"
			}
		}
	}
	return out, true, nil
}
