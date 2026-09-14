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

// ApprovedInstallVerificationOutput is a durable create-only reservation. A
// zero-length file means native execution did not produce a committed receipt
// and must be investigated instead of retried in place.
type ApprovedInstallVerificationOutput struct {
	root     *os.Root
	file     *os.File
	name     string
	identity os.FileInfo
	used     bool
}

// PrepareApprovedInstallVerificationOutput reserves a private receipt before
// any native artifact executes. The destination must be outside protected roots.
func PrepareApprovedInstallVerificationOutput(path string, protectedRoots ...string) (*ApprovedInstallVerificationOutput, error) {
	if path == "" {
		return nil, ErrApprovedInstallVerification
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, ErrApprovedInstallVerification
	}
	parent, name := filepath.Dir(abs), filepath.Base(abs)
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 ||
		name == "." || name == string(filepath.Separator) {
		return nil, ErrApprovedInstallVerification
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return nil, ErrApprovedInstallVerification
	}
	abs = filepath.Join(realParent, name)
	for _, protected := range protectedRoots {
		real, evalErr := canonicalPublishedInstallProtectedPath(protected)
		rel, relErr := filepath.Rel(real, abs)
		if evalErr != nil || relErr != nil || rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, ErrApprovedInstallVerification
		}
	}
	root, err := os.OpenRoot(realParent)
	if err != nil {
		return nil, ErrApprovedInstallVerification
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) || actual.Mode().Perm()&0022 != 0 {
		root.Close()
		return nil, ErrApprovedInstallVerification
	}
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		root.Close()
		return nil, ErrApprovedInstallVerification
	}
	if err = file.Chmod(0600); err != nil || file.Sync() != nil || syncPublishedInstallRoot(root) != nil {
		file.Close()
		root.Close()
		return nil, ErrApprovedInstallVerification
	}
	identity, identityErr := file.Stat()
	named, namedErr := root.Lstat(name)
	if identityErr != nil || namedErr != nil || !identity.Mode().IsRegular() || identity.Mode().Perm() != 0600 ||
		identity.Size() != 0 || !os.SameFile(identity, named) {
		file.Close()
		root.Close()
		return nil, ErrApprovedInstallVerification
	}
	return &ApprovedInstallVerificationOutput{root: root, file: file, name: name, identity: identity}, nil
}

// Commit writes one canonical receipt into the existing reservation. Any
// failure preserves residue for operator inspection.
func (output *ApprovedInstallVerificationOutput) Commit(receipt ApprovedInstallVerificationReceipt) (string, error) {
	if output == nil || output.root == nil || output.file == nil || output.used {
		return "", ErrApprovedInstallVerification
	}
	output.used = true
	body, err := MarshalApprovedInstallVerificationReceipt(receipt)
	if err != nil {
		return "", ErrApprovedInstallVerification
	}
	info, err := output.file.Stat()
	named, namedErr := output.root.Lstat(output.name)
	if err != nil || namedErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != 0 ||
		!os.SameFile(output.identity, info) || !os.SameFile(output.identity, named) {
		return "", ErrApprovedInstallVerification
	}
	written, writeErr := output.file.Write(body)
	syncErr := output.file.Sync()
	openBody, openInfo, readErr := readApprovedInstallReservation(output.file, int64(len(body)))
	namedBody, namedInfo, namedReadErr := readNamedApprovedInstallReservation(output.root, output.name, int64(len(body)))
	closeErr := output.file.Close()
	output.file = nil
	if writeErr != nil || written != len(body) || syncErr != nil || readErr != nil || namedReadErr != nil || closeErr != nil ||
		!os.SameFile(output.identity, openInfo) || !os.SameFile(output.identity, namedInfo) ||
		openInfo.Mode().Perm() != 0600 || namedInfo.Mode().Perm() != 0600 ||
		!bytes.Equal(openBody, body) || !bytes.Equal(namedBody, body) || syncPublishedInstallRoot(output.root) != nil {
		return "", ErrApprovedInstallVerification
	}
	finalBody, finalInfo, finalErr := readNamedApprovedInstallReservation(output.root, output.name, int64(len(body)))
	if finalErr != nil || !os.SameFile(output.identity, finalInfo) || finalInfo.Mode().Perm() != 0600 || !bytes.Equal(finalBody, body) {
		return "", ErrApprovedInstallVerification
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func readApprovedInstallReservation(file *os.File, expected int64) ([]byte, os.FileInfo, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, nil, err
	}
	body, err := io.ReadAll(io.LimitReader(file, expected+1))
	if err != nil || int64(len(body)) != expected {
		return nil, nil, ErrApprovedInstallVerification
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != expected {
		return nil, nil, ErrApprovedInstallVerification
	}
	return body, info, nil
}

func readNamedApprovedInstallReservation(root *os.Root, name string, expected int64) ([]byte, os.FileInfo, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	body, info, readErr := readApprovedInstallReservation(file, expected)
	closeErr := file.Close()
	return body, info, errors.Join(readErr, closeErr)
}

// Close releases handles without deleting an incomplete reservation.
func (output *ApprovedInstallVerificationOutput) Close() error {
	if output == nil {
		return nil
	}
	var fileErr, rootErr error
	if output.file != nil {
		fileErr = output.file.Close()
		output.file = nil
	}
	if output.root != nil {
		rootErr = output.root.Close()
		output.root = nil
	}
	return errors.Join(fileErr, rootErr)
}

// VerifyApprovedInstallVerificationReceipt binds a canonical retained receipt
// to a digest obtained independently of the receipt file.
func VerifyApprovedInstallVerificationReceipt(path, expectedSHA256 string) (ApprovedInstallVerificationReceipt, error) {
	if !trustFingerprint(expectedSHA256) {
		return ApprovedInstallVerificationReceipt{}, ErrApprovedInstallVerification
	}
	body, err := readRollbackFile(path, 32<<10)
	if err != nil || rollbackDigest(body) != expectedSHA256 {
		return ApprovedInstallVerificationReceipt{}, ErrApprovedInstallVerification
	}
	receipt, err := ParseApprovedInstallVerificationReceipt(body)
	if err != nil {
		return ApprovedInstallVerificationReceipt{}, ErrApprovedInstallVerification
	}
	return receipt, nil
}
