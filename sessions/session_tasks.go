package sessions

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

const MaxSessionTaskPageBytes = 1 << 20

var ErrSessionTasks = errors.New("session tasks unavailable or invalid")

// TaskHeadFence identifies one exact durable task head. Callers may present a
// fence when requesting an operation based on completed history; the receiving
// store must still re-read and validate the source atomically.
type TaskHeadFence struct {
	Version      int    `json:"version"`
	TaskID       string `json:"task_id"`
	SessionID    string `json:"session_id"`
	HeadSequence int64  `json:"head_sequence"`
	HeadEventID  string `json:"head_event_id"`
}

func (f TaskHeadFence) Validate() error {
	if f.Version != 1 || !ValidEventPageID(f.TaskID) || !ValidEventPageID(f.SessionID) || f.HeadSequence < 1 || f.HeadSequence > 10000 || !ValidEventPageID(f.HeadEventID) {
		return ErrSessionTasks
	}
	return nil
}

// SessionTask is content-free task-head and lineage metadata derived from a
// task's immutable start plus its current durable head.
type SessionTask struct {
	Version       int           `json:"version"`
	TaskID        string        `json:"task_id"`
	SessionID     string        `json:"session_id"`
	ParentTaskID  string        `json:"parent_task_id,omitempty"`
	RetryOfTaskID string        `json:"retry_of_task_id,omitempty"`
	State         string        `json:"state"`
	Sequence      int64         `json:"sequence"`
	StartedAt     time.Time     `json:"started_at"`
	Fence         TaskHeadFence `json:"fence"`
}

type SessionTaskPage struct {
	Version    int           `json:"version"`
	SessionID  string        `json:"session_id"`
	Items      []SessionTask `json:"items"`
	NextCursor string        `json:"next_cursor"`
	HasMore    bool          `json:"has_more"`
}

type SessionTaskListOptions struct {
	After string
	Limit int
}

// SessionTaskCursor freezes membership at one session-specific insertion
// high-water mark and cannot be replayed against another session.
type SessionTaskCursor struct {
	Version   int    `json:"version"`
	SessionID string `json:"session_id"`
	Last      int64  `json:"last"`
	HighWater int64  `json:"high_water"`
}

func (c SessionTaskCursor) valid() bool {
	return c.Version == 1 && ValidEventPageID(c.SessionID) && c.Last >= 1 && c.HighWater >= c.Last
}

func EncodeSessionTaskCursor(c SessionTaskCursor) (string, error) {
	if !c.valid() {
		return "", ErrSessionTasks
	}
	body, err := json.Marshal(c)
	if err != nil {
		return "", ErrSessionTasks
	}
	return base64.RawURLEncoding.EncodeToString(body), nil
}

func DecodeSessionTaskCursor(value string) (SessionTaskCursor, error) {
	if value == "" || len(value) > 1024 {
		return SessionTaskCursor{}, ErrSessionTasks
	}
	body, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return SessionTaskCursor{}, ErrSessionTasks
	}
	var cursor SessionTaskCursor
	if json.Unmarshal(body, &cursor) != nil {
		return SessionTaskCursor{}, ErrSessionTasks
	}
	canonical, err := EncodeSessionTaskCursor(cursor)
	if err != nil || canonical != value {
		return SessionTaskCursor{}, ErrSessionTasks
	}
	return cursor, nil
}

func (o SessionTaskListOptions) Validate(session string) error {
	if !ValidEventPageID(session) || o.Limit < 1 || o.Limit > 100 {
		return ErrSessionTasks
	}
	if o.After != "" {
		cursor, err := DecodeSessionTaskCursor(o.After)
		if err != nil || cursor.SessionID != session {
			return ErrSessionTasks
		}
	}
	return nil
}

func (t SessionTask) Validate() error {
	if t.Version != 1 || !ValidEventPageID(t.TaskID) || !ValidEventPageID(t.SessionID) || !ValidTaskState(t.State) || t.Sequence < 1 || t.Sequence > 10000 || t.StartedAt.IsZero() || t.Fence.Validate() != nil || t.Fence.TaskID != t.TaskID || t.Fence.SessionID != t.SessionID || t.Fence.HeadSequence != t.Sequence {
		return ErrSessionTasks
	}
	for _, related := range []string{t.ParentTaskID, t.RetryOfTaskID} {
		if related != "" && (!ValidEventPageID(related) || related == t.TaskID) {
			return ErrSessionTasks
		}
	}
	return nil
}

func (p SessionTaskPage) Validate() error {
	if p.Version != 1 || !ValidEventPageID(p.SessionID) || len(p.Items) > 100 || p.HasMore != (p.NextCursor != "") || (p.HasMore && len(p.Items) == 0) {
		return ErrSessionTasks
	}
	seen := map[string]bool{}
	for _, item := range p.Items {
		if item.Validate() != nil || item.SessionID != p.SessionID || seen[item.TaskID] {
			return ErrSessionTasks
		}
		seen[item.TaskID] = true
	}
	if p.HasMore {
		cursor, err := DecodeSessionTaskCursor(p.NextCursor)
		if err != nil || cursor.SessionID != p.SessionID {
			return ErrSessionTasks
		}
	}
	body, err := json.Marshal(p)
	if err != nil || len(body) > MaxSessionTaskPageBytes {
		return ErrSessionTasks
	}
	return nil
}
