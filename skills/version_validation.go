package skills

import (
	"encoding/json"
	"strings"
)

// Validate checks the bounded immutable-version serialization contract. It does
// not prove workflow correctness, authorize tools, or establish activation.
// Manual drafts need not carry generation evidence or a routing-domain tag.
func (v Version) Validate() error {
	if len(v.ID) != 32 || v.ID != strings.ToLower(v.ID) || !versionID(v.ID) || (v.Parent != "" && (len(v.Parent) != 32 || v.Parent != strings.ToLower(v.Parent) || !versionID(v.Parent))) || !generationTime(v.CreatedAt) {
		return ErrInvalid
	}
	// Check list counts, raw string bytes, UTF-8 and draft structure before
	// serializing the full record. This also enforces the draft's 256 KiB limit.
	if validateGeneratedDraft(v.Draft) != nil {
		return ErrInvalid
	}
	body, err := json.Marshal(v)
	if err != nil || len(body) > maxFile {
		return ErrInvalid
	}
	return nil
}
