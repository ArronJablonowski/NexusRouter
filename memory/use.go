package memory

import (
	"context"
	"time"
)

// UseStore optionally records attempted context use of a selected fact snapshot.
// Implementations must reject missing, expired or changed facts (all fields
// except LastUse) and never decrease LastUse. This is not a privacy grant:
// callers must retrieve facts through scope/privacy policy before recording use.
type UseStore interface {
	TouchMemoryFact(context.Context, Fact, time.Time) error
}
