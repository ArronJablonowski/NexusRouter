package cli

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

var errWebInstallation = errors.New("Web UI installation is missing or ambiguous; use --config with NEXUS_API_TOKEN, or --service for one running macOS service")

type webInstallation struct {
	label, path, token string
	env                map[string]string
}

// Explicit configuration wins. Automatic discovery never guesses between
// daemons or searches archives, the working directory, or private databases.
func resolveWebInstallation(path, label, token, home, userConfig string, env map[string]string, discover func() ([]webInstallation, error)) (webInstallation, error) {
	selected := webInstallation{path: path, token: token, env: env}
	if path != "" {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return selected, errWebInstallation
		}
		selected.path = absolute
	}
	if selected.path != "" && token != "" && label == "" {
		return selected, nil
	}
	services, err := discover()
	if err != nil {
		return selected, errWebInstallation
	}
	var matches []webInstallation
	for _, service := range services {
		if label != "" && service.label != label {
			continue
		}
		if selected.path != "" && service.path != selected.path {
			continue
		}
		matches = append(matches, service)
	}
	if len(matches) > 1 {
		return selected, errWebInstallation
	}
	if len(matches) == 1 {
		selected = matches[0]
		if token != "" {
			selected.token = token
		}
		return selected, nil
	}
	if label != "" {
		return selected, errWebInstallation
	}
	if selected.path != "" {
		return selected, nil
	}
	if !filepath.IsAbs(home) || !filepath.IsAbs(userConfig) {
		return selected, errWebInstallation
	}
	candidates := []string{
		filepath.Join(home, ".NexusRouter/config/config.yaml"),
		filepath.Join(home, ".NexusRouter/config/commander-config.json"),
		filepath.Join(home, ".NexusRouter/config/config.json"),
		filepath.Join(userConfig, "nexusrouter/config.yaml"),
		filepath.Join(userConfig, "darwinrouter/config.yaml"),
		filepath.Join(home, ".NexusRouter/data/live-test/config.yaml"),
		filepath.Join(home, "Library/Application Support/NexusRouter/live-test/config.yaml"),
		filepath.Join(home, "Library/Application Support/DarwinRouter/live-test/config.yaml"),
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		info, err := os.Stat(candidate)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return selected, errWebInstallation
		}
		// Invalid or non-Web configurations must not silently select another daemon.
		cfg, err := config.Load(config.Options{ProjectFile: candidate, Env: env})
		if err != nil {
			return selected, errWebInstallation
		}
		if !cfg.WebUI.Enabled {
			continue
		}
		if selected.path != "" {
			return selected, errWebInstallation
		}
		selected.path = candidate
	}
	if selected.path == "" {
		return selected, errWebInstallation
	}
	return selected, nil
}
