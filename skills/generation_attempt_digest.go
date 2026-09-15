package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// GenerationAttemptDigest returns the canonical digest used to bind a durable
// generation attempt to a published skill version. The caller's value is never
// mutated. Times are normalized to UTC before encoding so equivalent attempts
// have one stable representation.
func GenerationAttemptDigest(attempt GenerationAttempt) (string, error) {
	if attempt.Validate() != nil {
		return "", ErrInvalid
	}
	attempt.StartedAt = attempt.StartedAt.UTC()
	attempt.FinishedAt = attempt.FinishedAt.UTC()
	body, err := json.Marshal(attempt)
	if err != nil || len(body) > maxFile {
		return "", ErrInvalid
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}
