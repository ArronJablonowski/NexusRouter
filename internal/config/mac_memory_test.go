package config

import "testing"

func TestMacMemorySettingsRoundTrip(t *testing.T) {
	path := file(t, "version: 1\nskills:\n  enabled: false\n")
	saved, digest, err := ReadProjectToolAccess(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.MacMemoryPercent != 100 || saved.MacSwapGrowthGB != 4 {
		t.Fatal("defaults", saved.MacMemoryPercent, saved.MacSwapGrowthGB)
	}
	saved.MacMemoryPercent = 95
	saved.MacSwapGrowthGB = 3.5
	_, _, err = UpdateProjectToolAccess(path, digest, saved)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(Options{ProjectFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hardware.MacMemoryPercent != 95 || cfg.Hardware.MacSwapGrowthBytes() != 3_500_000_000 {
		t.Fatal("not persisted")
	}
	cfg.Hardware.MacMemoryPercent = 101
	if cfg.Validate() == nil {
		t.Fatal("invalid percentage accepted")
	}
}
