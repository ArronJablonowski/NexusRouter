package gridcollab

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/internal/agentchat"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"path/filepath"
	"strings"
	"testing"
)

type syncClient struct {
	source *agentchat.Store
	fail   bool
	calls  int
}

func (c *syncClient) Collaboration(ctx context.Context, peer, stream string, after int64) (agentchat.Batch, error) {
	c.calls++
	if c.fail {
		return agentchat.Batch{}, errors.New("offline")
	}
	return c.source.Export(ctx, stream, after)
}
func TestMessageSyncOfflineRecoveryPermissionAndRestart(t *testing.T) {
	ctx := context.Background()
	source, _ := agentchat.Open(ctx, filepath.Join(t.TempDir(), "source"))
	defer source.Close()
	source.Append(ctx, strings.Repeat("a", 64), webui.CollaborationMessage{Hostname: "spark", SenderModel: "Muse Glimmer", SenderID: "muse", Harness: "NexusRouter", Runner: "Ollama", Provider: "ollama", TaskID: "task", SessionID: "session", Recipient: "*", Topic: "bug", Text: "finding", Private: true})
	client := &syncClient{source: source, fail: true}
	dir := filepath.Join(t.TempDir(), "commander")
	s := &Syncer{Directory: dir, Client: client, Trust: fakeTrust{remote.Registry{Peers: []remote.Peer{{ID: "spark", Operations: []string{"logs"}}}}}}
	if e := s.Sync(ctx); e != nil {
		t.Fatal(e)
	}
	local, _ := agentchat.Open(ctx, dir)
	status, _ := local.SyncStatus(ctx)
	local.Close()
	if len(status) != 1 || status[0].State != "unavailable" {
		t.Fatal(status)
	}
	client.fail = false
	if e := s.Sync(ctx); e != nil {
		t.Fatal(e)
	}
	resumed := &Syncer{Directory: dir, Client: client, Trust: s.Trust}
	if e := resumed.Sync(ctx); e != nil {
		t.Fatal(e)
	}
	local, _ = agentchat.Open(ctx, dir)
	defer local.Close()
	page, e := local.Read(ctx, webui.CollaborationOptions{}, "", true)
	if e != nil || len(page.Messages) != 1 || page.Messages[0].SenderModel != "Muse Glimmer" || page.Messages[0].Hostname != "spark" {
		t.Fatal(page, e)
	}
	resumed.Trust = fakeTrust{remote.Registry{Peers: []remote.Peer{{ID: "spark", Operations: []string{"inspect"}}}}}
	before := client.calls
	if e = resumed.Sync(ctx); e != nil || client.calls != before {
		t.Fatal("permission expanded", e)
	}
	status, _ = local.SyncStatus(ctx)
	if status[0].State != "unavailable" {
		t.Fatal(status)
	}
}
