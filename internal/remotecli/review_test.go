package remotecli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/remote"
)

func TestOperatorReviewFileStrictPrivateInput(t *testing.T) {
	review := remote.OutcomeReview{Version: 1, ReceiptSHA256: strings.Repeat("a", 64), Review: harness.Review{Version: 1, ID: "review-1", ExecutionDigest: strings.Repeat("b", 64), Verdict: "passed", Method: "automated_ai", MethodVersion: "rubric-v1", Reviewer: "operator-ai", Confidence: 1, Quality: 1, CreatedAt: time.Now().UTC()}}
	body, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "unknown", "trailing", "oversized", "public", "symlink", "relative"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "review.json")
			data := append([]byte(nil), body...)
			switch mode {
			case "unknown":
				data = []byte(strings.Replace(string(data), "{", "{\"untrusted_extra\":true,", 1))
			case "trailing":
				data = append(data, []byte("{}")...)
			case "oversized":
				data = []byte(strings.Repeat("x", 32769))
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "public" {
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "symlink" {
				link := path + ".link"
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			}
			if mode == "relative" {
				path = "relative-review.json"
			}
			got, err := readOutcomeReview(path)
			if (err == nil) != (mode == "valid") {
				t.Fatal(mode, err)
			}
			if err == nil && got != review {
				t.Fatal("review changed", got)
			}
		})
	}
}
