package config

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
)

func TestProjectToolAccessConcurrentDigest(t *testing.T) {
	path := file(t, "version: 1\n")
	_, digest, err := ReadProjectToolAccess(path)
	if err != nil {
		t.Fatal(err)
	}
	next := ToolAccess{SpecialistsAllowCloud: true}
	var wg sync.WaitGroup
	var successes atomic.Int32
	start := make(chan struct{})
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, _, e := UpdateProjectToolAccess(path, digest, next); e == nil {
				successes.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("same digest accepted %d times", successes.Load())
	}
}

func TestProjectUpdateLockProcess(t *testing.T) {
	if path := os.Getenv("NEXUS_TEST_CONFIG_LOCK"); path != "" {
		unlock, err := lockProjectUpdate(path)
		if !errors.Is(err, ErrConfigConflict) {
			if unlock != nil {
				unlock()
			}
			t.Fatalf("expected cross-process conflict, got %v", err)
		}
		return
	}
	path := file(t, "version: 1\n")
	unlock, err := lockProjectUpdate(path)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestProjectUpdateLockProcess$")
	cmd.Env = append(os.Environ(), "NEXUS_TEST_CONFIG_LOCK="+path)
	output, err := cmd.CombinedOutput()
	unlock()
	if err != nil {
		t.Fatalf("child: %v: %s", err, output)
	}
	unlock, err = lockProjectUpdate(path)
	if err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
	unlock()
}
