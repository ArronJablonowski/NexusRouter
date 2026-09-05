package skills

import (
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"
)

// ValidateContextMetadata checks progressive-discovery metadata before Load.
// It does not prove activation: Discover's active-only contract remains the
// responsibility of the trusted store implementation.
func ValidateContextMetadata(m Metadata, scope, domain string) error {
	if len(scope) > 64 || len(domain) > 64 || len(m.Key.Scope) > 64 || len(m.Key.Name) > 64 || !m.Key.valid() || m.Key.Scope != scope || len(m.Version) != 32 || !versionID(m.Version) || len(m.Digest) != 64 || m.Digest != strings.ToLower(m.Digest) || len(m.Description) == 0 || len(m.Description) > 1024 || len(m.Tags) > 4096 {
		return ErrInvalid
	}
	if _, err := hex.DecodeString(m.Digest); err != nil {
		return ErrInvalid
	}
	budget := maxFile
	if !contextStrings(&budget, []string{m.Key.Scope, m.Key.Name, m.Version, m.Digest, m.Description}) || !contextStrings(&budget, m.Tags) {
		return ErrInvalid
	}
	matched := false
	for _, tag := range m.Tags {
		if !identifier.MatchString(tag) {
			return ErrInvalid
		}
		matched = matched || tag == domain
	}
	if !matched {
		return ErrInvalid
	}
	return nil
}

// ValidateContextVersion validates the loaded immutable draft against its
// discovery metadata without granting it permissions or proving activation.
// Bound raw bytes and list counts before serialization and digest computation.
func ValidateContextVersion(m Metadata, v Version, scope, domain string) error {
	if ValidateContextMetadata(m, scope, domain) != nil || len(v.ID) != 32 || !versionID(v.ID) || m.Version != v.ID || v.Draft.Key != m.Key || len(v.Parent) > 32 || (v.Parent != "" && !versionID(v.Parent)) || v.CreatedAt.IsZero() {
		return ErrInvalid
	}
	year := v.CreatedAt.UTC().Year()
	if year < 1970 || year > 2260 {
		return ErrInvalid
	}
	budget := maxFile
	if !contextStrings(&budget, []string{m.Key.Scope, m.Key.Name, m.Version, m.Digest, m.Description}) || !contextStrings(&budget, m.Tags) || !contextStrings(&budget, []string{v.ID, v.Parent, v.Draft.Key.Scope, v.Draft.Key.Name, v.Draft.Description, v.Draft.Configuration}) {
		return ErrInvalid
	}
	for _, list := range [][]string{v.Draft.Tags, v.Draft.SourceSessions, v.Draft.SourceEvidence, v.Draft.Steps, v.Draft.RequiredTools, v.Draft.Risks, v.Draft.ValidationCases} {
		if !contextStrings(&budget, list) {
			return ErrInvalid
		}
	}
	if !v.Draft.valid() || v.Draft.Description != m.Description || !slices.Equal(v.Draft.Tags, m.Tags) {
		return ErrInvalid
	}
	body, err := json.Marshal(v)
	if err != nil || len(body) > maxFile {
		return ErrInvalid
	}
	if digest(v) != m.Digest {
		return ErrInvalid
	}
	return nil
}

func contextStrings(budget *int, values []string) bool {
	if len(values) > 4096 {
		return false
	}
	for _, value := range values {
		if len(value) > *budget || !utf8.ValidString(value) {
			return false
		}
		*budget -= len(value)
	}
	return true
}
