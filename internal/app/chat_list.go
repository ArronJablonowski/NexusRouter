package app

import (
	"context"
	"errors"
	"os"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
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
	page, err := reader.ListChats(ctx, options)
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || ctx.Err() != nil || page.Validate() != nil || len(page.Items) > options.Limit || !selectionValueClean(page, secrets) {
		if ctx.Err() != nil {
			return sessions.ChatPage{}, ctx.Err()
		}
		return sessions.ChatPage{}, ErrInspection
	}
	return page, nil
}
