package telemetry

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

var ErrRecoveryRedaction = errors.New("recovery event requires redaction")

func recoveryEventsContainSecrets(events []runtime.Event, secrets []string) bool {
	canonical := make([]string, 0, len(secrets))
	seen := make(map[string]struct{}, len(secrets))
	total := 0
	for _, secret := range secrets {
		if !utf8.ValidString(secret) {
			return true
		}
		if secret == "" {
			continue
		}
		if _, duplicate := seen[secret]; duplicate {
			continue
		}
		seen[secret] = struct{}{}
		canonical = append(canonical, secret)
		total += len(secret)
		if len(canonical) > 64 || total > 64<<10 {
			return true
		}
	}
	for _, event := range events {
		body, err := json.Marshal(event)
		if err != nil || len(body) > 8<<20 {
			return true
		}
		var value any
		if json.Unmarshal(body, &value) != nil || valueContainsRecoverySecret(value, canonical) {
			return true
		}
	}
	return false
}

func valueContainsRecoverySecret(value any, secrets []string) bool {
	switch typed := value.(type) {
	case string:
		for _, secret := range secrets {
			if secret != "" && strings.Contains(typed, secret) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if valueContainsRecoverySecret(item, secrets) {
				return true
			}
		}
	case map[string]any:
		for _, item := range typed {
			if valueContainsRecoverySecret(item, secrets) {
				return true
			}
		}
	}
	return false
}
