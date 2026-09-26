package config

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

// WithCloudContextRecommendations returns an owned model slice. Codex's catalog
// describes the account/provider route, which can differ from the public API.
// Never substitute max_context_window (an opt-in maximum) for context_window.
// Missing, stale, or malformed metadata retains the operator's declared limit.
// No network request, inference, credential read, or local capacity probe occurs.
func WithCloudContextRecommendations(s Settings) Settings {
	needed := false
	kinds := make(map[string]string)
	for _, p := range s.Providers {
		kinds[p.ID] = p.Kind
	}
	for _, m := range s.Models {
		needed = needed || m.Locality == "cloud" && kinds[m.Provider] == "codex_app_server"
	}
	if !needed {
		return s
	}
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return s
		}
		home = filepath.Join(userHome, ".codex")
	}
	f, err := os.Open(filepath.Join(home, "models_cache.json"))
	if err != nil {
		return s
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 8<<20 {
		return s
	}
	data, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return s
	}
	windows := codexRecommendedWindows(data, time.Now())
	s.Models = append([]Model(nil), s.Models...)
	for i, m := range s.Models {
		if m.Locality == "cloud" && kinds[m.Provider] == "codex_app_server" && windows[m.Model] > 0 {
			s.Models[i].ContextTokens = windows[m.Model]
			s.Models[i].DefaultContextTokens = windows[m.Model]
		}
	}
	return s
}

func codexRecommendedWindows(data []byte, now time.Time) map[string]int {
	var catalog struct {
		FetchedAt time.Time `json:"fetched_at"`
		Models    []struct {
			Slug   string `json:"slug"`
			Window int    `json:"context_window"`
		} `json:"models"`
	}
	if json.Unmarshal(data, &catalog) != nil || catalog.FetchedAt.IsZero() || now.Sub(catalog.FetchedAt) > 24*time.Hour || catalog.FetchedAt.After(now.Add(5*time.Minute)) {
		return nil
	}
	windows := make(map[string]int)
	for _, m := range catalog.Models {
		if _, duplicate := windows[m.Slug]; duplicate {
			return nil
		}
		if m.Window < 1 || m.Window > 100_000_000 {
			return nil
		}
		windows[m.Slug] = m.Window
	}
	return windows
}
