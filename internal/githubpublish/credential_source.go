package githubpublish

import (
	"bytes"
	"context"
	"io"
	"os"
	"sync"
)

// FDCredentialSource reads one GitHub release credential from an already-open
// file descriptor. It is deliberately single-use and never accepts a token in
// process arguments. Regular files must be private to their owner; pipes and
// sockets are also accepted so an operator can stream a credential from a
// secret manager.
type FDCredentialSource struct {
	mu   sync.Mutex
	file *os.File
	used bool
}

// NewFDCredentialSource constructs a release-publisher credential source and
// takes ownership of the descriptor. Reading and closing are deferred until the
// publisher has completed its approval-bound offline preflight.
func NewFDCredentialSource(fd int) (*FDCredentialSource, error) {
	if fd < 0 {
		return nil, ErrCredentialTransport
	}
	file := os.NewFile(uintptr(fd), "darwinrouter-github-credential")
	if file == nil {
		return nil, ErrCredentialTransport
	}
	info, err := file.Stat()
	if err != nil || !secureCredentialDescriptor(info) {
		_ = file.Close()
		return nil, ErrCredentialTransport
	}
	return &FDCredentialSource{file: file}, nil
}

func (s *FDCredentialSource) GitHubCredential(ctx context.Context) (Credential, error) {
	if s == nil {
		return Credential{}, ErrCredentialTransport
	}
	s.mu.Lock()
	if s.used || s.file == nil {
		s.mu.Unlock()
		return Credential{}, ErrCredentialTransport
	}
	s.used = true
	file := s.file
	s.file = nil
	s.mu.Unlock()
	defer file.Close()
	if ctx == nil || ctx.Err() != nil {
		return Credential{}, ErrCredentialTransport
	}
	info, err := file.Stat()
	if err != nil || !secureCredentialDescriptor(info) {
		return Credential{}, ErrCredentialTransport
	}
	body, err := readCredential(ctx, file)
	if err != nil || ctx.Err() != nil || len(body) == 0 || len(body) > 4096 {
		clear(body)
		return Credential{}, ErrCredentialTransport
	}
	body = bytes.TrimSuffix(body, []byte{'\n'})
	body = bytes.TrimSuffix(body, []byte{'\r'})
	if !validCredentialToken(body) {
		clear(body)
		return Credential{}, ErrCredentialTransport
	}
	return Credential{Token: body, ContentsWrite: true, AdministrationRead: true}, nil
}

func secureCredentialDescriptor(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	mode := info.Mode()
	if mode.IsRegular() {
		return mode.Perm()&0o077 == 0
	}
	return mode&os.ModeNamedPipe != 0 || mode&os.ModeSocket != 0
}

func readCredential(ctx context.Context, file *os.File) ([]byte, error) {
	type result struct {
		body []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		body, err := io.ReadAll(io.LimitReader(file, 4097))
		done <- result{body: body, err: err}
	}()
	select {
	case value := <-done:
		return value.body, value.err
	case <-ctx.Done():
		_ = file.Close()
		value := <-done
		clear(value.body)
		return nil, ErrCredentialTransport
	}
}
