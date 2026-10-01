package remotecli

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/remote"
)

func TestEvaluateInvalidInputDoesNotCreateState(t *testing.T) {
	for _, tc := range []struct {
		name, operation, body, config, reviewer string
		cost                                    float64
	}{
		{"missing_config", "evaluate", "{}", "", "judge", 0},
		{"missing_reviewer", "evaluate", "{}", "config.yaml", "", 0},
		{"missing_cost", "evaluate", "{}", "config.yaml", "judge", -1},
		{"nan_cost", "evaluate", "{}", "config.yaml", "judge", math.NaN()},
		{"infinite_cost", "evaluate", "{}", "config.yaml", "judge", math.Inf(1)},
		{"unknown_field", "auto-evaluate", `{"unrecognized":true}`, "config.yaml", "judge", 0},
		{"trailing_value", "auto-evaluate", `{} {}`, "config.yaml", "judge", 0},
		{"oversize", "evaluate", strings.Repeat("x", remote.MaxBody+1), "config.yaml", "judge", 0},
		{"invalid_task", "evaluate", `{}`, "config.yaml", "judge", 0},
		{"invalid_operation", "dispatch", `{}`, "config.yaml", "judge", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "uncreated")
			result, err := evaluateOperation(context.Background(), nil, tc.operation, root, root, "key", tc.config, tc.reviewer, tc.cost, strings.NewReader(tc.body))
			if !errors.Is(err, remote.ErrInvalid) || result.Version != 0 {
				t.Fatal(result, err)
			}
			if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid input created state", err)
			}
		})
	}
}
