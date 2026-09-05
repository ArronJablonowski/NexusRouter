package skills

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func activationCatalogFixture(t *testing.T) (*FileStore, string, catalog, []Version) {
	t.Helper()
	path := testPath(t)
	store := openTest(t, path)
	ctx := context.Background()
	versions := []Version{}
	previous := ""
	for range 3 {
		v, err := store.Draft(ctx, sample(), false)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Activate(ctx, v.Draft.Key, v.ID, previous, pass, false); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, v)
		previous = v.ID
	}
	var c catalog
	if err := store.read("catalog.json", &c); err != nil {
		t.Fatal(err)
	}
	return store, path, c, versions
}

func activationFiles(t *testing.T, path string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, file := range entries {
		body, err := os.ReadFile(filepath.Join(path, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[file.Name()] = string(body)
	}
	return out
}

func TestActivationHistoryCorruptionRejectedWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name    string
		corrupt func(*entry)
	}{
		{"discontinuous", func(e *entry) { e.Activations[1].From = "" }},
		{"unknown_target", func(e *entry) { e.Activations[1].To = strings.Repeat("f", 32) }},
		{"missing_historical_proof", func(e *entry) { delete(e.Validated, e.Activations[0].To) }},
		{"wrong_final", func(e *entry) { e.Active = e.Activations[0].To }},
		{"same_target", func(e *entry) { e.Activations[1].To = e.Activations[1].From }},
		{"missing_history", func(e *entry) { e.Activations = nil }},
		{"forged_rollback", func(e *entry) {
			e.Activations = append(e.Activations, activation{From: e.Active, To: e.Activations[0].To, At: time.Now().UTC(), Rollback: true})
			e.Active = e.Activations[0].To
		}},
		{"reuse_undone_transition", func(e *entry) {
			first, second := e.Activations[0], e.Activations[1]
			e.Activations = []activation{first, second,
				{From: second.To, To: first.To, At: time.Now().UTC(), Rollback: true},
				{From: first.To, To: second.To, At: time.Now().UTC(), Rollback: true}}
			e.Active = second.To
		}},
		{"rollback_first_activation", func(e *entry) { e.Activations[0].Rollback = true }},
		{"zero_time", func(e *entry) { e.Activations[1].At = time.Time{} }},
		{"pre_epoch", func(e *entry) { e.Activations[1].At = time.Date(1969, 12, 31, 0, 0, 0, 0, time.UTC) }},
		{"late_time", func(e *entry) { e.Activations[1].At = time.Date(2261, 1, 1, 0, 0, 0, 0, time.UTC) }},
		{"non_utc", func(e *entry) {
			e.Activations[1].At = time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("offset", 3600))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, path, c, versions := activationCatalogFixture(t)
			key := versions[0].Draft.Key
			e := c.Skills[key.index()]
			test.corrupt(&e)
			c.Skills[key.index()] = e
			body, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(path, "catalog.json"), body, 0600); err != nil {
				t.Fatal(err)
			}
			before := activationFiles(t, path)
			if got, err := store.History(context.Background(), key); !errors.Is(err, ErrInvalid) || got.Active != "" || len(got.Versions) != 0 {
				t.Fatalf("corrupt history inspection accepted: %+v %v", got, err)
			}
			if err := store.Rollback(context.Background(), key, versions[2].ID, false); !errors.Is(err, ErrInvalid) {
				t.Fatalf("corrupt rollback accepted: %v", err)
			}
			if _, err := store.Draft(context.Background(), sample(), false); !errors.Is(err, ErrInvalid) {
				t.Fatalf("draft mutated corrupt history: %v", err)
			}
			if got, err := store.Discover(context.Background(), key.Scope, nil, 10); !errors.Is(err, ErrInvalid) || len(got) != 0 {
				t.Fatalf("corrupt discovery accepted: %+v %v", got, err)
			}
			if got, err := store.Load(context.Background(), key, versions[0].ID); !errors.Is(err, ErrInvalid) || got.ID != "" {
				t.Fatalf("corrupt explicit load accepted: %+v %v", got, err)
			}
			if got, err := store.Load(context.Background(), key, ""); !errors.Is(err, ErrInvalid) || got.ID != "" {
				t.Fatalf("corrupt active load accepted: %+v %v", got, err)
			}
			if readonly, err := OpenReadOnly(path, []string{key.Scope}); !errors.Is(err, ErrInvalid) || readonly != nil {
				if readonly != nil {
					_ = readonly.Close()
				}
				t.Fatalf("corrupt readonly open accepted: %v", err)
			}
			if after := activationFiles(t, path); !reflect.DeepEqual(before, after) {
				t.Fatal("inspection mutated catalog or version files")
			}
		})
	}
}

func TestActivationHistoryNeverActivatedDraftRemainsReadable(t *testing.T) {
	path := testPath(t)
	store := openTest(t, path)
	v, err := store.Draft(context.Background(), sample(), false)
	if err != nil {
		t.Fatal(err)
	}
	readonly, err := OpenReadOnly(path, []string{v.Draft.Key.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	if found, err := readonly.Discover(context.Background(), v.Draft.Key.Scope, nil, 10); err != nil || len(found) != 0 {
		t.Fatalf("draft unexpectedly active: %+v %v", found, err)
	}
	if loaded, err := readonly.Load(context.Background(), v.Draft.Key, v.ID); err != nil || loaded.ID != v.ID {
		t.Fatalf("valid draft rejected: %+v %v", loaded, err)
	}
}

func TestActivationHistoryTimestampBoundsAccepted(t *testing.T) {
	for _, at := range []time.Time{time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2260, 12, 31, 23, 59, 59, 999999999, time.UTC)} {
		t.Run(at.Format(time.RFC3339Nano), func(t *testing.T) {
			store, path, c, versions := activationCatalogFixture(t)
			key := versions[0].Draft.Key
			e := c.Skills[key.index()]
			for i := range e.Activations {
				e.Activations[i].At = at
			}
			c.Skills[key.index()] = e
			body, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(path, "catalog.json"), body, 0600); err != nil {
				t.Fatal(err)
			}
			if got, err := store.Load(context.Background(), key, ""); err != nil || got.ID != versions[2].ID {
				t.Fatalf("valid timestamp bound rejected: %+v %v", got, err)
			}
		})
	}
}
