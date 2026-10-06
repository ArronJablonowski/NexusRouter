// Package processaudit records direct subprocess launches without changing their
// command lines, environments, pipe ownership or exit semantics.
package processaudit

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

var state struct {
	sync.Mutex
	directory string
	commands  map[*exec.Cmd]string
}
var ErrAudit = errors.New("subprocess audit unavailable")

// Enable configures process-wide auditing before the executable accepts work.
// Library consumers may enable the same audit explicitly.
func Enable(directory string) error {
	if !filepath.IsAbs(directory) {
		return ErrAudit
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return ErrAudit
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return ErrAudit
	}
	state.Lock()
	defer state.Unlock()
	state.directory = directory
	state.commands = make(map[*exec.Cmd]string)
	return nil
}

type record struct {
	Version    int       `json:"version"`
	Time       time.Time `json:"time"`
	Host       string    `json:"host"`
	LaunchID   string    `json:"launch_id"`
	Event      string    `json:"event"`
	ParentPID  int       `json:"parent_pid"`
	PID        int       `json:"pid,omitempty"`
	Executable string    `json:"executable"`
	Args       []string  `json:"argv"`
	Directory  string    `json:"working_directory,omitempty"`
	ExitCode   *int      `json:"exit_code,omitempty"`
	Success    bool      `json:"success"`
}

// write is called with state locked. No raw error, environment, input or output
// content is copied into the audit.
func write(cmd *exec.Cmd, id, event string, success bool) error {
	host, _ := os.Hostname()
	r := record{Version: 1, Time: time.Now().UTC(), Host: host, LaunchID: id, Event: event, ParentPID: os.Getpid(), Executable: cmd.Path, Args: Redact(cmd.Args), Directory: cmd.Dir, Success: success}
	if cmd.Process != nil {
		r.PID = cmd.Process.Pid
	}
	if cmd.ProcessState != nil {
		code := cmd.ProcessState.ExitCode()
		r.ExitCode = &code
	}
	root, err := os.OpenRoot(state.directory)
	if err != nil {
		return ErrAudit
	}
	defer root.Close()
	name := "process-" + r.Time.Format("2006-01-02") + ".jsonl"
	if st, e := root.Lstat(name); e == nil && (!st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0) {
		return ErrAudit
	}
	f, err := root.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return ErrAudit
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return ErrAudit
	}
	if json.NewEncoder(f).Encode(r) != nil || f.Sync() != nil {
		return ErrAudit
	}
	return nil
}
func begin(cmd *exec.Cmd) (string, error) {
	state.Lock()
	defer state.Unlock()
	if state.directory == "" {
		return "", nil
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", ErrAudit
	}
	id := hex.EncodeToString(b[:])
	if err := write(cmd, id, "launch_requested", false); err != nil {
		return "", err
	}
	state.commands[cmd] = id
	return id, nil
}
func event(cmd *exec.Cmd, name string, success, terminal bool) {
	state.Lock()
	defer state.Unlock()
	id := state.commands[cmd]
	if id == "" {
		return
	}
	if write(cmd, id, name, success) != nil {
		fmt.Fprintln(os.Stderr, "NexusRouter: subprocess audit write failed")
	}
	if terminal {
		delete(state.commands, cmd)
	}
}
func Start(cmd *exec.Cmd) error {
	if _, err := begin(cmd); err != nil {
		return err
	}
	err := cmd.Start()
	if err != nil {
		event(cmd, "launch_failed", false, true)
	} else {
		event(cmd, "started", true, false)
	}
	return err
}
func Wait(cmd *exec.Cmd) error { err := cmd.Wait(); event(cmd, "exited", err == nil, true); return err }
func Run(cmd *exec.Cmd) error {
	if err := Start(cmd); err != nil {
		return err
	}
	return Wait(cmd)
}

// Output and CombinedOutput retain os/exec's exact buffering and error behavior.
// Their launch/exit records bracket execution; PID becomes available at exit.
func Output(cmd *exec.Cmd) ([]byte, error) {
	if _, err := begin(cmd); err != nil {
		return nil, err
	}
	b, err := cmd.Output()
	finish(cmd, err)
	return b, err
}
func CombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	if _, err := begin(cmd); err != nil {
		return nil, err
	}
	b, err := cmd.CombinedOutput()
	finish(cmd, err)
	return b, err
}
func finish(cmd *exec.Cmd, err error) {
	name := "exited"
	if cmd.Process == nil {
		name = "launch_failed"
	}
	event(cmd, name, err == nil, true)
}
