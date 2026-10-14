package remotecli

import (
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoteSDKInspectionStartsWithLocalComfyApprovalRequired(t *testing.T) {
	cfg := config.Defaults()
	cfg.ComfyUI = config.ComfyUI{Enabled: true, Endpoint: "http://127.0.0.1:8188", ReceiptDirectory: t.TempDir()}
	file := filepath.Join(t.TempDir(), "config.json")
	body, _ := json.Marshal(map[string]any{"version": 1, "comfyui": cfg.ComfyUI})
	if e := os.WriteFile(file, body, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := config.Load(config.Options{ProjectFile: file, Flags: map[string]string{"comfyui.enabled": "false"}}); e != nil {
		t.Fatal("fixture/config override", e)
	}
	if _, e := sdk.New(sdk.ConfigOptions{ProjectFile: file}); e == nil {
		t.Fatal("unattended SDK unexpectedly acquired ComfyUI write authority")
	}
	client, e := sdk.New(remoteSDKOptions(file, cfg, nil))
	if e != nil || client == nil {
		t.Fatal("read-only remote inspection cannot start", e)
	}
	if !cfg.ComfyUI.Enabled {
		t.Fatal("serving application configuration was mutated")
	}
	options := remoteSDKOptions(file, config.Defaults(), nil)
	if options.Overrides != nil {
		t.Fatal("disabled default configuration changed")
	}
}
