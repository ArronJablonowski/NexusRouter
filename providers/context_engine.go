package providers

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"
	"unicode/utf8"
)

// ContextEstimator supplements conservative admission estimates. Implementations
// are trusted host code, not sandboxed tokenizers; they must honor cancellation,
// support concurrent calls, and never mutate caller-owned state concurrently.
// Measurement must be local: do not transmit task context to external services.
type ContextEstimator interface {
	Estimate(context.Context, Request) (int, error)
}

var ErrContextEstimate = errors.New("context estimation failed")

const maxEstimateRequestBytes = 4 << 20

// EstimateWith never reduces the built-in serialized-byte estimate and framing
// reserve. Nil retains EstimateContext behavior. Custom estimators receive an
// isolated request, limited to 4 MiB serialized JSON, and a cooperative maximum
// three-second deadline. No goroutine is abandoned for a noncooperative callback;
// the host must ensure its estimator returns when its context is canceled.
func EstimateWith(ctx context.Context, estimator ContextEstimator, request Request) (estimate int, err error) {
	if ctx == nil || ctx.Err() != nil {
		return 0, ErrContextEstimate
	}
	if estimator == nil {
		estimate, err = EstimateContext(request)
		if err != nil || ctx.Err() != nil {
			return 0, ErrContextEstimate
		}
		return estimate, nil
	}
	v := reflect.ValueOf(estimator)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if v.IsNil() {
			return 0, ErrContextEstimate
		}
	}
	if !validEstimateRequest(request) {
		return 0, ErrContextEstimate
	}
	body, encodeErr := json.Marshal(request)
	if encodeErr != nil || len(body) > maxEstimateRequestBytes {
		return 0, ErrContextEstimate
	}
	var snapshot Request
	if json.Unmarshal(body, &snapshot) != nil {
		return 0, ErrContextEstimate
	}
	baseline, baseErr := EstimateContext(snapshot)
	if baseErr != nil {
		return 0, ErrContextEstimate
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if bounded.Err() != nil {
		return 0, ErrContextEstimate
	}
	defer func() {
		if recover() != nil {
			estimate, err = 0, ErrContextEstimate
		}
	}()
	custom, callbackErr := estimator.Estimate(bounded, snapshot)
	if callbackErr != nil || bounded.Err() != nil || custom < 0 {
		return 0, ErrContextEstimate
	}
	return max(baseline, custom), nil
}

// Preflight avoids unbounded serialization and rejects invalid UTF-8 rather
// than letting encoding/json silently replace it. JSON expansion is checked
// separately after marshaling. Element-count limits bound empty-value overhead.
func validEstimateRequest(r Request) bool {
	used := 0
	reserve := func(size int) bool {
		if size > maxEstimateRequestBytes-used {
			return false
		}
		used += size
		return true
	}
	add := func(s string) bool {
		return reserve(len(s)) && utf8.ValidString(s)
	}
	if !add(r.Model) || !add(string(r.JSONSchema)) || len(r.Messages) > maxEstimateRequestBytes/32 || len(r.Tools) > maxEstimateRequestBytes/32 {
		return false
	}
	for _, message := range r.Messages {
		if !reserve(32) || !add(message.Role) || !add(message.Content) || !add(message.ToolCallID) || len(message.ToolCalls) > maxEstimateRequestBytes/32 {
			return false
		}
		for _, call := range message.ToolCalls {
			if !reserve(32) || !add(call.ID) || !add(call.Name) || !add(string(call.Arguments)) {
				return false
			}
		}
	}
	for _, tool := range r.Tools {
		if !reserve(32) || !add(tool.Name) || !add(tool.Description) || !add(string(tool.Parameters)) {
			return false
		}
	}
	return true
}
