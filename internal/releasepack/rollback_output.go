package releasepack

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// RollbackEvidenceOutput is a create-only durable reservation. Residue is
// intentionally retained on failure so an interrupted ceremony is observable.
type RollbackEvidenceOutput struct {
	root     *os.Root
	file     *os.File
	name     string
	identity os.FileInfo
	used     bool
}

func PrepareRollbackEvidenceOutput(path string) (*RollbackEvidenceOutput, error) {
	if path == "" {
		return nil, ErrRollbackReadiness
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, ErrRollbackReadiness
	}
	parent, name := filepath.Dir(abs), filepath.Base(abs)
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 || name == "." {
		return nil, ErrRollbackReadiness
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return nil, ErrRollbackReadiness
	}
	root, err := os.OpenRoot(realParent)
	if err != nil {
		return nil, ErrRollbackReadiness
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) || actual.Mode().Perm()&0022 != 0 {
		root.Close()
		return nil, ErrRollbackReadiness
	}
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		root.Close()
		return nil, ErrRollbackReadiness
	}
	if file.Chmod(0600) != nil || file.Sync() != nil || syncPublishedInstallRoot(root) != nil {
		file.Close()
		root.Close()
		return nil, ErrRollbackReadiness
	}
	identity, e1 := file.Stat()
	named, e2 := root.Lstat(name)
	if e1 != nil || e2 != nil || !identity.Mode().IsRegular() || identity.Mode().Perm() != 0600 || identity.Size() != 0 || !os.SameFile(identity, named) {
		file.Close()
		root.Close()
		return nil, ErrRollbackReadiness
	}
	return &RollbackEvidenceOutput{root: root, file: file, name: name, identity: identity}, nil
}

func (o *RollbackEvidenceOutput) CommitCanonical(body []byte) (string, error) {
	if o == nil || o.root == nil || o.file == nil || o.used {
		return "", ErrRollbackReadiness
	}
	o.used = true
	if _, err := ParseRollbackReadiness(body); err != nil {
		if _, resultErr := ParseRollbackReadinessResult(body); resultErr != nil {
			return "", ErrRollbackReadiness
		}
	}
	info, e1 := o.file.Stat()
	named, e2 := o.root.Lstat(o.name)
	if e1 != nil || e2 != nil || info.Mode().Perm() != 0600 || info.Size() != 0 || !os.SameFile(o.identity, info) || !os.SameFile(o.identity, named) {
		return "", ErrRollbackReadiness
	}
	n, ew := o.file.Write(body)
	es := o.file.Sync()
	openBody, openInfo, er := readRollbackReservation(o.file, int64(len(body)))
	namedBody, namedInfo, enr := readNamedRollbackReservation(o.root, o.name, int64(len(body)))
	ec := o.file.Close()
	o.file = nil
	if ew != nil || n != len(body) || es != nil || er != nil || enr != nil || ec != nil || !os.SameFile(o.identity, openInfo) || !os.SameFile(o.identity, namedInfo) || !bytes.Equal(openBody, body) || !bytes.Equal(namedBody, body) || syncPublishedInstallRoot(o.root) != nil {
		return "", ErrRollbackReadiness
	}
	final, finalInfo, err := readNamedRollbackReservation(o.root, o.name, int64(len(body)))
	if err != nil || !os.SameFile(o.identity, finalInfo) || finalInfo.Mode().Perm() != 0600 || !bytes.Equal(final, body) {
		return "", ErrRollbackReadiness
	}
	return rollbackDigest(body), nil
}

func readRollbackReservation(file *os.File, expected int64) ([]byte, os.FileInfo, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, nil, err
	}
	body, err := io.ReadAll(io.LimitReader(file, expected+1))
	if err != nil || int64(len(body)) != expected {
		return nil, nil, ErrRollbackReadiness
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != expected {
		return nil, nil, ErrRollbackReadiness
	}
	return body, info, nil
}

func readNamedRollbackReservation(root *os.Root, name string, expected int64) ([]byte, os.FileInfo, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	body, info, readErr := readRollbackReservation(f, expected)
	return body, info, errors.Join(readErr, f.Close())
}

func (o *RollbackEvidenceOutput) Close() error {
	if o == nil {
		return nil
	}
	var a, b error
	if o.file != nil {
		a = o.file.Close()
		o.file = nil
	}
	if o.root != nil {
		b = o.root.Close()
		o.root = nil
	}
	return errors.Join(a, b)
}

func WriteRollbackReadiness(path string, record RollbackReadiness) (string, error) {
	body, err := MarshalRollbackReadiness(record)
	if err != nil {
		return "", err
	}
	out, err := PrepareRollbackEvidenceOutput(path)
	if err != nil {
		return "", err
	}
	defer out.Close()
	return out.CommitCanonical(body)
}
