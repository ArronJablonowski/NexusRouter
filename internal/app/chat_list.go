package app

import (
	"context"
	"errors"
	"os"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func (s *Service) ListChats(ctx context.Context, options sessions.ChatListOptions) (sessions.ChatPage, error) {
	if ctx == nil || options.Validate() != nil {
		return sessions.ChatPage{}, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return sessions.ChatPage{}, err
	}
	secrets := memorySecrets(s.settings, s.secret)
	if !selectionValueClean(options, secrets) {
		return sessions.ChatPage{}, ErrAdmission
	}
	reader, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && options.After == "" {
			return sessions.ChatPage{Version: 1, Items: []sessions.ChatSummary{}}, nil
		}
		return sessions.ChatPage{}, ErrInspection
	}
	defer reader.Close()
	preferences, err := config.ReadChatPreferences(s.settings.Telemetry.Database)
	if err != nil {
		return sessions.ChatPage{}, ErrInspection
	}
	page, err := reader.ListChatsWithPreferences(ctx, options, preferences.Chats)
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || ctx.Err() != nil || page.Validate() != nil || len(page.Items) > options.Limit || !selectionValueClean(page, secrets) {
		if ctx.Err() != nil {
			return sessions.ChatPage{}, ctx.Err()
		}
		return sessions.ChatPage{}, ErrInspection
	}
	return page, nil
}

func (s *Service) ChatPreference(ctx context.Context, r sessions.ChatPreferenceUpdate) (sessions.ChatPreference, error) {
	if ctx == nil || ctx.Err() != nil || r.Validate() != nil || !selectionValueClean(r, memorySecrets(s.settings, s.secret)) {
		return sessions.ChatPreference{}, ErrAdmission
	}
	reader, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return sessions.ChatPreference{}, ErrInspection
	}
	defer reader.Close()
	exists, err := reader.ChatExists(ctx, r.ChatID)
	if err != nil || !exists {
		return sessions.ChatPreference{}, ErrInspection
	}
	return config.UpdateChatPreference(s.settings.Telemetry.Database, r)
}

func (s *Service) ReadChatPreference(ctx context.Context, id string) (sessions.ChatPreference, error) {
	pin := false
	if ctx == nil || ctx.Err() != nil || (sessions.ChatPreferenceUpdate{Version: 1, ChatID: id, Pinned: &pin}).Validate() != nil {
		return sessions.ChatPreference{}, ErrAdmission
	}
	p, err := config.ReadChatPreferences(s.settings.Telemetry.Database)
	if err != nil {
		return sessions.ChatPreference{}, ErrInspection
	}
	out := p.Chats[id]
	if !selectionValueClean(out, memorySecrets(s.settings, s.secret)) {
		return sessions.ChatPreference{}, ErrInspection
	}
	return out, nil
}
