package config

import "errors"

type VLLMSettings struct {
	Enabled             bool   `yaml:"enabled" json:"enabled"`
	ModelID             string `yaml:"model_id" json:"model_id"`
	MinimumFreeRAMBytes uint64 `yaml:"minimum_free_ram_bytes" json:"minimum_free_ram_bytes"`
}

func (s Settings) validateVLLM() error {
	if !s.VLLM.Enabled {
		return nil
	}
	if s.VLLM.MinimumFreeRAMBytes == 0 {
		return errors.New("vllm requires minimum free RAM")
	}
	for _, m := range s.Models {
		if m.ID == s.VLLM.ModelID && m.Locality == "local" && m.RAMBytes > 0 && s.VLLM.MinimumFreeRAMBytes >= m.RAMBytes {
			for _, p := range s.Providers {
				if p.ID == m.Provider && p.Kind == "openai_compatible" {
					return nil
				}
			}
		}
	}
	return errors.New("vllm requires a configured local OpenAI-compatible model")
}
