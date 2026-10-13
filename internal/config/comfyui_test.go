package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestComfyUIConfigOptInAndRedaction(t *testing.T) {
	s := Defaults()
	if s.ComfyUI.Enabled {
		t.Fatal("default enabled")
	}
	s.ComfyUI = ComfyUI{Enabled: true, Endpoint: "http://127.0.0.1:8188", ReceiptDirectory: "/private/comfy-receipts"}
	if e := s.Validate(); e != nil {
		t.Fatal(e)
	}
	b, e := s.RedactedJSON()
	if e != nil || strings.Contains(string(b), "8188") || strings.Contains(string(b), "comfy-receipts") {
		t.Fatal("private configuration leaked")
	}
	for _, endpoint := range []string{"file:///secret", "http://user:password@host", "http://host?token=private"} {
		s.ComfyUI.Endpoint = endpoint
		if s.Validate() == nil {
			t.Fatal("unsafe endpoint")
		}
	}
}

func TestComfyDefaultPreservesExistingConfigEncoding(t *testing.T) {
	b, e := json.Marshal(Defaults())
	if e != nil || strings.Contains(string(b), `"comfyui"`) {
		t.Fatal("disabled connector changed existing configuration identity", e)
	}
}
