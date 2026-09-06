package app

import (
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

// Context leaves storage only after configured credentials are removed. Routing
// itself must not carry raw facts or secrets in its explanation metadata.
func memorySecrets(cfg config.Settings, secret func(string) string) []string {
	if secret == nil {
		return nil
	}
	secrets := []string{secret("DARWIN_API_TOKEN")}
	for _, p := range cfg.Providers {
		if p.APIKeyEnv != "" {
			secrets = append(secrets, secret(p.APIKeyEnv))
		}
	}
	return secrets
}

func contextTools(cfg config.Settings, extensions ...*tools.Extension) []string {
	var names []string
	for _, extension := range extensions {
		names = append(names, extension.Names()...)
	}
	if cfg.Tools.Enabled {
		names = append(names, "read_file")
	}
	if cfg.Tools.CreateEnabled {
		names = append(names, "create_file")
	}
	if cfg.Tools.ReplaceEnabled {
		names = append(names, "replace_file")
	}
	if cfg.Workers.DelegateModel != "" {
		names = append(names, "delegate", "delegate_batch")
	}
	return names
}

func (s *Service) bindToolExtension(r Request) Request {
	r.toolExtension = nil
	r.toolReviewer = nil
	r.toolPresenter = nil
	if r.delegatedParent == "" {
		r.toolExtension = s.toolExtension
		r.toolReviewer = s.toolReviewer
		r.toolPresenter = s.toolPresenter
		if len(r.toolExtension.Names()) > 0 {
			r.LocalRequired = true
		}
	}
	return r
}
