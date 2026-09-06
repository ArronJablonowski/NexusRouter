package skills

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const maxFile = 8 << 20

// FileStore is a private local backend. SQLite/event-log integration is separate.
// One manifest replacement commits visibility; orphan draft files are harmless.
// Cross-process locking serializes mutations and is released on process exit.
type FileStore struct {
	root            *os.Root
	lock            *os.File
	mu              sync.Mutex
	automatic       atomic.Bool
	outcomeRollback atomic.Bool
	scopes          map[string]bool
	readOnly        bool
}

type catalog struct {
	Schema                  int                               `json:"schema"`
	Skills                  map[string]entry                  `json:"skills"`
	Publications            map[string]PublicationRecord      `json:"publications,omitempty"`
	RegressionOperations    map[string]RegressionOperation    `json:"regression_operations,omitempty"`
	RegressionMonitors      map[string]RegressionMonitorState `json:"regression_monitors,omitempty"`
	RegressionMonitorChecks map[string]RegressionMonitorCheck `json:"regression_monitor_checks,omitempty"`
	OutcomeOperations       map[string]OutcomeRollbackReceipt `json:"outcome_operations,omitempty"`
}
type entry struct {
	Key         Key                 `json:"key"`
	Active      string              `json:"active"`
	Versions    []Metadata          `json:"versions"`
	Validated   map[string]Evidence `json:"validated"`
	Activations []activation        `json:"activations"`
}

// ActivationRecord records one durable transition. Regression, when present,
// attributes an automatic rollback to a failed deterministic validator.
type ActivationRecord struct {
	From               string    `json:"from"`
	To                 string    `json:"to"`
	At                 time.Time `json:"at"`
	Rollback           bool      `json:"rollback"`
	Regression         *Evidence `json:"regression,omitempty"`
	OperationID        string    `json:"operation_id,omitempty"`
	BeforeRevision     string    `json:"before_revision,omitempty"`
	Evidence           *Evidence `json:"evidence,omitempty"`
	OutcomeOperationID string    `json:"outcome_operation_id,omitempty"`
}

type activation = ActivationRecord

// Open creates a private directory; existing symlink path components are refused.
// Automatic updates default off. Call SetAutomatic(true) only under host policy.
func Open(path string, scopes []string) (*FileStore, error) {
	return openStore(path, scopes, false)
}

