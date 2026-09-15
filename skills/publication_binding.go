package skills

import "context"

// PublicationBinding is an immutable, value-only receipt that binds one
// generated skill version to the exact durable generation attempt that
// produced it. It attests to publication provenance only, not validation,
// activation, source authenticity, or workflow quality.
type PublicationBinding struct {
	Version       int    `json:"version"`
	Key           Key    `json:"key"`
	SkillVersion  string `json:"skill_version"`
	AttemptID     string `json:"attempt_id"`
	AttemptDigest string `json:"attempt_digest"`
}

// PublicationStore is the optional read-only provenance extension implemented
// by stores which retain generation-publication receipts. Store implementations
// without publication history remain source-compatible with the core Store
// interface.
type PublicationStore interface {
	Store
	PublicationBinding(context.Context, Key, string) (PublicationBinding, error)
}

var _ PublicationStore = (*FileStore)(nil)

// Validate checks the receipt's structural binding. It cannot establish that
// the referenced attempt exists in a host telemetry store.
func (b PublicationBinding) Validate() error {
	if b.Version != 1 || !b.Key.valid() || !versionID(b.SkillVersion) || !identifier.MatchString(b.AttemptID) || !validHexDigest(b.AttemptDigest) {
		return ErrInvalid
	}
	return nil
}

// PublicationBinding returns the unique publication receipt for an exact
// skill key and immutable version. The catalog and version body are validated
// from one read-only snapshot; no file is created or rewritten.
func (s *FileStore) PublicationBinding(ctx context.Context, key Key, skillVersion string) (PublicationBinding, error) {
	var result PublicationBinding
	if ctx == nil || s == nil || !s.permitted(key) || !versionID(skillVersion) {
		return result, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	err := s.with(ctx, func(c *catalog) error {
		var attemptID string
		var receipt PublicationRecord
		matches := 0
		for id, candidate := range c.Publications {
			if candidate.Key == key && candidate.Version == skillVersion {
				attemptID, receipt = id, candidate
				matches++
			}
		}
		if matches == 0 {
			return ErrNotFound
		}
		if matches != 1 {
			return ErrInvalid
		}

		entry, ok := c.Skills[key.index()]
		if !ok || entry.Key != key {
			return ErrInvalid
		}
		var metadata *Metadata
		for i := range entry.Versions {
			if entry.Versions[i].Version == skillVersion {
				if metadata != nil {
					return ErrInvalid
				}
				metadata = &entry.Versions[i]
			}
		}
		if metadata == nil || metadata.Key != key {
			return ErrInvalid
		}
		var stored Version
		if err := s.read("version-"+skillVersion+".json", &stored); err != nil {
			return err
		}
		if stored.Validate() != nil || stored.ID != skillVersion || stored.Draft.Key != key || metadata.Digest != digest(stored) || metadata.Privacy != stored.Draft.Privacy || metadata.Description != stored.Draft.Description || !sameStrings(metadata.Tags, stored.Draft.Tags) {
			return ErrInvalid
		}

		result = PublicationBinding{Version: 1, Key: key, SkillVersion: skillVersion, AttemptID: attemptID, AttemptDigest: receipt.AttemptDigest}
		return result.Validate()
	}, false)
	if err != nil {
		return PublicationBinding{}, err
	}
	return result, nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
