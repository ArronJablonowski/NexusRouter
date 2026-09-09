package skills

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// PublishGeneration copies a completed proposal into an inactive immutable
// skill version. It does not validate the workflow for activation or alter the
// active version. A receipt and version visibility commit in one catalog write;
// retrying the identical attempt returns the same version, even if the active
// skill changed meanwhile. Reusing its ID for a different proposal conflicts.
// The first publication upgrades older catalogs to schema 2 so older writers
// cannot silently discard receipts; a newer catalog is never downgraded.
// The host must establish the supplied attempt's authenticity and authorization.
func (s *FileStore) PublishGeneration(ctx context.Context, a GenerationAttempt, automatic bool) (Version, error) {
	if ctx == nil || s == nil || !s.permitted(a.Key) || a.Validate() != nil || a.Status != "drafted" {
		return Version{}, ErrInvalid
	}
	if ctx.Err() != nil {
		return Version{}, ctx.Err()
	}
	if s.readOnly || (automatic && !s.automatic.Load()) {
		return Version{}, ErrDisabled
	}
	a.StartedAt, a.FinishedAt = a.StartedAt.UTC(), a.FinishedAt.UTC()
	body, err := json.Marshal(a)
	if err != nil || len(body) > maxFile {
		return Version{}, ErrInvalid
	}
	hash := sha256.Sum256(body)
	attemptDigest := hex.EncodeToString(hash[:])
	// Own all slices before entering the critical section. Caller mutation is not
	// permitted during this snapshot, but cannot affect the subsequent commit.
	var owned GenerationAttempt
	if json.Unmarshal(body, &owned) != nil {
		return Version{}, ErrInvalid
	}
	draft := owned.Result.Draft
	draftBody, err := json.Marshal(draft)
	if err != nil {
		return Version{}, ErrInvalid
	}
	var version Version
	err = s.with(ctx, func(c *catalog) error {
		if automatic && !s.automatic.Load() {
			return ErrDisabled
		}
		if receipt, exists := c.Publications[owned.ID]; exists {
			if receipt.Key != owned.Key || receipt.AttemptDigest != attemptDigest {
				return ErrConflict
			}
			entry, exists := c.Skills[owned.Key.index()]
			if !exists {
				return ErrInvalid
			}
			var metadata *Metadata
			for i := range entry.Versions {
				if entry.Versions[i].Version == receipt.Version {
					metadata = &entry.Versions[i]
					break
				}
			}
			if metadata == nil {
				return ErrInvalid
			}
			if err := s.read("version-"+receipt.Version+".json", &version); err != nil {
				return err
			}
			storedDraft, err := json.Marshal(version.Draft)
			if err != nil || version.ID != receipt.Version || version.Draft.Key != owned.Key || !version.Draft.valid() || metadata.Key != owned.Key || metadata.Digest != digest(version) || metadata.Privacy != version.Draft.Privacy || metadata.Description != version.Draft.Description || !bytes.Equal(storedDraft, draftBody) || len(metadata.Tags) != len(version.Draft.Tags) {
				return ErrInvalid
			}
			for i, tag := range metadata.Tags {
				if tag != version.Draft.Tags[i] {
					return ErrInvalid
				}
			}
			// The catalog helper acknowledges exact retries without rewriting it.
			return errCatalogUnchanged
		}
		entry, exists := c.Skills[owned.Key.index()]
		if len(c.Publications) >= 10000 || len(entry.Versions) >= 1000 || (!exists && len(c.Skills) >= 1000) {
			return ErrInvalid
		}
		entry.Key = owned.Key
		version = Version{ID: randomID(), Parent: entry.Active, CreatedAt: time.Now().UTC(), Draft: draft}
		if err := s.write("version-"+version.ID+".json", version, true); err != nil {
			return err
		}
		entry.Versions = append(entry.Versions, Metadata{Key: owned.Key, Version: version.ID, Digest: digest(version), Privacy: draft.Privacy, Description: draft.Description, Tags: draft.Tags})
		c.Skills[owned.Key.index()] = entry
		if c.Publications == nil {
			c.Publications = map[string]PublicationRecord{}
		}
		c.Publications[owned.ID] = PublicationRecord{Key: owned.Key, Version: version.ID, AttemptDigest: attemptDigest}
		if c.Schema < 2 {
			c.Schema = 2
		}
		return nil
	}, true)
	if err != nil {
		return Version{}, err
	}
	return version, nil
}
