package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

func TestSettingsMenuSaveAndConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := []byte("version: 1\n")
	if e := os.WriteFile(path, original, 0600); e != nil {
		t.Fatal(e)
	}
	s := config.Defaults()
	raw, _ := json.Marshal(s)
	var draft map[string]any
	_ = json.Unmarshal(raw, &draft)
	setSettingsValue(draft, []string{"telemetry", "dns_logging"}, "managed")
	backup, e := saveSettingsMenu(path, original, true, draft)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(backup)
	if !bytes.Equal(b, original) {
		t.Fatal("backup mismatch")
	}
	loaded, e := config.Load(config.Options{ProjectFile: path})
	if e != nil || loaded.Telemetry.DNSLogging != "managed" {
		t.Fatalf("roundtrip: %v", e)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("not private")
	}
	if _, e = saveSettingsMenu(path, original, true, draft); e == nil {
		t.Fatal("lost update accepted")
	}
}
func TestSettingsMenuInvalidPreservesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := []byte("version: 1\n")
	_ = os.WriteFile(path, original, 0600)
	if _, e := saveSettingsMenu(path, original, true, map[string]any{"version": 99}); e == nil {
		t.Fatal("invalid saved")
	}
	b, _ := os.ReadFile(path)
	if !bytes.Equal(b, original) {
		t.Fatal("changed")
	}
}
func TestSettingsMenuNavigationAndEOF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	var out, errs bytes.Buffer
	if code := RunWithInput([]string{"settings", "--config", path}, strings.NewReader("q\n"), &out, &errs, "test"); code != 0 {
		t.Fatal(errs.String())
	}
	for _, f := range settingsFields(reflect.TypeOf(config.Settings{})) {
		if !strings.Contains(out.String(), f.name) {
			t.Fatalf("missing %s", f.name)
		}
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("discard created file")
	}
	out.Reset()
	if code := RunWithInput([]string{"config", "menu", "--config", path}, strings.NewReader(""), &out, &errs, "test"); code != 0 {
		t.Fatal(errs.String())
	}
}
func TestSettingsMenuSymlinkRejected(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	_ = os.WriteFile(target, []byte("version: 1\n"), 0600)
	path := filepath.Join(dir, "link")
	_ = os.Symlink(target, path)
	var out bytes.Buffer
	if runSettingsMenu([]string{"--config", path}, strings.NewReader("q\n"), &out, &out) != 1 {
		t.Fatal("symlink accepted")
	}
}

func TestSettingsMenuInteractiveSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "config.yaml")
	fields := settingsFields(reflect.TypeOf(config.Settings{}))
	section := 0
	for i, f := range fields {
		if f.name == "telemetry" {
			section = i + 1
		}
	}
	leaves := settingsFields(reflect.TypeOf(config.Defaults().Telemetry))
	field := 0
	for i, f := range leaves {
		if f.name == "dns_logging" {
			field = i + 1
		}
	}
	if section == 0 || field == 0 {
		t.Fatal("missing schema")
	}
	var out, errs bytes.Buffer
	script := fmt.Sprintf("%d\n\n%d\nmanaged\ns\n", section, field)
	if code := runSettingsMenu([]string{"--config", path}, strings.NewReader(script), &out, &errs); code != 0 {
		t.Fatal(errs.String())
	}
	loaded, e := config.Load(config.Options{ProjectFile: path})
	if e != nil || loaded.Telemetry.DNSLogging != "managed" {
		t.Fatalf("save failed: %v %s", e, errs.String())
	}
}
