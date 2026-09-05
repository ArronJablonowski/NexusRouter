package api

import "testing"

func TestDeprecationRejectsInvalidUTF8BeforeJSONReplacement(t *testing.T) {
	body := append([]byte(`{"version":1,"model_id":"model`), 255)
	body = append(body, []byte(`","domain":"code","profile":"default","policy":{"window":50,"min_samples":20,"failure_threshold":0.35}}`)...)
	if _, err := decodeDeprecation(body); err == nil {
		t.Fatal("malformed identifier replaced and accepted")
	}
}
