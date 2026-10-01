package harness

import (
	"strings"
	"testing"
)

func TestReviewStateCurrentHeadsAndCopies(t *testing.T) {
	e, r := observation("output", identity("model", "pi"), testClass, true)
	d, _ := e.Digest()
	for _, kind := range []string{"pending", "unverified", "withdrawn", "advisory", "confirmed", "non_quality"} {
		t.Run(kind, func(t *testing.T) {
			execution := e
			reviews := []Review{}
			switch kind {
			case "confirmed":
				reviews = []Review{r}
			case "advisory":
				r2 := r
				r2.Method = "automated_ai"
				reviews = []Review{r2}
			case "unverified":
				reviews = []Review{{Version: 1, ID: "abstain", ExecutionDigest: d, Verdict: "unverified", Reviewer: "auditor", CreatedAt: testNow}}
			case "withdrawn":
				reviews = []Review{r, {Version: 1, ID: "withdraw", ExecutionDigest: d, ExpectedHead: r.ID, Verdict: "withdrawn", Reviewer: "operator", CreatedAt: testNow}}
			case "non_quality":
				execution.Status = "infrastructure_failed"
				execution.OutputSHA256 = ""
			}
			digest, _ := execution.Digest()
			s, err := Replay([]Execution{execution}, reviews, testNow)
			if err != nil {
				t.Fatal(err)
			}
			got, found, err := s.ReviewState(digest)
			if err != nil || !found || got.Classification != kind {
				t.Fatal(got, found, err)
			}
			if got.Head != nil {
				original := got.Head.ID
				got.Head.ID = "mutated"
				again, _, err := s.ReviewState(digest)
				if err != nil || again.Head.ID != original {
					t.Fatal("mutable snapshot", again, err)
				}
			}
			if _, found, err := s.ReviewState(strings.Repeat("f", 64)); err != nil || found {
				t.Fatal("missing became recorded", found, err)
			}
		})
	}
}
