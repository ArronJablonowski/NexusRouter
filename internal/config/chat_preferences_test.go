package config

import (
	"errors"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestChatPreferencesPersistAndFenceConcurrentEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	title := "A renamed chat"
	pin := true
	first, err := UpdateChatPreference(path, sessions.ChatPreferenceUpdate{Version: 1, ChatID: "chat", Title: &title})
	if err != nil || first.Revision != 1 {
		t.Fatal(first, err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := UpdateChatPreference(path, sessions.ChatPreferenceUpdate{Version: 1, ChatID: "chat", ExpectedRevision: 1, Pinned: &pin})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrConfigConflict) && !errors.Is(err, ErrConfigWrite) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatal("lost update protection", successes)
	}
	saved, err := ReadChatPreferences(path)
	if err != nil || saved.Chats["chat"].Title != title || !saved.Chats["chat"].Pinned || saved.Chats["chat"].Revision != 2 {
		t.Fatal(saved, err)
	}
	info, err := os.Stat(path + ".chat-preferences.json")
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("preferences not private", err)
	}
	if _, err = UpdateChatPreference(path, sessions.ChatPreferenceUpdate{Version: 1, ChatID: "chat", ExpectedRevision: 1, Title: &title}); !errors.Is(err, ErrConfigConflict) {
		t.Fatal("stale edit accepted", err)
	}
}
func TestChatPreferencesRejectInvalidAndCorruptData(t *testing.T) {
	for _, title := range []string{"", "  ", "bad\nname", string([]byte{255})} {
		if (sessions.ChatPreferenceUpdate{Version: 1, ChatID: "chat", Title: &title}).Validate() == nil {
			t.Fatal("invalid title accepted")
		}
	}
	path := filepath.Join(t.TempDir(), "tasks.db")
	_ = os.WriteFile(path+".chat-preferences.json", []byte(`{"version":1,"chats":{"chat":{"title":"bad","revision":-1}}}`), 0600)
	if _, err := ReadChatPreferences(path); err == nil {
		t.Fatal("corruption accepted")
	}
}
