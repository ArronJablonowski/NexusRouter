package app

import (
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"testing"
	"time"
)

func TestRemoteCommanderAuthorityAndBounds(t *testing.T) {
	zero := 0.0
	cfg := config.Settings{}
	cfg.WebUI.DefaultModel = "commander"
	cfg.Workers.DelegateMaxCalls = 4
	cfg.Models = []config.Model{{ID: "commander", Locality: "local", ContextTokens: 8192, EstimatedCost: &zero}, {ID: "worker", Locality: "local", ContextTokens: 8192, EstimatedCost: &zero}}
	s := &Service{settings: cfg}
	r := Request{ModelID: "commander", LocalRequired: true, ContextTokens: 4096, RemoteExecution: &runtime.RemoteExecution{Mode: "commander", Depth: 1, SpecialistIDs: []string{"worker"}, MaxCalls: 2, Deadline: time.Now().Add(time.Minute)}}
	if s.checkRemoteExecution(r) != nil {
		t.Fatal("bounded request rejected")
	}
	for _, change := range []func(*Request){
		func(r *Request) { r.ModelID = "worker" },
		func(r *Request) { r.LocalRequired = false },
		func(r *Request) { r.MaxCost = 1 },
		func(r *Request) { r.RemoteExecution.MaxCalls = 5 },
		func(r *Request) { r.RemoteExecution.SpecialistIDs = []string{"unpermitted"} },
		func(r *Request) { r.RemoteExecution.Deadline = time.Now().Add(-time.Second) },
	} {
		bad := r
		x := *r.RemoteExecution
		bad.RemoteExecution = &x
		change(&bad)
		if s.checkRemoteExecution(bad) == nil {
			t.Fatal("authority or bounds bypass")
		}
	}
	direct := remoteExecutionSettings(cfg, Request{RemoteExecution: &runtime.RemoteExecution{Mode: "direct", Depth: 1}})
	if direct.Workers.DelegateModel != "" {
		t.Fatal("direct gained delegation")
	}
}
