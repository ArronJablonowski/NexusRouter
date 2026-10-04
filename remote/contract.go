// Package remote provides opt-in, mutually authenticated instance task control.
// Discovery is not trust. A remote destination is not a pooled GPU or a sandbox.
package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

const Version = 1
const MaxBody = 1 << 20

var ErrInvalid = errors.New("invalid remote contract")
var ErrDenied = errors.New("remote access denied")
var ErrConflict = errors.New("remote request identity conflict")
var ErrRateLimited = errors.New("remote request rate limited; retry the same request identity later")
var ErrUnavailable = errors.New("remote operation unavailable; delivery may be uncertain")

// Task deliberately excludes continuation, arbitrary messages, paths and tool
// authority. The destination's configured runtime retains all admission rules.
type Task struct {
	ExpectedHarnessIdentity *harness.Identity `json:"expected_harness_identity,omitempty"`
	HarnessID               string            `json:"harness_id,omitempty"`
	HarnessDifficulty       string            `json:"harness_difficulty,omitempty"`
	Version                 int               `json:"version"`
	ModelID                 string            `json:"model_id"`
	Prompt                  string            `json:"prompt"`
	Domain                  string            `json:"domain"`
	Profile                 string            `json:"profile"`
	ContextTokens           int               `json:"context_tokens"`
	MaxCost                 float64           `json:"max_cost"`
	// Private means both explicitly paired private transport and local inference.
	Private bool `json:"private"`
}

func (t Task) Validate() error {
	if t.ExpectedHarnessIdentity != nil && (t.ExpectedHarnessIdentity.Validate() != nil || t.HarnessID == "" || t.ContextTokens < 8192) {
		return ErrInvalid
	}
	if t.HarnessID == "" {
		if t.HarnessDifficulty != "" {
			return ErrInvalid
		}
	} else {
		if !name(t.HarnessID) || t.HarnessID == "auto" {
			return ErrInvalid
		}
		switch t.HarnessDifficulty {
		case "easy", "medium", "hard", "unknown":
		default:
			return ErrInvalid
		}
	}
	if t.Version != Version || !name(t.ModelID) || len(t.Prompt) == 0 || len(t.Prompt) > MaxBody/2 || !utf8.ValidString(t.Prompt) || !name(t.Domain) || !name(t.Profile) || t.ContextTokens < 1 || t.ContextTokens > 1<<24 || math.IsNaN(t.MaxCost) || math.IsInf(t.MaxCost, 0) || t.MaxCost < 0 {
		return ErrInvalid
	}
	return nil
}

// ModelObservation is a fresh advisory inventory lookup, not a capability
// attestation or reservation. Unknown must never be interpreted as available.
type ModelObservation struct {
	State     string    `json:"state"` // present, absent, unknown
	CheckedAt time.Time `json:"checked_at"`
}
type ResourceObservation struct {
	State        string    `json:"state"` // measured, unknown
	CheckedAt    time.Time `json:"checked_at"`
	TotalRAM     *uint64   `json:"total_ram_bytes,omitempty"`
	AvailableRAM *uint64   `json:"available_ram_bytes,omitempty"`
}
type Model struct {
	Observation *ModelObservation `json:"observation,omitempty"`

	EstimatedCost *float64 `json:"estimated_cost,omitempty"`
	ID            string   `json:"id"`
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	Harness       string   `json:"harness"`
	Capabilities  []string `json:"capabilities"`
	ContextTokens int      `json:"context_tokens"`
	Local         bool     `json:"local"`
}
type Info struct {
	Routing   *webui.RoutingInspection `json:"routing,omitempty"`
	Hostname  string                   `json:"hostname,omitempty"`
	Harnesses []Harness                `json:"harnesses,omitempty"`
	Resources *ResourceObservation     `json:"resources,omitempty"`
	Version   int                      `json:"version"`
	Instance  string                   `json:"instance"`
	Models    []Model                  `json:"models"`
	// Availability is advisory; Submit and the destination dispatcher recheck.
	Available bool `json:"available"`
}
type Backend interface {
	Info(context.Context) (Info, error)
	Submit(context.Context, string, Task) (submissions.Status, error)
	Status(context.Context, string) (submissions.Status, error)
	Cancel(context.Context, string) (submissions.Status, error)
	Events(context.Context, string, int64, int) (sessions.EventPage, error)
}

func name(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-:/", r)) {
			return false
		}
	}
	return true
}
func id(s string) bool        { return name(s) && !strings.ContainsAny(s, ".:/") && len(s) <= 64 }
func requestID(s string) bool { return id(s) && len(s) >= 16 }
func hash(v any) string {
	b, _ := json.Marshal(v)
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:])
}

// validHostname accepts bounded host metadata, never control characters or paths.
func validHostname(s string) bool {
	if len(s) > 253 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (i Info) ValidateRouting() error {
	if i.Routing == nil {
		return nil
	}
	models := make([]webui.ModelInspection, 0, len(i.Models))
	for _, m := range i.Models {
		locality := "cloud"
		if m.Local {
			locality = "local"
		}
		models = append(models, webui.ModelInspection{ID: m.ID, Locality: locality})
	}
	if i.Routing.Validate(models) != nil {
		return ErrInvalid
	}
	return nil
}
