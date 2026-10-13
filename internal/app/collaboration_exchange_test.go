package app

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/internal/agentchat"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/tools"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollaborationExchangeChunksUTF8AndPreservesRemoteModelHost(t *testing.T) {
	cfg := config.Settings{}
	cfg.Tools.CollaborationEnabled = true
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "runtime.db")
	sender := webui.CollaborationMessage{Hostname: "spark", SenderID: "muse", SenderModel: "Muse Glimmer", Provider: "ollama", Runner: "Ollama", Harness: "NexusRouter", SessionID: "remote-session", SourceInstance: "paired-spark", Private: true}
	text := strings.Repeat("界", 3000)
	ctx := context.Background()
	if e := recordExchange(ctx, cfg, "work", "reply", text, sender); e != nil {
		t.Fatal(e)
	}
	if e := recordExchange(ctx, cfg, "work", "reply", text, sender); e != nil {
		t.Fatal(e)
	}
	store, e := agentchat.Open(ctx, cfg.Telemetry.Database+".collaboration")
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	page, e := store.Read(ctx, webui.CollaborationOptions{}, "", true)
	if e != nil || len(page.Messages) != 2 || page.Messages[0].SenderModel != "Muse Glimmer" || page.Messages[0].Hostname != "spark" || page.Messages[0].SourceInstance != "paired-spark" {
		t.Fatal(page, e)
	}
	if page.Messages[1].Text+page.Messages[0].Text != text {
		t.Fatal("chunk truncation")
	}
}

func TestDelegateCollaborationRecordsExactRedactedAssignmentAndReply(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	cfg.Tools.CollaborationEnabled = true
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "tasks.db")
	db, e := telemetry.Open(ctx, cfg.Telemetry.Database)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	registry := &tools.Registry{}
	sender := webui.CollaborationMessage{Hostname: "commander", SenderID: "chief", SenderModel: "Commander Model", Provider: "local", Runner: "Ollama", Harness: "NexusRouter", SessionID: "session", Private: true}
	remoteSender := webui.CollaborationMessage{Hostname: "spark", SenderID: "muse", SenderModel: "Muse Glimmer", Provider: "ollama", Runner: "Ollama", Harness: "NexusRouter", SessionID: "remote-child", SourceInstance: "spark", Private: true}
	run := func(context.Context, string, string, string, bool) (Result, error) {
		return Result{TaskID: "remote-child", Text: "reply fixture-secret", CollaborationSender: &remoteSender}, nil
	}
	journal := redactingJournal{db: db, secrets: []string{"fixture-secret"}}
	if e = registerDelegate(registry, nil, db, journal, cfg, "parent", "session", "", true, run, nil, nil, sender); e != nil {
		t.Fatal(e)
	}
	executor := scopedDelegateTestExecutor{tools.Executor{Registry: registry, Policy: applicationToolPolicy()}}
	out, e := executor.Execute(ctx, delegateSafetyCall(`{"instance_id":"spark","prompt":"assignment fixture-secret","validation":"text"}`))
	if e != nil || out.Failed {
		t.Fatal(out, e)
	}
	store, e := agentchat.Open(ctx, cfg.Telemetry.Database+".collaboration")
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	page, e := store.Read(ctx, webui.CollaborationOptions{}, "", true)
	if e != nil || len(page.Messages) != 2 {
		t.Fatal(page, e)
	}
	if page.Messages[0].Hostname != "spark" || page.Messages[0].SenderModel != "Muse Glimmer" || page.Messages[1].Hostname != "commander" || strings.Contains(page.Messages[0].Text+page.Messages[1].Text, "fixture-secret") {
		t.Fatal("wrong or unredacted exchange", page)
	}
}
