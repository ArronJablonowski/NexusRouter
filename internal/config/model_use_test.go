package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModelUsePersistsAndFailsClosed(t *testing.T) {
	db := filepath.Join(t.TempDir(), "tasks.db")
	if !ModelUseAllowed(db, "local", "m") {
		t.Fatal("default")
	}
	if _, err := UpdateModelUse(db, "local", "m", false); err != nil {
		t.Fatal(err)
	}
	if ModelUseAllowed(db, "local", "m") || !ModelUseAllowed(db, "spark", "m") {
		t.Fatal("scope")
	}
	if _, err := UpdateModelUse(db, "spark", "m", false); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateModelUse(db, "local", "m", true); err != nil {
		t.Fatal(err)
	}
	if !ModelUseAllowed(db, "local", "m") || ModelUseAllowed(db, "spark", "m") {
		t.Fatal("lost other host")
	}
	if _, err := UpdateModelUse(db, "../bad", "m", true); err == nil {
		t.Fatal("bad ID")
	}
	os.WriteFile(db+".model-use.json", []byte("bad"), 0600)
	if ModelUseAllowed(db, "local", "m") {
		t.Fatal("corruption allowed")
	}
}
func TestModelUseRejectsSymlink(t *testing.T) {
	db := filepath.Join(t.TempDir(), "tasks.db")
	target := filepath.Join(t.TempDir(), "target")
	os.WriteFile(target, []byte(`{"version":1,"disabled":{}}`), 0600)
	os.Symlink(target, db+".model-use.json")
	if _, err := UpdateModelUse(db, "local", "m", false); err == nil {
		t.Fatal("symlink")
	}
}
