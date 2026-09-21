package providers

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"
)

const installedInventoryLimit = 1 << 20

// InstalledModel is provider-reported, read-only metadata for a model present
// on the local host. SizeBytes is the model blob size reported by the provider;
// aliases and shared layers mean it is not necessarily unique physical usage.
type InstalledModel struct {
	Name          string
	Digest        string
	SizeBytes     uint64
	ModifiedAt    time.Time
	Family        string
	ParameterSize string
	Quantization  string
	ContextTokens int64
}

// ModelInventoryProvider is an optional provider capability. Cloud adapters
// and custom providers that cannot authoritatively describe local storage do
// not need to implement it.
type ModelInventoryProvider interface {
	InstalledModels(context.Context) ([]InstalledModel, error)
}

func (p *HTTP) InstalledModels(ctx context.Context) ([]InstalledModel, error) {
	if p == nil || p.kind != "ollama" || ctx == nil {
		return nil, &Failure{Code: "invalid_request"}
	}
	resp, err := p.send(ctx, "GET", "/api/tags", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, installedInventoryLimit+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(body) > installedInventoryLimit {
		return nil, &Failure{Code: "invalid_response"}
	}
	var envelope struct {
		Models []struct {
			Name       string    `json:"name"`
			Model      string    `json:"model"`
			ModifiedAt time.Time `json:"modified_at"`
			Size       *uint64   `json:"size"`
			Digest     string    `json:"digest"`
			Details    *struct {
				Family            string `json:"family"`
				ParameterSize     string `json:"parameter_size"`
				QuantizationLevel string `json:"quantization_level"`
			} `json:"details"`
		} `json:"models"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Models == nil || len(envelope.Models) > 4096 {
		return nil, &Failure{Code: "invalid_response"}
	}
	out := make([]InstalledModel, 0, len(envelope.Models))
	seen := map[string]bool{}
	for _, item := range envelope.Models {
		name := item.Name
		if name == "" {
			name = item.Model
		}
		identity, identityErr := OllamaModelIdentity(name)
		if identityErr != nil || seen[identity] || item.Size == nil || *item.Size == 0 || !validInventoryDigest(item.Digest) ||
			!inventoryText(item.Details, item.ModifiedAt) {
			return nil, &Failure{Code: "invalid_response"}
		}
		seen[identity] = true
		model := InstalledModel{Name: identity, Digest: item.Digest, SizeBytes: *item.Size, ModifiedAt: item.ModifiedAt}
		if item.Details != nil {
			model.Family = item.Details.Family
			model.ParameterSize = item.Details.ParameterSize
			model.Quantization = item.Details.QuantizationLevel
		}
		contextTokens, showErr := p.ollamaContextWindow(ctx, identity)
		if showErr != nil {
			return nil, showErr
		}
		model.ContextTokens = contextTokens
		out = append(out, model)
	}
	return out, nil
}

func (p *HTTP) ollamaContextWindow(ctx context.Context, model string) (int64, error) {
	resp, err := p.send(ctx, "POST", "/api/show", map[string]string{"model": model})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, installedInventoryLimit+1))
	if err != nil || len(body) > installedInventoryLimit {
		return 0, &Failure{Code: "invalid_response"}
	}
	var envelope struct {
		ModelInfo map[string]json.RawMessage `json:"model_info"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.ModelInfo == nil {
		return 0, &Failure{Code: "invalid_response"}
	}
	var found int64
	for key, raw := range envelope.ModelInfo {
		if !strings.HasSuffix(key, ".context_length") {
			continue
		}
		var value int64
		if json.Unmarshal(raw, &value) != nil || value < 1 || value > MaxOutputTokens || found != 0 && found != value {
			return 0, &Failure{Code: "invalid_response"}
		}
		found = value
	}
	return found, nil
}

func validInventoryDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func inventoryText(details *struct {
	Family            string `json:"family"`
	ParameterSize     string `json:"parameter_size"`
	QuantizationLevel string `json:"quantization_level"`
}, modified time.Time) bool {
	if !modified.IsZero() && (modified.Year() < 1970 || modified.Year() > 2260) {
		return false
	}
	if details == nil {
		return true
	}
	for _, value := range []string{details.Family, details.ParameterSize, details.QuantizationLevel} {
		if len(value) > 128 || strings.TrimSpace(value) != value {
			return false
		}
	}
	return true
}
