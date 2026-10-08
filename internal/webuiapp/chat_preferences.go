package webuiapp

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func (h *Handler) serveChatPreference(w http.ResponseWriter, r *http.Request) {
	if !h.requireMutationAuthority(w, r) {
		return
	}
	select {
	case h.mutationSlots <- struct{}{}:
		defer func() { <-h.mutationSlots }()
	default:
		h.writeError(w, r, 503, "browser_capacity")
		return
	}
	var input sessions.ChatPreferenceUpdate
	if r.Method != http.MethodPost || decodeMutationJSON(r, &input, 4096) != nil || input.Validate() != nil {
		h.writeError(w, r, 400, "invalid_chat_preference")
		return
	}
	if h.mutations.ChatPreference == nil {
		h.writeError(w, r, 503, "chat_preferences_unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	out, err := safeCall(func() (sessions.ChatPreference, error) { return h.mutations.ChatPreference(ctx, input) })
	if errors.Is(err, config.ErrConfigConflict) {
		h.writeError(w, r, 409, "chat_changed")
		return
	}
	if err != nil {
		h.writeError(w, r, 503, "chat_preferences_unavailable")
		return
	}
	h.writeJSON(w, 200, struct {
		Version    int                     `json:"version"`
		ChatID     string                  `json:"chat_id"`
		Preference sessions.ChatPreference `json:"preference"`
	}{1, input.ChatID, out})
}

func (h *Handler) readChatPreference(w http.ResponseWriter, r *http.Request, id string) {
	if !h.authenticated(r) {
		h.writeError(w, r, 401, "unauthorized")
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		h.writeError(w, r, 503, "browser_capacity")
		return
	}
	pin := false
	if !strictBrowserGET(r) || (sessions.ChatPreferenceUpdate{Version: 1, ChatID: id, Pinned: &pin}).Validate() != nil {
		h.writeError(w, r, 400, "invalid_request")
		return
	}
	if h.reads.ChatPreference == nil {
		h.writeError(w, r, 503, "chat_preferences_unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	out, err := safeCall(func() (sessions.ChatPreference, error) { return h.reads.ChatPreference(ctx, id) })
	if err != nil {
		h.writeError(w, r, 503, "chat_preferences_unavailable")
		return
	}
	h.writeJSON(w, 200, struct {
		Version    int                     `json:"version"`
		ChatID     string                  `json:"chat_id"`
		Preference sessions.ChatPreference `json:"preference"`
	}{1, id, out})
}
