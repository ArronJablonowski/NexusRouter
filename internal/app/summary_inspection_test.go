package app

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func TestSummaryInspectionReadOnlyPaginationAndReopen(t *testing.T) {
	svc, db, task := codexCompactionFixture(t)
	ctx := context.Background()
	first := codexCompactionDraft(t, svc, task)
	second := codexCompactionDraft(t, svc, task)
	expected := []sessions.SummaryAttempt{first, second}
	sort.Slice(expected, func(i, j int) bool { return expected[i].ID < expected[j].ID })
	review, err := svc.ReviewSummary(ctx, first.ID, "", "approved", "Fixture reviewed")
	if err != nil {
		t.Fatal(err)
	}
	source, err := db.Read(ctx, task, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	path := svc.settings.Telemetry.Database
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, a := range expected {
		got, err := InspectSummaryAttempt(ctx, path, a.ID)
		if err != nil || !reflect.DeepEqual(got, a) {
			t.Fatal("record mismatch", err)
		}
		after := ""
		if i > 0 {
			after = expected[i-1].ID
		}
		page, err := ListSummaryAttempts(ctx, path, task, after, 1)
		if err != nil || len(page) != 1 || !reflect.DeepEqual(page[0], a) {
			t.Fatal("page mismatch", err)
		}
	}
	all, err := ListSummaryAttempts(ctx, path, "", "", 100)
	if err != nil || !reflect.DeepEqual(all, expected) {
		t.Fatal("all task page", err)
	}
	for _, filter := range []struct{ task, after string }{{task, expected[1].ID}, {"other-task", ""}} {
		empty, err := ListSummaryAttempts(ctx, path, filter.task, filter.after, 100)
		if err != nil || empty == nil || len(empty) != 0 {
			t.Fatal("empty page", empty, err)
		}
	}
	history, err := SummaryReviewHistory(ctx, path, first.ID)
	if err != nil || !reflect.DeepEqual(history, []sessions.SummaryReview{review}) {
		t.Fatal("review history", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("inspection modified database", err)
	}
	replayed, err := InspectTask(ctx, path, task)
	if err != nil || replayed.Sequence != int64(len(source)) {
		t.Fatal("source changed", err)
	}
}

func TestSummaryInspectionRejectsInputsAndDoesNotCreateStorage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	ctx := context.Background()
	invalid := []string{"", " leading", "trailing ", "line\nfeed", strings.Repeat("x", 129), string([]byte{255})}
	for _, id := range invalid {
		if got, err := InspectSummaryAttempt(ctx, path, id); err != ErrAdmission || !reflect.DeepEqual(got, sessions.SummaryAttempt{}) {
			t.Fatal("invalid inspect", err)
		}
		if got, err := SummaryReviewHistory(ctx, path, id); err != ErrAdmission || got != nil {
			t.Fatal("invalid history", err)
		}
		if id != "" {
			for _, pair := range [][2]string{{id, ""}, {"", id}} {
				got, err := ListSummaryAttempts(ctx, path, pair[0], pair[1], 1)
				if err != ErrAdmission || got != nil {
					t.Fatal("invalid cursor", err)
				}
			}
		}
	}
	for _, limit := range []int{0, -1, 101} {
		if got, err := ListSummaryAttempts(ctx, path, "", "", limit); err != ErrAdmission || got != nil {
			t.Fatal("invalid limit", err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, testCtx := range []context.Context{nil, canceled} {
		_, err := InspectSummaryAttempt(testCtx, path, "valid")
		want := ErrAdmission
		if testCtx != nil {
			want = context.Canceled
		}
		if !errors.Is(err, want) {
			t.Fatal("inspect context", err)
		}
		if got, err := ListSummaryAttempts(testCtx, path, "", "", 1); !errors.Is(err, want) || got != nil {
			t.Fatal("list context", err)
		}
		if got, err := SummaryReviewHistory(testCtx, path, "valid"); !errors.Is(err, want) || got != nil {
			t.Fatal("history context", err)
		}
	}
	if _, err := InspectSummaryAttempt(ctx, path, "valid"); err != ErrInspection {
		t.Fatal(err)
	}
	if got, err := ListSummaryAttempts(ctx, path, "", "", 1); err != ErrInspection || got != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("inspection created storage", err)
	}
}

func TestSummaryInspectionCorruptPageReturnsNoPartialData(t *testing.T) {
	svc, db, task := codexCompactionFixture(t)
	ctx := context.Background()
	a := codexCompactionDraft(t, svc, task)
	b := codexCompactionDraft(t, svc, task)
	db.Close()
	ids := []string{a.ID, b.ID}
	sort.Strings(ids)
	path := svc.settings.Telemetry.Database
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`UPDATE summary_attempts SET body=? WHERE id=?`, []byte(`{"private":"corrupt summary payload"}`), ids[1]); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	before, _ := os.ReadFile(path)
	page, err := ListSummaryAttempts(ctx, path, task, "", 100)
	if err != ErrInspection || page != nil {
		t.Fatal("partial corrupt page returned", err)
	}
	got, err := InspectSummaryAttempt(ctx, path, ids[1])
	if err != ErrInspection || !reflect.DeepEqual(got, sessions.SummaryAttempt{}) {
		t.Fatal("corrupt detail leaked", err)
	}
	if _, err = InspectSummaryAttempt(ctx, path, "missing"); err != ErrInspection {
		t.Fatal("missing record normalization", err)
	}
	after, _ := os.ReadFile(path)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("corruption inspection mutated database")
	}
}
