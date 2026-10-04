package remote

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/submissions"
	"testing"
)

func TestSDKBackendUsesServingStatusReader(t *testing.T) {
	ctx := context.Background()
	want := submissions.Status{Version: 1, ID: "owned-submission", State: "running"}
	calls := 0
	b := &SDKBackend{ReadStatus: func(got context.Context, id string) (submissions.Status, error) {
		calls++
		if got != ctx || id != want.ID {
			t.Fatal("status request changed")
		}
		return want, nil
	}}
	got, err := b.Status(ctx, want.ID)
	if err != nil || got.ID != want.ID || got.State != want.State || calls != 1 {
		t.Fatal(got, err, calls)
	}
	sentinel := errors.New("store unavailable")
	b.ReadStatus = func(context.Context, string) (submissions.Status, error) { return submissions.Status{}, sentinel }
	if _, err = b.Status(ctx, want.ID); !errors.Is(err, sentinel) {
		t.Fatal("must preserve failure without falling back", err)
	}
}
