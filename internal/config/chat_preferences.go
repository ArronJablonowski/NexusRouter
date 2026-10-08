package config

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/ArronJablonowski/NexusRouter/sessions"
)

type ChatPreferences struct {
	Version int                                `json:"version"`
	Chats   map[string]sessions.ChatPreference `json:"chats"`
}

func ReadChatPreferences(database string) (ChatPreferences, error) {
	p := ChatPreferences{Version: 1, Chats: map[string]sessions.ChatPreference{}}
	if database == "" || database == ":memory:" {
		return p, nil
	}
	path := database + ".chat-preferences.json"
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return p, ErrConfigWrite
	}
	f, err := os.Open(path)
	if err != nil {
		return p, ErrConfigWrite
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 || json.Unmarshal(raw, &p) != nil || p.Version != 1 || p.Chats == nil || len(p.Chats) > 1000 {
		return ChatPreferences{}, ErrConfigWrite
	}
	for id, item := range p.Chats {
		pin := item.Pinned
		if (sessions.ChatPreferenceUpdate{Version: 1, ChatID: id, ExpectedRevision: 0, Pinned: &pin}).Validate() != nil || !sessions.ValidChatTitle(item.Title) || item.Revision < 1 || item.Revision > 9007199254740991 {
			return ChatPreferences{}, ErrConfigWrite
		}
	}
	return p, nil
}
func UpdateChatPreference(database string, r sessions.ChatPreferenceUpdate) (sessions.ChatPreference, error) {
	if r.Validate() != nil || database == "" || database == ":memory:" {
		return sessions.ChatPreference{}, ErrConfigWrite
	}
	path := database + ".chat-preferences.json"
	unlock, err := lockProjectUpdate(path)
	if err != nil {
		return sessions.ChatPreference{}, err
	}
	defer unlock()
	p, err := ReadChatPreferences(database)
	if err != nil {
		return sessions.ChatPreference{}, err
	}
	item := p.Chats[r.ChatID]
	if item.Revision != r.ExpectedRevision {
		return item, ErrConfigConflict
	}
	if item.Revision == 0 && len(p.Chats) >= 1000 {
		return item, ErrConfigWrite
	}
	if r.Title != nil {
		item.Title = *r.Title
	}
	if r.Pinned != nil {
		item.Pinned = *r.Pinned
	}
	item.Revision++
	p.Chats[r.ChatID] = item
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > 1<<20 {
		return sessions.ChatPreference{}, ErrConfigWrite
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".chat-preferences-*")
	if err != nil {
		return sessions.ChatPreference{}, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = writeAndSync(f, raw); err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err == nil {
		err = syncDirectory(filepath.Dir(path))
	}
	return item, err
}
