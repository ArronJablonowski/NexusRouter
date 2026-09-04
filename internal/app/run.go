// Package app composes admission, providers and the durable runtime.
package app

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"darwinrouter/internal/config"
	"darwinrouter/internal/telemetry"
	"darwinrouter/policy"
	"darwinrouter/providers"
	"darwinrouter/runtime"
	"darwinrouter/sessions"
)

var ErrAdmission = errors.New("task admission failed")

type Request struct {
	ModelID, Prompt, ContinueTaskID string
	Messages                        []providers.Message
	Domain, Profile                 string
	Capabilities                    []string
	ContextTokens                   int
	MaxCost                         float64
	LocalRequired                   bool
	route                           *runtime.Data
}
type Result struct {
	TaskID, Text string
	Turns        int
	FinishReason string
	Usage        *providers.Usage
}

// RunExplicit is the initial headless application path. It executes one model
// turn, with no tools or implicit fallback. A local model always receives a
// loopback-only transport, even when the application mode permits cloud use.
func RunExplicit(ctx context.Context, s config.Settings, r Request, secret func(string) string) (Result, error) {
	result := Result{}
	if s.Validate() != nil || r.ModelID == "" || validateInput(r) != nil || s.Telemetry.OTEL {
		return result, ErrAdmission
	}
	var model config.Model
	found := false
	for _, m := range s.Models {
		if m.ID == r.ModelID {
			model = m
			found = true
			break
		}
	}
	if !found || (r.LocalRequired && model.Locality != "local") || (s.Mode == "local_only" && model.Locality != "local") || (s.Mode == "cloud_only" && model.Locality != "cloud") {
		return result, ErrAdmission
	}
	// Explicit selection bypasses ranking, not requested admission constraints.
	// Zero cost retains the legacy explicit-model operator override; automatic
	// routing instead interprets zero as a strict zero-cost ceiling.
	if r.ContextTokens > 0 && model.ContextTokens < r.ContextTokens {
		return result, ErrAdmission
	}
	if r.MaxCost > 0 && (model.EstimatedCost == nil || *model.EstimatedCost > r.MaxCost) {
		return result, ErrAdmission
	}
	for _, required := range r.Capabilities {
		matched := false
		for _, capability := range model.Capabilities {
			matched = matched || capability == required
		}
		if !matched {
			return result, ErrAdmission
		}
	}
	var provider config.Provider
	for _, p := range s.Providers {
		if p.ID == model.Provider {
			provider = p
			break
		}
	}
	tr, err := policy.NewTransport(s.Mode == "local_only" || model.Locality == "local", []string{provider.Endpoint})
	if err != nil {
		return result, ErrAdmission
	}
	defer tr.CloseIdleConnections()
	key := ""
	secrets := []string{}
	if secret != nil {
		if token := secret("DARWIN_API_TOKEN"); token != "" {
			secrets = append(secrets, token)
		}
	}
	for _, p := range s.Providers {
		if p.APIKeyEnv != "" && secret != nil {
			value := secret(p.APIKeyEnv)
			if value != "" {
				secrets = append(secrets, value)
			}
			if p.ID == provider.ID {
				key = value
			}
		}
	}
	if provider.APIKeyEnv != "" && key == "" {
		return result, ErrAdmission
	}
	p, err := providers.NewHTTP(provider.Endpoint, provider.Kind, key, tr)
	if err != nil {
		return result, ErrAdmission
	}
	db, err := telemetry.Open(ctx, s.Telemetry.Database)
	if err != nil {
		return result, errors.New("cannot open task storage")
	}
	defer db.Close()
	messages := []providers.Message{}
	sessionID := ""
	privacy := "cloud_allowed"
	if model.Locality == "local" {
		privacy = "local_only"
	}
	if r.ContinueTaskID != "" {
		history, err := sessions.Replay(ctx, db, r.ContinueTaskID)
		if err != nil || history.State != "completed" || history.InterruptedTurn || history.UncertainEffects || len(history.Pending) > 0 {
			return result, ErrAdmission
		}
		// Unknown legacy privacy is never interpreted as cloud consent.
		if history.Privacy != "cloud_allowed" && model.Locality != "local" {
			return result, ErrAdmission
		}
		if history.Privacy != "cloud_allowed" {
			privacy = "local_only"
		}
		messages = history.Messages
		sessionID = history.SessionID
	}
	if len(r.Messages) > 0 {
		messages = append(messages, r.Messages...)
	} else {
		messages = append(messages, providers.Message{Role: "user", Content: r.Prompt})
	}
	encoded, err := json.Marshal(messages)
	if err != nil || len(encoded) > 4<<20 {
		return result, ErrAdmission
	}
	result.TaskID = rand.Text()
	if sessionID == "" {
		sessionID = result.TaskID
	}
	j := redactingJournal{db: db, secrets: secrets}
	loop := runtime.Loop{Provider: p, Journal: j}
	out, err := loop.Run(ctx, runtime.RunRequest{Route: r.route, TaskID: result.TaskID, SessionID: sessionID, ProviderID: provider.ID, ParentTaskID: r.ContinueTaskID, Privacy: privacy, Inference: providers.Request{Model: model.Model, Messages: messages}, MaxTurns: 1, MaxOutputBytes: 1 << 20})
	result.Text = redact(out.Text, secrets)
	result.Turns = out.Turns
	result.FinishReason = out.FinishReason
	result.Usage = out.Usage
	return result, err
}

type redactingJournal struct {
	db      *telemetry.Store
	secrets []string
}

func (j redactingJournal) Append(ctx context.Context, expected int64, e runtime.Event) error {
	// Partial deltas can split a credential across records. Persist lifecycle
	// markers without delta text; the complete turn contains redacted text.
	if e.Kind == runtime.ModelDelta {
		e.Data.Text = ""
	}
	data, err := json.Marshal(e.Data)
	if err != nil {
		return err
	}
	var values any
	if json.Unmarshal(data, &values) != nil {
		return errors.New("cannot redact event")
	}
	var scrub func(any) any
	scrub = func(v any) any {
		switch x := v.(type) {
		case string:
			return redact(x, j.secrets)
		case []any:
			for i := range x {
				x[i] = scrub(x[i])
			}
		case map[string]any:
			for key, value := range x {
				x[key] = scrub(value)
			}
		}
		return v
	}
	data, err = json.Marshal(scrub(values))
	if err != nil {
		return err
	}
	if json.Unmarshal(data, &e.Data) != nil {
		return errors.New("cannot redact event")
	}
	return j.db.Append(ctx, expected, e)
}
func redact(value string, secrets []string) string {
	ordered := append([]string(nil), secrets...)
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, secret := range ordered {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}
