package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
)

type storeOpenLock struct {
	token chan struct{}
	users int
}

var storeOpenLocks = struct {
	sync.Mutex
	paths map[string]*storeOpenLock
}{paths: map[string]*storeOpenLock{}}

// Open creates a private on-disk database. Callers must use a dedicated data
// directory; application configuration must never point into shared scratch.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" || path == ":memory:" {
		return nil, errors.New("on-disk database path required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	info, statErr := f.Stat()
	if err = f.Close(); err != nil {
		return nil, err
	}
	if statErr != nil {
		return nil, statErr
	}
	// Apply connection-local safety settings to every replacement connection.
	query := url.Values{}
	for _, pragma := range []string{"busy_timeout(5000)", "synchronous(FULL)", "foreign_keys(ON)"} {
		query.Add("_pragma", pragma)
	}
	dsn := url.URL{Scheme: "file", Path: abs, RawQuery: query.Encode()}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: abs, fileInfo: info}
	lockPath, err := filepath.EvalSymlinks(abs)
	if err != nil {
		db.Close()
		return nil, err
	}
	release, err := lockStoreOpenPath(ctx, lockPath)
	if err != nil {
		db.Close()
		return nil, err
	}
	defer release()
	if err = s.initialize(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err = s.ValidateIdentity(ctx, abs); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func lockStoreOpenPath(ctx context.Context, path string) (func(), error) {
	storeOpenLocks.Lock()
	entry := storeOpenLocks.paths[path]
	if entry == nil {
		entry = &storeOpenLock{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		storeOpenLocks.paths[path] = entry
	}
	entry.users++
	storeOpenLocks.Unlock()
	select {
	case <-ctx.Done():
		storeOpenLocks.Lock()
		entry.users--
		if entry.users == 0 {
			delete(storeOpenLocks.paths, path)
		}
		storeOpenLocks.Unlock()
		return nil, ctx.Err()
	case <-entry.token:
	}
	return func() {
		entry.token <- struct{}{}
		storeOpenLocks.Lock()
		entry.users--
		if entry.users == 0 {
			delete(storeOpenLocks.paths, path)
		}
		storeOpenLocks.Unlock()
	}, nil
}
