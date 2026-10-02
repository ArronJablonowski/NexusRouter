package remote

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/internal/usagestats"
	"net/http"
	"slices"
)

// TaskSummary exposes caller-owned lifecycle metadata, never prompts or results.
// Unknown status grants no retry authority; recover using the original request.
type TaskSummary struct {
	Usage *usagestats.RemoteUsage `json:"usage,omitempty"`

	RequestID       string   `json:"request_id"`
	State           string   `json:"state"`
	TaskIDs         []string `json:"task_ids"`
	CancelRequested bool     `json:"cancel_requested"`
}
type TaskPage struct {
	Caller string `json:"caller,omitempty"`

	Version  int           `json:"version"`
	Instance string        `json:"instance"`
	After    string        `json:"after"`
	Next     string        `json:"next"`
	HasMore  bool          `json:"has_more"`
	Tasks    []TaskSummary `json:"tasks"`
}

// Tasks lists only the authenticated caller's requests, in request-ID order.
// This is a live traversal, not a snapshot. Restart from empty after to discover
// newly inserted keys that sort before an earlier cursor. No automatic retry.
func (c *Client) Tasks(ctx context.Context, destination, after string) (TaskPage, error) {
	var out TaskPage
	if after != "" && !requestID(after) {
		return out, ErrInvalid
	}
	err := c.call(ctx, destination, "inspect", http.MethodGet, "/v1/remote/tasks", nil, map[string]string{"X-Nexus-After-Request": after}, &out)
	if err == nil && (out.Version != Version || out.Instance != destination || out.After != after || !out.valid()) {
		return TaskPage{}, ErrUnavailable
	}
	if err == nil && c.UsageFile != "" {
		err = c.recordUsagePage(ctx, destination, out)
	}
	return out, err
}
func (p TaskPage) valid() bool {
	if len(p.Tasks) > 100 || (p.HasMore && len(p.Tasks) != 100) {
		return false
	}
	previous := p.After
	for _, t := range p.Tasks {
		if t.Usage != nil && !t.Usage.Valid() {
			return false
		}
		if !requestID(t.RequestID) || t.RequestID <= previous || len(t.TaskIDs) > 128 {
			return false
		}
		switch t.State {
		case "unknown", "queued", "running", "succeeded", "failed", "canceled":
		default:
			return false
		}
		for _, id := range t.TaskIDs {
			if !name(id) {
				return false
			}
		}
		if t.State == "unknown" && (len(t.TaskIDs) != 0 || t.CancelRequested) {
			return false
		}
		previous = t.RequestID
	}
	return p.Next == previous
}
func (s *Server) taskPage(ctx context.Context, caller, after string) (TaskPage, error) {
	out := TaskPage{Caller: caller, Version: Version, Instance: s.instance, After: after, Next: after, Tasks: []TaskSummary{}}
	if after != "" && !requestID(after) {
		return out, ErrInvalid
	}
	rows, err := s.journal.db.QueryContext(ctx, "SELECT request_id,submission FROM requests WHERE caller=? AND request_id>? ORDER BY request_id LIMIT 101", caller, after)
	if err != nil {
		return out, err
	}
	type owned struct{ key, submission string }
	var entries []owned
	for rows.Next() {
		var e owned
		if err = rows.Scan(&e.key, &e.submission); err != nil {
			rows.Close()
			return out, err
		}
		entries = append(entries, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(entries) > 100 {
		out.HasMore = true
		entries = entries[:100]
	}
	for _, e := range entries {
		t := TaskSummary{RequestID: e.key, State: "unknown", TaskIDs: []string{}}
		if e.submission != "" {
			status, err := s.backend.Status(ctx, e.submission)
			if err == nil && status.ID == e.submission && status.Version == Version {
				switch status.State {
				case "queued", "running", "succeeded", "failed", "canceled":
					t.State = status.State
					t.TaskIDs = slices.Clone(status.TaskIDs)
					t.CancelRequested = status.CancelRequested
					if b, ok := s.backend.(interface {
						RemoteUsage(context.Context, []string) (usagestats.RemoteUsage, error)
					}); ok {
						u, e := b.RemoteUsage(ctx, status.TaskIDs)
						if e == nil && u.Valid() {
							t.Usage = &u
						}
					}
				}
			}
		}
		if ctx.Err() != nil {
			return out, ErrUnavailable
		}
		out.Tasks = append(out.Tasks, t)
		out.Next = e.key
	}
	if !out.valid() {
		return TaskPage{}, ErrUnavailable
	}
	return out, nil
}
