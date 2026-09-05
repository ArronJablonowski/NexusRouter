package sessions

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

const MaxEventPageBytes = 8 << 20

var ErrEventCursor = errors.New("invalid event cursor")
var ErrEventPage = errors.New("invalid event page")
var ErrEventTooLarge = errors.New("event exceeds page size limit")

type EventPage struct {
	Version      int             `json:"version"`
	TaskID       string          `json:"task_id"`
	SessionID    string          `json:"session_id"`
	State        string          `json:"state"`
	FromSequence int64           `json:"from_sequence"`
	NextSequence int64           `json:"next_sequence"`
	HeadSequence int64           `json:"head_sequence"`
	HasMore      bool            `json:"has_more"`
	Events       []runtime.Event `json:"events"`
}

func ValidEventPageID(id string) bool {
	return id != "" && len(id) <= 128 && utf8.ValidString(id) && !strings.ContainsAny(id, ":") && !strings.ContainsFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

// EventPageStateMatches checks the durable head kind against its projection.
func EventPageStateMatches(state string, kind runtime.Kind) bool {
	switch state {
	case "completed":
		return kind == runtime.TaskCompleted
	case "failed":
		return kind == runtime.TaskFailed
	case "canceled":
		return kind == runtime.TaskCanceled
	case "running":
		switch kind {
		case runtime.TaskStarted, runtime.TurnStarted, runtime.TurnCompleted, runtime.ModelDelta, runtime.ToolStarted, runtime.ToolCompleted, runtime.WorkerStarted, runtime.WorkerHeartbeat, runtime.WorkerCompleted, runtime.RouteSelected, runtime.EvaluationRecorded, runtime.ErrorRecorded, runtime.SteeringApplied:
			return true
		}
		return false
	default:
		return false
	}
}

func (p EventPage) Validate() error {
	if p.FromSequence < 0 || p.FromSequence > p.HeadSequence {
		return ErrEventCursor
	}
	if p.Version != 1 || !ValidEventPageID(p.TaskID) || !ValidEventPageID(p.SessionID) || p.HeadSequence < 1 || p.NextSequence < p.FromSequence || p.NextSequence > p.HeadSequence || p.HasMore != (p.NextSequence < p.HeadSequence) || len(p.Events) > 100 || p.NextSequence-p.FromSequence != int64(len(p.Events)) {
		return ErrEventPage
	}
	if p.State != "running" && p.State != "completed" && p.State != "failed" && p.State != "canceled" {
		return ErrEventPage
	}
	if len(p.Events) == 0 && p.FromSequence != p.HeadSequence {
		return ErrEventPage
	}
	total := 0
	seen := map[string]bool{}
	for i, e := range p.Events {
		if e.Validate() != nil || seen[e.ID] || e.TaskID != p.TaskID || e.SessionID != p.SessionID || e.CorrelationID != p.TaskID || e.Sequence != p.FromSequence+int64(i)+1 || (e.Sequence == 1) != (e.Kind == runtime.TaskStarted) {
			return ErrEventPage
		}
		seen[e.ID] = true
		if e.Sequence == p.HeadSequence {
			if !EventPageStateMatches(p.State, e.Kind) {
				return ErrEventPage
			}
		} else if !EventPageStateMatches("running", e.Kind) {
			return ErrEventPage
		}
		body, err := json.Marshal(e)
		if err != nil || len(body) > MaxEventPageBytes-total {
			return ErrEventTooLarge
		}
		total += len(body)
	}
	return nil
}
