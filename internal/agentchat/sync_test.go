package agentchat

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSyncPreservesProvenanceResumesAndNeverEchoes(t *testing.T) {
	ctx := context.Background()
	source, _ := Open(ctx, filepath.Join(t.TempDir(), "source"))
	defer source.Close()
	dir := filepath.Join(t.TempDir(), "commander")
	dest, _ := Open(ctx, dir)
	for i := 0; i < 55; i++ {
		m := message()
		m.TaskID = fmt.Sprintf("task-%d", i)
		if _, e := source.Append(ctx, fmt.Sprintf("%064x", i+1), m); e != nil {
			t.Fatal(e)
		}
	}
	b, e := source.Export(ctx, "", 0)
	if e != nil || len(b.Messages) != 50 {
		t.Fatal(b, e)
	}
	if e = dest.Import(ctx, "spark", b); e != nil {
		t.Fatal(e)
	}
	dest.Close()
	dest, _ = Open(ctx, dir)
	defer dest.Close()
	stream, after, e := dest.Cursor(ctx, "spark")
	if e != nil || after != 50 {
		t.Fatal(stream, after, e)
	}
	tail, e := source.Export(ctx, stream, after)
	if e != nil || len(tail.Messages) != 5 {
		t.Fatal(e)
	}
	if e = dest.Import(ctx, "spark", tail); e != nil {
		t.Fatal(e)
	}
	page, e := dest.Read(ctx, webui.CollaborationOptions{}, "", true)
	if e != nil || len(page.Messages) != 50 {
		t.Fatal(e)
	}
	newest := page.Messages[0]
	original := tail.Messages[4]
	if newest.Text != original.Text || newest.SenderModel != original.SenderModel || newest.Hostname != original.Hostname || !newest.SentAt.Equal(original.SentAt) || newest.OriginInstance != "spark" || newest.OriginSequence != 55 {
		t.Fatal("provenance changed", newest)
	}
	// Exact replay after a crash before cursor commit must not duplicate records.
	dest.db.ExecContext(ctx, "UPDATE sync_state SET position=50 WHERE peer='spark'")
	if e = dest.Import(ctx, "spark", tail); e != nil {
		t.Fatal(e)
	}
	var count int
	dest.db.QueryRowContext(ctx, "SELECT count(*) FROM messages").Scan(&count)
	if count != 55 {
		t.Fatal("duplicate", count)
	}
	echo, e := dest.Export(ctx, "", 0)
	if e != nil || len(echo.Messages) != 0 {
		t.Fatal("import echoed", echo, e)
	}
	changed := tail
	changed.After = 55
	changed.Next = 56
	changed.Messages = []webui.CollaborationMessage{tail.Messages[0]}
	changed.Messages[0].Sequence = 56
	changed.Stream = "00000000000000000000000000000000"
	if e = dest.Import(ctx, "spark", changed); e != ErrConflict {
		t.Fatal("stream reset silently accepted", e)
	}
	if _, e = source.Export(ctx, stream, 100); e != ErrConflict {
		t.Fatal("source truncation ignored", e)
	}
	cloud, e := dest.Read(ctx, webui.CollaborationOptions{}, "a", false)
	if e != nil || len(cloud.Messages) != 0 {
		t.Fatal("private sync reached cloud", e)
	}
}

func TestSyncAcceptsSourceLimitWithAddedProvenance(t *testing.T) {
	ctx := context.Background()
	source, _ := Open(ctx, filepath.Join(t.TempDir(), "source"))
	defer source.Close()
	dest, _ := Open(ctx, filepath.Join(t.TempDir(), "dest"))
	defer dest.Close()
	m := message()
	m.Hostname = strings.Repeat("h", 253)
	m.SenderModel = strings.Repeat("m", 512)
	m.Sequence = 1
	m.SentAt = time.Unix(1, 0).UTC()
	body, _ := json.Marshal(m)
	m.Text = strings.Repeat("x", (9<<10)-64-len(body)+len(m.Text))
	if _, e := source.Append(ctx, strings.Repeat("a", 64), m); e != nil {
		t.Fatal(e)
	}
	batch, e := source.Export(ctx, "", 0)
	if e != nil {
		t.Fatal(e)
	}
	if e = dest.Import(ctx, strings.Repeat("p", 128), batch); e != nil {
		t.Fatal("provenance rejected valid source message", e)
	}
}
