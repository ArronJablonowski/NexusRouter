package main

import (
	"strings"
	"testing"
	"time"
)

const fixturePackage = "github.com/ArronJablonowski/DarwinRouter/internal/releasepack"

func TestAnalyzeReportsSlowestTopLevelTestsAndMargin(t *testing.T) {
	stream := strings.Join([]string{
		`{"Action":"run","Package":"` + fixturePackage + `","Test":"TestSlow"}`,
		`{"Action":"pass","Package":"` + fixturePackage + `","Test":"TestSlow/sub","Elapsed":3.25}`,
		`{"Action":"pass","Package":"` + fixturePackage + `","Test":"TestSlow","Elapsed":8.5}`,
		`{"Action":"pass","Package":"` + fixturePackage + `","Test":"TestRequired","Elapsed":1.25}`,
		`{"Action":"pass","Package":"` + fixturePackage + `","Elapsed":12}`,
	}, "\n")
	report, err := analyze(strings.NewReader(stream), fixturePackage, 45*time.Minute, 2, []string{"TestRequired"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"elapsed=12s timeout=45m0s margin=44m48s passed=2 expected_separate_gate_skips=0",
		"1\t8.5s\tTestSlow", "2\t1.25s\tTestRequired",
	} {
		if !strings.Contains(report, expected) {
			t.Fatalf("report %q missing %q", report, expected)
		}
	}
}

func TestAnalyzeFailsClosedOnIncompleteFailedSkippedOrMissingEvidence(t *testing.T) {
	tests := map[string]string{
		"malformed":        `not-json`,
		"wrong package":    `{"Action":"pass","Package":"other","Elapsed":1}`,
		"no package pass":  `{"Action":"pass","Package":"` + fixturePackage + `","Test":"TestRequired","Elapsed":1}`,
		"failed":           `{"Action":"fail","Package":"` + fixturePackage + `","Test":"TestRequired","Elapsed":1}` + "\n" + `{"Action":"pass","Package":"` + fixturePackage + `","Elapsed":2}`,
		"unexpected skip":  `{"Action":"skip","Package":"` + fixturePackage + `","Test":"TestRequired","Elapsed":0}` + "\n" + `{"Action":"pass","Package":"` + fixturePackage + `","Elapsed":2}`,
		"nested skipped":   `{"Action":"skip","Package":"` + fixturePackage + `","Test":"TestRequired/case","Elapsed":0}` + "\n" + `{"Action":"pass","Package":"` + fixturePackage + `","Test":"TestRequired","Elapsed":1}` + "\n" + `{"Action":"pass","Package":"` + fixturePackage + `","Elapsed":2}`,
		"missing required": `{"Action":"pass","Package":"` + fixturePackage + `","Test":"TestOther","Elapsed":1}` + "\n" + `{"Action":"pass","Package":"` + fixturePackage + `","Elapsed":2}`,
		"timeout exhausted": `{"Action":"pass","Package":"` + fixturePackage + `","Test":"TestRequired","Elapsed":1}` + "\n" +
			`{"Action":"pass","Package":"` + fixturePackage + `","Elapsed":2700}`,
	}
	for name, stream := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := analyze(strings.NewReader(stream), fixturePackage, 45*time.Minute, 10, []string{"TestRequired"}, nil); err == nil {
				t.Fatal("unsafe profile evidence accepted")
			}
		})
	}
}

func TestAnalyzeRequiresExactSeparateGateSkipSet(t *testing.T) {
	stream := `{"Action":"skip","Package":"` + fixturePackage + `","Test":"TestReleaseQualification","Elapsed":0}` + "\n" +
		`{"Action":"pass","Package":"` + fixturePackage + `","Test":"TestRequired","Elapsed":1}` + "\n" +
		`{"Action":"pass","Package":"` + fixturePackage + `","Elapsed":2}`
	if _, err := analyze(strings.NewReader(stream), fixturePackage, 45*time.Minute, 10, []string{"TestRequired"}, []string{"TestReleaseQualification"}); err != nil {
		t.Fatal(err)
	}
	if _, err := analyze(strings.NewReader(stream), fixturePackage, 45*time.Minute, 10, []string{"TestRequired"}, []string{"TestOther"}); err == nil {
		t.Fatal("mismatched separate-gate skip accepted")
	}
}

func TestSplitList(t *testing.T) {
	if got := strings.Join(splitList("TestA, TestB"), ","); got != "TestA,TestB" {
		t.Fatal(got)
	}
}
