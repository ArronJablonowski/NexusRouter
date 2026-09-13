package evaluation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// AuditRecordDigest returns the canonical identity of a validated advisory
// audit. UTC normalization makes equal instants produce equal identities even
// when callers supplied different location offsets.
func AuditRecordDigest(record AuditRecord) (string, error) {
	if record.Validate() != nil {
		return "", ErrAudit
	}
	record.Time = record.Time.UTC()
	body, err := json.Marshal(record)
	if err != nil {
		return "", ErrAudit
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}
