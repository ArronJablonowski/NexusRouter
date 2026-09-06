//go:build darwin || linux

package processguard

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOwnerDirectoryExplicitPrivateRoot(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "owners")
	t.Setenv("DARWIN_PROCESS_OWNER_DIR", parent)
	got, err := ownerDirectory()
	want, resolveErr := filepath.EvalSymlinks(parent)
	if err != nil || resolveErr != nil || got != want {
		t.Fatal(got, err, resolveErr)
	}
	info, err := os.Lstat(got)
	if err != nil || !privateNode(info, true) {
		t.Fatal("root not private", err)
	}
	if again, err := ownerDirectory(); err != nil || again != got {
		t.Fatal("existing root changed", again, err)
	}
	entries, err := os.ReadDir(got)
	if err != nil || len(entries) != 0 {
		t.Fatal("selection created a guard", err)
	}
}

func TestOwnerDirectoryDefaultConfigLocation(t *testing.T) {
	old, existed := os.LookupEnv("DARWIN_PROCESS_OWNER_DIR")
	if err := os.Unsetenv("DARWIN_PROCESS_OWNER_DIR"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("DARWIN_PROCESS_OWNER_DIR", old)
		} else {
			_ = os.Unsetenv("DARWIN_PROCESS_OWNER_DIR")
		}
	})
	base := t.TempDir()
	// UserConfigDir is platform-defined; isolate either platform from user data.
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ownerDirectory()
	want, resolveErr := filepath.EvalSymlinks(filepath.Join(config, "DarwinRouter", "process-owners"))
	if err != nil || resolveErr != nil || got != want {
		t.Fatal(runtime.GOOS, got, err, resolveErr)
	}
	info, err := os.Lstat(got)
	if err != nil || !privateNode(info, true) {
		t.Fatal("default root not private", err)
	}
}

func TestOwnerDirectoryRejectsInvalidOrInsecureRoots(t *testing.T) {
	base := t.TempDir()
	private := filepath.Join(base, "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "relative", base + "/../invalid", base + "/", base + "/control\n", string([]byte{'/', 255}), link, file, open} {
		t.Run(strings.ReplaceAll(path, "/", "_"), func(t *testing.T) {
			t.Setenv("DARWIN_PROCESS_OWNER_DIR", path)
			if got, err := ownerDirectory(); err == nil || got != "" {
				t.Fatal("invalid root accepted", got, err)
			}
		})
	}
	contents, err := os.ReadFile(file)
	if err != nil || string(contents) != "unchanged" {
		t.Fatal("invalid root modified", err)
	}
	info, err := os.Stat(open)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("insecure root repaired", err)
	}
}

func TestOwnerNewHolderPinnedRootAndLegacyProbe(t *testing.T) {
	// The explicit helper also permits the previous temp-parent layout. Probe
	// trusts the persisted identities, not the current configured root.
	for _, name := range []string{"durable", "legacy-temp"} {
		t.Run(name, func(t *testing.T) {
			parent, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(parent, 0700); err != nil {
				t.Fatal(err)
			}
			h, err := newHolderIn(parent)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = h.file.Close(); _ = h.root.Close() })
			if filepath.Dir(h.reference.Directory) != parent || h.reference.Validate() != nil {
				t.Fatal("wrong reference", h.reference)
			}
			ref := h.reference
			probe, err := Probe(context.Background(), ref)
			if err != nil || probe.State != Held {
				t.Fatal("new owner not held", err)
			}
			if err := probe.Close(); err != nil {
				t.Fatal(err)
			}
			if err := h.file.Close(); err != nil {
				t.Fatal(err)
			}
			if err := h.root.Close(); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DARWIN_PROCESS_OWNER_DIR", filepath.Join(parent, "unrelated"))
			probe, err = Probe(context.Background(), ref)
			if err != nil || probe.State != Unlocked || probe.ConfirmUnlocked(context.Background()) != nil {
				t.Fatal("stored owner not probeable", err)
			}
			if err := probe.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(ref.Directory, "owner.lock")); err != nil {
				t.Fatal("probe removed owner", err)
			}
		})
	}
}

func TestOwnerParentReplacementFailsClosed(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(base, "owners")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	h, err := newHolderIn(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer h.file.Close()
	defer h.root.Close()
	if err := os.Rename(parent, filepath.Join(base, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if verify(h) == nil {
		t.Fatal("replacement parent accepted")
	}
	if probe, err := Probe(context.Background(), h.reference); err == nil || probe != nil {
		t.Fatal("substituted path proved ownership", err)
	}
	info, err := h.file.Stat()
	if err != nil || identity(info) != h.reference.FileIdentity {
		t.Fatal("original holder lost pinned descriptor", err)
	}
}
