// Package processguard records conservative, local process-lifetime ownership.
// It does not attest remote requests or descendant processes, authorize retries,
// or reclaim leases. Missing or unverifiable files are never proof of exit.
package processguard

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrUnavailable = errors.New("process ownership unavailable")

// Reference is private storage metadata, not a public diagnostic or capability.
// The identities bind both the containing directory and the locked file inode.
type Reference struct {
	Version           int    `json:"version"`
	ID                string `json:"id"`
	Directory         string `json:"directory"`
	DirectoryIdentity string `json:"directory_identity"`
	FileIdentity      string `json:"file_identity"`
}

func (r Reference) Validate() error {
	if r.Version != 1 || len(r.ID) != 26 || len(r.Directory) > 4096 || !utf8.ValidString(r.Directory) || !filepath.IsAbs(r.Directory) || filepath.Clean(r.Directory) != r.Directory {
		return ErrUnavailable
	}
	for _, c := range r.ID {
		if !(c >= 'A' && c <= 'Z' || c >= '2' && c <= '7') {
			return ErrUnavailable
		}
	}
	for _, c := range r.Directory {
		if unicode.IsControl(c) {
			return ErrUnavailable
		}
	}
	suffix, ok := strings.CutPrefix(filepath.Base(r.Directory), "darwin-owner-")
	if !ok || len(suffix) == 0 || len(suffix) > 30 {
		return ErrUnavailable
	}
	for _, c := range suffix {
		if c < '0' || c > '9' {
			return ErrUnavailable
		}
	}
	for _, identity := range []string{r.DirectoryIdentity, r.FileIdentity} {
		dev, ino, ok := strings.Cut(identity, ":")
		if !ok || len(identity) > 64 || dev == "" || ino == "" {
			return ErrUnavailable
		}
		for _, part := range []string{dev, ino} {
			if _, err := strconv.ParseUint(part, 10, 64); err != nil {
				return ErrUnavailable
			}
			for _, c := range part {
				if c < '0' || c > '9' {
					return ErrUnavailable
				}
			}
		}
	}
	return nil
}

type State string

const (
	Held     State = "held"
	Unlocked State = "unlocked"
)

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return ErrUnavailable
	}
	return ctx.Err()
}
