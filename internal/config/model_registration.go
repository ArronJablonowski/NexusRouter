package config

import (
	"bytes"
	"go.yaml.in/yaml/v3"
	"os"
	"path/filepath"
)

// RegisterCloudModel adds one catalog-selected model through an existing cloud
// provider template. No provider, credential, default or active runtime changes.
// New models are disabled before publication and require restart then opt-in.
func RegisterCloudModel(path, digest, templateID, id, model string, contextTokens int) (string, error) {
	unlock, e := lockProjectUpdate(path)
	if e != nil {
		return "", e
	}
	defer unlock()
	data, mode, e := readWritableProject(path)
	if e != nil {
		return "", e
	}
	if digest == "" || configDigest(data) != digest {
		return "", ErrConfigConflict
	}
	settings, e := loadWithReader(Options{ProjectFile: path}, func(string) ([]byte, error) { return data, nil })
	if e != nil {
		return "", e
	}
	var template *Model
	for i := range settings.Models {
		m := &settings.Models[i]
		if m.ID == id {
			return "", ErrConfigConflict
		}
		if m.ID == templateID && m.Locality == "cloud" {
			template = m
		}
	}
	if template == nil || contextTokens < 1 || contextTokens > template.ContextTokens {
		return "", ErrConfigWrite
	}
	next := *template
	next.ID = id
	next.Model = model
	next.ContextTokens = contextTokens
	next.DefaultContextTokens = 0
	next.Capabilities = []string{"chat"}
	next.ReasoningEffort = ""
	next.ResidencyDigest = ""
	next.WarmRAMBytes = 0
	root, e := parse(data)
	if e != nil {
		return "", e
	}
	var models *yaml.Node
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "models" {
			models = root.Content[i+1]
			break
		}
	}
	if models == nil || models.Kind != yaml.SequenceNode {
		return "", ErrConfigWrite
	}
	var entry yaml.Node
	if e = entry.Encode(next); e != nil {
		return "", e
	}
	models.Content = append(models.Content, &entry)
	var encoded bytes.Buffer
	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(2)
	if e = encoder.Encode(root); e != nil {
		return "", e
	}
	encoder.Close()
	checked, e := loadWithReader(Options{ProjectFile: path}, func(string) ([]byte, error) { return encoded.Bytes(), nil })
	if e != nil || len(checked.Models) != len(settings.Models)+1 {
		return "", ErrConfigWrite
	}
	// Disabled-first is deliberate: a crash before config publication can only
	// leave an unused deny entry; it never enables an unverified cloud model.
	if _, e = UpdateModelUse(settings.Telemetry.Database, "local", id, false); e != nil {
		return "", e
	}
	dir := filepath.Dir(path)
	f, e := os.CreateTemp(dir, ".model-registration-")
	if e != nil {
		return "", e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(mode.Perm()); e != nil {
		f.Close()
		return "", e
	}
	if e = writeAndSync(f, encoded.Bytes()); e != nil {
		return "", e
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return "", e
	}
	if e = syncDirectory(dir); e != nil {
		return "", e
	}
	return configDigest(encoded.Bytes()), nil
}
