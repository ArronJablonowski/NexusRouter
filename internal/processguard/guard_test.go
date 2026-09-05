//go:build darwin || linux

package processguard_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/processguard"
)

func TestProcessGuardRejectsInvalidReferenceAndContext(t *testing.T) {
	child := startOwnedGuard(t)
	for _, mode := range []string{"version", "id", "relative", "unclean", "invalid_utf8_directory", "directory_identity", "file_identity"} {
		t.Run(mode, func(t *testing.T) {
			ref := child.ref
			switch mode {
			case "version":
				ref.Version = 2
			case "id":
				ref.ID = "../private"
			case "relative":
				ref.Directory = "relative"
			case "unclean":
				ref.Directory += "/../guard"
			case "invalid_utf8_directory":
				ref.Directory = filepath.Join(filepath.Dir(ref.Directory), string([]byte{0xff}), filepath.Base(ref.Directory))
			case "directory_identity":
				ref.DirectoryIdentity = "secret\nidentity"
			case "file_identity":
				ref.FileIdentity = ""
			}
			if ref.Validate() == nil {
				t.Fatal("malformed reference accepted")
			}
			observation, err := processguard.Probe(context.Background(), ref)
			if observation != nil {
				_ = observation.Close()
			}
			if err == nil {
				t.Fatal("invalid reference probed")
			}
		})
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, canceled} {
		if ref, err := processguard.Current(ctx); err == nil || ref.Version != 0 {
			t.Fatal("invalid context acquired singleton")
		}
		observation, err := processguard.Probe(ctx, child.ref)
		if observation != nil {
			_ = observation.Close()
		}
		if err == nil {
			t.Fatal("invalid context probed")
		}
	}
}

func TestProcessGuardMissingOrChangedIdentityNeverMeansUnlocked(t *testing.T) {
	for _, mode := range []string{"missing_file", "replaced_file", "symlink_file", "file_mode", "directory_mode", "missing_directory", "replaced_directory", "symlink_directory", "wrong_file_identity", "wrong_directory_identity", "wrong_id", "replaced_content", "hardlink", "nonregular_file"} {
		t.Run(mode, func(t *testing.T) {
			child := startOwnedGuard(t)
			child.kill(t)
			ref := child.ref
			file := filepath.Join(ref.Directory, "owner.lock")
			original, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			// The acknowledged private directory belongs only to the killed test child.
			// Never remove a broad directory or follow a supplied symlink recursively.
			if filepath.Base(ref.Directory) == "." || !strings.HasPrefix(filepath.Base(ref.Directory), "darwin-owner-") {
				t.Fatal("unexpected owned guard path")
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "missing_file":
				must(os.Remove(file))
			case "replaced_file":
				must(os.Rename(file, file+".original"))
				must(os.WriteFile(file, original, 0600))
			case "symlink_file":
				must(os.Rename(file, file+".original"))
				must(os.Symlink(file+".original", file))
			case "file_mode":
				must(os.Chmod(file, 0644))
			case "directory_mode":
				must(os.Chmod(ref.Directory, 0755))
			case "missing_directory":
				must(os.Rename(ref.Directory, ref.Directory+".original"))
			case "replaced_directory":
				must(os.Rename(ref.Directory, ref.Directory+".original"))
				must(os.Mkdir(ref.Directory, 0700))
				must(os.WriteFile(file, original, 0600))
			case "symlink_directory":
				must(os.Rename(ref.Directory, ref.Directory+".original"))
				must(os.Symlink(ref.Directory+".original", ref.Directory))
			case "wrong_file_identity":
				ref.FileIdentity = "1:1"
			case "wrong_directory_identity":
				ref.DirectoryIdentity = "1:1"
			case "wrong_id":
				if ref.ID[0] == 'A' {
					ref.ID = "B" + ref.ID[1:]
				} else {
					ref.ID = "A" + ref.ID[1:]
				}
				if ref.Validate() != nil {
					t.Fatal("wrong-ID fixture must remain structurally valid")
				}
			case "replaced_content":
				if len(original) != 26 {
					t.Fatal("unexpected guard header size")
				}
				changed := append([]byte(nil), original...)
				if changed[0] == 'A' {
					changed[0] = 'B'
				} else {
					changed[0] = 'A'
				}
				must(os.WriteFile(file, changed, 0600))
			case "hardlink":
				must(os.Link(file, file+".alias"))
			case "nonregular_file":
				must(os.Rename(file, file+".original"))
				must(os.Mkdir(file, 0600))
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			observation, err := processguard.Probe(ctx, ref)
			if observation != nil {
				_ = observation.Close()
			}
			if err == nil {
				t.Fatal("untrusted changed identity reported observable")
			}
			if mode == "missing_file" {
				if _, err := os.Lstat(file); !os.IsNotExist(err) {
					t.Fatal("probe recreated missing file", err)
				}
			}
			if mode == "missing_directory" {
				if _, err := os.Lstat(ref.Directory); !os.IsNotExist(err) {
					t.Fatal("probe recreated missing directory", err)
				}
			}
		})
	}
}
