package app

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// classifyRequestIntent resolves only high-confidence structured intent. It
// deliberately does not infer from free-form prompt text: a false domain would
// contaminate routing evidence, skill discovery, evaluation and learning.
func classifyRequestIntent(r Request) (Request, error) {
	if !validIntentLabel(r.Domain) || !validIntentLabel(r.Profile) {
		return Request{}, ErrAdmission
	}
	if r.Domain == "" {
		r.Domain = structuredDomain(r)
	}
	if r.Profile == "" {
		r.Profile = "default"
	}
	// Automatic routing has always required chat by default. Explicit model
	// selection preserves an empty list as no additional capability constraint.
	if len(r.Capabilities) == 0 && (r.ModelID == "" || r.ModelID == "auto") {
		r.Capabilities = []string{"chat"}
	}
	return r, nil
}

func validIntentLabel(value string) bool {
	return value == "" || len(value) <= 128 && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsFunc(value, unicode.IsControl)
}

func structuredDomain(r Request) string {
	if r.Validation == "go_source" {
		return "code"
	}
	selected := ""
	for _, capability := range r.Capabilities {
		domain := capabilityDomain(capability)
		if domain == "" {
			continue
		}
		if selected != "" && selected != domain {
			return "general"
		}
		selected = domain
	}
	if selected == "" {
		return "general"
	}
	return selected
}

func capabilityDomain(capability string) string {
	switch capability {
	case "code", "coding", "debugging", "complex_code", "code_generation":
		return "code"
	case "math", "mathematics":
		return "math"
	case "json", "structured_json", "structured_output":
		return "structured_json"
	case "creative", "creative_writing":
		return "creative"
	default:
		return ""
	}
}
