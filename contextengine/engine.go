// Package contextengine admits bounded host-supplied context planning engines.
// Engines are trusted local extensions, not sandboxes. Callbacks must cooperate
// with cancellation and must not mutate their arguments concurrently after return.
package contextengine

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

type Tier string

const (
	HistoryTier Tier = "history"
	MemoryTier  Tier = "memory"
	SkillsTier  Tier = "skills"
	CurrentTier Tier = "current"
)

type Assembly struct {
	Version                          int
	History, Memory, Skills, Current []providers.Message
}
type Plan struct {
	Version int
	Order   []Tier
}
type Prepared struct {
	Messages                       []providers.Message
	MemoryIncluded, SkillsIncluded bool
}
type Engine interface {
	providers.ContextEstimator
	Assemble(context.Context, Assembly) (Plan, error)
	PrepareCompaction(context.Context, sessions.Snapshot, sessions.CompactionRequest) (sessions.CompactionRequest, error)
}

var ErrEngine = errors.New("context engine admission failed")

type Default struct{}

func (Default) Estimate(ctx context.Context, r providers.Request) (int, error) {
	return providers.EstimateWith(ctx, nil, r)
}
func (Default) Assemble(ctx context.Context, _ Assembly) (Plan, error) {
	if ctx == nil || ctx.Err() != nil {
		return Plan{}, ErrEngine
	}
	return Plan{Version: 1, Order: []Tier{HistoryTier, MemoryTier, SkillsTier, CurrentTier}}, nil
}
func (Default) PrepareCompaction(ctx context.Context, _ sessions.Snapshot, r sessions.CompactionRequest) (sessions.CompactionRequest, error) {
	if ctx == nil || ctx.Err() != nil {
		return sessions.CompactionRequest{}, ErrEngine
	}
	var owned sessions.CompactionRequest
	if sessions.ValidateCompactionRequest(&r) != nil || cloneBounded(r, &owned) != nil {
		return sessions.CompactionRequest{}, ErrEngine
	}
	return owned, nil
}

func chosenEngine(engine Engine) (Engine, error) {
	if engine == nil {
		return Default{}, nil
	}
	v := reflect.ValueOf(engine)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Interface, reflect.Chan:
		if v.IsNil() {
			return nil, ErrEngine
		}
	}
	return engine, nil
}

// Assemble permits only bundle selection/order. Engines cannot author messages,
// split tool pairs, change roles, or rewrite host instructions through a plan.
func Assemble(ctx context.Context, engine Engine, input Assembly) (out Prepared, err error) {
	defer func() {
		if recover() != nil {
			out = Prepared{}
			err = ErrEngine
		}
	}()
	if ctx == nil || ctx.Err() != nil {
		return Prepared{}, ErrEngine
	}
	engine, err = chosenEngine(engine)
	if err != nil {
		return Prepared{}, ErrEngine
	}
	if input.Version != 1 || len(input.Current) == 0 || !validBundles(input.History, input.Memory, input.Skills, input.Current) {
		return Prepared{}, ErrEngine
	}
	var pristine, callback Assembly
	if cloneBounded(input, &pristine) != nil || cloneBounded(pristine, &callback) != nil {
		return Prepared{}, ErrEngine
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	plan, err := engine.Assemble(bounded, callback)
	if err != nil || bounded.Err() != nil || plan.Version != 1 || len(plan.Order) < 2 || len(plan.Order) > 4 {
		return Prepared{}, ErrEngine
	}
	order := append([]Tier(nil), plan.Order...)
	seen := map[Tier]bool{}
	for i, tier := range order {
		if seen[tier] {
			return Prepared{}, ErrEngine
		}
		seen[tier] = true
		switch tier {
		case HistoryTier:
			out.Messages = append(out.Messages, pristine.History...)
		case MemoryTier:
			out.Messages = append(out.Messages, pristine.Memory...)
			out.MemoryIncluded = len(pristine.Memory) > 0
		case SkillsTier:
			out.Messages = append(out.Messages, pristine.Skills...)
			out.SkillsIncluded = len(pristine.Skills) > 0
		case CurrentTier:
			if i != len(order)-1 {
				return Prepared{}, ErrEngine
			}
			out.Messages = append(out.Messages, pristine.Current...)
		default:
			return Prepared{}, ErrEngine
		}
	}
	if !seen[HistoryTier] || !seen[CurrentTier] || providers.ValidateMessages(out.Messages) != nil {
		return Prepared{}, ErrEngine
	}
	var owned Prepared
	if cloneBounded(out, &owned) != nil || bounded.Err() != nil {
		return Prepared{}, ErrEngine
	}
	return owned, nil
}

const maxBytes = 4 << 20

func cloneBounded(value, target any) error {
	body, err := json.Marshal(value)
	if err != nil || len(body) > maxBytes {
		return ErrEngine
	}
	if json.Unmarshal(body, target) != nil {
		return ErrEngine
	}
	return nil
}
