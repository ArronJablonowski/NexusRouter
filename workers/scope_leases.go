package workers

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

var ErrScopeLeaseStatus = errors.New("scope lease status unavailable")

// ScopeLeaseStatus identifies unresolved holders, not permission to release them.
type ScopeLeaseStatus struct {
	Version              int                `json:"version"`
	OverlapPolicyVersion int                `json:"overlap_policy_version"`
	Scope                string             `json:"scope"`
	ObservedAt           time.Time          `json:"observed_at"`
	StorageSchema        int                `json:"storage_schema"`
	Available            bool               `json:"available"`
	Holders              []ScopeLeaseHolder `json:"holders"`
}

type ScopeLeaseHolder struct {
	TaskID         string `json:"task_id"`
	LiveReaders    int64  `json:"live_readers"`
	ExpiredReaders int64  `json:"expired_readers"`
	LiveWriters    int64  `json:"live_writers"`
	ExpiredWriters int64  `json:"expired_writers"`
}

func ValidLeaseScope(scope string) bool {
	return len(scope) >= 1 && len(scope) <= 512 && utf8.ValidString(scope) && strings.TrimSpace(scope) == scope && strings.IndexFunc(scope, unicode.IsControl) < 0
}

func (s ScopeLeaseStatus) Validate() error {
	_, offset := s.ObservedAt.Zone()
	if s.Version != 1 || s.OverlapPolicyVersion != 1 || !ValidLeaseScope(s.Scope) || offset != 0 || s.ObservedAt.Year() < 1970 || s.ObservedAt.Year() >= 2261 || s.StorageSchema < 1 || s.StorageSchema > 25 || s.Available != (s.StorageSchema >= 3) || s.Holders == nil || len(s.Holders) > 1000 || !s.Available && len(s.Holders) != 0 {
		return ErrScopeLeaseStatus
	}
	var total int64
	previous := ""
	for _, h := range s.Holders {
		if !sessions.ValidEventPageID(h.TaskID) || h.TaskID <= previous {
			return ErrScopeLeaseStatus
		}
		previous = h.TaskID
		var count int64
		for _, n := range []int64{h.LiveReaders, h.ExpiredReaders, h.LiveWriters, h.ExpiredWriters} {
			if n < 0 || n > 1000 {
				return ErrScopeLeaseStatus
			}
			count += n
		}
		total += count
		if count == 0 || total > 1000 {
			return ErrScopeLeaseStatus
		}
	}
	return nil
}
