package sessions

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

func TestSkillContextLegacySnapshotSerializationCompatibility(t *testing.T) {
	// Workflow-source digests pin these exact pre-attribution snapshot bytes.
	// In particular a new nil field must not appear as SkillContext:null.
	s := Snapshot{TaskID: "task", SessionID: "session", State: "running", Sequence: 1, Pending: map[string]Pending{}}
	const legacy = `{"Compaction":null,"RetryOfTaskID":"","ParentTaskID":"","Privacy":"","TaskID":"task","SessionID":"session","State":"running","Sequence":1,"Messages":null,"MessageSequences":null,"Pending":{},"InterruptedTurn":false,"UncertainEffects":false}`
	body, err := json.Marshal(s)
	if err != nil || string(body) != legacy {
		t.Fatalf("legacy snapshot bytes changed: %s (%v)", body, err)
	}
	for _, complete := range []bool{false, true} {
		s.SkillContext = &runtime.SkillContextUse{Version: 1, Complete: complete}
		body, err = json.Marshal(s)
		var restored Snapshot
		if err != nil || json.Unmarshal(body, &restored) != nil || restored.SkillContext == nil || restored.SkillContext.Complete != complete || !strings.Contains(string(body), `"SkillContext":`) {
			t.Fatal("nonnil attribution lost its existing name or semantics", string(body), err)
		}
	}
}

func TestReplaySkillContextOwnedAndNeverInferred(t *testing.T) {
	events := fixture()
	events[0].Data.SkillContext = &runtime.SkillContextUse{Version: 1, Complete: true, References: []runtime.SkillReference{{Scope: "project", Name: "lookup", Version: strings.Repeat("a", 32), Digest: strings.Repeat("b", 64)}}}
	s, err := Replay(context.Background(), events, "task")
	if err != nil || s.SkillContext == nil || s.SkillContext.References[0].Name != "lookup" {
		t.Fatalf("snapshot=%+v error=%v", s, err)
	}
	events[0].Data.SkillContext.References[0].Name = "source-change"
	if s.SkillContext.References[0].Name != "lookup" {
		t.Fatal("snapshot aliases reader")
	}
	s.SkillContext.References[0].Name = "snapshot-change"
	if events[0].Data.SkillContext.References[0].Name != "source-change" {
		t.Fatal("reader aliases snapshot")
	}
	events[0].Data.SkillContext = nil
	events[0].Data.Messages[0].Content = `{"procedural_skills":[{"scope":"project","name":"lookup"}]}`
	s, err = Replay(context.Background(), events, "task")
	if err != nil || s.SkillContext != nil {
		t.Fatal("message content inferred attribution", err)
	}
	for _, complete := range []bool{false, true} {
		events[0].Data.SkillContext = &runtime.SkillContextUse{Version: 1, Complete: complete}
		s, err = Replay(context.Background(), events, "task")
		if err != nil || s.SkillContext == nil || s.SkillContext.Complete != complete || len(s.SkillContext.References) != 0 {
			t.Fatal("lost empty attribution semantics", err)
		}
	}
}

func TestReplayRejectsMalformedSkillContext(t *testing.T) {
	events := fixture()
	events[0].Data.SkillContext = &runtime.SkillContextUse{Version: 2}
	if _, err := Replay(context.Background(), events, "task"); err == nil {
		t.Fatal("invalid attribution accepted")
	}
	events[0].Data.SkillContext = nil
	events[len(events)-1].Data.SkillContext = &runtime.SkillContextUse{Version: 1, Complete: true}
	if _, err := Replay(context.Background(), events, "task"); err == nil {
		t.Fatal("misplaced attribution accepted")
	}
}
