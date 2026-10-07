package remote

import (
	"github.com/ArronJablonowski/NexusRouter/webui"
	"testing"
)

func TestInfoModelInspectionIdentity(t *testing.T) {
	d := &webui.ModelInspection{ID: "m", Provider: "p", Model: "worker", Locality: "local", Capabilities: []string{}, Health: "unknown"}
	i := Info{Models: []Model{{ID: "m", Provider: "p", Model: "worker", Local: true, Inspection: d}}}
	if err := i.ValidateRouting(); err != nil {
		t.Fatal(err)
	}
	d.ID = "other"
	if i.ValidateRouting() == nil {
		t.Fatal("accepted metadata for another model")
	}
	d.ID = "m"
	d.Health = "invented"
	if i.ValidateRouting() == nil {
		t.Fatal("accepted invalid health")
	}
}
