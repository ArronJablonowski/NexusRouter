package remote

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

type logBackend struct {
	*fakeBackend
	events *telemetry.Store
}

func (b *logBackend) CommittedLogs(ctx context.Context, o sessions.EventLogOptions) (sessions.CommittedEventPage, error) {
	return b.events.ReadCommittedEventPage(ctx, o)
}
func logFixture(t *testing.T) (*fixture, *telemetry.Store) {
	t.Helper()
	f := setup(t)
	db, e := telemetry.Open(context.Background(), filepath.Join(t.TempDir(), "runtime.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	f.server.backend = &logBackend{f.backend, db}
	return f, db
}
func permitLogs(t *testing.T, f *fixture) {
	t.Helper()
	f.serverPeer.Operations = append(f.serverPeer.Operations, "logs")
	f.clientPeer.Operations = append(f.clientPeer.Operations, "logs")
	writeRegistry(t, f.clientTrust, f.serverPeer)
	writeRegistry(t, f.serverTrust, f.clientPeer)
}
func eventForLogs(i int64) runtime.Event {
	return runtime.Event{Version: 1, ID: strings.Repeat("e", int(i)), TaskID: "task", SessionID: "session", CorrelationID: "task", Sequence: i, Time: time.Unix(100+i, 0).UTC(), Kind: runtime.TaskStarted, Data: runtime.Data{Messages: []providers.Message{{Role: "user", Content: "full private chat"}}}}
}
func TestCentralLogsPermissionHistoryRestartAndRevocation(t *testing.T) {
	ctx := context.Background()
	f, source := logFixture(t)
	if e := source.Append(ctx, 0, eventForLogs(1)); e != nil {
		t.Fatal(e)
	}
	if _, e := f.client.Logs(ctx, "node-a", "runtime", ""); !errors.Is(e, ErrDenied) {
		t.Fatal("ordinary inspect must not export private content", e)
	}
	// Outbound permission alone must not authorize the server.
	f.serverPeer.Operations = append(f.serverPeer.Operations, "logs")
	writeRegistry(t, f.clientTrust, f.serverPeer)
	if _, e := f.client.Logs(ctx, "node-a", "runtime", ""); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	f.clientPeer.Operations = append(f.clientPeer.Operations, "logs")
	writeRegistry(t, f.serverTrust, f.clientPeer)
	root := filepath.Join(t.TempDir(), "logs")
	store, e := OpenLogStore(root)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.client.CollectLogs(ctx, store, "node-a", "runtime"); e != nil {
		t.Fatal(e)
	}
	if e = f.client.CollectLogs(ctx, store, "node-a", "security"); e != nil {
		t.Fatal(e)
	}
	history, e := store.Read(ctx, "node-a", "runtime", "task", 0, 100)
	if e != nil || len(history) != 1 || !strings.Contains(string(history[0].Body), "full private chat") {
		t.Fatal(history, e)
	}
	store.Close()
	store, e = OpenLogStore(root)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	if e = f.client.CollectLogs(ctx, store, "node-a", "runtime"); e != nil {
		t.Fatal(e)
	}
	completed := eventForLogs(2)
	completed.Kind = runtime.TaskCompleted
	completed.Data = runtime.Data{Text: "full assistant reply"}
	if e = source.Append(ctx, 1, completed); e != nil {
		t.Fatal(e)
	}
	if e = f.client.CollectLogs(ctx, store, "node-a", "runtime"); e != nil {
		t.Fatal(e)
	}
	history, e = store.Read(ctx, "node-a", "runtime", "", 0, 100)
	if e != nil || len(history) != 2 || !strings.Contains(string(history[1].Body), "full assistant reply") {
		t.Fatal(history, e)
	}
	f.clientPeer.Operations = []string{"inspect"}
	writeRegistry(t, f.serverTrust, f.clientPeer)
	if e = f.client.CollectLogs(ctx, store, "node-a", "runtime"); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	status, e := store.Status(ctx)
	if e != nil || status[0].Records != 2 || status[0].Error != "denied" {
		t.Fatal(status, e)
	}
	for _, p := range []string{root, filepath.Join(root, "logs.sqlite")} {
		st, e := os.Stat(p)
		if e != nil || st.Mode().Perm()&0077 != 0 {
			t.Fatal(st, e)
		}
	}
}
func TestCentralSecurityCursorRejectsPruningAndRewriting(t *testing.T) {
	ctx := context.Background()
	f, _ := logFixture(t)
	permitLogs(t, f)
	page, e := f.client.Logs(ctx, "node-a", "security", "")
	if e != nil || len(page.Records) == 0 {
		t.Fatal(page, e)
	}
	c, e := parseAuditLogCursor(page.Next)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.journal.db.Exec("UPDATE audit SET outcome='tampered' WHERE sequence=?", c.Last); e != nil {
		t.Fatal(e)
	}
	if _, e = f.client.Logs(ctx, "node-a", "security", page.Next); !errors.Is(e, ErrConflict) {
		t.Fatal("rewrite accepted", e)
	}
	if _, e = f.journal.db.Exec("DELETE FROM audit WHERE sequence=?", c.Last); e != nil {
		t.Fatal(e)
	}
	if _, e = f.client.Logs(ctx, "node-a", "security", page.Next); !errors.Is(e, ErrConflict) {
		t.Fatal("pruned anchor accepted", e)
	}
	if _, e = f.client.Logs(ctx, "node-a", "security", ""); !errors.Is(e, ErrConflict) {
		t.Fatal("missing initial history silently skipped", e)
	}
}
func TestCentralLogsAtomicCursorAndSourceIsolation(t *testing.T) {
	ctx := context.Background()
	f, source := logFixture(t)
	permitLogs(t, f)
	if e := source.Append(ctx, 0, eventForLogs(1)); e != nil {
		t.Fatal(e)
	}
	page, e := f.client.Logs(ctx, "node-a", "runtime", "")
	if e != nil {
		t.Fatal(e)
	}
	store, e := OpenLogStore(filepath.Join(t.TempDir(), "logs"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	if e = store.append(ctx, page); e != nil {
		t.Fatal(e)
	}
	if e = store.append(ctx, page); !errors.Is(e, ErrConflict) {
		t.Fatal("stale collector not rejected", e)
	}
	page.Instance = "node-c"
	if e = store.append(ctx, page); e != nil {
		t.Fatal(e)
	}
	rows, e := store.Read(ctx, "node-c", "runtime", "", 0, 100)
	if e != nil || len(rows) != 1 {
		t.Fatal(rows, e)
	}
	page.Instance = "node-d"
	page.Records[0].Position = 2
	if e = store.append(ctx, page); e == nil {
		t.Fatal("gap accepted")
	}
	cursor, e := store.cursor(ctx, "node-d", "runtime")
	if e != nil || cursor != "" {
		t.Fatal(cursor, e)
	}
	var data map[string]any
	if json.Unmarshal(rows[0].Body, &data) != nil {
		t.Fatal("invalid saved JSON")
	}
}

func TestCentralLogsRejectUnsafeStoreAndUncommittedPages(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "logs")
	if e := os.Mkdir(root, 0755); e != nil {
		t.Fatal(e)
	}
	if _, e := OpenLogStore(root); !errors.Is(e, ErrDenied) {
		t.Fatal("public store accepted", e)
	}
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(t.TempDir(), "other")
	if e := os.WriteFile(target, nil, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(target, filepath.Join(root, "logs.sqlite")); e != nil {
		t.Fatal(e)
	}
	if _, e := OpenLogStore(root); !errors.Is(e, ErrDenied) {
		t.Fatal("symlink store accepted", e)
	}
	f, source := logFixture(t)
	permitLogs(t, f)
	if e := source.Append(ctx, 0, eventForLogs(1)); e != nil {
		t.Fatal(e)
	}
	page, e := f.client.Logs(ctx, "node-a", "runtime", "")
	if e != nil {
		t.Fatal(e)
	}
	store, e := OpenLogStore(filepath.Join(t.TempDir(), "replica"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	// Force a disk-side transactional failure after valid input was fetched.
	if _, e = store.db.Exec(`CREATE TRIGGER deny_cursor BEFORE UPDATE ON log_heads BEGIN SELECT RAISE(ABORT,'fixture failure'); END;`); e != nil {
		t.Fatal(e)
	}
	if e = store.append(ctx, page); e == nil {
		t.Fatal("write error hidden")
	}
	rows, e := store.Read(ctx, "node-a", "runtime", "", 0, 100)
	if e != nil || len(rows) != 0 {
		t.Fatal("partial records persisted", rows, e)
	}
	cursor, e := store.cursor(ctx, "node-a", "runtime")
	if e != nil || cursor != "" {
		t.Fatal("partial cursor persisted", cursor, e)
	}
}

func TestCentralSecurityPagesFreezeHighWater(t *testing.T) {
	ctx := context.Background()
	f, _ := logFixture(t)
	permitLogs(t, f)
	for range 125 {
		if e := f.journal.audit(ctx, "node-b", "inspect", "", "succeeded"); e != nil {
			t.Fatal(e)
		}
	}
	first, e := f.client.Logs(ctx, "node-a", "security", "")
	if e != nil || len(first.Records) != 100 || !first.HasMore {
		t.Fatal(e, len(first.Records))
	}
	c, e := parseAuditLogCursor(first.Next)
	if e != nil {
		t.Fatal(e)
	}
	second, e := f.client.Logs(ctx, "node-a", "security", first.Next)
	if e != nil || second.HasMore {
		t.Fatal(e)
	}
	next, e := parseAuditLogCursor(second.Next)
	if e != nil || next.Last != c.Through || next.Through != c.Through {
		t.Fatal(c, next, e)
	}
	third, e := f.client.Logs(ctx, "node-a", "security", second.Next)
	if e != nil || len(third.Records) == 0 {
		t.Fatal("new audits lost", e)
	}
}
