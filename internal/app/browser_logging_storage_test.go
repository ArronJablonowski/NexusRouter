package app

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/webui"
)

func TestLogStorageCountsAndDeduplicates(t *testing.T) {
	dir := t.TempDir()
	text := filepath.Join(dir, "a.log")
	os.WriteFile(text, []byte("one\ntwo"), 0600)
	link := filepath.Join(dir, "same.log")
	if err := os.Link(text, link); err != nil {
		t.Fatal(err)
	}
	dbpath := filepath.Join(dir, "fixture.db")
	db, err := sql.Open("sqlite", dbpath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE events(id INTEGER);INSERT INTO events VALUES(1),(2);CREATE TABLE other(id INTEGER);INSERT INTO other VALUES(3)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	p := webui.LoggingPage{Items: []webui.LogLocation{{ID: "application", Location: text}, {ID: "stderr", Location: link}, {ID: "runtime", Location: dbpath, Format: "SQLite"}, {ID: "dns", Location: dir}}}
	measureLogStorage(context.Background(), &p)
	st, _ := os.Stat(dbpath)
	if p.TotalBytes != 7+st.Size() || p.TotalEntries != 5 || p.TotalsPartial {
		t.Fatalf("unexpected totals %d %d %v", p.TotalBytes, p.TotalEntries, p.TotalsPartial)
	}
	if *p.Items[0].Entries != 2 || *p.Items[2].Entries != 3 || *p.Items[3].Bytes != 0 {
		t.Fatal("incorrect card counts")
	}
}
func TestLogStorageUnavailableIsNotZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.log")
	f, _ := os.Create(path)
	f.Truncate(65 << 20)
	f.Close()
	p := webui.LoggingPage{Items: []webui.LogLocation{{ID: "application", Location: path}}}
	measureLogStorage(context.Background(), &p)
	if !p.TotalsPartial || p.Items[0].Entries != nil || p.Items[0].Bytes == nil || *p.Items[0].Bytes != 65<<20 {
		t.Fatal("unknown count presented as complete")
	}
}
