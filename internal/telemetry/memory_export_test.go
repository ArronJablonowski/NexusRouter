package telemetry

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/memory"
	"modernc.org/sqlite"
)

func memoryExportFixture(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "memory.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func insertExportFacts(t *testing.T, s *Store, facts []memory.Fact) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, f := range facts {
		body, err := json.Marshal(f)
		if err != nil || f.Validate() != nil {
			t.Fatal("invalid fixture", err)
		}
		expiry := int64(0)
		if !f.Expires.IsZero() {
			expiry = f.Expires.UnixNano()
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO memory_facts(scope,id,revision,privacy,expires,content,body) VALUES(?,?,?,?,?,?,?)`, f.Scope, f.ID, f.Revision, f.Privacy, expiry, f.Content, body); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryExportCompleteScopeReadonlyAndRetirement(t *testing.T) {
	s, path := memoryExportFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	a, b, other, deleted := testFact(), testFact(), testFact(), testFact()
	a.ID = "a"
	b.ID = "b"
	b.Expires = b.Created.Add(time.Second)
	b.Privacy = "shareable"
	other.Scope = "other"
	deleted.ID = "deleted"
	insertExportFacts(t, s, []memory.Fact{b, a, other, deleted})
	if err := s.DeleteMemory(ctx, deleted.Scope, deleted.ID, 1); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	got, err := ro.ExportMemory(ctx, a.Scope, now)
	if err != nil || got.Version != 1 || got.Scope != a.Scope || !got.CapturedAt.Equal(now) || !reflect.DeepEqual(got.Facts, []memory.Fact{a, b}) {
		t.Fatal(got, err)
	}
	for _, f := range []memory.Fact{a, b} {
		saved, err := s.GetMemory(ctx, f.Scope, f.ID)
		if err != nil || !reflect.DeepEqual(saved, f) {
			t.Fatal("inspection touched fact", saved, err)
		}
	}
	empty, err := ro.ExportMemory(ctx, "missing-scope", now)
	if err != nil || empty.Facts == nil || len(empty.Facts) != 0 {
		t.Fatal(empty, err)
	}
	var retired int
	if err := s.db.QueryRow(`SELECT count(*) FROM memory_retired_ids`).Scan(&retired); err != nil || retired != 1 {
		t.Fatal("retirement changed", retired, err)
	}
}

func TestMemoryExportFactCountBoundary(t *testing.T) {
	s, _ := memoryExportFixture(t)
	facts := make([]memory.Fact, 1001)
	for i := range facts {
		facts[i] = testFact()
		facts[i].ID = fmt.Sprintf("fact-%04d", i)
	}
	insertExportFacts(t, s, facts[:1000])
	ctx := context.Background()
	now := time.Now().UTC()
	if out, err := s.ExportMemory(ctx, facts[0].Scope, now); err != nil || len(out.Facts) != 1000 {
		t.Fatal(len(out.Facts), err)
	}
	insertExportFacts(t, s, facts[1000:])
	if out, err := s.ExportMemory(ctx, facts[0].Scope, now); !errors.Is(err, memory.ErrInput) || !reflect.DeepEqual(out, memory.ExportSnapshot{}) {
		t.Fatal("partial overlimit export", len(out.Facts), err)
	}
}

func TestMemoryExportExactEnvelopeByteBoundary(t *testing.T) {
	s, _ := memoryExportFixture(t)
	now := time.Now().UTC()
	facts := make([]memory.Fact, 128)
	for i := range facts {
		facts[i] = testFact()
		facts[i].ID = fmt.Sprintf("fact-%04d", i)
		facts[i].Content = strings.Repeat("x", 65536)
	}
	facts[127].Content = "x"
	base := memory.ExportSnapshot{Version: 1, Scope: facts[0].Scope, CapturedAt: now, Facts: facts}
	body, _ := json.Marshal(base)
	remaining := memory.ExportMaxBytes - len(body)
	if remaining < 0 || remaining+1 > 65536 {
		t.Fatal("unexpected fixture envelope", remaining)
	}
	facts[127].Content = strings.Repeat("x", remaining+1)
	insertExportFacts(t, s, facts)
	out, err := s.ExportMemory(context.Background(), facts[0].Scope, now)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(out)
	if len(encoded) != memory.ExportMaxBytes {
		t.Fatal(len(encoded))
	}
	facts[127].Content += "x"
	body, _ = json.Marshal(facts[127])
	if _, err = s.db.Exec(`UPDATE memory_facts SET content=?,body=? WHERE scope=? AND id=?`, facts[127].Content, body, facts[127].Scope, facts[127].ID); err != nil {
		t.Fatal(err)
	}
	if out, err = s.ExportMemory(context.Background(), facts[0].Scope, now); !errors.Is(err, memory.ErrInput) || !reflect.DeepEqual(out, memory.ExportSnapshot{}) {
		t.Fatal("oversize returned partial data", err)
	}
}

func TestMemoryExportMalformedRowsFailWithoutPartial(t *testing.T) {
	for _, mode := range []string{"revision", "privacy", "content", "expiry", "unknown-body", "duplicate-body", "oversize-body", "oversize-id"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := memoryExportFixture(t)
			a, b := testFact(), testFact()
			a.ID = "a"
			b.ID = "b"
			insertExportFacts(t, s, []memory.Fact{a, b})
			var err error
			switch mode {
			case "revision":
				_, err = s.db.Exec(`UPDATE memory_facts SET revision=2 WHERE id='b'`)
			case "privacy":
				_, err = s.db.Exec(`UPDATE memory_facts SET privacy='shareable' WHERE id='b'`)
			case "content":
				_, err = s.db.Exec(`UPDATE memory_facts SET content='other' WHERE id='b'`)
			case "expiry":
				_, err = s.db.Exec(`UPDATE memory_facts SET expires=1 WHERE id='b'`)
			case "unknown-body":
				_, err = s.db.Exec(`UPDATE memory_facts SET body=json_set(body,'$.unknown','value') WHERE id='b'`)
			case "duplicate-body":
				body, _ := json.Marshal(b)
				body = []byte(strings.Replace(string(body), `"version":1`, `"version":1,"version":1`, 1))
				_, err = s.db.Exec(`UPDATE memory_facts SET body=? WHERE id='b'`, body)
			case "oversize-body":
				_, err = s.db.Exec(`UPDATE memory_facts SET body=zeroblob(524289) WHERE id='b'`)
			case "oversize-id":
				_, err = s.db.Exec(`UPDATE memory_facts SET id=? WHERE id='b'`, strings.Repeat("b", 513))
			}
			if err != nil {
				t.Fatal(err)
			}
			if out, err := s.ExportMemory(context.Background(), a.Scope, time.Now()); !errors.Is(err, memory.ErrInput) || !reflect.DeepEqual(out, memory.ExportSnapshot{}) {
				t.Fatal(mode, out, err)
			}
		})
	}
}

func TestMemoryExportCancellationAndInvalidInputs(t *testing.T) {
	s, _ := memoryExportFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, err := s.ExportMemory(ctx, "project-a", time.Now()); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(out, memory.ExportSnapshot{}) {
		t.Fatal(out, err)
	}
	for _, call := range []func() (memory.ExportSnapshot, error){func() (memory.ExportSnapshot, error) { return s.ExportMemory(nil, "scope", time.Now()) }, func() (memory.ExportSnapshot, error) { return s.ExportMemory(context.Background(), "", time.Now()) }, func() (memory.ExportSnapshot, error) {
		return (*Store)(nil).ExportMemory(context.Background(), "scope", time.Now())
	}} {
		if _, err := call(); !errors.Is(err, memory.ErrInput) {
			t.Fatal(err)
		}
	}
}

var memoryExportBarrier atomic.Uint64

func TestMemoryExportWALSnapshotDuringConcurrentAtomicWriter(t *testing.T) {
	s, path := memoryExportFixture(t)
	a, b := testFact(), testFact()
	a.ID = "a"
	b.ID = "b"
	insertExportFacts(t, s, []memory.Fact{a, b})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	name := fmt.Sprintf("export_barrier_%d", memoryExportBarrier.Add(1))
	if err := sqlite.RegisterScalarFunction(name, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		if first.CompareAndSwap(false, true) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return args[0], nil
	}); err != nil {
		t.Fatal(err)
	}
	// Test-only view interposes a barrier while the export reads its first row.
	// The production exporter has no hook; the writer updates the underlying
	// WAL table atomically while the reader's SQLite snapshot remains open.
	if _, err := s.db.Exec(`ALTER TABLE memory_facts RENAME TO memory_export_rows; CREATE VIEW memory_facts AS SELECT scope,id,revision,privacy,expires,` + name + `(content) AS content,body FROM memory_export_rows`); err != nil {
		t.Fatal(err)
	}
	writer, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	type result struct {
		snapshot memory.ExportSnapshot
		err      error
	}
	done := make(chan result, 1)
	go func() { out, err := reader.ExportMemory(ctx, a.Scope, time.Now().UTC()); done <- result{out, err} }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done
		}
	}()
	select {
	case <-entered:
	case early := <-done:
		joined = true
		t.Fatal("export exited before barrier", early.err)
	case <-ctx.Done():
		t.Fatal("export barrier not reached")
	}
	tx, err := writer.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	newFacts := []memory.Fact{a, b}
	for i := range newFacts {
		f := &newFacts[i]
		f.Revision = 2
		f.Updated = f.Updated.Add(time.Second)
		f.Content = "new atomic generation"
		body, _ := json.Marshal(f)
		if _, err = tx.ExecContext(ctx, `UPDATE memory_export_rows SET revision=?,content=?,body=? WHERE scope=? AND id=?`, f.Revision, f.Content, body, f.Scope, f.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal("WAL writer blocked or failed", err)
	}
	close(release)
	var got result
	select {
	case got = <-done:
		joined = true
	case <-ctx.Done():
		t.Fatal("export did not complete")
	}
	if got.err != nil || !reflect.DeepEqual(got.snapshot.Facts, []memory.Fact{a, b}) {
		t.Fatal("mixed or new data entered existing snapshot", got)
	}
	later, err := reader.ExportMemory(ctx, a.Scope, time.Now().UTC())
	if err != nil || !reflect.DeepEqual(later.Facts, newFacts) {
		t.Fatal("later snapshot missed committed writer", later, err)
	}
}
