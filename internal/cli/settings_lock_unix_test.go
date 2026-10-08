//go:build darwin || linux

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"golang.org/x/sys/unix"
)

func TestSettingsMenuHonorsWebWriterLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := []byte("version: 1\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(path+".update.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(config.Defaults())
	var draft map[string]any
	_ = json.Unmarshal(raw, &draft)
	setSettingsValue(draft, []string{"telemetry", "dns_logging"}, "managed")
	if _, err := saveSettingsMenu(path, original, true, draft); err == nil {
		t.Fatal("menu saved while Web writer held its compare/replace lock")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("blocked writer changed configuration", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if _, err := saveSettingsMenu(path, original, true, draft); err != nil {
		t.Fatal("menu could not save after writer released lock", err)
	}
}
