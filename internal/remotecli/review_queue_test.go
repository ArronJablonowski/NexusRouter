package remotecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/remote"
)

func TestReviewQueueCLIInvalidRequestsAreInert(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "absent")
	for _, operation := range []string{"auto-dispatch-review-job", "enqueue-review", "enqueue-auto-review", "run-review-jobs", "review-job-status"} {
		if err := Run(ctx, []string{operation, "--review-queue", root}, strings.NewReader("{}"), &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatal(operation)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatal("invalid CLI created queue", operation, err)
		}
	}
	if _, err := runReviewQueueOperation(ctx, &remote.Client{}, root, root, root, "config", "judge", -1, time.Hour); err != remote.ErrInvalid {
		t.Fatal(err)
	}
	if _, err := enqueueReviewOperation(ctx, &remote.Client{}, false, root, root, root, "request-key-0001", "config", "judge", 0, time.Now().Add(25*time.Hour), strings.NewReader("{}")); err != remote.ErrInvalid {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestReviewQueueReportsPartialProgressOnFailure(t *testing.T) {
	states := []remote.ReviewJobStatus{{Version: 1, RequestID: "completed-request", Status: "completed", ReviewApplied: true}, {Version: 1, RequestID: "pending-request", Status: "pending"}}
	var output bytes.Buffer
	err := writeReviewQueueResult(&output, "run-review-jobs", states, context.DeadlineExceeded)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost operation error: %v", err)
	}
	var got []remote.ReviewJobStatus
	if err = json.Unmarshal(output.Bytes(), &got); err != nil || len(got) != 2 || got[0] != states[0] || got[1] != states[1] {
		t.Fatalf("lost partial progress: %s, %v", output.Bytes(), err)
	}
	if err = writeReviewQueueResult(failedReviewOutput{}, "run-review-jobs", states, context.DeadlineExceeded); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("lost output failure: %v", err)
	}
	output.Reset()
	if err = writeReviewQueueResult(&output, "enqueue-review", nil, remote.ErrInvalid); err != remote.ErrInvalid || output.Len() != 0 {
		t.Fatal("invalid enqueue must remain silent", err, output.String())
	}
}

type failedReviewOutput struct{}

func (failedReviewOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
