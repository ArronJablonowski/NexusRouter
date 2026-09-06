package memory

import (
	"context"
	"encoding/json"
	"time"
)

const ExportMaxFacts = 1000
const ExportMaxBytes = 8 << 20

// ExportSnapshot contains all current facts in one scope, including expired and
// private facts. It excludes retired identities and is not a database backup or
// an import authorization. CapturedAt is the caller's observation timestamp,
// not a database revision or proof of wall-clock ordering with other writers.
type ExportSnapshot struct {
	Version    int       `json:"version"`
	Scope      string    `json:"scope"`
	CapturedAt time.Time `json:"captured_at"`
	Facts      []Fact    `json:"facts"`
}

// Exporter is optional: paging through Store.QueryMemory cannot guarantee a
// consistent snapshot. Implementations must read atomically, respect cancellation
// and return all facts or an error, never a silently truncated result.
type Exporter interface {
	ExportMemory(context.Context, string, time.Time) (ExportSnapshot, error)
}

// Validate verifies the bounded wire contract, not the backend's completeness or
// transaction isolation. Custom exporters remain trusted to provide those.
func (s ExportSnapshot) Validate() error {
	if s.Version != 1 || !ValidKey(s.Scope) || !ValidTime(s.CapturedAt) || s.Facts == nil || len(s.Facts) > ExportMaxFacts {
		return ErrInput
	}
	last := ""
	budget := ExportMaxBytes
	for _, f := range s.Facts {
		if f.Validate() != nil || f.Scope != s.Scope || f.ID <= last {
			return ErrInput
		}
		b, err := json.Marshal(f)
		budget -= len(b) + 1
		if err != nil || budget < 0 {
			return ErrInput
		}
		last = f.ID
	}
	b, err := json.Marshal(s)
	if err != nil || len(b) > ExportMaxBytes {
		return ErrInput
	}
	return nil
}
