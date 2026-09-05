package codexbridge

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestExtensionDisableOverrides(t *testing.T) {
	raw := json.RawMessage(`{"config":{"mcp_servers":{"z.dot":{"command":"never-copy","enabled":true},"a\"quote":{}},"plugins":{"p@test":{"enabled":true,"secret":"never-copy"}}}}`)
	got, err := ExtensionDisableOverrides(raw)
	want := []string{`mcp_servers={"a\"quote"={enabled=false},"z.dot"={enabled=false}}`, `plugins={"p@test"={enabled=false}}`}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected fixture overrides: %v %v", got, err)
	}
	if strings.Contains(strings.Join(got, ""), "never-copy") {
		t.Fatal("entry contents leaked")
	}
}

func TestExtensionDisableOverridesRejectUnknownOrMalformed(t *testing.T) {
	for _, raw := range []string{
		`{}`, `{"config":{"mcp_servers":{}}}`, `{"config":{"mcp_servers":null,"plugins":{}}}`,
		`{"config":{"mcp_servers":{},"plugins":[]}}`, `{"config":{"mcp_servers":{"x":null},"plugins":{}}}`,
		`{"config":{"mcp_servers":{"x":{},"x":{}},"plugins":{}}}`,
		`{"config":{"mcp_servers":{"bad\nkey":{}},"plugins":{}}}`,
		`{"config":{"mcp_servers":{"":{}},"plugins":{}}}`,
	} {
		got, err := ExtensionDisableOverrides(json.RawMessage(raw))
		if !errors.Is(err, ErrLaunchObservation) || got != nil {
			t.Fatal("invalid configuration produced overrides")
		}
	}
}

func TestExtensionObservationRequiresExplicitFalse(t *testing.T) {
	r, err := InspectLaunchConfig(json.RawMessage(`{"config":{"mcp_servers":{"a":{"enabled":false},"b":{},"c":{"enabled":null},"d":{"enabled":"false"}},"plugins":{"a":{"enabled":true},"b":{"enabled":false}}}}`), []string{"hooks"})
	if err != nil || r.MCPEntriesDisabled != 1 || r.PluginEntriesDisabled != 1 || r.MCPEntries != 4 || r.PluginEntries != 2 {
		t.Fatalf("incorrect disable evidence: %+v %v", r, err)
	}
}

func TestExtensionDisableOverrideBoundsAndQuoting(t *testing.T) {
	for _, name := range []string{`a.b@c`, `a\b"={x=true}`, `a <>& b`} {
		raw, _ := json.Marshal(map[string]any{"config": map[string]any{"mcp_servers": map[string]any{name: map[string]any{}}, "plugins": map[string]any{}}})
		got, err := ExtensionDisableOverrides(raw)
		quoted, _ := json.Marshal(name)
		if err != nil || got[0] != "mcp_servers={"+string(quoted)+"={enabled=false}}" {
			t.Fatal("identifier changed TOML structure")
		}
	}
	for _, name := range []string{strings.Repeat("x", 257), "bad\x7f", "non-ascii-π"} {
		raw, _ := json.Marshal(map[string]any{"config": map[string]any{"mcp_servers": map[string]any{name: map[string]any{}}, "plugins": map[string]any{}}})
		if _, err := ExtensionDisableOverrides(raw); !errors.Is(err, ErrLaunchObservation) {
			t.Fatal("unsupported identifier accepted")
		}
	}
	for _, count := range []int{64, 65} {
		entries := map[string]any{}
		for i := range count {
			name := strconv.Itoa(i)
			if count == 64 {
				name += strings.Repeat("<", 250) // JSON/TOML escaping exceeds output budget.
			}
			entries[name] = map[string]any{}
		}
		raw, _ := json.Marshal(map[string]any{"config": map[string]any{"mcp_servers": entries, "plugins": map[string]any{}}})
		if got, err := ExtensionDisableOverrides(raw); !errors.Is(err, ErrLaunchObservation) || got != nil {
			t.Fatal("inventory/output budget not enforced")
		}
	}
}
