package skills

import (
	"encoding/hex"
	"errors"
	"strings"
)

var errCatalogUnchanged = errors.New("skills: catalog unchanged")

// PublicationRecord links one generation attempt to one immutable inactive
// version. It does not attest to validation, activation or workflow quality.
type PublicationRecord struct {
	Key           Key    `json:"key"`
	Version       string `json:"version"`
	AttemptDigest string `json:"attempt_digest"`
}

func validatePublications(c *catalog) error {
	if len(c.Publications) > 10000 || (c.Schema == 1 && len(c.Publications) > 0) {
		return ErrInvalid
	}
	versions := map[string]bool{}
	for id, p := range c.Publications {
		if !identifier.MatchString(id) || !p.Key.valid() || !versionID(p.Version) || len(p.AttemptDigest) != 64 || strings.ToLower(p.AttemptDigest) != p.AttemptDigest {
			return ErrInvalid
		}
		if _, err := hex.DecodeString(p.AttemptDigest); err != nil {
			return ErrInvalid
		}
		if versions[p.Version] {
			return ErrInvalid
		}
		versions[p.Version] = true
		e, ok := c.Skills[p.Key.index()]
		if !ok {
			return ErrInvalid
		}
		found := false
		for _, m := range e.Versions {
			if m.Version == p.Version {
				found = true
				break
			}
		}
		if !found {
			return ErrInvalid
		}
	}
	return nil
}
