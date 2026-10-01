// Package harness defines versioned external execution identities and read-only
// model–harness ranking. A selection is not execution authority or a reservation.
package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const Version = 1
const MaxRecords = 100000

var ErrInvalid = errors.New("invalid harness routing contract")
var ErrConflict = errors.New("harness evidence identity or head conflict")
var ErrNoRoute = errors.New("no eligible model-harness combination")

// Identity binds evidence to a concrete implementation and configuration, not
// a display name. ModelRevision must change when weights/provider deployment
// change. ConfigSHA256 covers relevant harness/tool/model settings, not secrets.
// AdapterVersion pins the bridge implementation and protocol contract together.
type Identity struct {
	Version                                 int
	Harness, HarnessVersion, AdapterVersion string
	Provider, Model, ModelRevision          string
	ConfigSHA256                            string
}

func (i Identity) Validate() error {
	if i.Version != Version || !digest(i.ConfigSHA256) {
		return ErrInvalid
	}
	for _, s := range []string{i.Harness, i.HarnessVersion, i.AdapterVersion, i.Provider, i.Model, i.ModelRevision} {
		if !label(s) {
			return ErrInvalid
		}
	}
	return nil
}
func (i Identity) Key() (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	return hash(i), nil
}

// TaskClass uses explicit, stable rubric/profile and difficulty labels. It
// contains no prompt or output. Unknown difficulty must be written as unknown;
// no cross-task, cross-profile or cross-difficulty evidence is borrowed.
type TaskClass struct{ Domain, Profile, Difficulty string }

func (t TaskClass) Validate() error {
	if !label(t.Domain) || !label(t.Profile) {
		return ErrInvalid
	}
	switch t.Difficulty {
	case "easy", "medium", "hard", "unknown":
		return nil
	}
	return ErrInvalid
}

type Candidate struct {
	Identity Identity
	// The trusted host must derive these from fresh capability/policy checks,
	// including harness egress and tool authority, not just model locality.
	Local, Available, Authorized, Compatible, CapacityAvailable bool
	CredentialAvailable                                         bool
	Capabilities                                                []string
	ContextTokens                                               int64
	EstimatedCost                                               float64
}
type Request struct {
	Version       int
	Task          TaskClass
	Mode          string
	LocalRequired bool
	Capabilities  []string
	ContextTokens int64
	MaxCost       float64
	// Ordinary selection never explores. An explicitly budgeted evaluation task
	// may opt in, with a separate policy probability and injected random draw.
	AllowExploration bool
}
type Policy struct {
	Version     int
	HalfLife    time.Duration
	MinSamples  int
	Exploration float64
}

func DefaultPolicy() Policy {
	return Policy{Version: Version, HalfLife: 30 * 24 * time.Hour, MinSamples: 20}
}

type Exclusion struct {
	Identity Identity
	Reasons  []string
}
type Ranked struct {
	Identity Identity
	// Correctness and Quality are prior-shrunk weighted means, not guarantees.
	Correctness, Quality, Confidence                                          float64
	ConfirmedSamples, AdvisorySamples, PendingOutputs, InfrastructureFailures int
	Canceled, Indeterminate                                                   int
	EffectiveSamples                                                          float64
	LastEvidence                                                              time.Time
}
type Selection struct {
	Version  int
	Task     TaskClass
	AsOf     time.Time
	Primary  Ranked
	Ranked   []Ranked
	Excluded []Exclusion
	Explored bool
	Reason   string
}

func label(s string) bool {
	return len(s) > 0 && len(s) <= 256 && strings.TrimSpace(s) == s && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}
func digest(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func unit(v float64) bool        { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1 }
func nonnegative(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }
func labels(values []string) bool {
	if len(values) > 128 {
		return false
	}
	seen := map[string]bool{}
	for _, v := range values {
		if !label(v) || seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}

// Canonical UTC timestamps keep hashes stable and JSON serialization bounded.
func validTime(t time.Time) bool {
	return t.Location() == time.UTC && t.Year() >= 1970 && t.Year() < 2261
}
