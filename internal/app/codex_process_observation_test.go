//go:build darwin || linux

package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const codexProcessSnapshotLimit = 2 << 20
const codexProcessLineLimit = 4096
const codexProcessCountLimit = 32768

var errCodexProcessObservation = errors.New("process observation unavailable or invalid")

type codexObservedProcess struct {
	PID, Parent int
	Command     string
}

// The cap is applied while os/exec copies stdout, not after Output has already
// accumulated unbounded data. This helper captures command names, never argv.
type codexProcessCapture struct{ buffer bytes.Buffer }

func (b *codexProcessCapture) Len() int      { return b.buffer.Len() }
func (b *codexProcessCapture) Bytes() []byte { return b.buffer.Bytes() }

func (b *codexProcessCapture) Write(p []byte) (int, error) {
	if len(p) > codexProcessSnapshotLimit-b.Len() {
		return 0, errCodexProcessObservation
	}
	return b.buffer.Write(p)
}

func captureCodexProcesses(ctx context.Context) (map[int]codexObservedProcess, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, errCodexProcessObservation
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,ppid=,comm=")
	var output codexProcessCapture
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	if cmd.Run() != nil {
		return nil, errCodexProcessObservation
	}
	return parseCodexProcesses(output.Bytes())
}

func parseCodexProcesses(raw []byte) (map[int]codexObservedProcess, error) {
	if len(raw) == 0 || len(raw) > codexProcessSnapshotLimit || !utf8.Valid(raw) {
		return nil, errCodexProcessObservation
	}
	result := make(map[int]codexObservedProcess)
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 1024), codexProcessLineLimit+1)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) > codexProcessLineLimit || len(result) >= codexProcessCountLimit {
			return nil, errCodexProcessObservation
		}
		number := func() (int, bool) {
			line = strings.TrimLeft(line, " \t")
			i := 0
			for i < len(line) && line[i] >= '0' && line[i] <= '9' {
				i++
			}
			if i == 0 || i == len(line) || (line[i] != ' ' && line[i] != '\t') {
				return 0, false
			}
			v, err := strconv.ParseUint(line[:i], 10, 31)
			line = line[i:]
			return int(v), err == nil
		}
		pid, ok := number()
		if !ok || pid <= 0 {
			return nil, errCodexProcessObservation
		}
		parent, ok := number()
		if !ok || pid == parent {
			return nil, errCodexProcessObservation
		}
		command := strings.TrimLeft(line, " \t")
		if command == "" || strings.TrimSpace(command) == "" {
			return nil, errCodexProcessObservation
		}
		for _, c := range command {
			if c < 32 || c == 127 {
				return nil, errCodexProcessObservation
			}
		}
		if _, exists := result[pid]; exists {
			return nil, errCodexProcessObservation
		}
		result[pid] = codexObservedProcess{PID: pid, Parent: parent, Command: command}
	}
	if scanner.Err() != nil || len(result) == 0 {
		return nil, errCodexProcessObservation
	}
	return result, nil
}

// Descendants are snapshot ancestry, not proof of continuing ownership. This
// read-only helper never signals processes and never treats PID reuse as safe
// authorization to kill one. Root itself is deliberately excluded.
func codexDescendants(snapshot map[int]codexObservedProcess, rootPID int) map[int]codexObservedProcess {
	result := make(map[int]codexObservedProcess)
	if rootPID <= 0 {
		return result
	}
	children := make(map[int][]codexObservedProcess)
	for id, p := range snapshot {
		if id == p.PID && p.PID > 0 && p.PID != rootPID {
			children[p.Parent] = append(children[p.Parent], p)
		}
	}
	queue := []int{rootPID}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, p := range children[parent] {
			if _, seen := result[p.PID]; seen {
				continue
			}
			result[p.PID] = p
			queue = append(queue, p.PID)
		}
	}
	return result
}

func TestCodexProcessObservationPreservesCommandSpaces(t *testing.T) {
	p, err := parseCodexProcesses([]byte("    1 0 /sbin/launchd\n  20   1 /Applications/Codex App.app/Contents/MacOS/Codex\n21\t20\t/fixture/code-mode host\n"))
	if err != nil || len(p) != 3 || p[20].Command != "/Applications/Codex App.app/Contents/MacOS/Codex" || p[21].Parent != 20 {
		t.Fatal(p, err)
	}
}

func TestCodexProcessObservationRejectsInvalidSnapshots(t *testing.T) {
	for name, raw := range map[string]string{
		"empty": "", "blank": "\n", "missing command": "1 0 ", "missing parent": "1 command", "negative": "-1 0 command", "zero": "0 1 command", "self parent": "1 1 command", "overflow": "999999999999 1 command", "invalid parent": "2 x command", "duplicate": "1 0 first\n1 0 second\n", "control": "1 0 cmd\x00name", "invalid utf8": "1 0 \xff", "long line": "1 0 " + strings.Repeat("x", codexProcessLineLimit), "too many lines": strings.Repeat("1 0 command\n", codexProcessCountLimit+1), "oversized": strings.Repeat("x", codexProcessSnapshotLimit+1),
	} {
		t.Run(name, func(t *testing.T) {
			if out, err := parseCodexProcesses([]byte(raw)); err == nil || out != nil {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
}

func TestCodexProcessCaptureBoundsWrites(t *testing.T) {
	var b codexProcessCapture
	if n, err := b.Write(bytes.Repeat([]byte("x"), codexProcessSnapshotLimit)); err != nil || n != codexProcessSnapshotLimit {
		t.Fatal(n, err)
	}
	if n, err := b.Write([]byte("x")); err == nil || n != 0 || b.Len() != codexProcessSnapshotLimit {
		t.Fatal("capture exceeded byte ceiling")
	}
	// Do not expose bytes.Buffer.ReadFrom: io.Copy would otherwise bypass the
	// bounded Write method through its ReaderFrom optimization.
	if _, ok := any(&b).(io.ReaderFrom); ok {
		t.Fatal("capture exposes an unbounded copy path")
	}
}

func TestCodexProcessObservationBoundsUniqueLines(t *testing.T) {
	var input strings.Builder
	for i := 1; i <= codexProcessCountLimit+1; i++ {
		input.WriteString(strconv.Itoa(i) + " 0 command\n")
	}
	if out, err := parseCodexProcesses([]byte(input.String())); err == nil || out != nil {
		t.Fatal("line bound accepted excess unique processes")
	}
}

func TestCodexProcessDescendantsAreTransitiveAndExcludeRoot(t *testing.T) {
	p := map[int]codexObservedProcess{1: {PID: 1, Parent: 0}, 10: {PID: 10, Parent: 1}, 11: {PID: 11, Parent: 10}, 12: {PID: 12, Parent: 11}, 13: {PID: 13, Parent: 10}, 20: {PID: 20, Parent: 1}, 30: {PID: 30, Parent: 31}, 31: {PID: 31, Parent: 30}}
	d := codexDescendants(p, 10)
	if len(d) != 3 || d[11].PID != 11 || d[12].PID != 12 || d[13].PID != 13 {
		t.Fatal(d)
	}
	if len(codexDescendants(p, 0)) != 0 || len(codexDescendants(p, 99)) != 0 {
		t.Fatal("unrelated ancestry included")
	}
	if len(codexDescendants(p, 30)) != 1 {
		t.Fatal("cycle did not terminate or included root")
	}
	delete(p, 10)
	if len(codexDescendants(p, 10)) != 3 {
		t.Fatal("missing observed root lost descendants")
	}
}
