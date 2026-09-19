package skills

import (
	"context"
	"testing"
)

func TestLifecycleObservationsAreBoundedContentFreeAndNewestFirst(t *testing.T) {
	ctx := context.Background()
	store := openTest(t, testPath(t))
	draft := sample()
	first, err := store.Draft(ctx, draft, false)
	if err != nil || store.Activate(ctx, draft.Key, first.ID, "", pass, false) != nil {
		t.Fatal(first, err)
	}
	draft.Steps = []string{"Run newer checks"}
	second, err := store.Draft(ctx, draft, false)
	if err != nil || store.Activate(ctx, draft.Key, second.ID, first.ID, pass, false) != nil || store.Rollback(ctx, draft.Key, second.ID, false) != nil {
		t.Fatal(second, err)
	}
	observations, err := store.LifecycleObservations(ctx, 2)
	if err != nil || len(observations) != 2 || observations[0].Kind != "rolled_back" || observations[1].Kind != "activated" || observations[0].At.Before(observations[1].At) {
		t.Fatal(observations, err)
	}
	for _, item := range observations {
		if item.Kind != "activated" && item.Kind != "rolled_back" || item.At.IsZero() {
			t.Fatal(item)
		}
	}
}

func TestLifecycleObservationsRejectInvalidBounds(t *testing.T) {
	store := openTest(t, testPath(t))
	for _, limit := range []int{0, 101} {
		if out, err := store.LifecycleObservations(context.Background(), limit); err == nil || out != nil {
			t.Fatal(limit, out, err)
		}
	}
}
