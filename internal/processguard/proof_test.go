//go:build darwin || linux

package processguard_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/processguard"
)

func TestProcessGuardUnlockedProofRetainsExclusiveProbe(t *testing.T) {
	child := startOwnedGuard(t)
	child.kill(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	first, err := processguard.Probe(ctx, child.ref)
	if err != nil || first == nil || first.State != processguard.Unlocked {
		t.Fatal("missing unlocked probe", err)
	}
	defer first.Close()
	if err = first.ConfirmUnlocked(ctx); err != nil {
		t.Fatal("verified dead owner rejected", err)
	}
	second, err := processguard.Probe(ctx, child.ref)
	if err != nil || second == nil || second.State != processguard.Held {
		t.Fatal("proof did not retain exclusive lock", err)
	}
	if err = second.ConfirmUnlocked(ctx); err == nil {
		t.Fatal("competing probe acquired proof")
	}
	if err = second.Close(); err != nil {
		t.Fatal(err)
	}
	if err = first.ConfirmUnlocked(ctx); err != nil {
		t.Fatal("closing competing probe released original lock", err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	if err = first.ConfirmUnlocked(ctx); err == nil {
		t.Fatal("closed observation retained proof")
	}
	third, err := processguard.Probe(ctx, child.ref)
	if err != nil || third == nil || third.State != processguard.Unlocked {
		t.Fatal("closed proof retained lock", err)
	}
	defer third.Close()
	if err = third.ConfirmUnlocked(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestProcessGuardProofCannotBeForgedByPublicState(t *testing.T) {
	child := startOwnedGuard(t)
	ctx := context.Background()
	held, err := processguard.Probe(ctx, child.ref)
	if err != nil || held == nil || held.State != processguard.Held {
		t.Fatal("missing live owner observation", err)
	}
	defer held.Close()
	held.State = processguard.Unlocked
	if err = held.ConfirmUnlocked(ctx); err == nil {
		t.Fatal("mutable public state forged ownership proof")
	}
	for _, observation := range []*processguard.Observation{nil, {}, {State: processguard.Unlocked}} {
		if err = observation.ConfirmUnlocked(ctx); err == nil {
			t.Fatal("unacquired observation forged proof")
		}
	}
	child.kill(t)
	// Owner exit does not retroactively acquire the lock for a Held observation.
	if err = held.ConfirmUnlocked(ctx); err == nil {
		t.Fatal("old held probe silently promoted after owner death")
	}
	fresh, err := processguard.Probe(ctx, child.ref)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, bad := range []context.Context{nil, canceled} {
		if err = fresh.ConfirmUnlocked(bad); err == nil {
			t.Fatal("invalid context accepted proof")
		}
	}
	if err = fresh.ConfirmUnlocked(ctx); err != nil {
		t.Fatal("canceled confirmation destroyed ownership", err)
	}
}

func TestProcessGuardProofRevalidatesIdentityAfterProbe(t *testing.T) {
	for _, mode := range []string{"file_replaced", "directory_replaced", "header_changed", "file_missing", "directory_mode"} {
		t.Run(mode, func(t *testing.T) {
			child := startOwnedGuard(t)
			child.kill(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			observation, err := processguard.Probe(ctx, child.ref)
			if err != nil || observation == nil || observation.State != processguard.Unlocked {
				t.Fatal(err)
			}
			defer observation.Close()
			if err = observation.ConfirmUnlocked(ctx); err != nil {
				t.Fatal(err)
			}
			directory := child.ref.Directory
			file := filepath.Join(directory, "owner.lock")
			original, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "file_replaced":
				must(os.Rename(file, file+".original"))
				must(os.WriteFile(file, original, 0600))
			case "directory_replaced":
				must(os.Rename(directory, directory+".original"))
				must(os.Mkdir(directory, 0700))
				must(os.WriteFile(file, original, 0600))
			case "header_changed":
				if len(original) != 26 {
					t.Fatal("invalid header fixture")
				}
				if original[0] == 'A' {
					original[0] = 'B'
				} else {
					original[0] = 'A'
				}
				must(os.WriteFile(file, original, 0600))
			case "file_missing":
				must(os.Remove(file))
			case "directory_mode":
				must(os.Chmod(directory, 0755))
			}
			if err = observation.ConfirmUnlocked(ctx); err == nil {
				t.Fatal("stale probe accepted altered identity")
			}
		})
	}
}
