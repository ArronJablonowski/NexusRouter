package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/submissions"
	"strings"
	"testing"
)

func TestTaskInventoryLifecycleOwnershipAndUncertainDelivery(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	key := "owned-request-0001"
	status, err := f.client.Dispatch(ctx, "node-a", key, testTask())
	if err != nil {
		t.Fatal(err)
	}
	// A different caller's durable reservation must never be visible, even when
	// its key sorts within this page. Unbound requests are still owned records.
	if _, err = f.journal.reserve(ctx, "other-caller", "owned-request-0002", "other"); err != nil {
		t.Fatal(err)
	}
	f.backend.mu.Lock()
	f.backend.lost = true
	f.backend.mu.Unlock()
	if _, err = f.client.Dispatch(ctx, "node-a", "owned-request-0003", testTask()); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	page, err := f.client.Tasks(ctx, "node-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Tasks) != 2 || page.Tasks[0].RequestID != key || page.Tasks[0].State != "queued" || page.Tasks[1].State != "unknown" {
		t.Fatalf("%+v", page)
	}
	f.backend.mu.Lock()
	creates := f.backend.creates
	f.backend.mu.Unlock()
	if creates != 2 {
		t.Fatal(creates)
	}
	// Listing must not replay the ambiguous dispatch or turn it into a rejection.
	if _, err = f.client.Tasks(ctx, "node-a", ""); err != nil {
		t.Fatal(err)
	}
	f.backend.mu.Lock()
	if f.backend.creates != creates {
		t.Error("listing dispatched work")
	}
	f.backend.mu.Unlock()
	if _, err = f.client.Cancel(ctx, "node-a", key); err != nil {
		t.Fatal(err)
	}
	page, err = f.client.Tasks(ctx, "node-a", "")
	if err != nil || page.Tasks[0].State != "canceled" || !page.Tasks[0].CancelRequested {
		t.Fatal(page, err)
	}
	// Even completed results are projected into metadata only.
	f.backend.mu.Lock()
	s := f.backend.tasks[status.ID]
	s.State = "succeeded"
	s.Result = &submissions.Result{Text: "private-result-sentinel"}
	f.backend.tasks[status.ID] = s
	f.backend.mu.Unlock()
	page, err = f.client.Tasks(ctx, "node-a", "")
	if err != nil || page.Tasks[0].State != "succeeded" {
		t.Fatal(page, err)
	}
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "private test prompt") || strings.Contains(string(raw), "config_digest") || strings.Contains(string(raw), "result") {
		t.Fatal(string(raw))
	}
	var audits int
	if err = f.journal.db.QueryRow("SELECT count(*) FROM audit WHERE caller='node-b' AND action='inspect' AND outcome='succeeded'").Scan(&audits); err != nil || audits < 3 {
		t.Fatal(audits, err)
	}
	// Revocation of inspect is checked at the destination even when the client
	// registry continues to authorize the operation.
	peer := f.clientPeer
	peer.Operations = []string{"info", "dispatch"}
	writeRegistry(t, f.serverTrust, peer)
	if _, err = f.client.Tasks(ctx, "node-a", ""); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}

func TestTaskInventoryPaginationAndInvalidCursor(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for i := 0; i < 101; i++ {
		if _, err := f.journal.reserve(ctx, "node-b", fmt.Sprintf("request-%016d", i), "digest"); err != nil {
			t.Fatal(err)
		}
	}
	first, err := f.client.Tasks(ctx, "node-a", "")
	if err != nil || len(first.Tasks) != 100 || !first.HasMore {
		t.Fatal(first, err)
	}
	next, err := f.client.Tasks(ctx, "node-a", first.Next)
	if err != nil || len(next.Tasks) != 1 || next.HasMore || next.Tasks[0].RequestID <= first.Next {
		t.Fatal(next, err)
	}
	empty, err := f.client.Tasks(ctx, "node-a", next.Next)
	if err != nil || len(empty.Tasks) != 0 || empty.Next != next.Next {
		t.Fatal(empty, err)
	}
	if _, err = f.client.Tasks(ctx, "node-a", "../bad"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	var out TaskPage
	err = f.client.call(ctx, "node-a", "inspect", "GET", "/v1/remote/tasks", nil, map[string]string{"X-Nexus-After-Request": "../bad"}, &out)
	if !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestTaskInventoryUnavailableBackendStaysUnknown(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	key := "unavailable-request-001"
	if _, err := f.journal.reserve(ctx, "node-b", key, "digest"); err != nil {
		t.Fatal(err)
	}
	if err := f.journal.bind(ctx, "node-b", key, "missing-submission"); err != nil {
		t.Fatal(err)
	}
	page, err := f.client.Tasks(ctx, "node-a", "")
	if err != nil || len(page.Tasks) != 1 || page.Tasks[0].State != "unknown" {
		t.Fatal(page, err)
	}
}
