package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectToolAccessSnapshotMatchesDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	a := []byte("version: 1\nweb_ui:\n  specialists_allow_cloud: true\n")
	b := []byte("version: 1\nweb_ui:\n  specialists_allow_cloud: false\n")
	if err := os.WriteFile(path, a, 0600); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			for _, data := range [][]byte{a, b} {
				select {
				case <-stop:
					return
				default:
				}
				tmp := path + ".next"
				if os.WriteFile(tmp, data, 0600) != nil {
					return
				}
				if os.Rename(tmp, path) != nil {
					return
				}
			}
		}
	}()
	defer func() { close(stop); <-done }()
	for i := 0; i < 500; i++ {
		s, d, err := ReadProjectToolAccess(path)
		if err != nil {
			continue
		}
		want := configDigest(b)
		if s.SpecialistsAllowCloud {
			want = configDigest(a)
		}
		if d != want {
			t.Fatal("settings and digest came from different file snapshots")
		}
	}
}
