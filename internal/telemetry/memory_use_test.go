package telemetry

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/memory"
)

func TestMemoryRevisionUseMonotonicAndLegacyCompatible(t *testing.T) {
	db, _ := submissionStore(t)
	ctx := context.Background()
	f := testFact()
	if err := db.PutMemory(ctx, f, 0); err != nil {
		t.Fatal(err)
	}
	when := f.Created.Add(time.Hour)
	for _, at := range []time.Time{when, when.Add(-time.Minute), when} {
		if err := db.TouchMemoryFact(ctx, f, at); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.GetMemory(ctx, f.Scope, f.ID)
	if err != nil || !got.LastUse.Equal(when) || got.Revision != 1 || got.Content != f.Content || got.Privacy != f.Privacy || !got.Updated.Equal(f.Updated) {
		t.Fatal(got, err)
	}
	if err := db.TouchMemory(ctx, f.Scope, f.ID, when.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetMemory(ctx, f.Scope, f.ID)
	if err != nil || !got.LastUse.Equal(when.Add(time.Minute)) {
		t.Fatal(got, err)
	}
}

func TestMemoryRevisionUseRejectsStaleAndInvalidWithoutMutation(t *testing.T) {
	for _, mode := range []string{"revision", "corrected", "privacy", "deleted", "recreated", "expired", "expiry_boundary", "canceled", "nil_context", "zero_revision", "wrong_scope", "wrong_id", "before_creation", "corrupt_revision", "corrupt_privacy", "corrupt_expiry"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := submissionStore(t)
			ctx := context.Background()
			f := testFact()
			f.Privacy = "shareable"
			f.Expires = f.Created.Add(time.Hour)
			if err := db.PutMemory(ctx, f, 0); err != nil {
				t.Fatal(err)
			}
			now, revision, scope, id := f.Created.Add(time.Minute), int64(1), f.Scope, f.ID
			expected := f
			switch mode {
			case "revision":
				revision = 2
			case "zero_revision":
				revision = 0
			case "corrected", "privacy":
				f.Revision, f.Updated, f.Content = 2, now, "corrected fact"
				if mode == "privacy" {
					f.Privacy = "local_only"
				}
				if err := db.PutMemory(ctx, f, 1); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if err := db.DeleteMemory(ctx, scope, id, 1); err != nil {
					t.Fatal(err)
				}
			case "recreated":
				if err := db.DeleteMemory(ctx, scope, id, 1); err != nil {
					t.Fatal(err)
				}
				f.Content = "different replacement at same revision"
				if err := db.PutMemory(ctx, f, 0); err != nil {
					t.Fatal(err)
				}
			case "expired":
				now = f.Expires.Add(time.Second)
			case "expiry_boundary":
				now = f.Expires
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil_context":
				ctx = nil
			case "wrong_scope":
				scope = "another-scope"
			case "wrong_id":
				id = "another-id"
			case "before_creation":
				now = f.Created.Add(-time.Second)
			case "corrupt_revision":
				if _, err := db.db.Exec(`UPDATE memory_facts SET revision=2`); err != nil {
					t.Fatal(err)
				}
			case "corrupt_privacy":
				if _, err := db.db.Exec(`UPDATE memory_facts SET privacy='local_only'`); err != nil {
					t.Fatal(err)
				}
			case "corrupt_expiry":
				if _, err := db.db.Exec(`UPDATE memory_facts SET expires=0`); err != nil {
					t.Fatal(err)
				}
			}
			var before string
			if err := db.db.QueryRow(`SELECT COALESCE(group_concat(body,''),'') FROM memory_facts`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			expected.Scope, expected.ID, expected.Revision = scope, id, revision
			err := db.TouchMemoryFact(ctx, expected, now)
			if err == nil {
				t.Fatal("invalid use accepted")
			}
			if mode == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			var after string
			if err := db.db.QueryRow(`SELECT COALESCE(group_concat(body,''),'') FROM memory_facts`).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected use changed fact")
			}
		})
	}
}

var _ memory.UseStore = (*Store)(nil)

func TestMemoryUseMatchesCompleteFactExceptLastUse(t *testing.T) {
	for _, field := range []string{"content", "provenance", "confidence", "privacy", "created", "updated", "expires", "last_use"} {
		t.Run(field, func(t *testing.T) {
			db, _ := submissionStore(t)
			ctx := context.Background()
			f := testFact()
			if err := db.PutMemory(ctx, f, 0); err != nil {
				t.Fatal(err)
			}
			expected := f
			switch field {
			case "content":
				expected.Content = "other"
			case "provenance":
				expected.Provenance = "other"
			case "confidence":
				expected.Confidence = .5
			case "privacy":
				expected.Privacy = "shareable"
			case "created":
				expected.Created = expected.Created.Add(-time.Second)
			case "updated":
				expected.Updated = expected.Updated.Add(time.Second)
			case "expires":
				expected.Expires = expected.Created.Add(time.Hour)
			case "last_use":
				expected.LastUse = expected.Created.Add(time.Second)
			}
			err := db.TouchMemoryFact(ctx, expected, f.Created.Add(time.Minute))
			if field == "last_use" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, memory.ErrConflict) {
				t.Fatal("changed field not rejected", err)
			}
			got, err := db.GetMemory(ctx, f.Scope, f.ID)
			if err != nil || (field != "last_use" && !got.LastUse.IsZero()) {
				t.Fatal("rejected fact was touched", err)
			}
		})
	}
}
