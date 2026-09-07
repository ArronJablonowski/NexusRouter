package sessions

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

const MaxTaskPageBytes = 1 << 20

var ErrTaskList = errors.New("task list unavailable or invalid")

// TaskSummary is content-free durable task-head metadata. It does not establish
// continuation eligibility, owner liveness, route health, or retry authority.
type TaskSummary struct {
	Version   int       `json:"version"`
	TaskID    string    `json:"task_id"`
	SessionID string    `json:"session_id"`
	State     string    `json:"state"`
	Sequence  int64     `json:"sequence"`
	StartedAt time.Time `json:"started_at"`
}

type TaskPage struct {
	Version    int           `json:"version"`
	Items      []TaskSummary `json:"items"`
	NextCursor string        `json:"next_cursor"`
	HasMore    bool          `json:"has_more"`
}

type TaskListOptions struct {
	State string
	After string
	Limit int
}

// TaskListCursor freezes the insertion high-water mark. State can still change
// between pages, so a filtered listing is an observation rather than a snapshot.
type TaskListCursor struct {
	Version   int    `json:"version"`
	Last      int64  `json:"last"`
	HighWater int64  `json:"high_water"`
	State     string `json:"state"`
}

func ValidTaskState(state string) bool {
	switch state {
	case "running", "completed", "failed", "canceled":
		return true
	}
	return false
}

func (c TaskListCursor) valid() bool {
	return c.Version == 1 && c.Last >= 1 && c.HighWater >= c.Last && (c.State == "" || ValidTaskState(c.State))
}

func EncodeTaskListCursor(c TaskListCursor) (string, error) {
	if !c.valid() {
		return "", ErrTaskList
	}
	body, err := json.Marshal(c)
	if err != nil {
		return "", ErrTaskList
	}
	return base64.RawURLEncoding.EncodeToString(body), nil
}

func DecodeTaskListCursor(value string) (TaskListCursor, error) {
	if value == "" || len(value) > 1024 {
		return TaskListCursor{}, ErrTaskList
	}
	body, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return TaskListCursor{}, ErrTaskList
	}
	var cursor TaskListCursor
	if json.Unmarshal(body, &cursor) != nil {
		return TaskListCursor{}, ErrTaskList
	}
	canonical, err := EncodeTaskListCursor(cursor)
	if err != nil || canonical != value {
		return TaskListCursor{}, ErrTaskList
	}
	return cursor, nil
}

func (o TaskListOptions) Validate() error {
	if o.Limit < 1 || o.Limit > 100 || (o.State != "" && !ValidTaskState(o.State)) {
		return ErrTaskList
	}
	if o.After != "" {
		cursor, err := DecodeTaskListCursor(o.After)
		if err != nil || cursor.State != o.State {
			return ErrTaskList
		}
	}
	return nil
}

func (s TaskSummary) Validate() error {
	if s.Version != 1 || !ValidEventPageID(s.TaskID) || !ValidEventPageID(s.SessionID) || !ValidTaskState(s.State) || s.Sequence < 1 || s.Sequence > 10000 || s.StartedAt.IsZero() {
		return ErrTaskList
	}
	return nil
}

func (p TaskPage) Validate() error {
	if p.Version != 1 || len(p.Items) > 100 || p.HasMore != (p.NextCursor != "") || (p.HasMore && len(p.Items) == 0) {
		return ErrTaskList
	}
	seen := map[string]bool{}
	for _, item := range p.Items {
		if item.Validate() != nil || seen[item.TaskID] {
			return ErrTaskList
		}
		seen[item.TaskID] = true
	}
	if p.HasMore {
		cursor, err := DecodeTaskListCursor(p.NextCursor)
		if err != nil {
			return ErrTaskList
		}
		if cursor.State != "" {
			for _, item := range p.Items {
				if item.State != cursor.State {
					return ErrTaskList
				}
			}
		}
	}
	body, err := json.Marshal(p)
	if err != nil || len(body) > MaxTaskPageBytes {
		return ErrTaskList
	}
	return nil
}
