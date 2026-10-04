package webui

import "testing"

func TestRoutingInspectionRejectsUnpermittedIdentity(t *testing.T) {
	p := RoutingInspection{Rankings: []SpecialistRankingInspection{}, CommanderID: "hidden", CommanderSource: "configured"}
	models := []ModelInspection{{ID: "permitted", Locality: "local"}}
	if p.Validate(models) == nil {
		t.Fatal("commander leaked")
	}
	p.CommanderID = "permitted"
	if p.Validate(models) != nil {
		t.Fatal("permitted commander rejected")
	}
	p.Rankings = []SpecialistRankingInspection{{Key: "coding", Domain: "code", Profile: "default", Models: []SpecialistRankInspection{{ModelID: "hidden", Domain: "code", Profile: "default", Score: .5}}}}
	if p.Validate(models) == nil {
		t.Fatal("ranked model leaked")
	}
}
