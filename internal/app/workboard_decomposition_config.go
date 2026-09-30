package app

import (
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

// configuredWorkboardDecompositionPolicy translates validated effective
// settings into the domain policy carried by agent-authored card mutations.
// No model or transport argument participates in this translation.
func configuredWorkboardDecompositionPolicy(settings config.Settings, configID string) (workboard.DecompositionPolicy, error) {
	configured := settings.Workboard.Decomposition
	limits := workboard.DecompositionLimits{Version: configured.Version, MaxDepth: configured.MaxDepth,
		MaxChildren: configured.MaxChildrenPerParent}
	return workboard.NewDecompositionPolicy(limits, configID)
}
