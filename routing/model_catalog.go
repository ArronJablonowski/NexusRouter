package routing

import (
	"encoding/hex"
	"math"
	"strings"
)

// ConfiguredModel describes declared routing metadata, not discovered health,
// installed weights, current capacity, actual billing, or execution authority.
type ConfiguredModel struct {
	Version         int      `json:"version"`
	ID              string   `json:"id"`
	Provider        string   `json:"provider"`
	Model           string   `json:"model"`
	ReasoningEffort string   `json:"reasoning_effort,omitempty"`
	Locality        string   `json:"locality"`
	Capabilities    []string `json:"capabilities"`
	ContextTokens   int      `json:"context_tokens"`
	EstimatedCost   *float64 `json:"estimated_cost"`
	RAMBytes        uint64   `json:"ram_bytes"`
	VRAMBytes       uint64   `json:"vram_bytes"`
	GPUDevice       string   `json:"gpu_device,omitempty"`
	FailureDomain   string   `json:"failure_domain,omitempty"`
}

type ModelCatalog struct {
	Version  int               `json:"version"`
	ConfigID string            `json:"config_id"`
	Models   []ConfiguredModel `json:"models"`
}

func (m ConfiguredModel) Validate() error {
	if m.Version != 1 || !catalogIdentifier(m.ID) || !catalogIdentifier(m.Provider) || !safeExplanationLabel(m.Model, 512) || !validCatalogReasoningEffort(m.ReasoningEffort) || (m.Locality != "local" && m.Locality != "cloud") || m.ContextTokens < 0 || len(m.Capabilities) < 1 || len(m.Capabilities) > 128 || !safeOptionalExplanationLabel(m.FailureDomain, 128) || (m.FailureDomain != "" && !catalogIdentifier(m.FailureDomain)) {
		return ErrInvalid
	}
	if m.EstimatedCost != nil && (math.IsNaN(*m.EstimatedCost) || math.IsInf(*m.EstimatedCost, 0) || *m.EstimatedCost < 0) {
		return ErrInvalid
	}
	if m.GPUDevice != "" && (m.Locality != "local" || m.VRAMBytes == 0 || !safeExplanationLabel(m.GPUDevice, 128)) {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, capability := range m.Capabilities {
		if !catalogIdentifier(capability) || seen[capability] {
			return ErrInvalid
		}
		seen[capability] = true
	}
	return nil
}

func validCatalogReasoningEffort(value string) bool {
	switch value {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}

func (c ModelCatalog) Validate() error {
	if c.Version != 1 || len(c.Models) > 256 || c.Models == nil || len(c.ConfigID) != 64 || strings.ToLower(c.ConfigID) != c.ConfigID {
		return ErrInvalid
	}
	digest, err := hex.DecodeString(c.ConfigID)
	if err != nil || len(digest) != 32 {
		return ErrInvalid
	}
	ids := map[string]bool{}
	routes := map[[2]string]bool{}
	for _, model := range c.Models {
		route := [2]string{model.Provider, model.Model}
		if model.Validate() != nil || ids[model.ID] || routes[route] {
			return ErrInvalid
		}
		ids[model.ID], routes[route] = true, true
	}
	return nil
}

func catalogIdentifier(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for i, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || (i > 0 && (r == '_' || r == '.' || r == '-')) {
			continue
		}
		return false
	}
	return true
}
