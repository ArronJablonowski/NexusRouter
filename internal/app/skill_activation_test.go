package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

func TestSkillActivationPublishedVersionAndRestart(t *testing.T) {
	svc, a := publicationFixture(t)
	ctx := context.Background()
	v, err := svc.PublishSkillGeneration(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.SkillActivationState(ctx, a.Key)
	if err != nil || state.Active != "" || state.Validate() != nil {
		t.Fatal(state, err)
	}
	calls := 0
	validator := skills.ValidatorFunc(func(ctx context.Context, candidate skills.Version) (skills.Evidence, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 3*time.Second {
			t.Error("unbounded validation")
		}
		if !reflect.DeepEqual(candidate, v) {
			t.Error("wrong candidate")
		}
		return skills.Evidence{ID: "objective-check", Passed: true, Deterministic: true}, nil
	})
	if err := svc.ActivateSkillVersion(ctx, state, v.ID, validator); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	reopened, err := NewService(svc.settings, svc.secret)
	if err != nil {
		t.Fatal(err)
	}
	after, err := reopened.SkillActivationState(ctx, a.Key)
	if err != nil || after.Active != v.ID || after.Revision == state.Revision {
		t.Fatal(after, err)
	}
	if err := reopened.ActivateSkillVersion(ctx, state, v.ID, validator); err == nil || calls != 1 {
		t.Fatal("stale state invoked validator", err, calls)
	}
	ro, err := skills.OpenReadOnly(svc.settings.Skills.Root, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	loaded, err := ro.Load(ctx, a.Key, "")
	if err != nil || !reflect.DeepEqual(loaded, v) {
		t.Fatal("active version not resumable", loaded, err)
	}
	metadata, err := ro.Discover(ctx, "project", []string{"creative"}, 3)
	if err != nil || len(metadata) != 1 || metadata[0].Version != v.ID {
		t.Fatal("activated skill not discoverable", metadata, err)
	}
	received := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		received <- string(body)
		fmt.Fprintln(w, `{"message":{"content":"Used the workflow"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	svc.settings.Providers[0].Endpoint = server.URL
	if _, err := svc.Run(ctx, Request{ModelID: "a", Prompt: "Write another creative response", Domain: "creative"}); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-received:
		if !strings.Contains(body, "Publication workflow") || !strings.Contains(body, "Inspect requirements") {
			t.Fatal("active skill absent from runtime context")
		}
	default:
		t.Fatal("no provider request")
	}
}

func TestSkillActivationDenialsAndEvidenceDoNotMutate(t *testing.T) {
	for _, mode := range []string{"scope", "disabled", "automatic", "injected", "nil-context", "nil-validator", "canceled", "secret-version", "secret-proof", "rotated-proof", "old-secret-proof", "judge-only", "failed-proof", "error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			svc, a := publicationFixture(t)
			ctx := context.Background()
			v, err := svc.PublishSkillGeneration(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			state, err := svc.SkillActivationState(ctx, a.Key)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(svc.settings.Skills.Root, "catalog.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			var validator skills.Validator = skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) {
				calls++
				e := skills.Evidence{ID: "objective-check", Passed: true, Deterministic: true}
				switch mode {
				case "old-secret-proof":
					e.ID = "runtime-token"
					svc.secret = func(name string) string {
						if name == "DARWIN_API_TOKEN" {
							return "new-secret"
						}
						return ""
					}
				case "rotated-proof":
					svc.secret = func(name string) string {
						if name == "DARWIN_API_TOKEN" {
							return "objective-check"
						}
						return ""
					}
				case "secret-proof":
					e.ID = "runtime-token"
				case "judge-only":
					e.Deterministic = false
				case "failed-proof":
					e.Passed = false
				case "error":
					return e, errors.New("private-validator-error")
				case "panic":
					panic("private-validator-error")
				}
				return e, nil
			})
			switch mode {
			case "scope":
				state.Key.Scope = "other"
			case "disabled":
				svc.settings.Skills.Enabled = false
			case "automatic":
				svc.settings.Skills.AutoActivate = false
			case "injected":
				svc.skillStore, _ = contextSkillStore(t)
			case "nil-context":
				ctx = nil
			case "nil-validator":
				validator = nil
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "secret-version":
				svc.secret = func(string) string { return "Publication workflow" }
			case "secret-proof", "old-secret-proof":
				svc.secret = func(name string) string {
					if name == "DARWIN_API_TOKEN" {
						return "runtime-token"
					}
					return ""
				}
			}
			err = svc.ActivateSkillVersion(ctx, state, v.ID, validator)
			if err == nil || strings.Contains(err.Error(), "private-validator-error") || strings.Contains(err.Error(), "runtime-token") {
				t.Fatal("unsafe activation failure", err)
			}
			wantCalls := 0
			switch mode {
			case "secret-proof", "rotated-proof", "old-secret-proof", "judge-only", "failed-proof", "error", "panic":
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatal("unexpected validation callback", calls, wantCalls)
			}
			after, err := os.ReadFile(path)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("rejected activation changed catalog", err)
			}
		})
	}
}

func TestSkillActivationMissingStoreAndNilService(t *testing.T) {
	svc, a := publicationFixture(t)
	state := skills.ActivationState{Version: 1, Key: a.Key, Revision: strings.Repeat("a", 64)}
	calls := 0
	validator := skills.ValidatorFunc(func(context.Context, skills.Version) (skills.Evidence, error) { calls++; return skills.Evidence{}, nil })
	if _, err := svc.SkillActivationState(context.Background(), a.Key); err == nil {
		t.Fatal("missing state accepted")
	}
	if err := svc.ActivateSkillVersion(context.Background(), state, strings.Repeat("a", 32), validator); err == nil || calls != 0 {
		t.Fatal("missing store invoked callback")
	}
	if _, err := os.Stat(svc.settings.Skills.Root); !os.IsNotExist(err) {
		t.Fatal("activation initialized root", err)
	}
	var absent *Service
	if _, err := absent.SkillActivationState(context.Background(), a.Key); err == nil {
		t.Fatal("nil service state accepted")
	}
	if err := absent.ActivateSkillVersion(context.Background(), state, strings.Repeat("a", 32), validator); err == nil {
		t.Fatal("nil service activation accepted")
	}
}
