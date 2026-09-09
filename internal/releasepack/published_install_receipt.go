package releasepack

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// PublishedInstallEvidenceOutput is a durable, create-only reservation for a
// published-install evidence record. A zero-length reservation means the
// side-effecting rehearsal did not produce a complete retained record and must
// be investigated rather than retried in place.
type PublishedInstallEvidenceOutput struct {
	root     *os.Root
	file     *os.File
	name     string
	identity os.FileInfo
	used     bool
}

// PreparePublishedInstallEvidenceOutput validates and durably reserves an
// evidence destination before any downloaded native code is executed.
func PreparePublishedInstallEvidenceOutput(path string, protectedRoots ...string) (*PublishedInstallEvidenceOutput, error) {
	if path == "" {
		return nil, ErrPublishedInstallEvidence
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, ErrPublishedInstallEvidence
	}
	parent, name := filepath.Dir(abs), filepath.Base(abs)
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 || name == "." || name == string(filepath.Separator) {
		return nil, ErrPublishedInstallEvidence
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return nil, ErrPublishedInstallEvidence
	}
	abs = filepath.Join(realParent, name)
	for _, protected := range protectedRoots {
		real, evalErr := canonicalPublishedInstallProtectedPath(protected)
		rel, relErr := filepath.Rel(real, abs)
		if evalErr != nil || relErr != nil || rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, ErrPublishedInstallEvidence
		}
	}
	root, err := os.OpenRoot(realParent)
	if err != nil {
		return nil, ErrPublishedInstallEvidence
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) || actual.Mode().Perm()&0022 != 0 {
		root.Close()
		return nil, ErrPublishedInstallEvidence
	}
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		root.Close()
		return nil, ErrPublishedInstallEvidence
	}
	if err = file.Chmod(0600); err != nil || file.Sync() != nil || syncPublishedInstallRoot(root) != nil {
		file.Close()
		root.Close()
		return nil, ErrPublishedInstallEvidence
	}
	identity, identityErr := file.Stat()
	named, namedErr := root.Lstat(name)
	if identityErr != nil || namedErr != nil || !identity.Mode().IsRegular() || identity.Mode().Perm() != 0600 || identity.Size() != 0 ||
		!os.SameFile(identity, named) {
		file.Close()
		root.Close()
		return nil, ErrPublishedInstallEvidence
	}
	return &PublishedInstallEvidenceOutput{root: root, file: file, name: name, identity: identity}, nil
}

func canonicalPublishedInstallProtectedPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if real, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
		return real, nil
	}
	realParent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	return filepath.Join(realParent, filepath.Base(abs)), nil
}

// Commit writes canonical evidence into the already durable reservation. It
// may be called once; any error leaves residue that must be inspected.
func (o *PublishedInstallEvidenceOutput) Commit(evidence PublishedInstallEvidence) (string, error) {
	if o == nil || o.root == nil || o.file == nil || o.used {
		return "", ErrPublishedInstallEvidence
	}
	o.used = true
	body, err := MarshalPublishedInstallEvidence(evidence)
	if err != nil {
		return "", ErrPublishedInstallEvidence
	}
	info, err := o.file.Stat()
	named, namedErr := o.root.Lstat(o.name)
	if err != nil || namedErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != 0 ||
		!os.SameFile(o.identity, info) || !os.SameFile(o.identity, named) {
		return "", ErrPublishedInstallEvidence
	}
	written, writeErr := o.file.Write(body)
	syncErr := o.file.Sync()
	openBody, openInfo, readErr := readPublishedInstallReservation(o.file, int64(len(body)))
	namedBody, namedInfo, namedReadErr := readNamedPublishedInstallReservation(o.root, o.name, int64(len(body)))
	closeErr := o.file.Close()
	o.file = nil
	if writeErr != nil || written != len(body) || syncErr != nil || readErr != nil || namedReadErr != nil || closeErr != nil ||
		!os.SameFile(o.identity, openInfo) || !os.SameFile(o.identity, namedInfo) ||
		openInfo.Mode().Perm() != 0600 || namedInfo.Mode().Perm() != 0600 ||
		!bytes.Equal(openBody, body) || !bytes.Equal(namedBody, body) || syncPublishedInstallRoot(o.root) != nil {
		return "", ErrPublishedInstallEvidence
	}
	finalBody, finalInfo, finalErr := readNamedPublishedInstallReservation(o.root, o.name, int64(len(body)))
	if finalErr != nil || !os.SameFile(o.identity, finalInfo) || finalInfo.Mode().Perm() != 0600 || !bytes.Equal(finalBody, body) {
		return "", ErrPublishedInstallEvidence
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func readPublishedInstallReservation(file *os.File, expected int64) ([]byte, os.FileInfo, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, nil, err
	}
	body, err := io.ReadAll(io.LimitReader(file, expected+1))
	if err != nil || int64(len(body)) != expected {
		return nil, nil, ErrPublishedInstallEvidence
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != expected {
		return nil, nil, ErrPublishedInstallEvidence
	}
	return body, info, nil
}

func readNamedPublishedInstallReservation(root *os.Root, name string, expected int64) ([]byte, os.FileInfo, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	body, info, readErr := readPublishedInstallReservation(file, expected)
	closeErr := file.Close()
	return body, info, errors.Join(readErr, closeErr)
}

// Close releases reservation handles. It deliberately does not remove an
// incomplete record, because its presence is evidence of an interrupted or
// uncertain ceremony.
func (o *PublishedInstallEvidenceOutput) Close() error {
	if o == nil {
		return nil
	}
	var fileErr, rootErr error
	if o.file != nil {
		fileErr = o.file.Close()
		o.file = nil
	}
	if o.root != nil {
		rootErr = o.root.Close()
		o.root = nil
	}
	return errors.Join(fileErr, rootErr)
}

// WritePublishedInstallEvidence retains one canonical result with exclusive,
// durable creation. The output must be outside each protected input root.
func WritePublishedInstallEvidence(path string, evidence PublishedInstallEvidence, protectedRoots ...string) (string, error) {
	if _, err := MarshalPublishedInstallEvidence(evidence); err != nil {
		return "", ErrPublishedInstallEvidence
	}
	output, err := PreparePublishedInstallEvidenceOutput(path, protectedRoots...)
	if err != nil {
		return "", err
	}
	defer output.Close()
	return output.Commit(evidence)
}

// VerifyPublishedInstallReceipt binds a retained canonical record to an exact
// digest obtained through an independent evidence channel.
func VerifyPublishedInstallReceipt(path, expectedSHA256 string) (PublishedInstallEvidence, error) {
	if !trustFingerprint(expectedSHA256) {
		return PublishedInstallEvidence{}, ErrPublishedInstallEvidence
	}
	body, err := readRollbackFile(path, 32<<10)
	if err != nil || rollbackDigest(body) != expectedSHA256 {
		return PublishedInstallEvidence{}, ErrPublishedInstallEvidence
	}
	evidence, err := ParsePublishedInstallEvidence(body)
	if err != nil {
		return PublishedInstallEvidence{}, ErrPublishedInstallEvidence
	}
	return evidence, nil
}
