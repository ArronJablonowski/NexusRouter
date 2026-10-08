package telemetry

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"path/filepath"
	"testing"
	"time"
)

func TestChatsPinnedAcrossPagesAndPreferenceChanges(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, id := range []string{"old", "middle", "new"} {
		appendListedTask(t, db, id, id, "completed", time.Unix(100, 0))
	}
	prefs := map[string]sessions.ChatPreference{"old": {Title: "Old but pinned", Pinned: true, Revision: 1}}
	page, err := db.ListChatsWithPreferences(ctx, sessions.ChatListOptions{Limit: 1}, prefs)
	if err != nil || len(page.Items) != 1 || page.Items[0].ChatID != "old" || page.Items[0].Title != "Old but pinned" || !page.HasMore {
		t.Fatal(page, err)
	}
	appendListedTask(t, db, "newer", "newer", "completed", time.Unix(200, 0))
	next, err := db.ListChatsWithPreferences(ctx, sessions.ChatListOptions{Limit: 1, After: page.NextCursor}, prefs)
	if err != nil || next.Items[0].ChatID != "new" {
		t.Fatal(next, err)
	}
	last, err := db.ListChatsWithPreferences(ctx, sessions.ChatListOptions{Limit: 1, After: next.NextCursor}, prefs)
	if err != nil || last.Items[0].ChatID != "middle" || last.HasMore {
		t.Fatal(last, err)
	}
	prefs["old"] = sessions.ChatPreference{Title: "Rename only", Pinned: true, Revision: 2}
	if _, err := db.ListChatsWithPreferences(ctx, sessions.ChatListOptions{Limit: 1, After: page.NextCursor}, prefs); err != nil {
		t.Fatal("rename invalidated ordering", err)
	}
	prefs["old"] = sessions.ChatPreference{Title: "Unpinned", Revision: 3}
	if _, err := db.ListChatsWithPreferences(ctx, sessions.ChatListOptions{Limit: 1, After: page.NextCursor}, prefs); err == nil {
		t.Fatal("changed pin order reused old cursor")
	}
	fresh, err := db.ListChatsWithPreferences(ctx, sessions.ChatListOptions{Limit: 10}, prefs)
	if err != nil || fresh.Items[0].ChatID != "newer" || fresh.Items[3].Title != "Unpinned" {
		t.Fatal(fresh, err)
	}
}

func TestChatsMultiplePinsKeepRecencyWithinGroups(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, id := range []string{"a", "b", "c", "d"} {
		appendListedTask(t, db, id, id, "completed", time.Unix(100, 0))
	}
	prefs := map[string]sessions.ChatPreference{"a": {Pinned: true, Revision: 1}, "c": {Pinned: true, Revision: 1}}
	after := ""
	for _, want := range []string{"c", "a", "d", "b"} {
		page, err := db.ListChatsWithPreferences(ctx, sessions.ChatListOptions{Limit: 1, After: after}, prefs)
		if err != nil || len(page.Items) != 1 || page.Items[0].ChatID != want {
			t.Fatal(page, err, want)
		}
		after = page.NextCursor
	}
	if after != "" {
		t.Fatal("unexpected continuation")
	}
}
