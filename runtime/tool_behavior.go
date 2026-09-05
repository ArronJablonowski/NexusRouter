package runtime

// ToolBehavior is a trusted declaration about an operation, not an observed
// effect or authorization to repeat it. Idempotent writes can still fail after
// an uncertain effect and require the same approval and writer ownership.
type ToolBehavior string

const (
	BehaviorReadOnly           ToolBehavior = "read_only"
	BehaviorIdempotentWrite    ToolBehavior = "idempotent_write"
	BehaviorNonIdempotentWrite ToolBehavior = "non_idempotent_write"
)

func (b ToolBehavior) Valid() bool {
	return b == BehaviorReadOnly || b == BehaviorIdempotentWrite || b == BehaviorNonIdempotentWrite
}

// ToolBehaviorProvider optionally supplies immutable per-tool declarations.
// Empty means legacy/unknown, never an inferred read-only or retry capability.
// The runtime snapshots this before durable dispatch, independently of model
// descriptions. Implementations are cooperative trusted host code.
type ToolBehaviorProvider interface {
	ToolBehavior(name string) ToolBehavior
}

func declaredToolBehavior(executor ToolExecutor, name string) (behavior ToolBehavior, err error) {
	defer func() {
		if recover() != nil {
			behavior, err = "", ErrTool
		}
	}()
	if provider, ok := executor.(ToolBehaviorProvider); ok {
		behavior = provider.ToolBehavior(name)
		if behavior != "" && !behavior.Valid() {
			return "", ErrTool
		}
	}
	return behavior, nil
}
