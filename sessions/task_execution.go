package sessions

import "time"

// TaskExecution is a read-only observation, not a repair of the task journal.
type TaskExecution struct {
	State      string    `json:"state"`
	Evidence   string    `json:"evidence"`
	ObservedAt time.Time `json:"observed_at"`
}
