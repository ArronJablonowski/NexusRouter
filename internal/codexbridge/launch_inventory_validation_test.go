package codexbridge

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const cleanLaunchSkills = `{"data":[{"cwd":"/probe","skills":[],"errors":[]}]}`
const cleanLaunchHooks = `{"data":[{"cwd":"/probe","hooks":[],"errors":[],"warnings":[]}]}`

func TestCheckLaunchInventories(t *testing.T) {
	for _, mcp := range []string{`{"data":[]}`, `{"data":[],"nextCursor":null}`, `{"data":[{"tools":{}}]}`} {
		for _, skills := range []string{cleanLaunchSkills, `{"data":[{"cwd":"/probe","skills":[{"path":"/private/SKILL.md","enabled":false}],"errors":[]}]}`} {
			if err := CheckLaunchInventories("/probe", json.RawMessage(mcp), json.RawMessage(skills), json.RawMessage(cleanLaunchHooks)); err != nil {
				t.Fatal("known-empty or explicitly disabled inventory rejected")
			}
		}
	}
}

func TestCheckLaunchInventoriesRejectsMCP(t *testing.T) {
	cases := []string{`{}`, `{"data":null}`, `{"Data":[]}`, `{"data":{}}`, `{"data":[null]}`, `{"data":[{}]}`, `{"data":[{"tools":null}]}`, `{"data":[{"tools":[]}]}`, `{"data":[{"Tools":{}}]}`, `{"data":[{"tools":{"private":{}}}]}`, `{"data":[],"nextCursor":""}`, `{"data":[],"nextCursor":false}`, `{"data":[],"NextCursor":null}`, `{"data":[],"data":[]}`, `{"data":[{"tools":{},"tools":{}}]}`, `{"data":[],"nextCursor":"private-cursor"}`}
	cases = append(cases, `{"data":[`+strings.TrimSuffix(strings.Repeat(`{"tools":{}},`, 65), ",")+`]}`)
	cases = append(cases, "{\"data\":[],\"private\":\""+strings.Repeat("x", 1<<20)+"\"}")
	for i, raw := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if err := CheckLaunchInventories("/probe", json.RawMessage(raw), json.RawMessage(cleanLaunchSkills), json.RawMessage(cleanLaunchHooks)); err != ErrLaunchObservation {
				t.Fatal("unsafe MCP observation did not fail statically")
			}
		})
	}
}

func TestCheckLaunchInventoriesRejectsSkills(t *testing.T) {
	cases := []string{`{}`, `{"data":null}`, `{"data":[]}`, strings.Replace(cleanLaunchSkills, "/probe", "/other", 1), strings.Replace(cleanLaunchSkills, `"cwd"`, `"CWD"`, 1), strings.Replace(cleanLaunchSkills, `"skills":[]`, `"skills":null`, 1), strings.Replace(cleanLaunchSkills, `"errors":[]`, `"errors":[{}]`, 1)}
	for _, skill := range []string{`{}`, `null`, `{"path":"/private","enabled":true}`, `{"path":"/private","enabled":null}`, `{"path":"/private","Enabled":false}`, `{"path":"/private","enabled":"false"}`, `{"path":"/private","enabled":false,"enabled":true}`} {
		cases = append(cases, strings.Replace(cleanLaunchSkills, `"skills":[]`, `"skills":[`+skill+`]`, 1))
	}
	for i, raw := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if err := CheckLaunchInventories("/probe", json.RawMessage(`{"data":[]}`), json.RawMessage(raw), json.RawMessage(cleanLaunchHooks)); err != ErrLaunchObservation {
				t.Fatal("unsafe skill observation did not fail statically")
			}
		})
	}
}

func TestCheckLaunchInventoriesRejectsHooks(t *testing.T) {
	cases := []string{`{}`, `{"data":null}`, `{"data":[]}`, `{"data":[null]}`, `{"data":[{},{}]}`, strings.Replace(cleanLaunchHooks, "/probe", "/other", 1), strings.Replace(cleanLaunchHooks, `"cwd"`, `"CWD"`, 1)}
	for _, field := range []string{"hooks", "errors", "warnings"} {
		cases = append(cases, strings.Replace(cleanLaunchHooks, `,"`+field+`":[]`, "", 1))
		for _, replacement := range []string{"null", "{}", "[{}]"} {
			cases = append(cases, strings.Replace(cleanLaunchHooks, `"`+field+`":[]`, `"`+field+`":`+replacement, 1))
		}
		cases = append(cases, strings.Replace(cleanLaunchHooks, `"`+field+`":[]`, `"`+strings.ToUpper(field)+`":[]`, 1))
		cases = append(cases, strings.Replace(cleanLaunchHooks, `"`+field+`":[]`, `"`+field+`":[],"`+field+`":[]`, 1))
	}
	for i, raw := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if err := CheckLaunchInventories("/probe", json.RawMessage(`{"data":[]}`), json.RawMessage(cleanLaunchSkills), json.RawMessage(raw)); err != ErrLaunchObservation {
				t.Fatal("unsafe hook observation did not fail statically")
			}
		})
	}
}
