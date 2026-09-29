package webui

import (
	"fmt"
	"testing"
)

func TestSpecialistRankingContractFourteenCardsAndMeasuredEvidence(t *testing.T) {
	models := []ModelInspection{{ID: "m"}}
	rows := []SpecialistRankingInspection{}
	for i := range 14 {
		key := fmt.Sprintf("category%d", i)
		rows = append(rows, SpecialistRankingInspection{Key: key, Domain: key, Profile: "benchmark-v1", RequiresEvidence: true, Models: []SpecialistRankInspection{}})
	}
	if err := validateSpecialistRankings(rows, models, Available); err != nil {
		t.Fatal("expanded grid rejected", err)
	}
	overflow := append(append([]SpecialistRankingInspection{}, rows...), SpecialistRankingInspection{Key: "overflow", Domain: "overflow", Profile: "default", Models: []SpecialistRankInspection{}})
	if validateSpecialistRankings(overflow, models, Available) == nil {
		t.Fatal("unbounded grid accepted")
	}
	rows[0].Models = []SpecialistRankInspection{{ModelID: "m", Domain: rows[0].Domain, Profile: rows[0].Profile, Score: 0.9}}
	if validateSpecialistRankings(rows, models, Available) == nil {
		t.Fatal("prior accepted as measured evidence")
	}
	rows[0].Models[0].Samples = 1
	if err := validateSpecialistRankings(rows, models, Available); err != nil {
		t.Fatal(err)
	}
}
