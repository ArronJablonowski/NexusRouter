//go:build darwin || linux

package app

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestDocumentProcessGroupCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := filepath.Join(t.TempDir(), "listener")
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDocumentProcessOwnedHelper$")
	cmd.Env = append(os.Environ(), "NEXUS_DOCUMENT_TEST=parent", "NEXUS_DOCUMENT_TEST_PATH="+path)
	cleanup := configureDocumentProcess(cmd)
	defer cleanup()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	var address []byte
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		address, _ = os.ReadFile(path)
		if len(address) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(address) == 0 {
		t.Fatal("owned child did not start")
	}
	c, err := net.DialTimeout("tcp", string(address), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("cancellation did not stop parser")
	}
	end = time.Now().Add(3 * time.Second)
	for time.Now().Before(end) {
		c, err = net.DialTimeout("tcp", string(address), 100*time.Millisecond)
		if err != nil {
			return
		}
		c.Close()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("OCR descendant survived parser cancellation")
}

func TestDocumentProcessOwnedHelper(t *testing.T) {
	switch os.Getenv("NEXUS_DOCUMENT_TEST") {
	case "parent", "crash-parent":
		cmd := exec.Command(os.Args[0], "-test.run=^TestDocumentProcessOwnedHelper$")
		cmd.Env = append(os.Environ(), "NEXUS_DOCUMENT_TEST=child")
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		if os.Getenv("NEXUS_DOCUMENT_TEST") == "crash-parent" {
			end := time.Now().Add(5 * time.Second)
			for time.Now().Before(end) {
				if b, _ := os.ReadFile(os.Getenv("NEXUS_DOCUMENT_TEST_PATH")); len(b) > 0 {
					os.Exit(3)
				}
				time.Sleep(10 * time.Millisecond)
			}
			os.Exit(2)
		}
		cmd.Wait()
		os.Exit(0)
	case "child":
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			os.Exit(2)
		}
		if os.WriteFile(os.Getenv("NEXUS_DOCUMENT_TEST_PATH"), []byte(l.Addr().String()), 0600) != nil {
			os.Exit(2)
		}
		for {
			c, err := l.Accept()
			if err != nil {
				os.Exit(0)
			}
			c.Close()
		}
	}
}

func TestDocumentProcessCleanupAfterParserCrash(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "listener")
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDocumentProcessOwnedHelper$")
	cmd.Env = append(os.Environ(), "NEXUS_DOCUMENT_TEST=crash-parent", "NEXUS_DOCUMENT_TEST_PATH="+path)
	cleanup := configureDocumentProcess(cmd)
	defer cleanup()
	if err := cmd.Run(); err == nil {
		t.Fatal("parser did not simulate failure")
	}
	address, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.DialTimeout("tcp", string(address), time.Second)
	if err != nil {
		t.Fatal("descendant not alive before cleanup", err)
	}
	c.Close()
	cleanup()
	end := time.Now().Add(3 * time.Second)
	for time.Now().Before(end) {
		c, err = net.DialTimeout("tcp", string(address), 100*time.Millisecond)
		if err != nil {
			return
		}
		c.Close()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("descendant survived failed-parser cleanup")
}
