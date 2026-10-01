package goose

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLinuxHarnessDiesWithHost(t *testing.T) {
	if path := os.Getenv("NEXUS_GOOSE_PARENT_DEATH_FIXTURE"); path != "" {
		cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", `echo $$ > "$1"; exec sleep 30`, "fixture", path)
		_ = runProcess(cmd)
		return
	}
	path := filepath.Join(t.TempDir(), "child.pid")
	owner := exec.Command(os.Args[0], "-test.run=^TestLinuxHarnessDiesWithHost$")
	owner.Env = append(os.Environ(), "NEXUS_GOOSE_PARENT_DEATH_FIXTURE="+path)
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Process.Kill(); _ = owner.Wait() }()
	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(path)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		if pid > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 0 {
		t.Fatal("child did not start")
	}
	statPath := filepath.Join("/proc", strconv.Itoa(pid), "stat")
	before, err := os.ReadFile(statPath)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(before)[strings.LastIndex(string(before), ")")+1:])
	if len(fields) <= 19 {
		t.Fatal("missing process identity")
	}
	started := fields[19]
	defer func() {
		current, err := os.ReadFile(statPath)
		if err != nil {
			return
		}
		fields := strings.Fields(string(current)[strings.LastIndex(string(current), ")")+1:])
		if len(fields) > 19 && fields[19] == started {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}()
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait()
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if os.IsNotExist(err) {
			return
		}
		if err == nil {
			fields := strings.Fields(string(b)[strings.LastIndex(string(b), ")")+1:])
			if len(fields) > 0 && fields[0] == "Z" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("harness survived owning host SIGKILL")
}
