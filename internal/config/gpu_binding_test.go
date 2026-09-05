package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func gpuBindingSettings() Settings {
	s := Defaults()
	s.Providers = []Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:11434"}}
	s.Models = []Model{{ID: "chat", Provider: "local", Model: "chat", Locality: "local", Capabilities: []string{"chat"}, RAMBytes: 1, VRAMBytes: 1}}
	return s
}

func TestGPUDeviceBindingValidation(t *testing.T) {
	for _, id := range []string{"", "amd:card0", "amd:card32", "nvidia:GPU-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"} {
		s := gpuBindingSettings()
		s.Models[0].GPUDevice = id
		if err := s.Validate(); err != nil {
			t.Fatal(id, err)
		}
	}
	for _, id := range []string{"card0", "amd:card01", "amd:card-1", "amd:card+1", "amd:card", "amd:card1/../../etc", "amd:card0\n", " amd:card0", "nvidia:0", "nvidia:GPU-invalid", "unknown:card0", strings.Repeat("x", 200)} {
		s := gpuBindingSettings()
		s.Models[0].GPUDevice = id
		if s.Validate() == nil {
			t.Fatal("accepted invalid binding", id)
		}
	}
	s := gpuBindingSettings()
	s.Models[0].GPUDevice = "amd:card0"
	s.Models[0].Locality = "cloud"
	if s.Validate() == nil {
		t.Fatal("accepted cloud GPU binding")
	}
	s = gpuBindingSettings()
	s.Models[0].GPUDevice = "amd:card0"
	s.Models[0].VRAMBytes = 0
	if s.Validate() == nil {
		t.Fatal("accepted binding without VRAM footprint")
	}
}

func TestGPUDeviceBindingEmptyOmittedFromJSON(t *testing.T) {
	s := gpuBindingSettings()
	body, err := json.Marshal(s.Models[0])
	if err != nil || strings.Contains(string(body), "gpu_device") {
		t.Fatal(string(body), err)
	}
	s.Models[0].GPUDevice = "amd:card0"
	body, err = json.Marshal(s.Models[0])
	if err != nil || !strings.Contains(string(body), `"gpu_device":"amd:card0"`) {
		t.Fatal(string(body), err)
	}
}

func TestGPUDeviceBindingLayerTypes(t *testing.T) {
	base := "providers:\n  - id: local\n    kind: ollama\n    endpoint: http://127.0.0.1:11434\nmodels:\n  - id: chat\n    provider: local\n    model: chat\n    locality: local\n    capabilities: [chat]\n    ram_bytes: 1\n    vram_bytes: 1\n    gpu_device: "
	for _, value := range []string{"amd:card0", "'amd:card2'", "''"} {
		if _, err := Load(Options{ProjectFile: file(t, base+value+"\n")}); err != nil {
			t.Fatal(value, err)
		}
	}
	good := file(t, base+"amd:card0\n")
	for _, value := range []string{"42", "true", "false", "1.5", "null", "[]", "{}"} {
		bad := file(t, base+value+"\n")
		for _, options := range []Options{{ProjectFile: bad}, {UserFile: bad, ProjectFile: good}} {
			if _, err := Load(options); err == nil {
				t.Fatal("accepted nonstring binding", value)
			}
		}
	}
}
