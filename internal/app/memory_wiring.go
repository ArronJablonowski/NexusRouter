package app

import "github.com/ArronJablonowski/DarwinRouter/internal/config"

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

func contextTools(cfg config.Settings) []string {
	if cfg.Tools.Enabled {
		return []string{"read_file"}
	}
	return nil
}
