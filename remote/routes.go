package remote

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// RouteBinding is immutable caller-side routing intent, not proof of delivery.
// It stores no prompt, result, private key or credential path.
type RouteBinding struct {
	Version           int    `json:"version"`
	RequestID         string `json:"request_id"`
	Destination       string `json:"destination"`
	CallerFingerprint string `json:"caller_fingerprint"`
	TaskSHA256        string `json:"task_sha256"`
}

func (b RouteBinding) valid() bool {
	return b.Version == Version && requestID(b.RequestID) && id(b.Destination) && hexDigest(b.CallerFingerprint) && hexDigest(b.TaskSHA256)
}
func hexDigest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}

// RouteStore uses atomic no-replace publication of synced immutable files.
// Keep a dedicated private directory; deleting a binding removes retry safety.
type RouteStore struct{ directory string }

func OpenRouteStore(directory string) (*RouteStore, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, ErrInvalid
	}
	if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	s := &RouteStore{directory}
	if err := s.check(); err != nil {
		return nil, err
	}
	parent, err := os.Open(filepath.Dir(directory))
	if err != nil {
		return nil, err
	}
	err = parent.Sync()
	closeErr := parent.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return s, nil
}
func (s *RouteStore) check() error {
	if s == nil {
		return ErrInvalid
	}
	st, err := os.Lstat(s.directory)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 {
		return ErrDenied
	}
	return nil
}
func (s *RouteStore) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(s.directory, hex.EncodeToString(sum[:])+".route.json")
}
func (s *RouteStore) Lookup(key string) (RouteBinding, error) {
	var out RouteBinding
	if !requestID(key) {
		return out, ErrInvalid
	}
	if err := s.check(); err != nil {
		return out, err
	}
	path := s.path(key)
	st, err := os.Lstat(path)
	if err != nil {
		return out, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 4096 {
		return out, ErrDenied
	}
	f, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(st, actual) {
		return out, ErrDenied
	}
	d := json.NewDecoder(io.LimitReader(f, 4097))
	d.DisallowUnknownFields()
	if d.Decode(&out) != nil || d.Decode(new(any)) != io.EOF || !out.valid() || out.RequestID != key {
		return RouteBinding{}, ErrInvalid
	}
	return out, nil
}
func (s *RouteStore) syncDirectory() error {
	f, err := os.Open(s.directory)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// Bind commits the choice before network dispatch. Exact retries are idempotent;
// any changed destination, caller certificate or task is a conflict. Link or
// sync failures are uncertain: preserve evidence and do not dispatch or delete.
func (s *RouteStore) Bind(b RouteBinding) error {
	if !b.valid() {
		return ErrInvalid
	}
	if err := s.check(); err != nil {
		return err
	}
	old, err := s.Lookup(b.RequestID)
	if err == nil {
		if old != b {
			return ErrConflict
		}
		return s.syncDirectory()
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	body, err := json.Marshal(b)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.directory, ".route-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(bytes.Clone(body), '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Link(f.Name(), s.path(b.RequestID)); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	old, err = s.Lookup(b.RequestID)
	if err != nil {
		return err
	}
	if old != b {
		return ErrConflict
	}
	return s.syncDirectory()
}
