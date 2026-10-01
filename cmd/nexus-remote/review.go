package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/ArronJablonowski/NexusRouter/remote"
)

// Review files are operator-supplied, not a model-tool input. Preserve the exact
// saved file for retries and use ExpectedHead for a changed verdict.
func readOutcomeReview(path string) (remote.OutcomeReview, error) {
	var out remote.OutcomeReview
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return out, remote.ErrInvalid
	}
	st, err := os.Lstat(path)
	if err != nil {
		return out, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 32768 {
		return out, remote.ErrDenied
	}
	f, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(st, actual) {
		return out, remote.ErrDenied
	}
	body, err := io.ReadAll(io.LimitReader(f, 32769))
	if err != nil || len(body) > 32768 {
		return out, remote.ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || decoder.Decode(new(any)) != io.EOF {
		return remote.OutcomeReview{}, remote.ErrInvalid
	}
	return out, nil
}
