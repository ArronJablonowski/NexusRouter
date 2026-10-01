package wirejson

import "testing"

func TestUniqueRejectsDecoderAliasesAndMalformedInput(t *testing.T) {
	for _, body := range []string{
		`{"status":"ok","status":"error"}`,
		`{"status":"ok","STATUS":"error"}`,
		`{"status":"ok","ſtatus":"error"}`,
		`{"key":1,"Key":2}`,
		`{"status":"ok","\u0073tatus":"error"}`,
		`{"nested":{"a":1,"A":2}}`,
		`{} {}`, `{"key":`, "{\"key\":\"\xff\"}",
	} {
		if Unique([]byte(body)) {
			t.Fatalf("accepted ambiguity %q", body)
		}
	}
	if !Unique([]byte(`{"nested":[{"key":1},{"key":2}],"other":true}`)) {
		t.Fatal("rejected independent object keys")
	}
}
