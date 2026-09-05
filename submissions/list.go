package submissions

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxPageBytes = 1 << 20

type Summary struct {
	Version         int        `json:"version"`
	ID              string     `json:"id"`
	State           string     `json:"state"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	ConfigDigest    string     `json:"config_digest"`
	TaskIDs         []string   `json:"task_ids"`
	CancelRequested bool       `json:"cancel_requested"`
	LeaseExpiresAt  *time.Time `json:"lease_expires_at,omitempty"`
	LeaseExpired    bool       `json:"lease_expired"`
	ErrorCode       string     `json:"error_code,omitempty"`
}

type Page struct {
	Version    int       `json:"version"`
	Items      []Summary `json:"items"`
	NextCursor string    `json:"next_cursor"`
	HasMore    bool      `json:"has_more"`
}
type ListOptions struct {
	State string
	After string
	Limit int
}

// ListCursor fences insertions, not changes in state membership across pages.
type ListCursor struct {
	Version   int    `json:"version"`
	Last      int64  `json:"last"`
	HighWater int64  `json:"high_water"`
	State     string `json:"state"`
}

func validState(s string) bool {
	switch s {
	case "queued", "running", "succeeded", "failed", "canceled":
		return true
	}
	return false
}
func (c ListCursor) valid() bool {
	return c.Version == 1 && c.Last >= 0 && c.HighWater >= c.Last && (c.State == "" || validState(c.State))
}
func EncodeListCursor(c ListCursor) (string, error) {
	if !c.valid() {
		return "", ErrInvalid
	}
	b, e := json.Marshal(c)
	if e != nil {
		return "", ErrInvalid
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func DecodeListCursor(s string) (ListCursor, error) {
	var c ListCursor
	if len(s) == 0 || len(s) > 1024 {
		return c, ErrInvalid
	}
	b, e := base64.RawURLEncoding.DecodeString(s)
	if e != nil || json.Unmarshal(b, &c) != nil {
		return ListCursor{}, ErrInvalid
	}
	canonical, e := EncodeListCursor(c)
	if e != nil || canonical != s {
		return ListCursor{}, ErrInvalid
	}
	return c, nil
}
func (o ListOptions) Validate() error {
	if o.Limit < 1 || o.Limit > 100 || (o.State != "" && !validState(o.State)) {
		return ErrInvalid
	}
	if o.After != "" {
		c, e := DecodeListCursor(o.After)
		if e != nil || c.State != o.State {
			return ErrInvalid
		}
	}
	return nil
}
func validID(s string) bool {
	if s == "" || len(s) > 128 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func (s Summary) Validate() error {
	digest, e := hex.DecodeString(s.ConfigDigest)
	if s.Version != 1 || !validID(s.ID) || !validState(s.State) || s.CreatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt) || e != nil || len(digest) != 32 || strings.ToLower(s.ConfigDigest) != s.ConfigDigest || len(s.TaskIDs) > 1000 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range s.TaskIDs {
		if !validID(id) || seen[id] {
			return ErrInvalid
		}
		seen[id] = true
	}
	if s.LeaseExpiresAt != nil && s.LeaseExpiresAt.IsZero() {
		return ErrInvalid
	}
	if s.State == "running" && s.LeaseExpiresAt == nil {
		return ErrInvalid
	}
	if s.LeaseExpired && (s.State != "running" || s.LeaseExpiresAt == nil) {
		return ErrInvalid
	}
	switch s.ErrorCode {
	case "", "task_failed", "execution_failed", "canceled", "interrupted", "lease_lost", "admission_denied", "deadline_exceeded", "persistence_failed", "recovery_exhausted":
	default:
		return ErrInvalid
	}
	if s.ErrorCode != "" && s.State != "failed" && s.State != "canceled" {
		return ErrInvalid
	}
	return nil
}
func (p Page) Validate() error {
	if p.Version != 1 || len(p.Items) > 100 || p.HasMore != (p.NextCursor != "") || (p.HasMore && len(p.Items) == 0) {
		return ErrInvalid
	}
	var c ListCursor
	if p.HasMore {
		var e error
		c, e = DecodeListCursor(p.NextCursor)
		if e != nil {
			return ErrInvalid
		}
	}
	seen := map[string]bool{}
	for _, s := range p.Items {
		if s.Validate() != nil || seen[s.ID] || (c.State != "" && s.State != c.State) {
			return ErrInvalid
		}
		seen[s.ID] = true
	}
	b, e := json.Marshal(p)
	if e != nil || len(b) > MaxPageBytes {
		return ErrInvalid
	}
	return nil
}
