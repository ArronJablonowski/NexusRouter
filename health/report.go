// Package health describes bounded operational observations, not proof that a
// model can successfully execute a particular task.
package health

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid health report")

type Check struct {
	Component string `json:"component"`
	ID        string `json:"id,omitempty"`
	Status    string `json:"status"`
	Code      string `json:"code"`
}

type Report struct {
	Version   int       `json:"version"`
	CheckedAt time.Time `json:"checked_at"`
	Status    string    `json:"status"`
	Ready     bool      `json:"ready"`
	Checks    []Check   `json:"checks"`
}

func (c Check) Validate() error {
	switch c.Component {
	case "daemon", "database", "supervisor", "workboard_scheduler", "learning", "skill_regression", "outcome_supervision", "metrics_export", "trace_export", "harness_evidence", "resources", "provider", "model":
	default:
		return ErrInvalid
	}
	if len(c.ID) > 128 || !utf8.ValidString(c.ID) || strings.ContainsFunc(c.ID, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
		return ErrInvalid
	}
	if (c.Component == "provider" || c.Component == "model") && c.ID == "" {
		return ErrInvalid
	}
	if (c.Component == "daemon" || c.Component == "database" || c.Component == "supervisor" || c.Component == "workboard_scheduler" || c.Component == "learning" || c.Component == "skill_regression" || c.Component == "outcome_supervision" || c.Component == "metrics_export" || c.Component == "trace_export" || c.Component == "harness_evidence") && c.ID != "" {
		return ErrInvalid
	}
	switch c.Status {
	case "healthy", "degraded", "unavailable", "disabled", "unknown":
	default:
		return ErrInvalid
	}
	switch c.Code {
	case "learning_budget_wait":
		if c.Component != "learning" {
			return ErrInvalid
		}
	case "serving", "available", "unavailable", "configuration_limit", "disabled_by_policy", "credentials_missing", "discovery_failed", "model_missing", "model_metadata_missing", "capacity_available", "capacity_exhausted", "metrics_unknown", "supervisor_unavailable", "supervisor_ok", "supervisor_starting", "supervisor_stopping", "supervisor_stopped", "supervisor_error", "supervisor_stalled":
	default:
		return ErrInvalid
	}
	if c.Component == "workboard_scheduler" && c.Code != "supervisor_ok" && c.Code != "supervisor_starting" &&
		c.Code != "supervisor_stalled" && c.Code != "supervisor_error" && c.Code != "supervisor_stopping" && c.Code != "supervisor_stopped" {
		return ErrInvalid
	}
	validState := false
	switch c.Code {
	case "serving", "available", "capacity_available", "supervisor_ok", "learning_budget_wait":
		validState = c.Status == "healthy"
	case "configuration_limit", "credentials_missing", "discovery_failed", "model_missing", "model_metadata_missing", "supervisor_stopping", "supervisor_stopped":
		validState = c.Status == "unavailable"
	case "disabled_by_policy":
		validState = c.Status == "disabled"
	case "metrics_unknown", "supervisor_starting":
		validState = c.Status == "unknown"
	case "capacity_exhausted":
		validState = c.Status == "degraded" || c.Status == "unavailable"
	case "supervisor_error", "supervisor_stalled":
		validState = c.Status == "degraded"
	case "unavailable", "supervisor_unavailable":
		validState = c.Status == "unavailable" || c.Status == "unknown"
	}
	if !validState {
		return ErrInvalid
	}
	return nil
}

// Outcome derives readiness from operational prerequisites and at least one
// discovered configured model. Unknown supplemental measurements degrade the
// report but do not assert that an otherwise usable model is unavailable.
// An included Workboard scheduler must be healthy. Included learning and
// regression supervisors must be healthy or disabled. Older reports without
// these optional components retain their prior semantics.
// Metrics and trace export are supplemental: failure degrades status, not
// serving readiness.
func Outcome(checks []Check) (string, bool) {
	core := map[string]bool{}
	model, all, schedulerReady, learningReady := false, true, true, true
	for _, c := range checks {
		if c.Component == "daemon" || c.Component == "database" || c.Component == "supervisor" {
			core[c.Component] = c.Status == "healthy"
		}
		if c.Component == "model" && c.Status == "healthy" {
			model = true
		}
		if c.Component == "workboard_scheduler" {
			schedulerReady = schedulerReady && c.Status == "healthy"
		}
		if c.Component == "learning" || c.Component == "skill_regression" || c.Component == "outcome_supervision" {
			learningReady = learningReady && (c.Status == "healthy" || c.Status == "disabled")
		}
		if c.Status != "healthy" && c.Status != "disabled" {
			all = false
		}
	}
	ready := core["daemon"] && core["database"] && core["supervisor"] && model && schedulerReady && learningReady
	if !ready {
		return "unavailable", false
	}
	if all {
		return "healthy", true
	}
	return "degraded", true
}

func (r Report) Validate() error {
	if r.Version != 1 || r.CheckedAt.IsZero() || len(r.Checks) < 4 || len(r.Checks) > 512 {
		return ErrInvalid
	}
	seen, components := map[string]bool{}, map[string]bool{}
	for _, c := range r.Checks {
		key := c.Component + "\x00" + c.ID
		if c.Validate() != nil || seen[key] {
			return ErrInvalid
		}
		seen[key], components[c.Component] = true, true
	}
	for _, required := range []string{"daemon", "database", "supervisor", "resources"} {
		if !components[required] {
			return ErrInvalid
		}
	}
	status, ready := Outcome(r.Checks)
	if r.Status != status || r.Ready != ready {
		return ErrInvalid
	}
	body, err := json.Marshal(r)
	if err != nil || len(body) > 128<<10 {
		return ErrInvalid
	}
	return nil
}
