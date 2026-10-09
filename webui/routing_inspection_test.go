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

func TestRemoteModelProvenanceRejectsHostMarkup(t *testing.T) {
	model := ModelInspection{ID: "paired-coder", Provider: "paired-coder", Model: "Coder", RemoteInstance: "spark", Hostname: "spark-host", Locality: "local", Configured: true, Enabled: true, Usable: true, Health: "healthy", Capabilities: []string{"code"}}
	if model.Validate() != nil {
		t.Fatal("remote provenance rejected")
	}
	for _, host := range []string{"<img src=x>", "host name", "host/secret", "host\nname"} {
		model.Hostname = host
		if model.Validate() == nil {
			t.Fatal("unsafe hostname accepted", host)
		}
	}
	model.Hostname = "spark-host"
	model.RemoteInstance = ""
	if model.Validate() == nil {
		t.Fatal("host without paired identity accepted")
	}
}
