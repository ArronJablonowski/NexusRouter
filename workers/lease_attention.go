package workers

import (
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

var ErrLeaseAttention = errors.New("lease attention unavailable")

// LeaseAttention is an observation of an unresolved lease, never authority to
// release it, retry work, or conclude that its original holder has exited.
type LeaseAttention struct {
	Version       int       `json:"version"`
	ID            string    `json:"id"`
	TaskID        string    `json:"task_id"`
	Writer        bool      `json:"writer"`
	State         string    `json:"state"`
	Reason        string    `json:"reason"`
	FirstObserved time.Time `json:"first_observed"`
	UpdatedAt     time.Time `json:"updated_at"`
	LeaseExpires  time.Time `json:"lease_expires"`
}

type LeaseAttentionOptions struct {
	State string `json:"state"`
	After string `json:"after"`
	Limit int    `json:"limit"`
}

func (o LeaseAttentionOptions) Validate() error {
	if o.Limit < 1 || o.Limit > 100 || (o.After != "" && !sessions.ValidEventPageID(o.After)) {
		return ErrLeaseAttention
	}
	switch o.State {
	case "open", "resolved", "all":
		return nil
	}
	return ErrLeaseAttention
}

type LeaseAttentionPage struct {
	Version       int              `json:"version"`
	StorageSchema int              `json:"storage_schema"`
	Available     bool             `json:"available"`
	Items         []LeaseAttention `json:"items"`
	NextCursor    string           `json:"next_cursor"`
	HasMore       bool             `json:"has_more"`
}

func (a LeaseAttention) Validate() error {
	if a.Version != 1 || !sessions.ValidEventPageID(a.ID) || !sessions.ValidEventPageID(a.TaskID) || !attentionTime(a.FirstObserved) || !attentionTime(a.UpdatedAt) || !attentionTime(a.LeaseExpires) || a.FirstObserved.After(a.UpdatedAt) {
		return ErrLeaseAttention
	}
	switch {
	case a.State == "open" && a.Reason == "expired_unreleased" && !a.LeaseExpires.After(a.UpdatedAt):
		return nil
	case a.State == "resolved" && a.Reason == "lease_released":
		return nil
	case a.State == "resolved" && a.Reason == "lease_renewed" && a.LeaseExpires.After(a.UpdatedAt):
		return nil
	}
	return ErrLeaseAttention
}

func (p LeaseAttentionPage) Validate() error {
	if p.Version != 1 || p.StorageSchema < 1 || p.StorageSchema > 30 || p.Available != (p.StorageSchema >= 24) || p.Items == nil || len(p.Items) > 100 || p.HasMore != (p.NextCursor != "") || (!p.Available && (len(p.Items) != 0 || p.HasMore)) {
		return ErrLeaseAttention
	}
	previous := ""
	for _, item := range p.Items {
		if item.Validate() != nil || item.ID <= previous {
			return ErrLeaseAttention
		}
		previous = item.ID
	}
	if p.HasMore && (len(p.Items) == 0 || p.NextCursor != previous) {
		return ErrLeaseAttention
	}
	return nil
}

func attentionTime(t time.Time) bool {
	_, offset := t.Zone()
	return offset == 0 && t.Year() >= 1970 && t.Year() < 2261
}
