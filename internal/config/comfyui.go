package config

import (
	"github.com/ArronJablonowski/NexusRouter/connectors/comfyui"
	"path/filepath"
)

// ComfyUI is operator-owned; neither models nor browser requests select targets.
type ComfyUI struct {
	Enabled          bool   `yaml:"enabled" json:"enabled"`
	Endpoint         string `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	ReceiptDirectory string `yaml:"receipt_directory,omitempty" json:"receipt_directory,omitempty"`
}

func (c ComfyUI) Validate() bool {
	if !c.Enabled {
		return true
	}
	_, e := comfyui.New(c.Endpoint)
	return e == nil && filepath.IsAbs(c.ReceiptDirectory) && len(c.ReceiptDirectory) <= 4096
}
