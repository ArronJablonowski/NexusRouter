package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

var ErrResidency = errors.New("model residency operation unavailable or unconfirmed")

type ResidentModel struct {
	Name          string    `json:"name"`
	Model         string    `json:"model"`
	Size          uint64    `json:"size"`
	SizeVRAM      uint64    `json:"size_vram"`
	Digest        string    `json:"digest"`
	ExpiresAt     time.Time `json:"expires_at"`
	ContextLength int       `json:"context_length,omitempty"`
}
type ResidencyController interface {
	ResidentModels(context.Context) ([]ResidentModel, error)
	UnloadModel(context.Context, string) error
}

// OllamaModelIdentity applies only Ollama's documented default latest tag.
// Registry/library aliases, digest equivalence and case folding are not inferred.
func OllamaModelIdentity(name string) (string, error) {
	if name == "" || len(name) > 256 || strings.TrimSpace(name) != name {
		return "", ErrResidency
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-/:", r)) {
			return "", ErrResidency
		}
	}
	parts := strings.Split(name, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", ErrResidency
		}
	}
	last := parts[len(parts)-1]
	if strings.Count(last, ":") > 1 || strings.HasPrefix(last, ":") || strings.HasSuffix(last, ":") {
		return "", ErrResidency
	}
	if !strings.Contains(last, ":") {
		name += ":latest"
	}
	if len(name) > 256 {
		return "", ErrResidency
	}
	return name, nil
}

// ResidentModels observes running models, not installed/downloaded models.
func (p *HTTP) ResidentModels(ctx context.Context) ([]ResidentModel, error) {
	if p == nil || p.kind != "ollama" || ctx == nil || ctx.Err() != nil {
		return nil, ErrResidency
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	body, err := p.residencyRequest(bounded, http.MethodGet, "/api/ps", nil)
	if err != nil {
		return nil, ErrResidency
	}
	models, err := decodeResidency(body)
	if err != nil || bounded.Err() != nil {
		return nil, ErrResidency
	}
	return models, nil
}

// UnloadModel is an explicit residency mutation, never model deletion or pull.
// Success requires the unload acknowledgement AND subsequent observed absence.
// Failure/timeout can mean the unload happened; it never authorizes a retry.
func (p *HTTP) UnloadModel(ctx context.Context, model string) error {
	identity, err := OllamaModelIdentity(model)
	if err != nil || p == nil || p.kind != "ollama" || ctx == nil || ctx.Err() != nil {
		return ErrResidency
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	payload, _ := json.Marshal(struct {
		Model     string `json:"model"`
		Prompt    string `json:"prompt"`
		KeepAlive int    `json:"keep_alive"`
		Stream    bool   `json:"stream"`
	}{model, "", 0, false})
	body, err := p.residencyRequest(bounded, http.MethodPost, "/api/generate", payload)
	if err != nil || decodeUnload(body, identity) != nil {
		return ErrResidency
	}
	models, err := p.ResidentModels(bounded)
	if err != nil {
		return ErrResidency
	}
	for _, resident := range models {
		name, _ := OllamaModelIdentity(resident.Name)
		model, _ := OllamaModelIdentity(resident.Model)
		if name == identity || model == identity {
			return ErrResidency
		}
	}
	if bounded.Err() != nil {
		return ErrResidency
	}
	return nil
}

const residencyLimit = 1 << 20

func (p *HTTP) residencyRequest(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, p.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, ErrResidency
	}
	request.GetBody = nil // Non-idempotent unload must not be transparently replayed.
	request.Header.Set("Content-Type", "application/json")
	if p.key != "" {
		request.Header.Set("Authorization", "Bearer "+p.key)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return nil, ErrResidency
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, ErrResidency
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, residencyLimit+1))
	if err != nil || len(raw) > residencyLimit || ctx.Err() != nil {
		return nil, ErrResidency
	}
	return raw, nil
}
