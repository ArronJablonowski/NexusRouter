package remote

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// Logs contain full private session content. The separate logs permission grants
// instance-wide read access, including tasks submitted by other callers. It does
// not grant dispatch, cancellation, or authority to change learning evidence.
type LogRecord struct {
	Position  int64           `json:"position"`
	ID        string          `json:"id"`
	At        time.Time       `json:"at"`
	Kind      string          `json:"kind"`
	TaskID    string          `json:"task_id,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Body      json.RawMessage `json:"body"`
}
type LogPage struct {
	Version  int         `json:"version"`
	Instance string      `json:"instance"`
	Stream   string      `json:"stream"`
	After    string      `json:"after"`
	Next     string      `json:"next"`
	HasMore  bool        `json:"has_more"`
	Records  []LogRecord `json:"records"`
}

const maxLogPageBytes = 9 << 20

func logStream(s string) bool { return s == "runtime" || s == "security" }

func (c *Client) Logs(ctx context.Context, destination, stream, after string) (LogPage, error) {
	var p LogPage
	if !logStream(stream) || len(after) > sessions.MaxEventLogCursorBytes {
		return p, ErrInvalid
	}
	err := c.call(ctx, destination, "logs", "GET", "/v1/remote/logs", nil, map[string]string{"X-Nexus-Log-Stream": stream, "X-Nexus-Log-Cursor": after}, &p)
	if err == nil {
		err = p.validate(destination, stream, after)
	}
	return p, err
}
func (p LogPage) validate(destination, stream, after string) error {
	if p.Version != 1 || p.Instance != destination || !id(destination) || p.Stream != stream || !logStream(stream) || p.After != after || p.Next == "" || len(p.Records) > 100 {
		return ErrInvalid
	}
	var previous int64
	if stream == "runtime" && after != "" {
		c, e := sessions.ParseEventLogCursor(after)
		if e != nil {
			return ErrInvalid
		}
		previous = c.LastPosition
	}
	if stream == "security" && after != "" {
		c, e := parseAuditLogCursor(after)
		if e != nil {
			return e
		}
		previous = c.Last
	}
	for _, r := range p.Records {
		if r.Position != previous+1 || r.ID == "" || r.At.IsZero() || r.Kind == "" || !json.Valid(r.Body) {
			return ErrConflict
		}
		if stream == "runtime" {
			var event runtime.Event
			if json.Unmarshal(r.Body, &event) != nil || event.Validate() != nil || event.ID != r.ID || !event.Time.Equal(r.At) || string(event.Kind) != r.Kind || event.TaskID != r.TaskID || event.SessionID != r.SessionID {
				return ErrConflict
			}
		} else {
			var event AuditEntry
			if json.Unmarshal(r.Body, &event) != nil || event.Sequence != r.Position || event.Destination != destination || !event.At.Equal(r.At) || r.ID != strconv.FormatInt(event.Sequence, 10) || r.Kind != event.Action+"."+event.Outcome || r.TaskID != "" || r.SessionID != "" {
				return ErrConflict
			}
		}
		previous = r.Position
	}
	if stream == "runtime" {
		next, e := sessions.ParseEventLogCursor(p.Next)
		if e != nil || next.LastPosition != previous || p.HasMore != (next.LastPosition < next.HighWaterPosition) {
			return ErrConflict
		}
		if len(p.Records) > 0 && next.LastEventID != p.Records[len(p.Records)-1].ID {
			return ErrConflict
		}
	} else {
		next, e := parseAuditLogCursor(p.Next)
		if e != nil || next.Last != previous || p.HasMore != (next.Last < next.Through) {
			return ErrConflict
		}
		if len(p.Records) > 0 && next.Anchor != certificateDigest(p.Records[len(p.Records)-1].Body) {
			return ErrConflict
		}
	}
	body, e := json.Marshal(p)
	if e != nil || len(body) > maxLogPageBytes {
		return ErrInvalid
	}
	return nil
}
func (s *Server) logPage(ctx context.Context, r *http.Request) (LogPage, error) {
	stream, after := r.Header.Get("X-Nexus-Log-Stream"), r.Header.Get("X-Nexus-Log-Cursor")
	out := LogPage{Version: 1, Instance: s.instance, Stream: stream, After: after, Records: []LogRecord{}}
	if !logStream(stream) || len(after) > sessions.MaxEventLogCursorBytes {
		return out, ErrInvalid
	}
	if stream == "security" {
		return s.journal.logPage(ctx, out)
	}
	backend, ok := s.backend.(interface {
		CommittedLogs(context.Context, sessions.EventLogOptions) (sessions.CommittedEventPage, error)
	})
	if !ok {
		return out, ErrUnavailable
	}
	page, err := backend.CommittedLogs(ctx, sessions.EventLogOptions{After: after, Limit: 100})
	if err != nil {
		if errors.Is(err, sessions.ErrEventLog) {
			return out, ErrConflict
		}
		return out, err
	}
	if page.Validate() != nil {
		return out, ErrUnavailable
	}
	for _, item := range page.Events {
		body, err := json.Marshal(item.Event)
		if err != nil {
			return out, ErrUnavailable
		}
		e := item.Event
		out.Records = append(out.Records, LogRecord{item.Position, e.ID, e.Time, string(e.Kind), e.TaskID, e.SessionID, body})
	}
	out.Next, out.HasMore = page.NextCursor, page.HasMore
	return out, out.validate(s.instance, stream, after)
}

type auditLogCursor struct {
	Last    int64  `json:"last"`
	Anchor  string `json:"anchor"`
	Through int64  `json:"through"`
}

func parseAuditLogCursor(value string) (auditLogCursor, error) {
	var c auditLogCursor
	if value == "" {
		return c, nil
	}
	b, e := base64.RawURLEncoding.DecodeString(value)
	if e != nil || len(value) > 4096 || json.Unmarshal(b, &c) != nil || c.Last < 0 || c.Through < c.Last || (c.Last == 0 && c.Anchor != "") || (c.Last > 0 && !hexDigest(c.Anchor)) {
		return c, ErrInvalid
	}
	if encodeAuditLogCursor(c) != value {
		return c, ErrInvalid
	}
	return c, nil
}
func encodeAuditLogCursor(c auditLogCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}
func (j *Journal) logPage(ctx context.Context, out LogPage) (LogPage, error) {
	c, err := parseAuditLogCursor(out.After)
	if err != nil {
		return out, err
	}
	tx, err := j.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var maximum int64
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(sequence),0) FROM audit").Scan(&maximum); err != nil {
		return out, err
	}
	if c.Last > 0 {
		entries, e := readAuditRows(ctx, tx, j.instance, c.Last-1, c.Last, 1)
		if e != nil || len(entries) != 1 {
			return out, ErrConflict
		}
		body, _ := json.Marshal(entries[0])
		if certificateDigest(body) != c.Anchor {
			return out, ErrConflict
		}
	}
	if c.Through > maximum {
		return out, ErrConflict
	}
	if c.Last == c.Through {
		c.Through = maximum
	}
	entries, err := readAuditRows(ctx, tx, j.instance, c.Last, c.Through, 100)
	if err != nil {
		return out, err
	}
	for _, e := range entries {
		if e.Sequence != c.Last+1 {
			return out, ErrConflict
		}
		body, _ := json.Marshal(e)
		out.Records = append(out.Records, LogRecord{Position: e.Sequence, ID: strconv.FormatInt(e.Sequence, 10), At: e.At, Kind: e.Action + "." + e.Outcome, Body: body})
		c.Last, c.Anchor = e.Sequence, certificateDigest(body)
	}
	if c.Last < c.Through && len(entries) == 0 {
		return out, ErrConflict
	}
	out.Next, out.HasMore = encodeAuditLogCursor(c), c.Last < c.Through
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return out, out.validate(j.instance, "security", out.After)
}
