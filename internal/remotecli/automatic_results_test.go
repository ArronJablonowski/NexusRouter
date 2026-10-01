package remotecli

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"strings"
	"testing"
)

func TestAutomaticResultsCLIRejectsUnknownOrTrailingInput(t *testing.T) {
	for _, op := range []string{"auto-status", "auto-cancel", "auto-output", "auto-reconcile", "auto-review"} {
		for _, body := range []string{`{`, `{} {}`, `{"unexpected":true}`} {
			_, e := automaticResultOperation(context.Background(), &remote.Client{}, op, "", "", "", "", strings.NewReader(body))
			if !errors.Is(e, remote.ErrInvalid) {
				t.Fatal(op, e)
			}
		}
	}
}
