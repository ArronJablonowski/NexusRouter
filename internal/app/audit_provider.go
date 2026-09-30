package app

import (
	"context"
	"encoding/json"
	"sync/atomic"

	"github.com/ArronJablonowski/NexusRouter/internal/codexbridge"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

// Native auxiliary inference has one model call, no tools, and no retry.
// Construction is inert: the caller persists its attempt and admits assembled
// context before Stream. Its timeout includes CLI startup and owned cleanup.
type codexAuxiliaryProvider struct {
	settings config.Settings
	provider config.Provider
	model    config.Model
	privacy  string
	launch   codexLaunch
	used     atomic.Bool
	// Optional host guard rechecks immutable admitted inputs after estimation
	// and again after CLI startup. It cannot rewrite the estimated request.
	beforeStream func() error
}

func (p *codexAuxiliaryProvider) Models(context.Context) ([]string, error) {
	return nil, ErrAdmission // Not a discovery or health-check surface.
}

func (p *codexAuxiliaryProvider) Stream(ctx context.Context, request providers.Request, emit func(providers.Chunk) error) (err error) {
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
	if p.beforeStream != nil && p.beforeStream() != nil {
		return ErrAdmission
	}
	adapter, closeProvider, err := openOwnedCodexProvider(ctx, p.settings, p.provider, p.model, p.privacy, p.launch)
	if err != nil {
		return ErrAdmission
	}
	defer closeProvider()
	if p.beforeStream != nil && p.beforeStream() != nil {
		return ErrAdmission
	}
	return adapter.Stream(ctx, request, emit)
}

func (s *Service) openAuxiliaryProvider(ctx context.Context, provider config.Provider, model config.Model, privacy, key string) (providers.Provider, func(), error) {
	if provider.Kind == "codex_app_server" {
		if ctx == nil || ctx.Err() != nil || privacy != "cloud_allowed" || model.Locality != "cloud" || (s.settings.Mode != "hybrid" && s.settings.Mode != "cloud_only") {
			return nil, nil, ErrAdmission
		}
		return &codexAuxiliaryProvider{settings: s.settings, provider: provider, model: model, privacy: privacy, launch: s.codexLauncher}, func() {}, nil
	}
	return openTaskProvider(ctx, s.settings, provider, model, Request{providerFactory: s.providerFactory}, nil, privacy, key, providers.PurposeAuxiliary)
}
