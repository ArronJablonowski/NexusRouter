package releasepack

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// WritePostPublicationReceipt creates one canonical receipt without replacing
// an existing path. The output must be outside every supplied protected root.
func WritePostPublicationReceipt(path string, receipt PostPublicationReceipt, protectedRoots ...string) (string, error) {
	body, err := MarshalPostPublicationReceipt(receipt)
	if err != nil || path == "" {
		return "", ErrPublicationAuthorization
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", ErrPublicationAuthorization
	}
	parent, name := filepath.Dir(abs), filepath.Base(abs)
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || name == "." || name == string(filepath.Separator) {
		return "", ErrPublicationAuthorization
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", ErrPublicationAuthorization
	}
	abs = filepath.Join(realParent, name)
	for _, protected := range protectedRoots {
		real, evalErr := filepath.EvalSymlinks(protected)
		rel, relErr := filepath.Rel(real, abs)
		if evalErr != nil || relErr != nil || rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", ErrPublicationAuthorization
		}
	}
	root, err := os.OpenRoot(realParent)
	if err != nil {
		return "", ErrPublicationAuthorization
	}
	defer root.Close()
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		return "", ErrPublicationAuthorization
	}
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return "", ErrPublicationAuthorization
	}
	_, writeErr := file.Write(body)
	syncErr, closeErr := file.Sync(), file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return "", ErrPublicationAuthorization
	}
	directory, err := root.Open(".")
	if err != nil {
		return "", ErrPublicationAuthorization
	}
	dirSyncErr, dirCloseErr := directory.Sync(), directory.Close()
	if dirSyncErr != nil || dirCloseErr != nil {
		return "", ErrPublicationAuthorization
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
