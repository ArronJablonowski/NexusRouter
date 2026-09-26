package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

var (
	ErrConfigConflict = errors.New("configuration changed")
	ErrConfigWrite    = errors.New("configuration update failed")
)

// ToolAccess is the deliberately small project-file surface exposed to the
// local operator UI. It includes the adjacent specialist-locality policy but
// is not a general-purpose configuration editor.
type ToolAccess struct {
	SkillsEnabled         bool
	SkillsAutoDraft       bool
	SkillsRoot            string
	SkillsScope           string
	Enabled               bool
	DelegateReadTools     bool
	ReadRoot              string
	SpecialistsAllowCloud bool
}

func ReadProjectToolAccess(path string) (ToolAccess, string, error) {
	data, _, err := readWritableProject(path)
	if err != nil {
		return ToolAccess{}, "", err
	}
	settings, err := Load(Options{ProjectFile: path})
	if err != nil {
		return ToolAccess{}, "", err
	}
	return toolAccess(settings), configDigest(data), nil
}

// UpdateProjectToolAccess compares the exact file digest, validates the whole
// resulting configuration, and atomically replaces the project file. Runtime
// services remain unchanged until their owning process restarts.
func UpdateProjectToolAccess(path, expectedDigest string, next ToolAccess) (ToolAccess, string, error) {
	if next.SkillsEnabled && (next.SkillsRoot == "" || next.SkillsScope == "") {
		return ToolAccess{}, "", ErrConfigWrite
	}
	data, mode, err := readWritableProject(path)
	if err != nil {
		return ToolAccess{}, "", err
	}
	if expectedDigest == "" || configDigest(data) != expectedDigest {
		return ToolAccess{}, "", ErrConfigConflict
	}
	root, err := parse(data)
	if err != nil {
		return ToolAccess{}, "", err
	}
	setConfigScalar(root, []string{"tools", "enabled"}, "!!bool", boolText(next.Enabled))
	setConfigScalar(root, []string{"tools", "read_root"}, "!!str", next.ReadRoot)
	setConfigScalar(root, []string{"workers", "delegate_read_tools"}, "!!bool", boolText(next.DelegateReadTools))
	setConfigScalar(root, []string{"web_ui", "specialists_allow_cloud"}, "!!bool", boolText(next.SpecialistsAllowCloud))
	setConfigScalar(root, []string{"skills", "enabled"}, "!!bool", boolText(next.SkillsEnabled))
	setConfigScalar(root, []string{"skills", "auto_draft"}, "!!bool", boolText(next.SkillsAutoDraft))
	setConfigScalar(root, []string{"skills", "root"}, "!!str", next.SkillsRoot)
	setConfigScalar(root, []string{"skills", "scope"}, "!!str", next.SkillsScope)
	var encoded bytes.Buffer
	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(2)
	if encoder.Encode(root) != nil || encoder.Close() != nil || encoded.Len() > MaxFileBytes {
		return ToolAccess{}, "", ErrConfigWrite
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".darwin-config-*")
	if err != nil {
		return ToolAccess{}, "", ErrConfigWrite
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	ok := false
	defer func() {
		if !ok {
			_ = temporary.Close()
		}
	}()
	if temporary.Chmod(mode.Perm()) != nil || writeAndSync(temporary, encoded.Bytes()) != nil {
		return ToolAccess{}, "", ErrConfigWrite
	}
	settings, err := Load(Options{ProjectFile: temporaryPath})
	if err != nil || toolAccess(settings) != next {
		return ToolAccess{}, "", errOrWrite(err)
	}
	if os.Rename(temporaryPath, path) != nil {
		return ToolAccess{}, "", ErrConfigWrite
	}
	ok = true
	if syncDirectory(directory) != nil {
		return ToolAccess{}, "", ErrConfigWrite
	}
	return next, configDigest(encoded.Bytes()), nil
}

func readWritableProject(path string) ([]byte, os.FileMode, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxFileBytes {
		return nil, 0, ErrConfigWrite
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, ErrConfigWrite
	}
	data, readErr := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(data) > MaxFileBytes {
		return nil, 0, ErrConfigWrite
	}
	return data, info.Mode(), nil
}

func setConfigScalar(node *yaml.Node, path []string, tag, value string) {
	for len(path) > 1 {
		var child *yaml.Node
		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Value == path[0] {
				child = node.Content[i+1]
				break
			}
		}
		if child == nil {
			child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: path[0]}, child)
		}
		node, path = child, path[1:]
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == path[0] {
			node.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
			return
		}
	}
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: path[0]}, &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value})
}

func toolAccess(settings Settings) ToolAccess {
	return ToolAccess{SkillsEnabled: settings.Skills.Enabled, SkillsAutoDraft: settings.Skills.AutoDraft, SkillsRoot: settings.Skills.Root, SkillsScope: settings.Skills.Scope, Enabled: settings.Tools.Enabled, DelegateReadTools: settings.Workers.DelegateReadTools, ReadRoot: settings.Tools.ReadRoot, SpecialistsAllowCloud: settings.WebUI.SpecialistsAllowCloud}
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func configDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func writeAndSync(file *os.File, data []byte) error {
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	if closeErr := directory.Close(); err == nil {
		err = closeErr
	}
	return err
}

func errOrWrite(err error) error {
	if err != nil {
		return err
	}
	return ErrConfigWrite
}
