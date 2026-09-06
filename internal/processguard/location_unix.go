//go:build darwin || linux

package processguard

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Resolve only before creating the process singleton. Existing references remain
// independently probeable wherever their original guard was stored.
func ownerDirectory() (string, error) {
	path, explicit := os.LookupEnv("DARWIN_PROCESS_OWNER_DIR")
	if !explicit {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", ErrUnavailable
		}
		path = filepath.Join(base, "DarwinRouter", "process-owners")
	}
	if path == "" || len(path) > 4000 || !utf8.ValidString(path) || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.TrimSpace(path) != path || strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return "", ErrUnavailable
	}
	// Do not repair existing nodes. Canonical ancestor aliases (such as macOS
	// /var) are allowed, but the selected private root itself cannot be a link.
	if info, err := os.Lstat(path); err == nil {
		if !privateNode(info, true) {
			return "", ErrUnavailable
		}
	} else if !os.IsNotExist(err) {
		return "", ErrUnavailable
	} else if err = createOwnerParents(path); err != nil {
		return "", ErrUnavailable
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", ErrUnavailable
	}
	original, err := os.Lstat(path)
	if err != nil || !privateNode(original, true) {
		return "", ErrUnavailable
	}
	resolved, err := os.Lstat(canonical)
	if err != nil || !privateNode(resolved, true) || identity(original) != identity(resolved) {
		return "", ErrUnavailable
	}
	return canonical, nil
}

func createOwnerParents(path string) error {
	var missing []string
	ancestor := path
	for {
		if _, err := os.Stat(ancestor); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return ErrUnavailable
		}
		missing = append(missing, filepath.Base(ancestor))
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return ErrUnavailable
		}
		ancestor = parent
	}
	root, err := os.OpenRoot(ancestor)
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = root.Close() }()
	for i := len(missing) - 1; i >= 0; i-- {
		name := missing[i]
		if err = root.Mkdir(name, 0700); err != nil && !os.IsExist(err) {
			return ErrUnavailable
		}
		info, err := root.Lstat(name)
		if err != nil || !privateNode(info, true) {
			return ErrUnavailable
		}
		child, err := root.OpenRoot(name)
		if err != nil {
			return ErrUnavailable
		}
		pinned, statErr := child.Stat(".")
		if statErr != nil || identity(info) != identity(pinned) || syncDirectory(child) != nil || syncDirectory(root) != nil {
			_ = child.Close()
			return ErrUnavailable
		}
		_ = root.Close()
		root = child
	}
	return nil
}

func syncDirectory(root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	err = file.Sync()
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
