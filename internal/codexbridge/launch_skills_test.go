package codexbridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func skillProbeResult(paths ...string) json.RawMessage {
	skills := make([]map[string]any, 0, len(paths))
	for i, path := range paths {
		skills = append(skills, map[string]any{"path": path, "enabled": i%2 == 0, "description": "private-description", "name": "private-name"})
	}
	b, _ := json.Marshal(map[string]any{"data": []any{map[string]any{"cwd": "/private/cwd", "skills": skills, "errors": []any{}}}})
	return b
}

func TestSkillDisableOverride(t *testing.T) {
	raw := skillProbeResult("/z/SKILL.md", `/a/"quote\@.=/SKILL.md`, "/é/SKILL.md")
	original := bytes.Clone(raw)
	got, err := SkillDisableOverride(raw)
	want := `skills.config=[{path="/a/\"quote\\@.=/SKILL.md",enabled=false},{path="/z/SKILL.md",enabled=false},{path="/é/SKILL.md",enabled=false}]`
	if err != nil || got != want || !bytes.Equal(raw, original) {
		t.Fatal("deterministic private-path projection failed")
	}
	if strings.Contains(got, "private-") || strings.Contains(got, "enabled=true") {
		t.Fatal("non-path source data escaped projection")
	}
	empty, err := SkillDisableOverride(skillProbeResult())
	if err != nil || empty != "skills.config=[]" {
		t.Fatal("known-empty skill catalog rejected")
	}
}

func TestSkillDisableOverrideRejectsMalformed(t *testing.T) {
	cases := []string{
		`{}`, `{"data":null}`, `{"data":[]}`, `{"data":[null]}`,
		`{"data":[{},{}]}`, `{"data":{}}`,
		`{"data":[{"cwd":"/x","skills":[],"errors":null}]}`,
		`{"data":[{"cwd":"/x","skills":[],"errors":[{"message":"secret"}]}]}`,
		`{"data":[{"cwd":"/x","skills":null,"errors":[]}]}`,
		`{"data":[{"cwd":"/x","skills":[],"errors":[]},{"cwd":"/x","skills":[],"errors":[]}]}`,
		`{"data":[{"cwd":"relative","skills":[],"errors":[]}]}`,
		`{"data":[{"skills":[],"errors":[]}]}`,
		`{"data":[{"cwd":"/x","skills":[],"errors":[],"errors":[]}]}`,
	}
	for _, skill := range []string{`null`, `[]`, `{}`, `{"path":"/x"}`, `{"path":"/x","enabled":null}`, `{"path":"/x","enabled":"false"}`, `{"path":"/x","enabled":0}`, `{"path":"/x","Enabled":false}`, `{"path":"/x","enabled":false,"enabled":true}`, `{"path":"relative","enabled":false}`, `{"path":null,"enabled":false}`} {
		cases = append(cases, `{"data":[{"cwd":"/x","skills":[`+skill+`],"errors":[]}]}`)
	}
	for i, raw := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			got, err := SkillDisableOverride(json.RawMessage(raw))
			if got != "" || err != ErrLaunchObservation {
				t.Fatal("malformed result did not fail statically")
			}
		})
	}
}

func TestSkillDisableOverrideRejectsUnsafeAndBounds(t *testing.T) {
	cases := []json.RawMessage{
		skillProbeResult("/same", "/same"),
		skillProbeResult("/a\nprivate"), skillProbeResult("/a\x00private"),
		skillProbeResult("/a\u007fprivate"), skillProbeResult("/a\u0085private"),
		skillProbeResult("/" + strings.Repeat("a", 4096)),
		json.RawMessage("{\"data\":\"\xff\"}"),
		json.RawMessage(`{"data":[{"cwd":"/x","skills":[{"path":"/\ud800","enabled":true}],"errors":[]}]}`),
		json.RawMessage("{\"unknown\":\"" + strings.Repeat("x", 1<<20) + "\"}"),
	}
	paths := make([]string, 65)
	for i := range paths {
		paths[i] = fmt.Sprintf("/path/%d", i)
	}
	cases = append(cases, skillProbeResult(paths...))
	for i := range paths[:5] {
		paths[i] = "/" + strings.Repeat("a", 4000) + fmt.Sprint(i)
	}
	cases = append(cases, skillProbeResult(paths[:5]...))
	for i, raw := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			got, err := SkillDisableOverride(raw)
			if got != "" || err != ErrLaunchObservation {
				t.Fatal("unsafe result did not fail statically")
			}
		})
	}
	if _, err := SkillDisableOverride(skillProbeResult("/" + strings.Repeat("a", 4095))); err != nil {
		t.Fatal("maximum supported path length rejected")
	}
}
