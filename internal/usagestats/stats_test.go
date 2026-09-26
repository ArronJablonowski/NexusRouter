package usagestats

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/ArronJablonowski/DarwinRouter/accounting"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"path/filepath"
	"testing"
)

func TestStatsResetPreservesLifetimeAndOtherTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE usage_records(id TEXT PRIMARY KEY,body BLOB);CREATE TABLE usage_heads(base_id TEXT,current_id TEXT);CREATE TABLE usage_corrections(id TEXT,body BLOB);`)
	if err != nil {
		t.Fatal(err)
	}
	add := func(id, model string, usage *providers.Usage) {
		t.Helper()
		body, _ := json.Marshal(accounting.Record{ID: id, Provider: "p", Model: model, Usage: usage})
		if _, e := db.Exec("INSERT INTO usage_records VALUES(?,?)", id, body); e != nil {
			t.Fatal(e)
		}
		if _, e := db.Exec("INSERT INTO usage_heads VALUES(?,?)", id, id); e != nil {
			t.Fatal(e)
		}
	}
	kinds := map[[2]string]string{{"p", "c"}: "cloud", {"p", "l"}: "local"}
	add("a", "c", &providers.Usage{InputTokens: 100, OutputTokens: 20})
	add("b", "l", &providers.Usage{InputTokens: 30, OutputTokens: 5})
	add("unknown", "c", nil)
	add("unclassified", "x", nil)
	before, e := Read(ctx, path, kinds, nil)
	if e != nil || before.Cloud.Lifetime.Input != "100" || before.Cloud.Trip.Unknown != 1 || before.Unclassified != 1 {
		t.Fatal(before, e)
	}
	after, e := Read(ctx, path, kinds, &Reset{1, "cloud", 0, true})
	if e != nil || after.Cloud.Trip.Input != "0" || after.Cloud.Lifetime.Input != "100" || after.Local.Trip.Input != "30" || after.Cloud.ResetAt == "" {
		t.Fatal(after, e)
	}
	if _, e = Read(ctx, path, kinds, &Reset{1, "cloud", 0, true}); !errors.Is(e, ErrConflict) {
		t.Fatal("stale reset was accepted", e)
	}
	add("new", "c", &providers.Usage{InputTokens: 7, OutputTokens: 3})
	again, e := Read(ctx, path, kinds, nil)
	if e != nil || again.Cloud.Trip.Input != "7" || again.Cloud.Lifetime.Input != "107" || again.Cloud.Revision != 1 {
		t.Fatal(again, e)
	}
	correction, _ := json.Marshal(accounting.Correction{Record: accounting.Record{Provider: "p", Model: "c", Usage: &providers.Usage{InputTokens: 110, OutputTokens: 25}}})
	if _, e = db.Exec("INSERT INTO usage_corrections VALUES('fix',?);UPDATE usage_heads SET current_id='fix' WHERE base_id='a'", correction); e != nil {
		t.Fatal(e)
	}
	corrected, e := Read(ctx, path, kinds, nil)
	if e != nil || corrected.Cloud.Lifetime.Input != "117" || corrected.Cloud.Trip.Input != "7" {
		t.Fatal(corrected, e)
	}
	if _, e = Read(ctx, path, kinds, &Reset{1, "cloud", 1, false}); e == nil {
		t.Fatal("unconfirmed reset accepted")
	}
	var n int
	db.QueryRow("SELECT count(*) FROM usage_records").Scan(&n)
	if n != 5 {
		t.Fatal("usage history changed")
	}
}
