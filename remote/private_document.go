package remote

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
)

func privateDocumentParent(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", ErrInvalid
	}
	parent := filepath.Dir(path)
	st, err := os.Lstat(parent)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 {
		return "", ErrDenied
	}
	return parent, nil
}
func syncPrivateDocumentParent(path string) error {
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
func readPrivateDocument(path string, limit int, sync bool) ([]byte, error) {
	if _, err := privateDocumentParent(path); err != nil {
		return nil, err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > int64(limit) {
		return nil, ErrDenied
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(st, actual) {
		return nil, ErrDenied
	}
	body, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > limit {
		return nil, ErrInvalid
	}
	if sync {
		if err = f.Sync(); err != nil {
			return nil, err
		}
		if err = syncPrivateDocumentParent(path); err != nil {
			return nil, err
		}
	}
	return body, nil
}
func publishPrivateDocument(path string, body []byte, limit int) error {
	if limit <= 0 || len(body) > limit {
		return ErrInvalid
	}
	parent, err := privateDocumentParent(path)
	if err != nil {
		return err
	}
	compare := func() error {
		saved, err := readPrivateDocument(path, limit, true)
		if err != nil {
			return err
		}
		if !bytes.Equal(saved, body) {
			return ErrConflict
		}
		return nil
	}
	if err = compare(); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(parent, ".nexus-record-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(body); err != nil {
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
	if err = os.Link(f.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return compare()
}
