// Package telegrambridge joins private Telegram text chats to durable NexusRouter
// submissions. It owns a separate task database, never the commander's history.
package telegrambridge

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

var ErrState = errors.New("Telegram private state unavailable")
var ErrUncertain = errors.New("Telegram reply delivery uncertain; automatic resend stopped")

type pending struct {
	Update     int64    `json:"update"`
	Chat       int64    `json:"chat"`
	Text       string   `json:"text"`
	Previous   string   `json:"previous,omitempty"`
	Submission string   `json:"submission,omitempty"`
	Replies    []string `json:"replies,omitempty"`
	Next       int      `json:"next,omitempty"`
	Sending    bool     `json:"delivery_uncertain,omitempty"`
}
type state struct {
	Version  int              `json:"version"`
	Identity string           `json:"identity"`
	Offset   int64            `json:"offset"`
	Chats    map[int64]string `json:"chats"`
	Pending  *pending         `json:"pending,omitempty"`
}
type store struct {
	root   *os.Root
	unlock func()
	value  state
}

func openStore(directory, identity string) (*store, error) {
	if !filepath.IsAbs(directory) {
		return nil, ErrState
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, ErrState
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrState
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, ErrState
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(actual, info) {
		root.Close()
		return nil, ErrState
	}
	// Refuse pre-existing database/SQLite sidecars outside this private store.
	hasDatabase := false
	for _, name := range []string{"tasks.db", "tasks.db-wal", "tasks.db-shm", "tasks.db-journal", "tasks.db.model-use.json"} {
		st, e := root.Lstat(name)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		hasDatabase = true
		if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
			root.Close()
			return nil, ErrState
		}
	}
	unlock, err := lockState(root)
	if err != nil {
		root.Close()
		return nil, err
	}
	s := &store{root: root, unlock: unlock, value: state{Version: 1, Identity: identity, Chats: map[int64]string{}}}
	fail := func() (*store, error) { s.close(); return nil, ErrState }
	info, err = root.Lstat("state.json")
	if errors.Is(err, os.ErrNotExist) {
		if hasDatabase {
			return fail()
		}
		if s.save() != nil {
			return fail()
		}
		return s, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return fail()
	}
	f, err := root.Open("state.json")
	if err != nil {
		return fail()
	}
	actual, err = f.Stat()
	if err != nil || !os.SameFile(actual, info) {
		f.Close()
		return fail()
	}
	body, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	f.Close()
	if err != nil || len(body) > 1<<20 || json.Unmarshal(body, &s.value) != nil || s.value.Version != 1 || s.value.Identity != identity || s.value.Offset < 0 || s.value.Chats == nil || len(s.value.Chats) > 32 {
		return fail()
	}
	if p := s.value.Pending; p != nil {
		if p.Update < s.value.Offset || p.Update < 0 || p.Chat <= 0 || len(p.Text) > 16384 || len(p.Replies) > 8 || p.Next < 0 || p.Next > len(p.Replies) {
			return fail()
		}
	}
	return s, nil
}
func (s *store) save() error {
	body, err := json.Marshal(s.value)
	if err != nil || len(body) > 1<<20 {
		return ErrState
	}
	// The stable lock covers the temporary inode and the rename. Never follow a
	// pre-existing temporary path, including one left by an interrupted save.
	if err = s.root.Remove("state.tmp"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrState
	}
	f, err := s.root.OpenFile("state.tmp", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrState
	}
	_, err = f.Write(body)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return ErrState
	}
	if s.root.Rename("state.tmp", "state.json") != nil {
		return ErrState
	}
	dir, err := s.root.Open(".")
	if err != nil {
		return ErrState
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return ErrState
	}
	return nil
}
func (s *store) close() { s.unlock(); s.root.Close() }
