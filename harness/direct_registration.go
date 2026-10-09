package harness

import (
	"crypto/sha256"
	"fmt"
)

// DirectRegistration names the built-in text-only provider route for one model.
// It adds no external harness/tool authority beyond existing model dispatch scope.
func DirectRegistration(modelID string) string {
	digest := sha256.Sum256([]byte(modelID))
	return fmt.Sprintf("nexus-direct-%x", digest[:16])
}
