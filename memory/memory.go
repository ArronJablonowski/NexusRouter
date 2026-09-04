// Package memory defines local-first factual memory. Facts are data, never
// instructions or authority to change tool permissions. Scopes are assigned by
// the trusted application, not accepted from model-generated arguments.
package memory

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"
)

var ErrInput = errors.New("invalid memory request")
var ErrConflict = errors.New("memory revision conflict or record missing")

type Fact struct {
	Version    int       `json:"version"`
	ID         string    `json:"id"`
	Scope      string    `json:"scope"`
	Revision   int64     `json:"revision"`
	Content    string    `json:"content"`
	Provenance string    `json:"provenance"`
	Confidence float64   `json:"confidence"`
	Privacy    string    `json:"privacy"`
	Created    time.Time `json:"created"`
	Updated    time.Time `json:"updated"`
	LastUse    time.Time `json:"last_use,omitempty"`
	Expires    time.Time `json:"expires,omitempty"`
}

// Query pages by immutable ID. LocalOnly must be true before private facts can
// enter local context. A false value returns only explicitly shareable facts.
// IncludeExpired is intended for operator inspection/export, not prompt use.
type Query struct {
	Scope, AfterID, Contains  string
	Limit                     int
	Now                       time.Time
	LocalOnly, IncludeExpired bool
}

type Store interface {
	PutMemory(context.Context, Fact, int64) error
	QueryMemory(context.Context, Query) ([]Fact, error)
	TouchMemory(context.Context, string, string, time.Time) error
	DeleteMemory(context.Context, string, string, int64) error
	ExpireMemory(context.Context, string, time.Time) (int64, error)
}

func ValidTime(t time.Time) bool { return t.UTC().Year() >= 1970 && t.UTC().Year() < 2261 }
func ValidKey(s string) bool {
	return strings.TrimSpace(s) != "" && len(s) <= 512 && !strings.ContainsRune(s, '\x00')
}

func (f Fact) Validate() error {
	if f.Version != 1 || !ValidKey(f.ID) || !ValidKey(f.Scope) || f.Revision < 1 ||
		strings.TrimSpace(f.Content) == "" || len(f.Content) > 65536 ||
		strings.TrimSpace(f.Provenance) == "" || len(f.Provenance) > 4096 ||
		math.IsNaN(f.Confidence) || math.IsInf(f.Confidence, 0) || f.Confidence < 0 || f.Confidence > 1 ||
		(f.Privacy != "local_only" && f.Privacy != "shareable") ||
		!ValidTime(f.Created) || !ValidTime(f.Updated) || f.Updated.Before(f.Created) ||
		(!f.LastUse.IsZero() && (!ValidTime(f.LastUse) || f.LastUse.Before(f.Created))) ||
		(!f.Expires.IsZero() && (!ValidTime(f.Expires) || !f.Expires.After(f.Created))) {
		return ErrInput
	}
	return nil
}

func (q Query) Validate() error {
	if !ValidKey(q.Scope) || len(q.AfterID) > 512 || len(q.Contains) > 1024 || q.Limit < 1 || q.Limit > 1000 || !ValidTime(q.Now) {
		return ErrInput
	}
	return nil
}
