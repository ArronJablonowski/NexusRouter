package app

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"testing"
	"time"
)

func TestChatPreferenceServiceBindsExistingChat(t *testing.T) {
	svc, cfg, _ := intentClassifierService(t, false)
	ctx := context.Background()
	db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	event := runtime.Event{Version: 1, ID: "start", TaskID: "task", SessionID: "chat", CorrelationID: "task", Sequence: 1, Time: time.Now(), Kind: runtime.TaskStarted}
	if err = db.Append(ctx, 0, event); err != nil {
		t.Fatal(err)
	}
	db.Close()
	title := "My chat"
	request := sessions.ChatPreferenceUpdate{Version: 1, ChatID: "missing", Title: &title}
	if _, err = svc.ChatPreference(ctx, request); err == nil {
		t.Fatal("unknown chat accepted")
	}
	request.ChatID = "chat"
	if _, err = svc.ChatPreference(ctx, request); err != nil {
		t.Fatal(err)
	}
	page, err := svc.ListChats(ctx, sessions.ChatListOptions{Limit: 25})
	if err != nil || len(page.Items) != 1 || page.Items[0].Title != title {
		t.Fatal(page, err)
	}
	read, err := svc.ReadChatPreference(ctx, "chat")
	if err != nil || read.Title != title {
		t.Fatal(read, err)
	}
}
