package codexbridge

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/ArronJablonowski/NexusRouter/internal/codexrpc"
)

// NewCheckedSession additionally verifies launch controls on this same wire
// before thread creation. features must come from the host's trusted, pinned
// CLI profile, not model input. This is not a launcher, privacy admission or
// complete built-in-tool/sandbox attestation. The host still owns those gates.
// Like NewSession, construction is inert and never takes credentials.
func NewCheckedSession(ctx context.Context, w Wire, options Options, features []string) (*Session, error) {
	if len(features) == 0 || len(features) > 256 {
		return nil, failure(false)
	}
	seen := map[string]bool{}
	for _, name := range features {
		if !namePattern.MatchString(name) || seen[name] {
			return nil, failure(false)
		}
		seen[name] = true
	}
	s, err := NewSession(ctx, w, options)
	if err != nil {
		return nil, err
	}
	s.launchFeatures = append([]string(nil), features...)
	s.allowDisabledStatus = true
	return s, nil
}

// Prepare initializes and, for NewCheckedSession, verifies configuration and
// inventories without sending a thread, turn, prompt or tool definition.
// Stream calls the same path automatically; initialization is never repeated.
func (s *Session) Prepare(ctx context.Context) (err error) {
	if !s.mu.TryLock() {
		return failure(false)
	}
	defer s.mu.Unlock()
	defer func() {
		if recover() != nil {
			err = failure(false)
		}
		if err != nil {
			_ = s.Close()
		}
	}()
	if ctx == nil || ctx.Err() != nil || s.closed.Load() || s.started {
		return failure(false)
	}
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stop()
	return s.prepare()
}

func (s *Session) prepare() error {
	if s.prepared {
		if s.launchFeatures != nil {
			// An explicit earlier Prepare is not a permanent admission token.
			return s.checkLaunch()
		}
		return nil
	}
	initialized, err := s.call("1", "initialize", map[string]any{"clientInfo": map[string]string{"name": "darwin_router", "version": "0.1.0"}, "capabilities": map[string]bool{"experimentalApi": true}})
	if err != nil {
		return err
	}
	var initialization struct {
		UserAgent string `json:"userAgent"`
	}
	if decodePayload(initialized, &initialization) != nil || initialization.UserAgent == "" {
		return failure(false)
	}
	if s.w.Write(codexrpc.Envelope{Method: "initialized"}) != nil {
		return failure(false)
	}
	if s.launchFeatures != nil {
		if err := s.checkLaunch(); err != nil {
			return err
		}
	}
	s.prepared = true
	return nil
}

func (s *Session) checkLaunch() error {
	config, err := s.launchCall("config/read", map[string]any{"includeLayers": false})
	if err != nil {
		return err
	}
	r, err := InspectLaunchConfig(config, s.launchFeatures)
	if err != nil || r.ExpectedFeatures != r.ObservedFeatures || r.ExpectedFeatures != r.MatchingFeatures || r.AdditionalFeatures != 0 ||
		!r.MCPEntriesKnown || !r.PluginEntriesKnown || r.MCPEntries != r.MCPEntriesDisabled || r.PluginEntries != r.PluginEntriesDisabled ||
		!r.ProjectContextDisabled || !r.NotifyDisabled || !r.WebSearchDisabled {
		return failure(false)
	}
	mcp, err := s.launchCall("mcpServerStatus/list", map[string]any{"limit": 100, "detail": "toolsAndAuthOnly"})
	if err != nil {
		return err
	}
	skills, err := s.launchCall("skills/list", map[string]any{"cwds": []string{s.options.CWD}, "forceReload": true})
	if err != nil {
		return err
	}
	hooks, err := s.launchCall("hooks/list", map[string]any{"cwds": []string{s.options.CWD}})
	if err != nil {
		return err
	}
	if CheckLaunchInventories(s.options.CWD, mcp, skills, hooks) != nil {
		return failure(false)
	}
	return nil
}

func (s *Session) launchCall(method string, params any) (json.RawMessage, error) {
	id := strconv.Itoa(10 + s.launchRequestID)
	s.launchRequestID++
	return s.call(id, method, params)
}

func disabledRemoteControl(raw json.RawMessage) bool {
	var status struct {
		Status string `json:"status"`
	}
	return decodePayload(raw, &status) == nil && status.Status == "disabled"
}
