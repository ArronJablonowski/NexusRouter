package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

type contextEngineStore struct {
	skills.Store
	discover func(context.Context, string, []string, int) ([]skills.Metadata, error)
	load     func(context.Context, skills.Key, string) (skills.Version, error)
}

func (s contextEngineStore) Discover(ctx context.Context, scope string, tags []string, limit int) ([]skills.Metadata, error) {
	if s.discover != nil {
		return s.discover(ctx, scope, tags, limit)
	}
	return s.Store.Discover(ctx, scope, tags, limit)
}
func (s contextEngineStore) Load(ctx context.Context, key skills.Key, id string) (skills.Version, error) {
	if s.load != nil {
		return s.load(ctx, key, id)
	}
	return s.Store.Load(ctx, key, id)
}

func TestSkillEngineSuccessRedactionAndCallerOwnership(t *testing.T) {
	store, settings := contextSkillStore(t)
	version := seedContextSkill(t, store, "project", "workflow", "code", "Use private-marker safely", []string{"read_file"}, true)
	metadata, err := store.Discover(context.Background(), "project", []string{"code"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	originalTags := append([]string{}, metadata[0].Tags...)
	originalSteps := append([]string{}, version.Draft.Steps...)
	discoverCalls, loadCalls := 0, 0
	custom := contextEngineStore{Store: store, discover: func(ctx context.Context, scope string, tags []string, limit int) ([]skills.Metadata, error) {
		discoverCalls++
		if scope != "project" || !reflect.DeepEqual(tags, []string{"code"}) || limit != settings.MaxSkills {
			t.Fatal(scope, tags, limit)
		}
		return metadata, nil
	}, load: func(ctx context.Context, key skills.Key, id string) (skills.Version, error) {
		loadCalls++
		if key != version.Draft.Key || id != version.ID {
			t.Fatal("version not pinned")
		}
		return version, nil
	}}
	tools, secrets := []string{"read_file"}, []string{"private-marker"}
	result, err := loadSkillContextFrom(context.Background(), custom, settings, "code", tools, secrets)
	if err != nil {
		t.Fatal(err)
	}
	workflows := readContextSkills(t, result)
	if discoverCalls != 1 || loadCalls != 1 || len(workflows) != 1 || strings.Contains(result.Messages[1].Content, "private-marker") || !result.LocalOnly {
		t.Fatal(result, discoverCalls, loadCalls)
	}
	if !reflect.DeepEqual(originalTags, metadata[0].Tags) || !reflect.DeepEqual(originalSteps, version.Draft.Steps) || tools[0] != "read_file" || secrets[0] != "private-marker" {
		t.Fatal("caller data mutated")
	}
	result.Messages[0].Content = "caller changed result"
	if version.Draft.Steps[0] != "Use private-marker safely" {
		t.Fatal("returned alias")
	}
}

func TestSkillEngineDisabledAndUnavailableTools(t *testing.T) {
	store, settings := contextSkillStore(t)
	seedContextSkill(t, store, "project", "workflow", "general", "workflow", []string{"shell"}, true)
	custom := contextEngineStore{Store: store, discover: func(context.Context, string, []string, int) ([]skills.Metadata, error) {
		t.Fatal("disabled store called")
		return nil, nil
	}}
	settings.Enabled = false
	if out, err := loadSkillContextFrom(context.Background(), custom, settings, "", nil, nil); err != nil || out != nil {
		t.Fatal(out, err)
	}
	settings.Enabled = true
	if out, err := loadSkillContextFrom(context.Background(), contextEngineStore{Store: store}, settings, "", []string{"read_file"}, nil); err != nil || out != nil {
		t.Fatal("unavailable tool included", out, err)
	}
}

func TestSkillEngineRejectsMalformedCustomStore(t *testing.T) {
	for _, mode := range []string{"scope", "digest", "version", "description", "domain", "duplicates", "overlimit", "loadversion", "loadbody", "discovererror", "loaderror", "discoverpanic", "loadpanic"} {
		t.Run(mode, func(t *testing.T) {
			store, settings := contextSkillStore(t)
			version := seedContextSkill(t, store, "project", "workflow", "code", "workflow", nil, true)
			metadata, err := store.Discover(context.Background(), "project", []string{"code"}, 3)
			if err != nil {
				t.Fatal(err)
			}
			custom := contextEngineStore{Store: store, discover: func(context.Context, string, []string, int) ([]skills.Metadata, error) {
				switch mode {
				case "scope":
					metadata[0].Key.Scope = "other"
				case "digest":
					metadata[0].Digest = "invalid"
				case "version":
					metadata[0].Version = "invalid"
				case "description":
					metadata[0].Description = "different"
				case "domain":
					metadata[0].Tags = []string{"other"}
				case "duplicates":
					metadata = append(metadata, metadata[0])
				case "overlimit":
					metadata = append(metadata, metadata[0], metadata[0], metadata[0])
				case "discovererror":
					return nil, errors.New("private store error")
				case "discoverpanic":
					panic("private store panic")
				}
				return metadata, nil
			}, load: func(context.Context, skills.Key, string) (skills.Version, error) {
				switch mode {
				case "loadversion":
					version.ID = strings.Repeat("b", 32)
				case "loadbody":
					version.Draft.Steps = []string{"changed body"}
				case "loaderror":
					return skills.Version{}, errors.New("private load error")
				case "loadpanic":
					panic("private load panic")
				}
				return version, nil
			}}
			out, err := loadSkillContextFrom(context.Background(), custom, settings, "code", nil, nil)
			if err == nil || out != nil || strings.Contains(err.Error(), "private") {
				t.Fatal(out, err)
			}
		})
	}
}

func TestSkillEngineCancellation(t *testing.T) {
	store, settings := contextSkillStore(t)
	version := seedContextSkill(t, store, "project", "workflow", "code", "workflow", nil, true)
	metadata, err := store.Discover(context.Background(), "project", []string{"code"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"before", "discover", "load"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if phase == "before" {
				cancel()
			}
			custom := contextEngineStore{Store: store, discover: func(context.Context, string, []string, int) ([]skills.Metadata, error) {
				if phase == "before" {
					t.Fatal("called canceled store")
				}
				if phase == "discover" {
					cancel()
				}
				return metadata, nil
			}, load: func(context.Context, skills.Key, string) (skills.Version, error) {
				if phase == "load" {
					cancel()
				}
				return version, nil
			}}
			out, err := loadSkillContextFrom(ctx, custom, settings, "code", nil, nil)
			if err == nil || out != nil {
				t.Fatal("canceled context accepted", out, err)
			}
		})
	}
}
