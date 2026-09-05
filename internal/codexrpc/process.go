package codexrpc

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var (
	ErrProcessStart = errors.New("codex RPC process start failed")
	ErrProcessExit  = errors.New("codex RPC process exited unsuccessfully")
)

// ProcessSpec is trusted host input, never model-controlled configuration.
// It is transport plumbing, NOT a safe Codex launch policy: callers must first
// enforce privacy, tool isolation, supported CLI version and configuration.
// Env must be explicitly supplied; nil never inherits the host environment.
// No shell or PATH lookup is performed by this package.
type ProcessSpec struct {
	Executable string
	Args       []string
	Env        []string
	Dir        string
}

// Process owns one direct stdio subprocess and its pipe descriptors. It
// never forwards stderr, credentials, executable paths or exit diagnostics.
// It does NOT supervise descendants. A real launcher needs an additional
// containment boundary before enabling any runtime that can spawn children.
// This is not an OS sandbox or permission to run arbitrary model commands.
type Process struct {
	cmd      *exec.Cmd
	input    *os.File
	output   *os.File
	decoder  *Decoder
	encoder  *Encoder
	stopOnce sync.Once
	done     chan struct{}
	stopped  chan struct{}
	waitErr  error // written before done is closed
}

func validProcessSpec(s ProcessSpec) bool {
	if !filepath.IsAbs(s.Executable) || !filepath.IsAbs(s.Dir) || s.Env == nil || len(s.Args) > 256 || len(s.Env) > 256 {
		return false
	}
	remaining := 64 << 10
	add := func(value string) bool {
		if len(value) > remaining || strings.ContainsRune(value, 0) {
			return false
		}
		remaining -= len(value)
		return true
	}
	if !add(s.Executable) || !add(s.Dir) {
		return false
	}
	for _, arg := range s.Args {
		if !add(arg) {
			return false
		}
	}
	seen := make(map[string]bool, len(s.Env))
	for _, env := range s.Env {
		key, _, ok := strings.Cut(env, "=")
		if !add(env) || !ok || key == "" || seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

// StartProcess launches only after explicit caller admission. Construction is
// intentionally not hidden inside the existing HTTP Provider factory. Callers
// must defer Close immediately; context cancellation also stops the process.
func StartProcess(ctx context.Context, spec ProcessSpec) (*Process, error) {
	if ctx == nil || ctx.Err() != nil || !validProcessSpec(spec) {
		return nil, ErrProcessStart
	}
	cmd := exec.Command(spec.Executable, append([]string(nil), spec.Args...)...)
	cmd.Dir = spec.Dir
	cmd.Env = append([]string{}, spec.Env...)
	if !processPlatformSupported() {
		return nil, ErrProcessStart
	}
	// Own both ends ourselves: exec.Wait must not close stdout before buffered
	// frames are consumed, nor wait for stderr-copy goroutines held by children.
	childIn, input, err := os.Pipe()
	if err != nil {
		return nil, ErrProcessStart
	}
	defer childIn.Close()
	output, childOut, err := os.Pipe()
	if err != nil {
		_ = input.Close()
		return nil, ErrProcessStart
	}
	defer childOut.Close()
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, ErrProcessStart
	}
	defer devnull.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = childIn, childOut, devnull
	if ctx.Err() != nil || cmd.Start() != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, ErrProcessStart
	}
	p := &Process{cmd: cmd, input: input, output: output, decoder: NewDecoder(output, 0), encoder: NewEncoder(input, 0), done: make(chan struct{}), stopped: make(chan struct{})}
	go func() {
		if cmd.Wait() != nil {
			p.waitErr = ErrProcessExit
		}
		// Reap the leader once. Close deliberately owns transport shutdown so
		// callers may still drain already-written frames after normal exit.
		close(p.done)
	}()
	go func() {
		select {
		case <-ctx.Done():
			p.stop()
		case <-p.stopped:
		}
	}()
	if ctx.Err() != nil {
		_ = p.Close()
		return nil, ErrProcessStart
	}
	return p, nil
}

func (p *Process) stop() {
	p.stopOnce.Do(func() {
		// Use os.Process lifetime synchronization, not a raw numeric PID/group
		// signal that can race Wait and target a reused identifier.
		_ = p.cmd.Process.Kill()
		_ = p.input.Close()
		_ = p.output.Close()
		close(p.stopped)
	})
}

// Read has a single reader. EOF is transport termination, not task success.
// A malformed/failed stream is terminal; close immediately to release a peer
// blocked on a full pipe. Cancellation closes descriptors to unblock IO.
func (p *Process) Read() (Envelope, error) {
	select {
	case <-p.stopped:
		return Envelope{}, ErrRead
	default:
	}
	e, err := p.decoder.Read()
	if err != nil {
		p.stop()
		if err == io.EOF {
			return Envelope{}, io.EOF
		}
	}
	if err == nil {
		select {
		case <-p.stopped:
			return Envelope{}, ErrRead
		default:
		}
	}
	return e, err
}

func (p *Process) Write(e Envelope) error {
	err := p.encoder.Write(e)
	if err != nil {
		p.stop()
	}
	return err
}

// Close aborts and waits for reaping; it is safe to call concurrently/repeatedly.
// It does not claim graceful protocol completion or successful inference.
func (p *Process) Close() error {
	p.stop()
	<-p.done
	return nil
}

// Wait reports only OS exit status, never model/task completion.
func (p *Process) Wait() error { <-p.done; return p.waitErr }

// Done closes when the direct child is reaped. Always Close to release pipes.
func (p *Process) Done() <-chan struct{} { return p.done }
