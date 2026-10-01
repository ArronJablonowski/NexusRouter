package remotecli

import (
	"bytes"
	"context"
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
