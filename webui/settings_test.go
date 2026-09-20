package webui

import "testing"

func TestSettingsContract(t *testing.T) {
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	off := ToolAccessSettings{}
	on := ToolAccessSettings{ToolsEnabled: true, DelegateReadTools: true, ReadRoot: "/workspace", SpecialistsAllowCloud: true}
	for _, value := range []interface{ Validate() error }{
		off, on,
		SettingsInspection{Version: 1, Digest: digest, Active: off, Saved: on, RestartRequired: true},
		SettingsUpdateRequest{Version: 1, ExpectedDigest: digest, Settings: on},
	} {
		if err := value.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []interface{ Validate() error }{
		ToolAccessSettings{DelegateReadTools: true},
		ToolAccessSettings{ToolsEnabled: true, ReadRoot: "relative"},
		SettingsInspection{Version: 1, Digest: digest, Active: off, Saved: on, RestartRequired: false},
		SettingsUpdateRequest{Version: 1, ExpectedDigest: "bad", Settings: off},
	} {
		if err := value.Validate(); err == nil {
			t.Fatal("invalid settings accepted")
		}
	}
}
