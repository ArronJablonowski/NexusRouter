package codexbridge

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestProbeMetadataCaptureIsBounded(t *testing.T) {
	b := &probeMetadataBuffer{}
	if _, err := io.Copy(b, strings.NewReader(strings.Repeat("x", 64<<10))); err != nil || b.buf.Len() != 64<<10 {
		t.Fatal("metadata limit boundary rejected")
	}
	if _, err := io.Copy(b, strings.NewReader("x")); err == nil || b.buf.Len() != 64<<10 {
		t.Fatal("metadata capture exceeded bound")
	}
}

// Inventory-only diagnostic, never a tool invocation or an admission decision.
// Names, descriptions, source paths, errors and other payloads stay withheld.
func probeLaunchInventory(t *testing.T, request func(string, string, json.RawMessage) json.RawMessage) (string, bool) {
	t.Helper()
	var mcp struct {
		Data []struct {
			Tools map[string]json.RawMessage `json:"tools"`
		} `json:"data"`
		NextCursor *string `json:"nextCursor"`
	}
	if decodePayload(request("3", "mcpServerStatus/list", json.RawMessage(`{"limit":100,"detail":"toolsAndAuthOnly"}`)), &mcp) != nil || mcp.Data == nil || mcp.NextCursor != nil {
		t.Fatal("complete MCP inventory unavailable")
	}
	tools := 0
	for _, server := range mcp.Data {
		if server.Tools == nil {
			t.Fatal("MCP tool inventory unknown")
		}
		tools += len(server.Tools)
	}
	t.Logf("observed MCP inventory: servers=%d tools=%d", len(mcp.Data), tools)
	var skills struct {
		Data []struct {
			Skills []struct {
				Enabled *bool `json:"enabled"`
			} `json:"skills"`
			Errors []json.RawMessage `json:"errors"`
		} `json:"data"`
	}
	skillResult := request("4", "skills/list", json.RawMessage(`{"cwds":[],"forceReload":true}`))
	if decodePayload(skillResult, &skills) != nil || len(skills.Data) != 1 || skills.Data[0].Skills == nil || skills.Data[0].Errors == nil {
		t.Fatal("skill inventory unavailable")
	}
	disabled := 0
	for _, skill := range skills.Data[0].Skills {
		if skill.Enabled != nil && !*skill.Enabled {
			disabled++
		}
	}
	override, err := SkillDisableOverride(skillResult)
	if err != nil {
		t.Fatal("skill overrides unavailable")
	}
	t.Logf("observed skill inventory: skills=%d disabled=%d errors=%d", len(skills.Data[0].Skills), disabled, len(skills.Data[0].Errors))
	var hooks struct {
		Data []struct {
			Hooks    []json.RawMessage `json:"hooks"`
			Errors   []json.RawMessage `json:"errors"`
			Warnings []json.RawMessage `json:"warnings"`
		} `json:"data"`
	}
	if decodePayload(request("5", "hooks/list", json.RawMessage(`{"cwds":[]}`)), &hooks) != nil || len(hooks.Data) != 1 || hooks.Data[0].Hooks == nil || hooks.Data[0].Errors == nil || hooks.Data[0].Warnings == nil {
		t.Fatal("hook inventory unavailable")
	}
	t.Logf("observed hook inventory: hooks=%d errors=%d warnings=%d", len(hooks.Data[0].Hooks), len(hooks.Data[0].Errors), len(hooks.Data[0].Warnings))
	return override, disabled == len(skills.Data[0].Skills) && len(skills.Data[0].Errors) == 0
}
