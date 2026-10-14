package remote

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/internal/agentchat"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"path/filepath"
	"strings"
	"testing"
)

type collaborationBackend struct {
	*fakeBackend
	store *agentchat.Store
}

func (b *collaborationBackend) Collaboration(ctx context.Context, stream string, after int64) (agentchat.Batch, error) {
	return b.store.Export(ctx, stream, after)
}
func TestCollaborationExportRequiresInstanceWideGrantAndResumes(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	store, e := agentchat.Open(ctx, filepath.Join(t.TempDir(), "messages"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	m := webui.CollaborationMessage{SessionID: "s", Hostname: "spark", Harness: "NexusRouter", Runner: "Ollama", Provider: "ollama", SenderID: "qwen", SenderModel: "Qwen", Topic: "bug", Recipient: "*", TaskID: "task", Text: "private idea", Private: true}
	store.Append(ctx, strings.Repeat("a", 64), m)
	f.server.backend = &collaborationBackend{f.backend, store}
	if _, e = f.client.Collaboration(ctx, "node-a", "", 0); !errors.Is(e, ErrDenied) {
		t.Fatal("inspect leaked journal", e)
	}
	f.serverPeer.Operations = append(f.serverPeer.Operations, "logs")
	writeRegistry(t, f.clientTrust, f.serverPeer)
	if _, e = f.client.Collaboration(ctx, "node-a", "", 0); !errors.Is(e, ErrDenied) {
		t.Fatal("outbound alone leaked journal", e)
	}
	f.clientPeer.Operations = append(f.clientPeer.Operations, "logs")
	writeRegistry(t, f.serverTrust, f.clientPeer)
	page, e := f.client.Collaboration(ctx, "node-a", "", 0)
	if e != nil || len(page.Messages) != 1 || page.Messages[0].Hostname != "spark" {
		t.Fatal(page, e)
	}
	tail, e := f.client.Collaboration(ctx, "node-a", page.Stream, page.Next)
	if e != nil || len(tail.Messages) != 0 {
		t.Fatal(tail, e)
	}
	f.clientPeer.Operations = []string{"inspect"}
	writeRegistry(t, f.serverTrust, f.clientPeer)
	if _, e = f.client.Collaboration(ctx, "node-a", page.Stream, page.Next); !errors.Is(e, ErrDenied) {
		t.Fatal("revocation ignored", e)
	}
}
