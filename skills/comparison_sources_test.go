package skills

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestComparisonSourcesValidationAndLegacyEncoding(t *testing.T) {
	r := selectionFixtureReport(t)
	body, _ := json.Marshal(r)
	if strings.Contains(string(body), `"sources"`) {
		t.Fatal("legacy changed")
	}
	r.Sources = &ComparisonSources{Version: 1, Tasks: make([]string, 40)}
	for i := range r.Sources.Tasks {
		r.Sources.Tasks[i] = fmt.Sprintf("task-%03d", i)
	}
	if r.Validate() != nil {
		t.Fatal(r)
	}
	for _, tasks := range [][]string{nil, {}, {"z", "a"}, {"a", "a"}, {"bad/id"}, make([]string, 201)} {
		r.Sources.Tasks = tasks
		if r.Validate() == nil {
			t.Fatal("invalid sources accepted", tasks)
		}
	}
	if (ComparisonSources{Version: 1, Tasks: []string{}}).Validate() != nil {
		t.Fatal("known empty rejected")
	}
	if (ComparisonSources{Version: 2, Tasks: []string{}}).Validate() == nil {
		t.Fatal("unknown version")
	}
}
