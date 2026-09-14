package runtime

import (
	"encoding/hex"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// IntentClassificationUse is the redacted, terminal attribution for the
// optional auxiliary classification that informed a task's frozen intent. It
// contains no prompt, raw model output, provider identity, or usage data.
type IntentClassificationUse struct {
	Version        int    `json:"version"`
	AttemptID      string `json:"attempt_id"`
	Status         string `json:"status"`
	Code           string `json:"code,omitempty"`
	DecisionDigest string `json:"decision_digest,omitempty"`
}

func (u IntentClassificationUse) Validate() error {
	if u.Version != 1 || !validIntentClassificationID(u.AttemptID) {
		return errors.New("invalid intent classification use")
	}
	switch u.Status {
	case "completed":
		if u.Code != "" || !validIntentClassificationDigest(u.DecisionDigest) {
			return errors.New("invalid completed intent classification use")
		}
	case "failed":
		if u.DecisionDigest != "" || !validIntentClassificationFailureCode(u.Code) {
			return errors.New("invalid failed intent classification use")
		}
	case "canceled":
		if u.Code != "canceled" || u.DecisionDigest != "" {
			return errors.New("invalid canceled intent classification use")
		}
	default:
		return errors.New("invalid intent classification status")
	}
	return nil
}

func validIntentClassificationFailureCode(code string) bool {
	switch code {
	case "provider_failed", "invalid_response", "timeout", "persistence_failed":
		return true
	default:
		return false
	}
}

func validIntentClassificationID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index, b := range []byte(value) {
		if b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || index > 0 && (b == '_' || b == '.' || b == '-') {
			continue
		}
		return false
	}
	return true
}

func validIntentClassificationDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func validTaskCapabilities(values []string) bool {
	if len(values) > 128 {
		return false
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if len(value) == 0 || len(value) > 128 || !utf8.ValidString(value) || strings.TrimSpace(value) != value || strings.ContainsFunc(value, unicode.IsControl) || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}
