package codexbridge

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/internal/codexrpc"
	"os"
	"strconv"
)

var ErrCredentialsMissing = errors.New("codex credentials unavailable")

// HealthModels performs only initialization, account/read and model/list.
// It never creates a thread, starts a turn, refreshes login, or sends a prompt.
// The caller supplies a deadline and owns cloud-policy admission.
func HealthModels(ctx context.Context, executable string) ([]string, error) {
	dir, err := os.MkdirTemp("", "darwin-codex-health-")
	if err != nil {
		return nil, ErrLaunchObservation
	}
	defer os.RemoveAll(dir)
	env := []string{}
	for _, name := range []string{"HOME", "PATH", "TMPDIR", "CODEX_HOME"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	spec := codexrpc.ProcessSpec{Executable: executable, Dir: dir, Env: env}
	version, err := launchMetadata(ctx, spec, "--version")
	if err != nil {
		return nil, ErrLaunchObservation
	}
	inventory, err := launchMetadata(ctx, spec, "features", "list")
	if err != nil {
		return nil, ErrLaunchObservation
	}
	_, spec.Args, err = launchProfile(version, inventory)
	if err != nil {
		return nil, err
	}
	w, err := codexrpc.StartProcess(ctx, spec)
	if err != nil {
		return nil, ErrLaunchObservation
	}
	defer w.Close()
	return healthModels(ctx, w, dir)
}

func healthModels(ctx context.Context, w Wire, dir string) ([]string, error) {
	s, err := NewSession(ctx, w, Options{Model: "health-discovery", CWD: dir})
	if err != nil {
		return nil, err
	}
	defer s.Close()
	s.allowDisabledStatus = true
	if err = s.Prepare(ctx); err != nil {
		return nil, err
	}
	raw, err := s.call("2", "account/read", map[string]any{"refreshToken": false})
	if err != nil {
		return nil, err
	}
	var account struct {
		Account *struct {
			Type string `json:"type"`
		} `json:"account"`
	}
	if json.Unmarshal(raw, &account) != nil {
		return nil, failure(false)
	}
	if account.Account == nil {
		return nil, ErrCredentialsMissing
	}
	if account.Account.Type != "chatgpt" && account.Account.Type != "apiKey" {
		return nil, failure(false)
	}
	names := []string{}
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 16; page++ {
		params := map[string]any{"limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err = s.call(strconv.Itoa(page+3), "model/list", params)
		if err != nil {
			return nil, err
		}
		var result struct {
			Data []struct {
				Model string `json:"model"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if json.Unmarshal(raw, &result) != nil || result.Data == nil || len(result.Data) > 100 {
			return nil, failure(false)
		}
		for _, model := range result.Data {
			if model.Model == "" || len(model.Model) > 128 {
				return nil, failure(false)
			}
			names = append(names, model.Model)
		}
		if result.NextCursor == nil {
			return names, nil
		}
		cursor = *result.NextCursor
		if cursor == "" || len(cursor) > 4096 || seen[cursor] {
			return nil, failure(false)
		}
		seen[cursor] = true
	}
	return nil, failure(false)
}
