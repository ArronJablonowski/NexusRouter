package remote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/harness"
)

func TestRecordedStatusRetainsDestinationCallerAndDoesNotRepair(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "routes")
	store, err := OpenRouteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	key := "recorded-status-0001"
	submitted, err := f.client.DispatchRecorded(ctx, store, "node-a", key, testTask())
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.path(key))
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExistingRouteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.client.InspectRecorded(ctx, reopened, key)
	if err != nil || result.Destination != "node-a" || result.RequestID != key || result.Status.ID != submitted.ID {
		t.Fatal(result, err)
	}
	after, _ := os.ReadFile(store.path(key))
	if string(before) != string(after) || f.backend.creates != 1 {
		t.Fatal("inspection changed intent or dispatched")
	}
	f.backend.lost = true
	_, err = f.client.DispatchRecorded(ctx, store, "node-a", "recorded-uncertain-01", testTask())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	_, err = f.client.InspectRecorded(ctx, store, "recorded-uncertain-01")
	if err == nil || f.backend.creates != 2 {
		t.Fatal("ambiguous intake repaired or hidden", err)
	}
	original := f.client.Credentials
	replacement, _ := f.ca.leaf(t, "replacement-caller")
	f.client.Credentials = replacement
	if _, err = f.client.InspectRecorded(ctx, store, key); !errors.Is(err, ErrConflict) {
		t.Fatal("caller rotation accepted", err)
	}
	f.client.Credentials = original
	// Registry updates require a private parent; setup only prepares read access.
	if err = os.Chmod(filepath.Dir(string(f.client.Trust)), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = f.client.Trust.Revoke("node-a", mustRegistry(t, f.client.Trust).Digest()); err != nil {
		t.Fatal(err)
	}
	if _, err = f.client.InspectRecorded(ctx, store, key); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked peer accepted", err)
	}
}
func mustRegistry(t *testing.T, trust TrustFile) Registry {
	t.Helper()
	r, e := trust.Read()
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestExistingRouteStoreNeverCreatesMissingEvidence(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	if _, err := OpenExistingRouteStore(dir); err == nil {
		t.Fatal("missing evidence opened")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("inspection created directory", err)
	}
}
func TestRecordedStatusInspectsAutomaticChoiceWithoutOriginalPrompt(t *testing.T) {
	f, routes, request, candidates, a, b := automaticFixture(t)
	key := "automatic-readonly-01"
	ctx := context.Background()
	submitted, choice, err := f.client.DispatchAutomatic(ctx, routes, filepath.Join(t.TempDir(), "evidence"), key, request, harness.DefaultPolicy(), candidates[:1], 0)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.client.InspectRecorded(ctx, routes, key)
	if err != nil || result.Destination != choice.Destination || result.Status.ID != submitted.ID || a.creates != 1 || b.creates != 0 {
		t.Fatal("automatic status rerouted", result, err)
	}
}
