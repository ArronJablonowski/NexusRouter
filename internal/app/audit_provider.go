package app

import (
	"context"
	"encoding/json"
	"sync/atomic"

	"github.com/ArronJablonowski/DarwinRouter/internal/codexbridge"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/providers"
)

// A native audit has one model call, no tools, and no retry. Construction is
// inert: Reviewer admits the assembled context after BeginReview is durable,
// then supplies its timeout context to Stream, including CLI startup time.
type codexAuditProvider struct {
	settings config.Settings
	provider config.Provider
	model    config.Model
	privacy  string
	launch   codexLaunch
	used     atomic.Bool
}

func (p *codexAuditProvider) Models(context.Context) ([]string, error) {
	return nil, ErrAdmission // Not a discovery or health-check surface.
}

func (p *codexAuditProvider) Stream(ctx context.Context, request providers.Request, emit func(providers.Chunk) error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrAdmission
		}
	}()
	if !p.used.CompareAndSwap(false, true) || ctx == nil || ctx.Err() != nil || emit == nil || request.Model != p.model.Model || len(request.Tools) != 0 || (request.JSONSchema != nil && !json.Valid(request.JSONSchema)) || len(request.Messages) != 2 || request.Messages[0].Role != "system" || request.Messages[1].Role != "user" || codexbridge.ValidateInitialMessages(request.Messages) != nil {
		return ErrAdmission
	}
	for _, message := range request.Messages {
		if len(message.ToolCalls) != 0 || message.ToolCallID != "" {
			return ErrAdmission
		}
	}
	adapter, closeProvider, err := openOwnedCodexProvider(ctx, p.settings, p.provider, p.model, p.privacy, p.launch)
	if err != nil {
		return ErrAdmission
	}
	defer closeProvider()
	return adapter.Stream(ctx, request, emit)
}

func (s *Service) openAuditProvider(ctx context.Context, provider config.Provider, model config.Model, privacy, key string) (providers.Provider, func(), error) {
	if provider.Kind == "codex_app_server" {
		if ctx == nil || ctx.Err() != nil || privacy != "cloud_allowed" || model.Locality != "cloud" || (s.settings.Mode != "hybrid" && s.settings.Mode != "cloud_only") {
			return nil, nil, ErrAdmission
		}
		return &codexAuditProvider{settings: s.settings, provider: provider, model: model, privacy: privacy, launch: s.codexLauncher}, func() {}, nil
	}
	return openTaskProvider(ctx, s.settings, provider, model, Request{providerFactory: s.providerFactory}, nil, privacy, key)
}
