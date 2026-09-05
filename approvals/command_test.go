package approvals

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCommandValidationAndPayload(t *testing.T) {
	c := Command{Expected: validRequest(), ID: "decision_1", Allowed: true}
	if c.Validate() != nil {
		t.Fatal("valid command rejected")
	}
	for _, id := range []string{"", "bad id", "../id", strings.Repeat("a", 129)} {
		bad := c
		bad.ID = id
		if bad.Validate() != ErrInvalid {
			t.Fatal("invalid ID admitted", id)
		}
	}
	bad := c
	bad.Expected.Scope = "*"
	if bad.Validate() != ErrInvalid {
		t.Fatal("invalid expected request admitted")
	}
	body, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || len(fields) != 3 || fields["expected"] == nil || fields["id"] == nil || fields["allowed"] == nil {
		t.Fatal("command exposes unexpected fields", string(body))
	}
	var got Command
	if json.Unmarshal(body, &got) != nil || got.Validate() != nil || !got.Expected.Matches(c.Expected) || got.ID != c.ID || got.Allowed != c.Allowed {
		t.Fatal("command roundtrip changed")
	}
}
