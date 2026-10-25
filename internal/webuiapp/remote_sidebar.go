package webuiapp

import (
	"context"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/internal/chatfiles"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (d *RecordedRemoteDispatcher) indexConversations(ctx context.Context) (bool, error) {
	if d == nil || d.Store == nil || d.UploadDirectory == "" {
		return false, remote.ErrInvalid
	}
	d.indexMu.Lock()
	defer d.indexMu.Unlock()
	if d.indexDirty.Swap(false) {
		d.indexAfter = 0
	}
	store, e := chatfiles.Open(ctx, d.UploadDirectory)
	if e != nil {
		return false, e
	}
	defer store.Close()
	snapshots, e := store.ConversationSnapshots(ctx, d.indexAfter, 25)
	if e != nil {
		return false, e
	}
	for _, v := range snapshots {
		var c remoteConversation
		// Scan only conversation snapshots. Invalid/unbound preparation is never
		// promoted into a chat or execution authority. Later dispatch resets scan.
		if json.Unmarshal(v.Body, &c) == nil {
			valid, err := d.conversation(ctx, c.Instance, c.RequestID)
			if err == nil {
				title := []rune(strings.Join(strings.Fields(valid.Messages[0].Text), " "))
				if len(title) > 100 {
					title = append(title[:97], '.', '.', '.')
				}
				entry := chatfiles.RemoteIndex{Instance: c.Instance, RequestID: c.RequestID, Root: valid.Messages[0].RequestID, Title: string(title), Model: c.Task.ExpectedModel, CreatedAt: c.CreatedAt}
				if e = store.IndexConversation(ctx, conversationScope(c.Instance, c.RequestID), entry); e != nil {
					return false, e
				}
			}
		}
		d.indexAfter = v.Sequence
	}
	return len(snapshots) > 0, nil
}
func (d *RecordedRemoteDispatcher) savedConversation(ctx context.Context, instance, key string) (remoteTaskControlPage, error) {
	c, e := d.conversation(ctx, instance, key)
	if e != nil {
		return remoteTaskControlPage{}, e
	}
	p := remoteTaskControlPage{Version: 1, Instance: instance, RequestID: key, State: "queued", Cached: true, Conversation: c.Messages}
	store, e := chatfiles.Open(ctx, d.UploadDirectory)
	if e != nil {
		return p, e
	}
	defer store.Close()
	if b, e := store.RemoteObservation(ctx, conversationScope(instance, key)); e == nil {
		var saved remoteTaskControlPage
		if json.Unmarshal(b, &saved) != nil || saved.Instance != instance || saved.RequestID != key || saved.Version != 1 || !saved.Cached {
			return p, remote.ErrInvalid
		}
		p = saved
		p.Conversation = c.Messages
		if p.State == "succeeded" && p.ResultText != "" {
			p.Conversation = append(p.Conversation, remoteConversationMessage{Role: "assistant", Text: p.ResultText, RequestID: key, Model: c.Task.ExpectedModel, Host: instance})
		}
	}
	// No cached projection ever grants a tool, continuation or cancellation.
	p.Continuation = nil
	p.Cached = true
	return p, nil
}
func (d *RecordedRemoteDispatcher) cacheConversation(ctx context.Context, p remoteTaskControlPage) error {
	if p.State != "succeeded" && p.State != "failed" && p.State != "canceled" {
		return nil
	}
	if _, e := d.conversation(ctx, p.Instance, p.RequestID); e != nil {
		return e
	}
	store, e := chatfiles.Open(ctx, d.UploadDirectory)
	if e != nil {
		return e
	}
	defer store.Close()
	p.Conversation = nil
	p.Continuation = nil
	p.Cached = true
	if old, e := store.RemoteObservation(ctx, conversationScope(p.Instance, p.RequestID)); e == nil {
		var prior remoteTaskControlPage
		if json.Unmarshal(old, &prior) != nil {
			return remote.ErrInvalid
		}
		p.ObservedAt = prior.ObservedAt
	}
	b, e := json.Marshal(p)
	if e != nil {
		return e
	}
	return store.CacheRemoteObservation(ctx, conversationScope(p.Instance, p.RequestID), b)
}
func (h *Handler) serveRemoteChats(w http.ResponseWriter, r *http.Request) bool {
	prefix := h.basePath + "/api/v1/remote-chats"
	if r.URL.Path != prefix && !strings.HasPrefix(r.URL.Path, prefix+"/") {
		return false
	}
	if !h.authenticated(r) {
		h.writeError(w, r, 401, "session_required")
		return true
	}
	if !strictBrowserGET(r) {
		h.writeError(w, r, 400, "invalid_request")
		return true
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		h.writeError(w, r, 503, "browser_capacity")
		return true
	}
	d, ok := h.remoteDispatcher.(*RecordedRemoteDispatcher)
	if !ok || d.UploadDirectory == "" {
		h.writeJSON(w, 200, struct {
			Version  int                     `json:"version"`
			Items    []chatfiles.RemoteIndex `json:"items"`
			Next     string                  `json:"next_cursor"`
			Indexing bool                    `json:"indexing"`
		}{1, []chatfiles.RemoteIndex{}, "", false})
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if r.URL.Path != prefix {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, prefix+"/"), "/")
		if len(parts) != 2 || !remoteControlID(parts[0], 1) || !remoteControlID(parts[1], 16) || r.URL.RawQuery != "" {
			h.writeError(w, r, 400, "invalid_request")
			return true
		}
		p, e := d.savedConversation(ctx, parts[0], parts[1])
		if e != nil {
			h.writeError(w, r, 404, "conversation_unavailable")
			return true
		}
		h.writeJSON(w, 200, p)
		return true
	}
	q, queryErr := url.ParseQuery(r.URL.RawQuery)
	if queryErr != nil || len(r.URL.RawQuery) > 4096 {
		h.writeError(w, r, 400, "invalid_request")
		return true
	}
	after := int64(0)
	if len(q) > 1 || len(q["after"]) > 1 {
		h.writeError(w, r, 400, "invalid_request")
		return true
	}
	if len(q) == 1 {
		v, exists := q["after"]
		if !exists || len(v) != 1 {
			h.writeError(w, r, 400, "invalid_request")
			return true
		}
		var e error
		after, e = strconv.ParseInt(v[0], 10, 64)
		if e != nil || after < 1 || strconv.FormatInt(after, 10) != v[0] {
			h.writeError(w, r, 400, "invalid_request")
			return true
		}
	}
	indexing, e := d.indexConversations(ctx)
	if e != nil {
		h.writeError(w, r, 503, "conversations_unavailable")
		return true
	}
	store, e := chatfiles.Open(ctx, d.UploadDirectory)
	if e != nil {
		h.writeError(w, r, 503, "conversations_unavailable")
		return true
	}
	defer store.Close()
	items, more, e := store.RemoteChats(ctx, after, 25)
	if e != nil {
		h.writeError(w, r, 503, "conversations_unavailable")
		return true
	}
	next := ""
	if more {
		next = chatfiles.RemoteCursor(items[len(items)-1].Sequence)
	}
	h.writeJSON(w, 200, struct {
		Version  int                     `json:"version"`
		Items    []chatfiles.RemoteIndex `json:"items"`
		Next     string                  `json:"next_cursor"`
		Indexing bool                    `json:"indexing"`
	}{1, items, next, indexing})
	return true
}
