package runtime_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func delegationCompactionAuthorityFixture(t *testing.T) runtime.DelegationCompactionAuthority {
	t.Helper()
	authority, err := runtime.SealDelegationCompactionAuthority(runtime.DelegationCompactionAuthority{
		RootTaskID:            "root-task",
		PlanDigest:            planHash("plan"),
		InheritedEngineDigest: planHash("engine"),
		Scope:                 "delegation-root-task",
		ParentPolicy:          json.RawMessage(` { "tools" : { "write" : "ask", "read" : "allow" }, "limit" : 9007199254740993 } `),
		ChildPolicy:           json.RawMessage(` { "tools" : { "write" : "deny", "read" : "allow" } } `),
		ParentPolicyDigest:    planHash("caller-supplied-parent"),
		ChildPolicyDigest:     planHash("caller-supplied-child"),
		AuthorityDigest:       planHash("caller-supplied-authority"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func delegationCompactionWorkerStart(t *testing.T) runtime.Event {
	t.Helper()
	authority := delegationCompactionAuthorityFixture(t)
	return runtime.Event{
		Version: 1, ID: "work-start", TaskID: "work-task", SessionID: "session", CorrelationID: "work-task",
		WorkerID: "worker", Sequence: 1, Time: time.Unix(100, 0).UTC(), Kind: runtime.TaskStarted,
		Data: runtime.Data{
			ParentTaskID:         authority.RootTaskID,
			DelegationOrigin:     &runtime.DelegationOrigin{Version: 1, TurnID: "turn", AttemptID: "attempt", ToolCallID: "call", ToolName: "delegate"},
			DelegationCompaction: &authority,
		},
	}
}

func TestDelegationCompactionAuthorityCanonicalOwnedAndRoundTrips(t *testing.T) {
	parent := json.RawMessage(` { "tools" : { "write" : "ask", "read" : "allow" }, "limit" : 9007199254740993 } `)
	child := json.RawMessage(` { "tools" : { "write" : "deny", "read" : "allow" } } `)
	authority := delegationCompactionAuthorityFixture(t)
	if authority.Validate() != nil || authority.Version != runtime.DelegationCompactionAuthorityVersion ||
		string(authority.ParentPolicy) != `{"limit":9007199254740993,"tools":{"read":"allow","write":"ask"}}` ||
		string(authority.ChildPolicy) != `{"tools":{"read":"allow","write":"deny"}}` {
		t.Fatalf("authority was not canonically sealed: %+v", authority)
	}
	digest, err := authority.CanonicalDigest()
	if err != nil || digest != authority.AuthorityDigest || authority.ParentPolicyDigest == "" || authority.ChildPolicyDigest == "" {
		t.Fatal(digest, err)
	}
	body, err := json.Marshal(authority)
	if err != nil || len(body) > runtime.MaxDelegationCompactionAuthorityBytes {
		t.Fatal(len(body), err)
	}
	var decoded runtime.DelegationCompactionAuthority
	if json.Unmarshal(body, &decoded) != nil || decoded.Validate() != nil || decoded.AuthorityDigest != authority.AuthorityDigest {
		t.Fatal("authority did not round trip")
	}
	parent[0], child[0] = 'x', 'x'
	if authority.Validate() != nil {
		t.Fatal("sealed authority aliases caller policy bytes")
	}
	clone := authority.Clone()
	clone.ParentPolicy[0], clone.ChildPolicy[0] = 'x', 'x'
	if authority.Validate() != nil || clone.Validate() == nil {
		t.Fatal("clone did not own policy bytes")
	}
}

func TestDelegationCompactionAuthorityRejectsStructuralAndDigestDrift(t *testing.T) {
	mutations := map[string]func(*runtime.DelegationCompactionAuthority){
		"version":          func(a *runtime.DelegationCompactionAuthority) { a.Version++ },
		"root empty":       func(a *runtime.DelegationCompactionAuthority) { a.RootTaskID = "" },
		"root whitespace":  func(a *runtime.DelegationCompactionAuthority) { a.RootTaskID = " root-task" },
		"root control":     func(a *runtime.DelegationCompactionAuthority) { a.RootTaskID = "root\ntask" },
		"root utf8":        func(a *runtime.DelegationCompactionAuthority) { a.RootTaskID = string([]byte{0xff}) },
		"root long":        func(a *runtime.DelegationCompactionAuthority) { a.RootTaskID = strings.Repeat("r", 129) },
		"plan digest":      func(a *runtime.DelegationCompactionAuthority) { a.PlanDigest = planHash("different") },
		"plan uppercase":   func(a *runtime.DelegationCompactionAuthority) { a.PlanDigest = strings.ToUpper(a.PlanDigest) },
		"engine digest":    func(a *runtime.DelegationCompactionAuthority) { a.InheritedEngineDigest = planHash("different") },
		"engine malformed": func(a *runtime.DelegationCompactionAuthority) { a.InheritedEngineDigest = "not-a-digest" },
		"scope":            func(a *runtime.DelegationCompactionAuthority) { a.Scope = "delegation-other" },
		"parent policy":    func(a *runtime.DelegationCompactionAuthority) { a.ParentPolicy[1] = 'x' },
		"parent digest":    func(a *runtime.DelegationCompactionAuthority) { a.ParentPolicyDigest = planHash("different") },
		"child policy":     func(a *runtime.DelegationCompactionAuthority) { a.ChildPolicy[1] = 'x' },
		"child digest":     func(a *runtime.DelegationCompactionAuthority) { a.ChildPolicyDigest = planHash("different") },
		"authority digest": func(a *runtime.DelegationCompactionAuthority) { a.AuthorityDigest = planHash("different") },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			authority := delegationCompactionAuthorityFixture(t)
			mutate(&authority)
			if !errors.Is(authority.Validate(), runtime.ErrDelegationCompactionAuthority) {
				t.Fatal("mutated authority validated")
			}
		})
	}
}

func TestSealDelegationCompactionAuthorityRejectsInvalidPolicyAndEnvelope(t *testing.T) {
	valid := func() runtime.DelegationCompactionAuthority {
		return runtime.DelegationCompactionAuthority{
			RootTaskID: "root", PlanDigest: planHash("plan"), InheritedEngineDigest: planHash("engine"),
			Scope: "delegation-root", ParentPolicy: json.RawMessage(`{"read":"allow"}`), ChildPolicy: json.RawMessage(`{"read":"allow"}`),
		}
	}
	for name, mutate := range map[string]func(*runtime.DelegationCompactionAuthority){
		"empty parent":     func(a *runtime.DelegationCompactionAuthority) { a.ParentPolicy = nil },
		"empty child":      func(a *runtime.DelegationCompactionAuthority) { a.ChildPolicy = nil },
		"scalar":           func(a *runtime.DelegationCompactionAuthority) { a.ParentPolicy = json.RawMessage(`true`) },
		"array":            func(a *runtime.DelegationCompactionAuthority) { a.ChildPolicy = json.RawMessage(`[]`) },
		"null":             func(a *runtime.DelegationCompactionAuthority) { a.ParentPolicy = json.RawMessage(`null`) },
		"trailing":         func(a *runtime.DelegationCompactionAuthority) { a.ParentPolicy = json.RawMessage(`{} {}`) },
		"duplicate top":    func(a *runtime.DelegationCompactionAuthority) { a.ParentPolicy = json.RawMessage(`{"x":1,"x":2}`) },
		"duplicate nested": func(a *runtime.DelegationCompactionAuthority) { a.ChildPolicy = json.RawMessage(`{"x":{"y":1,"y":2}}`) },
		"oversize": func(a *runtime.DelegationCompactionAuthority) {
			a.ParentPolicy = json.RawMessage(`{"x":"` + strings.Repeat("x", runtime.MaxDelegationCompactionAuthorityBytes) + `"}`)
		},
		"bad root": func(a *runtime.DelegationCompactionAuthority) { a.RootTaskID = " root" },
		"bad plan": func(a *runtime.DelegationCompactionAuthority) { a.PlanDigest = "bad" },
		"bad engine": func(a *runtime.DelegationCompactionAuthority) {
			a.InheritedEngineDigest = strings.ToUpper(a.InheritedEngineDigest)
		},
		"bad scope": func(a *runtime.DelegationCompactionAuthority) { a.Scope = "delegation-foreign" },
	} {
		t.Run(name, func(t *testing.T) {
			input := valid()
			mutate(&input)
			if _, err := runtime.SealDelegationCompactionAuthority(input); !errors.Is(err, runtime.ErrDelegationCompactionAuthority) {
				t.Fatal("invalid authority sealed", err)
			}
		})
	}
}

func TestDelegationCompactionAuthorityEventPlacement(t *testing.T) {
	valid := delegationCompactionWorkerStart(t)
	if valid.Validate() != nil {
		t.Fatal("valid worker start rejected")
	}
	for name, mutate := range map[string]func(*runtime.Event){
		"not task start": func(e *runtime.Event) { e.Kind = runtime.WorkerStarted },
		"no worker":      func(e *runtime.Event) { e.WorkerID = "" },
		"wrong parent":   func(e *runtime.Event) { e.Data.ParentTaskID = "other-root" },
		"no origin":      func(e *runtime.Event) { e.Data.DelegationOrigin = nil },
		"bad origin":     func(e *runtime.Event) { e.Data.DelegationOrigin.ToolName = "shell" },
		"bad authority":  func(e *runtime.Event) { e.Data.DelegationCompaction.Scope = "delegation-other" },
	} {
		t.Run(name, func(t *testing.T) {
			event, err := valid.Clone()
			if err != nil {
				t.Fatal(err)
			}
			mutate(&event)
			if event.Validate() == nil {
				t.Fatal("invalid authority placement accepted")
			}
		})
	}
	clone, err := valid.Clone()
	if err != nil {
		t.Fatal(err)
	}
	clone.Data.DelegationCompaction.ParentPolicy[0] = 'x'
	if valid.Validate() != nil || clone.Validate() == nil {
		t.Fatal("event clone aliases delegation authority")
	}
}
