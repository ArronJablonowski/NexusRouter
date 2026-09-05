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
	"unicode"
	"unicode/utf8"
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

// Store implementations exposed to operator management must enforce atomic
// revision checks, preserve privacy on corrections, and prevent deleted IDs
// from being reused in the same scope. Revision checks alone do not prevent
// a stale mutation from targeting a recreated record whose revision reset.
// The built-in SQLite store retires IDs without retaining factual payloads.
type Store interface {
	GetMemory(context.Context, string, string) (Fact, error)
	PutMemory(context.Context, Fact, int64) error
	QueryMemory(context.Context, Query) ([]Fact, error)
	TouchMemory(context.Context, string, string, time.Time) error
	DeleteMemory(context.Context, string, string, int64) error
	ExpireMemory(context.Context, string, time.Time) (int64, error)
}

func ValidTime(t time.Time) bool { return t.UTC().Year() >= 1970 && t.UTC().Year() < 2261 }
func ValidKey(s string) bool {
	return s != "" && strings.TrimSpace(s) == s && len(s) <= 512 && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

// Factual prose and substring searches may contain tabs and newlines, unlike
// identifiers. Invalid UTF-8 must not be normalized lossily by JSON encoding;
// NUL is excluded to avoid divergent storage/search string interpretations.
func validText(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, '\x00') }

func (f Fact) Validate() error {
	if f.Version != 1 || !ValidKey(f.ID) || !ValidKey(f.Scope) || f.Revision < 1 ||
		strings.TrimSpace(f.Content) == "" || len(f.Content) > 65536 || !validText(f.Content) ||
		strings.TrimSpace(f.Provenance) == "" || len(f.Provenance) > 4096 || !validText(f.Provenance) ||
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
	if !ValidKey(q.Scope) || (q.AfterID != "" && !ValidKey(q.AfterID)) || len(q.Contains) > 1024 || !validText(q.Contains) || q.Limit < 1 || q.Limit > 1000 || !ValidTime(q.Now) {
		return ErrInput
	}
	return nil
}
