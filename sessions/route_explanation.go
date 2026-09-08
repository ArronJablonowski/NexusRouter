package sessions

import (
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/accounting"
	"github.com/ArronJablonowski/DarwinRouter/routing"
)

var ErrRouteExplanation = errors.New("route explanation unavailable or invalid")

// RouteExplanation is metadata-only. It contains no prompt, model output,
// provider endpoint, credential, tool argument or result field.
type RouteExplanation struct {
	Version    int                 `json:"version"`
	TaskID     string              `json:"task_id"`
	SessionID  string              `json:"session_id"`
	RouteID    string              `json:"route_id"`
	Sequence   int64               `json:"sequence"`
	RecordedAt time.Time           `json:"recorded_at"`
	ConfigID   string              `json:"config_id"`
	Domain     string              `json:"domain"`
	Profile    string              `json:"profile"`
	Model      string              `json:"model"`
	Provider   string              `json:"provider"`
	Candidates []routing.Candidate `json:"candidates"`
	Policy     routing.Policy      `json:"policy"`
	Selection  routing.Selection   `json:"selection"`
	// Usage is observed when the explanation is inspected, after the immutable
	// route decision. Nil preserves source compatibility for callers that build
	// or decode the original route-only contract.
	Usage *accounting.Totals `json:"usage,omitempty"`
}

func (r RouteExplanation) Validate() error {
	if r.Version != 1 || !ValidEventPageID(r.TaskID) || !ValidEventPageID(r.SessionID) || !ValidEventPageID(r.RouteID) || r.Sequence != 2 || r.RecordedAt.IsZero() || len(r.ConfigID) != 64 || !validRouteText(r.Domain, 128) || !validRouteText(r.Profile, 128) || !validRouteText(r.Model, 512) || !validRouteText(r.Provider, 128) || r.Selection.Primary.Model != r.Model || r.Selection.Primary.Provider != r.Provider || routing.ValidateExplanation(r.Candidates, &r.Policy, &r.Selection) != nil || !validRouteObservationTimes(r.Selection.Ranked, r.RecordedAt) {
		return ErrRouteExplanation
	}
	if r.Usage != nil && (r.Usage.Validate() != nil || r.Usage.Scope.TaskID != r.TaskID || r.Usage.Scope.SessionID != r.SessionID || r.Usage.CalculatedAt.Before(r.RecordedAt)) {
		return ErrRouteExplanation
	}
	digest, err := hex.DecodeString(r.ConfigID)
	if err != nil || len(digest) != 32 || strings.ToLower(r.ConfigID) != r.ConfigID {
		return ErrRouteExplanation
	}
	return nil
}

func validRouteObservationTimes(ranked []routing.Ranked, recordedAt time.Time) bool {
	for _, item := range ranked {
		for _, window := range []struct {
			applied bool
			end     time.Time
		}{{item.DecayApplied, item.WindowEnd}, {item.AdvisoryDecayApplied, item.AdvisoryWindowEnd}, {item.ValidityDecayApplied, item.ValidityWindowEnd}} {
			if window.applied && window.end.After(recordedAt) {
				return false
			}
		}
	}
	return true
}

func validRouteText(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsFunc(value, unicode.IsControl)
}
