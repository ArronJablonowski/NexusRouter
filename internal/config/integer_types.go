package config

import (
	"errors"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v3"
)

// YAML's ordinary typed decoder permits float-to-integer coercion. Count and
// byte limits must instead be integer scalars, including inside model arrays.
// Derive targets from the configuration types so new integer fields are covered
// without maintaining a second hand-written list of security-sensitive paths.
func strictIntegers(n *yaml.Node, target reflect.Type) error {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	switch target.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if n.Kind != yaml.ScalarNode || n.Tag != "!!int" {
			return errors.New("integer configuration field requires an integer scalar")
		}
		// Decode the scalar alone to reject malformed explicit tags and overflow.
		if n.Decode(reflect.New(target).Interface()) != nil {
			return errors.New("integer configuration value is invalid or out of range")
		}
	case reflect.Struct:
		if n.Kind != yaml.MappingNode {
			return nil
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < target.NumField(); i++ {
			field := target.Field(i)
			name := strings.Split(field.Tag.Get("yaml"), ",")[0]
			if field.IsExported() && name != "" && name != "-" {
				fields[name] = field.Type
			}
		}
		for i := 0; i < len(n.Content); i += 2 {
			// Pressure controls are deliberately strict strings in every layer.
			// Keep legacy string coercion unchanged for unrelated settings.
			name := n.Content[i].Value
			if target == reflect.TypeOf(Hardware{}) && (name == "local_pressure_policy" || name == "local_queue_timeout") {
				value := n.Content[i+1]
				if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
					return errors.New("local pressure configuration requires string scalars")
				}
			}
			if field, ok := fields[n.Content[i].Value]; ok {
				if err := strictIntegers(n.Content[i+1], field); err != nil {
					return err
				}
			}
		}
	case reflect.Slice, reflect.Array:
		if n.Kind != yaml.SequenceNode {
			return nil
		}
		for _, child := range n.Content {
			if err := strictIntegers(child, target.Elem()); err != nil {
				return err
			}
		}
	case reflect.Map:
		if n.Kind != yaml.MappingNode {
			return nil
		}
		for i := 1; i < len(n.Content); i += 2 {
			if err := strictIntegers(n.Content[i], target.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
