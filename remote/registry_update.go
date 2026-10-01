package remote

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// Digest identifies a validated registry for a compare-and-swap update.
func (r Registry) Digest() string { return hash(r) }

// Replace is a local administrator operation. expected is the validated current
// digest, or "absent" for first registration. Concurrent cooperating writers
// fail closed. The lock is never stolen: after a crash an administrator must
// establish that no writer remains before removing it. Runtime readers never
// observe partially written policies. The containing directory must be private.
func (f TrustFile) Replace(next Registry, expected string) error {
	path := string(f)
	if !filepath.IsAbs(path) {
		return ErrInvalid
	}
	if expected != "absent" {
		b, e := hex.DecodeString(expected)
		if e != nil || len(b) != 32 {
			return ErrInvalid
		}
	}
	parent := filepath.Dir(path)
	st, e := os.Lstat(parent)
	if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 {
		return ErrDenied
	}
	lock := path + ".lock"
	if os.Mkdir(lock, 0700) != nil {
		return ErrConflict
	}
	defer os.Remove(lock)
	if expected == "absent" {
		if _, e := os.Lstat(path); !os.IsNotExist(e) {
			return ErrConflict
		}
	} else {
		old, e := f.Read()
		if e != nil || old.Digest() != expected {
			return ErrConflict
		}
	}
	// Validate the exact staged bytes through the same runtime reader.
	file, e := os.CreateTemp(parent, ".nexus-trust-*")
	if e != nil {
		return e
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	body, e := json.Marshal(next)
	if e == nil {
		_, e = file.Write(body)
	}
	if e == nil {
		e = file.Sync()
	}
	closeErr := file.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if _, e = TrustFile(tmp).Read(); e != nil {
		return e
	}
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	dir, e := os.Open(parent)
	if e != nil {
		return ErrUnavailable
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return ErrUnavailable
	}
	return nil
}
