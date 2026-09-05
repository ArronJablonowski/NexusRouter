package api

import "testing"

func TestNativeAdditionalSummaryCategories(t *testing.T) {
	for _, category := range []string{"requirements", "activity"} {
		request, err := decodeCompactionRequest([]byte(`{"keep":1,"summary":{"` + category + `":["retained detail"]}}`))
		if err != nil {
			t.Fatalf("category %s rejected: %v", category, err)
		}
		got := request.Summary.Requirements
		if category == "activity" {
			got = request.Summary.Activity
		}
		if len(got) != 1 || got[0] != "retained detail" {
			t.Fatal("category not forwarded")
		}
		for _, body := range []string{`{"` + category + `":null}`, `{"` + category + `":[1]}`, `{"` + category + `":[" "]}`, `{"` + category + `":["a"],"` + category + `":["b"]}`} {
			if _, err := decodeCompactionRequest([]byte(`{"keep":1,"summary":` + body + `}`)); err == nil {
				t.Fatal("invalid category accepted")
			}
		}
	}
}
