package memory

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMemoryKeysRejectAmbiguousEncodingAndControls(t *testing.T) {
	for _, key := range []string{"", " ", " padded", "padded ", "bad\x00key", "bad\nkey", "bad\tkey", "bad\u0085key", string([]byte{255}), strings.Repeat("é", 257)} {
		if ValidKey(key) {
			t.Fatalf("invalid key admitted %q", key)
		}
	}
	for _, key := range []string{"scope", "scope with spaces", "项目-42", strings.Repeat("é", 256)} {
		if !ValidKey(key) {
			t.Fatalf("stable key refused %q", key)
		}
	}
}

func TestMemoryFactTextRoundTripsWithoutLoss(t *testing.T) {
	now := time.Unix(100, 0)
	fact := Fact{Version: 1, ID: "事实", Scope: "project", Revision: 1, Content: "Line one\n\tquoted \"value\" \\ café", Provenance: "operator\nreference", Confidence: .5, Privacy: "local_only", Created: now, Updated: now}
	if fact.Validate() != nil {
		t.Fatal("multiline prose rejected")
	}
	body, err := json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Fact
	if json.Unmarshal(body, &decoded) != nil || decoded.Content != fact.Content || decoded.Provenance != fact.Provenance || decoded.ID != fact.ID {
		t.Fatal("factual text changed")
	}
	for _, invalid := range []string{string([]byte{255}), "embedded\x00null"} {
		for _, field := range []string{"content", "provenance", "id", "scope"} {
			bad := fact
			switch field {
			case "content":
				bad.Content = invalid
			case "provenance":
				bad.Provenance = invalid
			case "id":
				bad.ID = invalid
			case "scope":
				bad.Scope = invalid
			}
			if bad.Validate() == nil {
				t.Fatal("invalid fact accepted", field)
			}
		}
	}
}

func TestMemoryQueryCursorAndLiteralTextValidation(t *testing.T) {
	query := Query{Scope: "project", Limit: 10, Now: time.Unix(100, 0), Contains: "line\n\t内容", AfterID: "fact-42"}
	if query.Validate() != nil {
		t.Fatal("valid literal query rejected")
	}
	for _, invalid := range []string{" padded", "bad\nID", string([]byte{255}), strings.Repeat("x", 513)} {
		bad := query
		bad.AfterID = invalid
		if bad.Validate() == nil {
			t.Fatal("invalid cursor accepted")
		}
	}
	for _, invalid := range []string{string([]byte{255}), "a\x00b", strings.Repeat("x", 1025)} {
		bad := query
		bad.Contains = invalid
		if bad.Validate() == nil {
			t.Fatal("invalid substring accepted")
		}
	}
	query.AfterID = ""
	query.Contains = ""
	if query.Validate() != nil {
		t.Fatal("empty optional cursor or substring rejected")
	}
}
