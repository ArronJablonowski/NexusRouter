package app

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/codexbridge"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func TestCodexSkillGenerationCredentialRotationFencesDispatch(t *testing.T) {
	for _, boundary := range []string{"estimation", "startup"} {
		t.Run(boundary, func(t *testing.T) {
			svc, tasks, db := codexSkillGenerationFixture(t)
			ctx := context.Background()
			rotated := false
			svc.secret = func(name string) string {
				if name == "DARWIN_API_TOKEN" {
					if rotated {
						return "Another"
					}
					return "private-token"
				}
				return ""
			}
			launches, streams := 0, 0
			directory := ""
			provider := &codexAuditProviderFixture{stream: func(context.Context, providers.Request, func(providers.Chunk) error) error {
				streams++
				return errors.New("unsafe dispatch should have been rejected")
			}}
			svc.contextEstimator = auxiliaryContextEstimator(func(_ context.Context, request providers.Request) (int, error) {
				if !strings.Contains(request.Messages[1].Content, "Another") {
					t.Fatal("rotation fixture absent from admitted source")
				}
				if boundary == "estimation" {
					rotated = true
				}
				return 1, nil
			})
			svc.codexLauncher = func(_ context.Context, spec codexbridge.LaunchSpec) (taskProvider, error) {
				launches++
				directory = spec.CWD
				started, err := db.SkillGenerationAttempt(ctx, "rotating-generation")
				if err != nil || started.Status != "started" {
					t.Fatal("launch without durable claim", started, err)
				}
				rotated = true
				return provider, nil
			}
			key := skills.Key{Scope: "project", Name: "rotation-workflow"}
			if _, err := svc.GenerateSkillDraft(ctx, "rotating-generation", "brain", key, tasks, 0); err == nil {
				t.Fatal("stale sanitized input admitted")
			}
			wantLaunches := 0
			if boundary == "startup" {
				wantLaunches = 1
			}
			if launches != wantLaunches || streams != 0 || provider.closed != wantLaunches {
				t.Fatal("credential fence failed", launches, streams, provider.closed)
			}
			if directory != "" {
				if _, err := os.Stat(directory); !os.IsNotExist(err) {
					t.Fatal("startup directory retained", err)
				}
			}
			failed, err := db.SkillGenerationAttempt(ctx, "rotating-generation")
			if err != nil || failed.Status != "failed" || failed.Result != nil {
				t.Fatal("rotation failure not durable", failed, err)
			}
			if _, err := svc.GenerateSkillDraft(ctx, "rotating-generation", "brain", key, tasks, 0); err == nil || launches != wantLaunches || streams != 0 {
				t.Fatal("rotation retry redispatched", err)
			}
		})
	}
}

func TestCodexSkillGenerationExecutableSecretRejectedBeforeAttempt(t *testing.T) {
	svc, tasks, db := codexSkillGenerationFixture(t)
	executable := svc.settings.Providers[len(svc.settings.Providers)-1].Executable
	svc.secret = func(name string) string {
		if name == "DARWIN_API_TOKEN" {
			return executable
		}
		return ""
	}
	launches := 0
	svc.codexLauncher = func(context.Context, codexbridge.LaunchSpec) (taskProvider, error) {
		launches++
		return nil, errors.New("must not launch")
	}
	if _, err := svc.GenerateSkillDraft(context.Background(), "executable-collision", "brain", skills.Key{Scope: "project", Name: "workflow"}, tasks, 0); err == nil || launches != 0 {
		t.Fatal("executable credential admitted", err, launches)
	}
	if _, err := db.SkillGenerationAttempt(context.Background(), "executable-collision"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("preexisting credential collision persisted attempt", err)
	}
}
