package openclaw

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// isolatedFiles prepares a fresh private directory owned by a future runner.
// gatewayKey must be an ephemeral child token, never an upstream credential.
// This is configuration isolation for a trusted installation, not an OS sandbox.
// No production Run API is exposed until gateway and lifecycle binding exist.
func isolatedFiles(dir, gatewayURL, gatewayKey, provider, model string, contextTokens, outputTokens int) (configPath string, env []string, err error) {
	u, e := url.Parse(gatewayURL)
	if e != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Path != "/v1" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || !identifier(gatewayKey) || !identifier(provider) || strings.Contains(provider, "/") || !identifier(model) || contextTokens < 8192 || outputTokens < 1 || outputTokens > 65536 || outputTokens >= contextTokens || !filepath.IsAbs(dir) {
		return "", nil, ErrProjection
	}
	port, e := strconv.Atoi(u.Port())
	if e != nil || port < 1 || port > 65535 {
		return "", nil, ErrProjection
	}
	info, e := os.Lstat(dir)
	if e != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", nil, ErrProjection
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) != 0 {
		return "", nil, ErrProjection
	}
	config := map[string]any{
		"env":     map[string]any{"shellEnv": map[string]bool{"enabled": false}},
		"plugins": map[string]bool{"enabled": false},
		"tools":   map[string]any{"deny": []string{"*"}, "codeMode": map[string]bool{"enabled": false}},
		"skills":  map[string]any{"allowBundled": []string{}},
		"models": map[string]any{"mode": "replace", "providers": map[string]any{provider: map[string]any{
			"baseUrl": gatewayURL, "apiKey": gatewayKey, "api": "openai-completions",
			"agentRuntime": map[string]string{"id": "openclaw"},
			"models": []any{map[string]any{"id": model, "name": model, "reasoning": false, "input": []string{"text"}, "contextWindow": contextTokens, "contextTokens": contextTokens, "maxTokens": outputTokens,
				// Child costs are presentation only; host accounting must use the
				// real admitted model prices and verified gateway usage.
				"cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}}},
		}}},
		"agents": map[string]any{"defaults": map[string]any{
			"model":         map[string]any{"primary": provider + "/" + model, "fallbacks": []string{}},
			"skipBootstrap": true, "skills": []string{},
		}},
	}
	body, e := json.Marshal(config)
	if e != nil {
		return "", nil, ErrProjection
	}
	configPath = filepath.Join(dir, "config.json")
	file, e := os.OpenFile(configPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return "", nil, e
	}
	_, writeErr := file.Write(body)
	closeErr := file.Close()
	if writeErr != nil {
		return "", nil, writeErr
	}
	if closeErr != nil {
		return "", nil, closeErr
	}
	// Deliberately omit provider keys, auth-store paths, NODE_OPTIONS, proxies,
	// plugin paths and shell startup environment. Do not alter HOME.
	env = []string{"PATH=" + os.Getenv("PATH"), "OPENCLAW_HOME=" + dir, "OPENCLAW_STATE_DIR=" + filepath.Join(dir, "state"), "OPENCLAW_AGENT_DIR=" + filepath.Join(dir, "agent"), "OPENCLAW_CONFIG_PATH=" + configPath, "NO_COLOR=1", "OPENCLAW_NO_AUTO_UPDATE=1", "OPENCLAW_NODE_UPDATE_RESPAWNED=1"}
	return configPath, env, nil
}
