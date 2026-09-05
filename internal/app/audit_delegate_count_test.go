package app

import (
	"strings"
	"testing"
)

func TestAuditBatchTaskCountRejectsAmbiguousArguments(t *testing.T) {
	const task = `{"validation":"text","prompt":"Do work"}`
	valid := `{"tasks":[` + task + `,` + task + `]}`
	if count, err := auditBatchTaskCount([]byte(valid)); err != nil || count != 2 {
		t.Fatal("valid reordered model arguments rejected", count, err)
	}
	for _, raw := range []string{
		strings.Replace(valid, `"tasks"`, `"Tasks"`, 1),
		strings.Replace(valid, `"tasks":`, `"tasks":[],"tasks":`, 1),
		strings.Replace(valid, `"prompt":`, `"prompt":"hidden","prompt":`, 1),
		strings.Replace(valid, `"prompt"`, `"Prompt"`, 1),
		strings.Replace(valid, `"prompt":`, `"extra":true,"prompt":`, 1),
		strings.Replace(valid, `"Do work"`, `null`, 1),
		strings.Replace(valid, `"text"`, `"unknown"`, 1),
		valid + `{}`,
		`{"tasks":[` + task + `]}`,
		`{"tasks":null}`,
		strings.Repeat(" ", (512<<10)+1),
	} {
		if count, err := auditBatchTaskCount([]byte(raw)); err == nil {
			t.Fatalf("ambiguous or invalid arguments accepted: count=%d", count)
		}
	}
}
