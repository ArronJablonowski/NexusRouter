package tools

import (
	"sort"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

type Behavior = runtime.ToolBehavior

const (
	BehaviorReadOnly           = runtime.BehaviorReadOnly
	BehaviorIdempotentWrite    = runtime.BehaviorIdempotentWrite
	BehaviorNonIdempotentWrite = runtime.BehaviorNonIdempotentWrite
)

// Description is owned inspection metadata, not a provider tool definition or
// permission grant. It contains neither a handler nor executable schema bytes.
type Description struct {
	Name     string               `json:"name"`
	Scope    string               `json:"scope"`
	Behavior runtime.ToolBehavior `json:"behavior"`
}

func normalizeBehavior(behavior runtime.ToolBehavior, readOnly bool) (runtime.ToolBehavior, error) {
	if behavior == "" {
		behavior = runtime.BehaviorNonIdempotentWrite
		if readOnly {
			behavior = runtime.BehaviorReadOnly
		}
	}
	if !behavior.Valid() || readOnly != (behavior == runtime.BehaviorReadOnly) {
		return "", ErrDefinition
	}
	return behavior, nil
}

// ToolBehavior reports registered classification, never observed effect or
// retry authority. Unknown tools deliberately return an invalid empty class.
func (e Executor) ToolBehavior(name string) runtime.ToolBehavior {
	if e.Registry == nil {
		return ""
	}
	e.Registry.mu.RLock()
	defer e.Registry.mu.RUnlock()
	return e.Registry.entries[name].Behavior
}

func (r *Registry) Descriptions() []Description {
	out := []Description{}
	if r == nil {
		return out
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.entries {
		out = append(out, Description{Name: e.Tool.Name, Scope: e.Scope, Behavior: e.Behavior})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (e *Extension) Descriptions() []Description {
	out := []Description{}
	if e == nil {
		return out
	}
	for _, item := range e.entries {
		out = append(out, Description{Name: item.Tool.Name, Scope: item.Scope, Behavior: item.Behavior})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
