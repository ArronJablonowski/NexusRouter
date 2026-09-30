package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func runSkillComparisonSelection(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	path := ""
	if len(args) == 2 && args[0] == "--config" {
		path = args[1]
	} else if len(args) == 1 {
		path, _ = strings.CutPrefix(args[0], "--config=")
		if !strings.HasPrefix(args[0], "--config=") {
			path = ""
		}
	}
	if path == "" {
		return skillsError(stderr, "usage: nexus skills compare-select --config path < request.json", 2)
	}
	request, err := decodeSkillComparisonSelection(stdin)
	if err != nil {
		return skillsError(stderr, "skills compare-select: invalid request", 2)
	}
	cfg, err := config.Load(config.Options{ProjectFile: path, Env: config.Environment(os.Environ())})
	if err != nil {
		return skillsError(stderr, "skills compare-select: configuration unavailable", 1)
	}
	service, err := app.NewService(cfg, os.Getenv)
	if err != nil {
		return skillsError(stderr, "skills compare-select: configuration unavailable", 1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	report, err := service.SelectSkillComparison(ctx, request)
	if err != nil {
		return skillsError(stderr, "skills compare-select: evidence unavailable", 1)
	}
	return writeSubmissionJSON(ctx, stdout, report)
}

func decodeSkillComparisonSelection(input io.Reader) (skills.ComparisonSelectionRequest, error) {
	zero := skills.ComparisonSelectionRequest{}
	if input == nil {
		return zero, skills.ErrInvalid
	}
	body, err := io.ReadAll(io.LimitReader(input, skillComparisonInputLimit+1))
	if err != nil || len(body) > skillComparisonInputLimit || !configuredMemoryUnicodeValid(body) {
		return zero, skills.ErrInvalid
	}
	allowed := map[string]bool{}
	for _, key := range []string{"version", "model_id", "domain", "profile", "name", "baseline_version", "candidate_version", "source", "min_samples", "min_drop", "privacy", "tasks_per_version"} {
		allowed[key] = true
	}
	seen := map[string]bool{}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return zero, skills.ErrInvalid
	}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || !allowed[key] || seen[key] {
			return zero, skills.ErrInvalid
		}
		seen[key] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return zero, skills.ErrInvalid
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return zero, skills.ErrInvalid
	}
	if _, err := decoder.Token(); err != io.EOF || len(seen) != len(allowed) {
		return zero, skills.ErrInvalid
	}
	var request skills.ComparisonSelectionRequest
	if json.Unmarshal(body, &request) != nil || request.Validate() != nil {
		return zero, skills.ErrInvalid
	}
	return request, nil
}
