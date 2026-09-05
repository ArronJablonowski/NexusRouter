package v1

import "github.com/ArronJablonowski/DarwinRouter/runtime"

// ToolBehavior declares the handler's operation class, independently of the
// observed effect of one execution. Idempotence is not approval or retry authority.
type ToolBehavior = runtime.ToolBehavior

const (
	BehaviorReadOnly           = runtime.BehaviorReadOnly
	BehaviorIdempotentWrite    = runtime.BehaviorIdempotentWrite
	BehaviorNonIdempotentWrite = runtime.BehaviorNonIdempotentWrite
)
