package tools

import (
	"encoding/json"
	"errors"
	"strings"
)

// ErrScope is an internal trusted-configuration failure. Executor deliberately
// surfaces it as ErrDenied rather than exposing resolver details to a model.
var ErrScope = errors.New("invalid tool scope")

func resolveToolScope(base string, resolver func(json.RawMessage) (string, error), arguments json.RawMessage) (scope string, err error) {
	if resolver == nil {
		return base, nil
	}
	defer func() {
		if recover() != nil {
			scope, err = "", ErrScope
		}
	}()
	// A resolver cannot rewrite the exact bytes later bound into approval and
	// dispatched to the handler.
	scope, err = resolver(append(json.RawMessage(nil), arguments...))
	if err != nil || !extensionScope(scope) || scope != base && !strings.HasPrefix(scope, base+":") {
		return "", ErrScope
	}
	return scope, nil
}

// IdentifierScope derives namespace:<argument> for one closed-schema resource
// identifier such as a workboard board_id. It rejects aliases, nesting, and
// punctuation that could create a second scope spelling for the same resource.
func IdentifierScope(namespace, field string) (func(json.RawMessage) (string, error), error) {
	if !extensionScope(namespace) || strings.Contains(namespace, ":") || !extensionName(field) {
		return nil, ErrScope
	}
	return func(raw json.RawMessage) (string, error) {
		value, decodeErr := decode(raw)
		object, objectOK := value.(map[string]any)
		identifier, identifierOK := object[field].(string)
		if decodeErr != nil || !objectOK || !identifierOK || !scopeIdentifier(identifier) {
			return "", ErrScope
		}
		return namespace + ":" + identifier, nil
	}, nil
}

func scopeIdentifier(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for index, char := range []byte(value) {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') ||
			index > 0 && (char == '_' || char == '-') {
			continue
		}
		return false
	}
	return true
}
