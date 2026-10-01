package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/submissions"
	"testing"
)

// Historical control envelopes remain readable for recovery; admitting the
// current envelope does not bypass the application's exact configuration fence.
func TestControlEnvelopeVersionsDurableAdmission(t *testing.T) {
	ctx := context.Background()
	db, _ := submissionStore(t)
	resume := recoveredModelSource(t, db, "local_only")
	resumeBody, _ := resumeEnvelope(t, resume, true)
	completedBranchSource(t, db, "branch-source", "local_only")
	branch, e := db.BranchSource(ctx, "branch-source")
	if e != nil {
		t.Fatal(e)
	}
	branchBody, _ := branchEnvelope(t, branch, true)
	for _, version := range []int{1, 2, 3, 4, 5, 6, 7, 8} {
		for _, kind := range []string{"branch", "resume"} {
			t.Run(fmt.Sprintf("%s-v%d", kind, version), func(t *testing.T) {
				raw := branchBody
				if kind == "resume" {
					raw = resumeBody
				}
				raw = bytes.Replace(raw, []byte(`"version":2`), []byte(fmt.Sprintf(`"version":%d`, version)), 1)
				sum := sha256.Sum256(raw)
				key := submitDigest(fmt.Sprintf("%s-v%d", kind, version))
				config := submitDigest("config")
				var status submissions.Status
				var err error
				if kind == "resume" {
					status, err = db.CreateResumeSubmission(ctx, key, hex.EncodeToString(sum[:]), config, raw)
				} else {
					status, err = db.CreateBranchSubmission(ctx, key, hex.EncodeToString(sum[:]), config, raw)
				}
				if version == 1 || version == 8 {
					if !errors.Is(err, submissions.ErrInvalid) {
						t.Fatal("unsupported envelope accepted", status, err)
					}
					return
				}
				if err != nil || status.State != "queued" {
					t.Fatal(status, err)
				}
			})
		}
	}
}
