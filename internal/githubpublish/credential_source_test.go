package githubpublish

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestFDCredentialSourceReadsPrivateSingleUseToken(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = read.Close() })
	if _, err = write.WriteString("github_pat_operator-secret\n"); err != nil || write.Close() != nil {
		t.Fatal(err)
	}
	source, err := NewFDCredentialSource(int(read.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := source.GitHubCredential(t.Context())
	if err != nil || string(credential.Token) != "github_pat_operator-secret" || !credential.ContentsWrite || !credential.AdministrationRead {
		t.Fatal("credential source rejected valid bounded input")
	}
	if _, err = read.Stat(); err != nil {
		t.Fatal("credential consumption closed caller descriptor", err)
	}
	clear(credential.Token)
	if _, err = source.GitHubCredential(t.Context()); !errors.Is(err, ErrCredentialTransport) || strings.Contains(err.Error(), "operator-secret") {
		t.Fatal("credential source was reusable or leaked input", err)
	}
}

func TestFDCredentialSourceOwnsDuplicateNotCallerDescriptor(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewFDCredentialSource(int(read.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if err = read.Close(); err != nil {
		t.Fatal("constructor closed caller descriptor", err)
	}
	if _, err = write.WriteString("github_pat_duplicate-owner\n"); err != nil || write.Close() != nil {
		t.Fatal(err)
	}
	credential, err := source.GitHubCredential(t.Context())
	if err != nil || string(credential.Token) != "github_pat_duplicate-owner" {
		t.Fatal("owned duplicate did not survive caller close", err)
	}
	clear(credential.Token)
}

func TestFDCredentialSourceRejectsUnsafeInput(t *testing.T) {
	for name, value := range map[string]string{
		"empty":     "",
		"newline":   "token\nsecond\n",
		"oversized": strings.Repeat("x", 4097),
	} {
		t.Run(name, func(t *testing.T) {
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer read.Close()
			if _, err = write.WriteString(value); err != nil || write.Close() != nil {
				t.Fatal(err)
			}
			source, err := NewFDCredentialSource(int(read.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = source.GitHubCredential(t.Context()); !errors.Is(err, ErrCredentialTransport) || value != "" && strings.Contains(err.Error(), value) {
				t.Fatal("unsafe credential accepted or leaked", err)
			}
		})
	}
}

func TestFDCredentialSourceRejectsCanceledAndPublicRegularFile(t *testing.T) {
	path := t.TempDir() + "/credential"
	if err := os.WriteFile(path, []byte("token"), 0o644); err != nil {
		t.Fatal(err)
	}
	// WriteFile applies the process umask. Force the deliberately unsafe mode so
	// this fixture remains public when the test suite runs under umask 077.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err = NewFDCredentialSource(int(file.Fd())); !errors.Is(err, ErrCredentialTransport) {
		t.Fatal("public regular credential file accepted", err)
	}
	if _, err = file.Stat(); err != nil {
		t.Fatal("constructor rejection closed caller descriptor", err)
	}

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	if _, err = write.WriteString("token"); err != nil || write.Close() != nil {
		t.Fatal(err)
	}
	source, err := NewFDCredentialSource(int(read.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = source.GitHubCredential(ctx); !errors.Is(err, ErrCredentialTransport) {
		t.Fatal("canceled credential read accepted", err)
	}
}

func TestFDCredentialSourceCancellationInterruptsPipeRead(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer write.Close()
	source, err := NewFDCredentialSource(int(read.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, readErr := source.GitHubCredential(ctx)
		done <- readErr
	}()
	cancel()
	select {
	case err = <-done:
		if !errors.Is(err, ErrCredentialTransport) {
			t.Fatal("canceled pipe read did not fail closed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled credential pipe remained blocked")
	}
}

func TestFDCredentialSourceRechecksPrivateFileBeforeRead(t *testing.T) {
	path := t.TempDir() + "/credential"
	if err := os.WriteFile(path, []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewFDCredentialSource(int(file.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = source.GitHubCredential(t.Context()); !errors.Is(err, ErrCredentialTransport) {
		t.Fatal("credential file made public after construction was accepted", err)
	}
}

func TestFDCredentialSourceRejectsCharacterDevice(t *testing.T) {
	device, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewFDCredentialSource(int(device.Fd())); !errors.Is(err, ErrCredentialTransport) {
		t.Fatal("character-device credential input accepted", err)
	}
	if _, err = device.Stat(); err != nil {
		t.Fatal("constructor rejection closed caller descriptor", err)
	}
}
