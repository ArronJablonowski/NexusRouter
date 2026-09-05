package config

import (
	"math"
	"testing"
)

func delegationSettings() Settings {
	s := Defaults()
	zero := 0.0
	s.Providers = []Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:11434"}}
	s.Models = []Model{{ID: "worker", Provider: "local", Model: "worker", Locality: "local", Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero, RAMBytes: 1}}
	s.Workers.DelegateModel = "worker"
	return s
}

func TestDelegationConfigDefaultsAndValidation(t *testing.T) {
	defaults := Defaults()
	if defaults.Workers.DelegateModel != "" || defaults.Workers.DelegateMaxCalls != 4 || defaults.Workers.DelegateMaxCost != 0 || defaults.Validate() != nil {
		t.Fatal(defaults.Workers)
	}
	for _, mode := range []string{"valid", "callsmin", "callsmax", "zerocalls", "manycalls", "negativecost", "nancost", "infcost", "missingmodel", "invalidmodel", "context", "unknowncost", "expensive", "heartbeat", "leaseequal", "leasemax", "leaselong", "localmode", "cloudmode"} {
		t.Run(mode, func(t *testing.T) {
			s := delegationSettings()
			valid := false
			switch mode {
			case "valid":
				valid = true
			case "callsmin":
				s.Workers.DelegateMaxCalls = 1
				valid = true
			case "callsmax":
				s.Workers.DelegateMaxCalls = 16
				valid = true
			case "zerocalls":
				s.Workers.DelegateMaxCalls = 0
			case "manycalls":
				s.Workers.DelegateMaxCalls = 17
			case "negativecost":
				s.Workers.DelegateMaxCost = -1
			case "nancost":
				s.Workers.DelegateMaxCost = math.NaN()
			case "infcost":
				s.Workers.DelegateMaxCost = math.Inf(1)
			case "missingmodel":
				s.Workers.DelegateModel = "missing"
			case "invalidmodel":
				s.Workers.DelegateModel = "../worker"
			case "context":
				s.Models[0].ContextTokens = 0
			case "unknowncost":
				s.Models[0].EstimatedCost = nil
			case "expensive":
				cost := 0.01
				s.Models[0].EstimatedCost = &cost
			case "heartbeat":
				s.Workers.Heartbeat = "1us"
			case "leaseequal":
				s.Workers.Heartbeat = "5s"
				s.Workers.Lease = "10s"
			case "leasemax":
				s.Workers.Lease = "10m"
				valid = true
			case "leaselong":
				s.Workers.Lease = "11m"
			case "localmode":
				s.Mode = "local_only"
				s.Models[0].Locality = "cloud"
			case "cloudmode":
				s.Mode = "cloud_only"
			}
			if err := s.Validate(); (err == nil) != valid {
				t.Fatal(mode, err)
			}
		})
	}
}

func TestDelegationConfigLayeredScalars(t *testing.T) {
	project := file(t, "workers:\n  delegate_max_calls: 2\n  delegate_max_cost: 0.25\n")
	s, err := Load(Options{ProjectFile: project, Env: map[string]string{"workers.delegate_max_calls": "3", "workers.delegate_max_cost": "0.5"}, Flags: map[string]string{"workers.delegate_max_calls": "16", "workers.delegate_max_cost": "0"}})
	if err != nil || s.Workers.DelegateMaxCalls != 16 || s.Workers.DelegateMaxCost != 0 {
		t.Fatal(s.Workers, err)
	}
	for _, value := range []string{"0", "17", "1.0", "'4'", "true", "999999999999999999999"} {
		if _, err := Load(Options{ProjectFile: file(t, "workers:\n  delegate_max_calls: "+value+"\n")}); err == nil {
			t.Fatal("accepted calls", value)
		}
	}
	for _, value := range []string{"-1", ".nan", ".inf"} {
		if _, err := Load(Options{ProjectFile: file(t, "workers:\n  delegate_max_cost: "+value+"\n")}); err == nil {
			t.Fatal("accepted cost", value)
		}
	}
}

func TestDelegationSupervisorBoundsOnlyWhenEnabled(t *testing.T) {
	s := delegationSettings()
	s.Workers.Heartbeat = "1ms"
	s.Workers.Lease = "3ms"
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.Workers.DelegateModel = ""
	s.Workers.Heartbeat = "1us"
	s.Workers.Lease = "2us"
	if err := s.Validate(); err != nil {
		t.Fatal("disabled delegation changed legacy timing", err)
	}
}
