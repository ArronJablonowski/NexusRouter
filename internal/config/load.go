package config

import (
	"bytes"
	"errors"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

const MaxFileBytes = 1 << 20

type Options struct {
	UserFile    string
	ProjectFile string
	// Env contains only scalar overrides, addressed by YAML paths such as mode
	// or workers.max_in_process. Arrays are replaced in files, never index-merged.
	Env   map[string]string
	Flags map[string]string
}

func Load(options Options) (Settings, error) {
	return loadWithReader(options, readConfigFile)
}

func readConfigFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open configuration file")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil || len(data) > MaxFileBytes {
		return nil, errors.New("configuration file exceeds limit or cannot be read")
	}
	return data, nil
}

func loadWithReader(options Options, readFile func(string) ([]byte, error)) (Settings, error) {
	data, _ := yaml.Marshal(Defaults())
	root, err := parse(data)
	if err != nil {
		return Settings{}, err
	}
	for _, path := range []string{options.UserFile, options.ProjectFile} {
		if path == "" {
			continue
		}
		data, err := readFile(path)
		if err != nil {
			return Settings{}, err
		}
		n, err := parse(data)
		if err != nil {
			return Settings{}, err
		}
		// Check every layer before merging, so an invalid shadowed field cannot hide.
		if _, err = decode(n); err != nil {
			return Settings{}, err
		}
		merge(root, n)
	}
	for _, overrides := range []map[string]string{options.Env, options.Flags} {
		if err := normalizeRoutingClassifierOverrideTypes(root, overrides); err != nil {
			return Settings{}, err
		}
		if err := seedMetricsExportOverrides(root, overrides); err != nil {
			return Settings{}, err
		}
		if err := seedTraceExportOverrides(root, overrides); err != nil {
			return Settings{}, err
		}
		keys := make([]string, 0, len(overrides))
		for k := range overrides {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := override(root, strings.Split(key, "."), overrides[key]); err != nil {
				return Settings{}, err
			}
		}
		// Reject malformed lower-precedence overrides even when a later layer
		// would replace them, matching the per-file type checks above.
		if _, err := decode(root); err != nil {
			return Settings{}, err
		}
	}
	s, err := decode(root)
	if err != nil {
		return Settings{}, err
	}
	if err = s.Validate(); err != nil {
		return Settings{}, err
	}
	return s, nil
}

func parse(data []byte) (*yaml.Node, error) {
	var doc yaml.Node
	d := yaml.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(&doc); err != nil {
		return nil, errors.New("invalid configuration YAML")
	}
	var extra yaml.Node
	if err := d.Decode(&extra); err != io.EOF {
		return nil, errors.New("exactly one configuration document required")
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("configuration must be a mapping")
	}
	if err := safeNode(doc.Content[0]); err != nil {
		return nil, err
	}
	return doc.Content[0], nil
}

func safeNode(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode || n.Tag == "!!null" || n.Tag == "!!merge" {
		return errors.New("aliases, merge keys, and null are not supported")
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || seen[k.Value] {
				return errors.New("invalid or duplicate configuration key")
			}
			seen[k.Value] = true
		}
	}
	for _, child := range n.Content {
		if err := safeNode(child); err != nil {
			return err
		}
	}
	return nil
}

func decode(n *yaml.Node) (Settings, error) {
	if err := strictIntegers(n, reflect.TypeOf(Settings{})); err != nil {
		return Settings{}, err
	}
	data, err := yaml.Marshal(n)
	if err != nil {
		return Settings{}, errors.New("cannot encode configuration")
	}
	var s Settings
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if err = d.Decode(&s); err != nil {
		return Settings{}, errors.New("unknown configuration field or invalid value type")
	}
	return s, nil
}

func merge(dst, src *yaml.Node) {
	for i := 0; i < len(src.Content); i += 2 {
		key, value := src.Content[i], src.Content[i+1]
		index := -1
		for j := 0; j < len(dst.Content); j += 2 {
			if dst.Content[j].Value == key.Value {
				index = j
				break
			}
		}
		if index < 0 {
			dst.Content = append(dst.Content, key, value)
			continue
		}
		old := dst.Content[index+1]
		if old.Kind == yaml.MappingNode && value.Kind == yaml.MappingNode {
			merge(old, value)
		} else {
			dst.Content[index+1] = value
		}
	}
}

func override(n *yaml.Node, path []string, value string) error {
	if len(path) == 0 || path[0] == "" {
		return errors.New("invalid override path")
	}
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value != path[0] {
			continue
		}
		child := n.Content[i+1]
		if len(path) > 1 {
			if child.Kind != yaml.MappingNode {
				return errors.New("override path must name a scalar setting")
			}
			return override(child, path[1:], value)
		}
		if child.Kind != yaml.ScalarNode {
			return errors.New("only scalar overrides are supported")
		}
		// Keep the schema's scalar tag; do not interpret override text as YAML.
		child.Value = value
		return nil
	}
	return errors.New("unknown override path")
}

// Environment selects NEXUS__SECTION__FIELD with legacy DARWIN__ fallback. Values remain literal;
// secret environment variables are neither read nor expanded into the config.
func Environment(env []string) map[string]string {
	out := map[string]string{}
	// Apply legacy values first; canonical names win independent of input order.
	for _, prefix := range []string{"DARWIN__", "NEXUS__"} {
		for _, entry := range env {
			key, value, ok := strings.Cut(entry, "=")
			if !ok || !strings.HasPrefix(key, prefix) {
				continue
			}
			key = strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(key, prefix), "__", "."))
			out[key] = value
		}
	}
	return out
}
