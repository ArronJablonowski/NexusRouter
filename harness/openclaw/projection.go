// Package openclaw implements the pinned OpenClaw agent exec boundary.
// A projection alone is not execution evidence: the exec envelope omits the
// provider stop reason. A runner must also verify its policy gateway exchange,
// process exit, artifact identity, isolation and cleanup before recording output.
package openclaw

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/harness/internal/wirejson"
)

const SupportedVersion = "2026.9.7"
const MaxEnvelopeBytes = 8 << 20
const MaxTextBytes = 4 << 20

var ErrProjection = errors.New("invalid OpenClaw exec projection")

// Projection contains untrusted harness-reported output, not a quality verdict
// or canonical completion. Usage is intentionally not exported as measured
// provider accounting; the gateway must establish that separately.
type Projection struct {
	Provider, Model, SessionID, Text string
}

type payload struct {
	Text                               *string
	MediaURL                           *string  `json:"mediaUrl"`
	MediaURLs                          []string `json:"mediaUrls"`
	IsError, IsReasoning, IsCommentary bool
}

// ParseProjection validates the one-turn, text-only exec contract. exitCode must
// be obtained from a reaped process, never from its JSON. Additive envelope fields
// are tolerated, but duplicate keys and known unsupported behavior are rejected.
func ParseProjection(body []byte, exitCode int, provider, model string) (Projection, error) {
	bad := func() (Projection, error) { return Projection{}, ErrProjection }
	if exitCode != 0 || !identifier(provider) || !identifier(model) || len(body) == 0 || len(body) > MaxEnvelopeBytes || !utf8.Valid(body) || !uniqueJSON(body) {
		return bad()
	}
	var e struct {
		OK                                        *bool `json:"ok"`
		Status, Final, Provider, Model, SessionID string
		Payloads                                  []payload
		Error                                     json.RawMessage
		AssistantTurns                            *int
		CodeModeEngaged                           *bool
		BridgeCalls                               map[string]int
		ToolSummary                               *struct {
			Calls           int
			Tools           []string
			Failures        int
			TotalToolTimeMS float64
		}
		Usage   map[string]json.RawMessage
		CostUSD *float64
	}
	if json.Unmarshal(body, &e) != nil || e.OK == nil || !*e.OK || e.Status != "ok" || len(e.Error) != 0 || e.Provider != provider || e.Model != model || !identifier(e.SessionID) || e.AssistantTurns == nil || *e.AssistantTurns != 1 || (e.CodeModeEngaged != nil && *e.CodeModeEngaged) || strings.TrimSpace(e.Final) == "" || len(e.Final) > MaxTextBytes || len(e.Payloads) > 1024 {
		return bad()
	}
	for _, calls := range e.BridgeCalls {
		if calls != 0 {
			return bad()
		}
	}
	if t := e.ToolSummary; t != nil && (t.Calls != 0 || len(t.Tools) != 0 || t.Failures != 0 || t.TotalToolTimeMS != 0) {
		return bad()
	}
	for key, raw := range e.Usage {
		if key == "cost" {
			var costs map[string]float64
			if json.Unmarshal(raw, &costs) != nil {
				return bad()
			}
			for _, cost := range costs {
				if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
					return bad()
				}
			}
			continue
		}
		var count float64
		if json.Unmarshal(raw, &count) != nil {
			return bad()
		}
		if math.IsNaN(count) || math.IsInf(count, 0) || count < 0 || count > 1<<53 || math.Trunc(count) != count {
			return bad()
		}
	}
	if e.CostUSD != nil && (math.IsNaN(*e.CostUSD) || math.IsInf(*e.CostUSD, 0) || *e.CostUSD < 0) {
		return bad()
	}
	var visible []string
	for _, p := range e.Payloads {
		if p.IsError || p.MediaURL != nil || len(p.MediaURLs) != 0 {
			return bad()
		}
		if p.Text == nil || p.IsReasoning || p.IsCommentary || strings.TrimSpace(*p.Text) == "" {
			continue
		}
		visible = append(visible, strings.TrimRightFunc(*p.Text, jsWhitespace))
	}
	// OpenClaw may use finalAssistantVisibleText when no visible payload exists.
	// Otherwise the final text must equal its documented payload projection.
	if len(visible) != 0 && strings.Join(visible, "\n") != e.Final {
		return bad()
	}
	return Projection{Provider: e.Provider, Model: e.Model, SessionID: e.SessionID, Text: e.Final}, nil
}

func identifier(s string) bool {
	if s == "" || len(s) > 256 || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

// Match ECMAScript trimEnd, including BOM but excluding Go's extra NEL space.
func jsWhitespace(r rune) bool {
	return r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r' || r == ' ' || r == '\u00a0' || r == '\u1680' || r >= '\u2000' && r <= '\u200a' || r == '\u2028' || r == '\u2029' || r == '\u202f' || r == '\u205f' || r == '\u3000' || r == '\ufeff'
}

func uniqueJSON(body []byte) bool { return wirejson.Unique(body) }
