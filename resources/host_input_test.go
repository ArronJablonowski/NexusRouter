package resources

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostMemoryInputBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory")
	for _, size := range []int{maxProbeBytes, maxProbeBytes + 1} {
		if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0600); err != nil {
			t.Fatal(err)
		}
		body, err := readHostMemory(context.Background(), path)
		if size == maxProbeBytes && (err != nil || len(body) != size) {
			t.Fatal(len(body), err)
		}
		if size > maxProbeBytes && (err == nil || body != nil) {
			t.Fatal("oversized input accepted")
		}
	}
	if _, err := readHostMemory(context.Background(), t.TempDir()); err == nil {
		t.Fatal("directory accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readHostMemory(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := Profile(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := Profile(nil); !errors.Is(err, ErrProfile) {
		t.Fatal(err)
	}
}

func TestDarwinMemoryRejectsDuplicateCategories(t *testing.T) {
	prefix := "Mach Virtual Memory Statistics: (page size of 4096 bytes)\n"
	for _, body := range []string{
		"Pages free: 1.\nPages free: 2.\nPages speculative: 3.\n",
		"Pages free: 1.\nPages inactive: 2.\nPages speculative: 3.\nPages free: 4.\n",
	} {
		if _, err := darwinAvailable(prefix + body); err == nil {
			t.Fatal("duplicate category accepted")
		}
	}
}