func openStore(path string, scopes []string, readOnly bool) (*FileStore, error) {
	if path == "" {
		return nil, ErrInvalid
	}
	abs, err := filepath.Abs(path)
	if err != nil || len(scopes) == 0 || abs == filepath.Dir(abs) {
		return nil, ErrInvalid
	}
	allowed := map[string]bool{}
	for _, s := range scopes {
		if !identifier.MatchString(s) {
			return nil, ErrInvalid
		}
		allowed[s] = true
	}
	for p := abs; ; p = filepath.Dir(p) {
		st, e := os.Lstat(p)
		if e == nil && (!st.IsDir() || st.Mode()&os.ModeSymlink != 0) {
			return nil, ErrInvalid
		}
		if e != nil && !os.IsNotExist(e) {
			return nil, ErrInvalid
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	if st, e := os.Lstat(abs); e == nil {
		if st.Mode().Perm()&0077 != 0 {
			return nil, ErrInvalid
		}
	} else if os.IsNotExist(e) {
		if readOnly {
			return nil, ErrNotFound
		}
		if err = os.MkdirAll(abs, 0700); err != nil {
			return nil, err
		}
	} else {
		return nil, e
	}
	r, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	if readOnly {
		s := &FileStore{root: r, scopes: allowed, readOnly: true}
		if err = s.with(context.Background(), func(*catalog) error { return nil }, false); err != nil {
			s.Close()
			return nil, err
		}
		return s, nil
	}
	if st, e := r.Lstat("lock"); e == nil && !st.Mode().IsRegular() {
		r.Close()
		return nil, ErrInvalid
	}
	l, err := r.OpenFile("lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		r.Close()
		return nil, err
	}
	s := &FileStore{root: r, lock: l, scopes: allowed}
	if err = s.with(context.Background(), func(c *catalog) error { return nil }, false); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *FileStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return s.root.Close()
	}
	return errors.Join(s.lock.Close(), s.root.Close())
}
func (s *FileStore) SetAutomatic(enabled bool) { s.automatic.Store(enabled) }
func (s *FileStore) permitted(k Key) bool      { return k.valid() && s.scopes[k.Scope] }

func (s *FileStore) with(ctx context.Context, fn func(*catalog) error, write bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.readOnly && write {
		return ErrDisabled
	}
	if s.lock != nil {
		if err := lockFile(ctx, s.lock); err != nil {
			return err
		}
		defer unlockFile(s.lock)
	}
	c := catalog{Schema: 1, Skills: map[string]entry{}}
	if err := s.read("catalog.json", &c); err != nil && (s.readOnly || !os.IsNotExist(err)) {
		return err
	}
	if (c.Schema < 1 || c.Schema > 6) || c.Skills == nil || len(c.Skills) > 1000 {
		return ErrInvalid
	}
	for index, e := range c.Skills {
		if !e.Key.valid() || index != e.Key.index() || len(e.Versions) > 1000 || len(e.Activations) > 10000 {
			return ErrInvalid
		}
		known := map[string]bool{}
		for _, v := range e.Versions {
			if !versionID(v.Version) || v.Key != e.Key || known[v.Version] || len(v.Digest) != 64 {
				return ErrInvalid
			}
			known[v.Version] = true
		}
		for id, proof := range e.Validated {
			if !known[id] || !proof.Passed || !proof.Deterministic || !identifier.MatchString(proof.ID) {
				return ErrInvalid
			}
		}
		if e.Active != "" {
			if !known[e.Active] || !e.Validated[e.Active].Passed {
				return ErrInvalid
			}
		}
		if _, err := activationStack(e); err != nil {
			return err
		}
	}
	if err := validatePublications(&c); err != nil {
		return err
	}
	if err := validateActivationOperations(&c); err != nil {
		return err
	}
	if err := validateRegressionOperations(&c); err != nil {
		return err
	}
	if err := validateRegressionMonitors(&c); err != nil {
		return err
	}
	if err := validateOutcomeOperations(&c); err != nil {
		return err
	}
	if err := fn(&c); err != nil {
		if err == errCatalogUnchanged {
			return ctx.Err()
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if write {
		return s.write("catalog.json", c, false)
	}
	return nil
}

func (s *FileStore) read(name string, out any) error {
	st, err := s.root.Lstat(name)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Size() > maxFile {
		return ErrInvalid
	}
	f, err := s.root.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(st, actual) || !actual.Mode().IsRegular() {
		return ErrInvalid
	}
	b, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return err
	}
	if len(b) > maxFile {
		return ErrInvalid
	}
	if json.Unmarshal(b, out) != nil {
		return ErrInvalid
	}
	return nil
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func versionID(id string) bool {
	b, e := hex.DecodeString(id)
	return e == nil && len(b) == 16 && len(id) == 32
}

func digest(v Version) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s *FileStore) write(name string, value any, immutable bool) error {
	b, err := json.Marshal(value)
	if err != nil || len(b) > maxFile {
		return ErrInvalid
	}
	if st, e := s.root.Lstat(name); e == nil {
		if immutable || !st.Mode().IsRegular() {
			return ErrConflict
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	tmp := "tmp-" + randomID()
	f, err := s.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer s.root.Remove(tmp)
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = s.root.Rename(tmp, name); err != nil {
		return err
	}
	d, err := s.root.Open(".")
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (s *FileStore) Discover(ctx context.Context, scope string, tags []string, limit int) ([]Metadata, error) {
	if !s.scopes[scope] || limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	var result []Metadata
	err := s.with(ctx, func(c *catalog) error {
		for _, e := range c.Skills {
			if e.Key.Scope != scope || e.Active == "" {
				continue
			}
			for _, m := range e.Versions {
				if m.Version != e.Active {
					continue
				}
				match := len(tags) == 0
				for _, want := range tags {
					for _, tag := range m.Tags {
						if want == tag {
							match = true
						}
					}
				}
				if match {
					result = append(result, m)
				}
			}
		}
		sort.Slice(result, func(i, j int) bool { return result[i].Key.Name < result[j].Key.Name })
		if len(result) > limit {
			result = result[:limit]
		}
		return nil
	}, false)
	return result, err
}

func (s *FileStore) Load(ctx context.Context, key Key, id string) (Version, error) {
	var v Version
	if !s.permitted(key) || (id != "" && !versionID(id)) {
		return v, ErrInvalid
	}
	err := s.with(ctx, func(c *catalog) error {
		e, ok := c.Skills[key.index()]
		if !ok {
			return ErrNotFound
		}
		if id == "" {
			id = e.Active
		}
		found := false
		wantDigest := ""
		var metadata Metadata
		for _, m := range e.Versions {
			if m.Version == id {
				found = true
				wantDigest = m.Digest
				metadata = m
			}
		}
		if !found {
			return ErrNotFound
		}
		if err := s.read("version-"+id+".json", &v); err != nil {
			return err
		}
		if v.ID != id || v.Draft.Key != key || !v.Draft.valid() || digest(v) != wantDigest {
			return ErrInvalid
		}
		if metadata.Description != v.Draft.Description || len(metadata.Tags) != len(v.Draft.Tags) {
			return ErrInvalid
		}
		for i, tag := range metadata.Tags {
			if tag != v.Draft.Tags[i] {
				return ErrInvalid
			}
		}
		return nil
	}, false)
	return v, err
}

func (s *FileStore) Draft(ctx context.Context, d Draft, automatic bool) (Version, error) {
	var v Version
	if !s.permitted(d.Key) || !d.valid() {
		return v, ErrInvalid
	}
	if automatic && !s.automatic.Load() {
		return v, ErrDisabled
	}
	b, err := json.Marshal(d)
	if err != nil || len(b) > 256<<10 {
		return v, ErrInvalid
	}
	d = Draft{}
	json.Unmarshal(b, &d) // Own slices supplied by caller.
	err = s.with(ctx, func(c *catalog) error {
		if automatic && !s.automatic.Load() {
			return ErrDisabled
		}
		e := c.Skills[d.Key.index()]
		if len(e.Versions) >= 1000 {
			return ErrInvalid
		}
		if len(e.Versions) == 0 && len(c.Skills) >= 1000 {
			return ErrInvalid
		}
		e.Key = d.Key
		v = Version{ID: randomID(), Parent: e.Active, CreatedAt: time.Now().UTC(), Draft: d}
		if err := s.write("version-"+v.ID+".json", v, true); err != nil {
			return err
		}
		e.Versions = append(e.Versions, Metadata{Key: d.Key, Version: v.ID, Digest: digest(v), Description: d.Description, Tags: d.Tags})
		c.Skills[d.Key.index()] = e
		return nil
	}, true)
	return v, err
}
