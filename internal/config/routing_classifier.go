package config

import (
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/classification"
	"go.yaml.in/yaml/v3"
)

// validateRoutingClassifier keeps the optional auxiliary route independent and
// bounded. A disabled classifier cannot retain a model binding that a future
// configuration change might activate accidentally.
func (s Settings) validateRoutingClassifier() error {
	classifier := s.Routing.Classifier
	timeout, timeoutErr := Duration(classifier.Timeout)
	if timeoutErr != nil || timeout < 100*time.Millisecond || timeout > time.Minute {
		return errors.New("invalid routing classifier timeout")
	}
	if !finite(classifier.MaxCost) || classifier.MaxCost < 0 {
		return errors.New("invalid routing classifier cost ceiling")
	}
	if classifier.MaxInputTokens < 0 || classifier.MaxOutputTokens < 0 || classifier.MaxOutputTokens > classification.MaxClassifierOutputTokens {
		return errors.New("invalid routing classifier token ceiling")
	}
	if !classifier.Enabled {
		if classifier.ModelID != "" {
			return errors.New("disabled routing classifier cannot bind a model")
		}
		return nil
	}
	if !identifier.MatchString(classifier.ModelID) || classifier.MaxInputTokens == 0 || classifier.MaxOutputTokens == 0 {
		return errors.New("enabled routing classifier requires a model and positive token ceilings")
	}
	model, found := configuredModel(s.Models, classifier.ModelID)
	if !found || !modelAvailableInMode(model, s.Mode) || model.ContextTokens < 1 || model.EstimatedCost == nil ||
		!finite(*model.EstimatedCost) || *model.EstimatedCost < 0 || *model.EstimatedCost > classifier.MaxCost {
		return errors.New("routing classifier model unavailable within configured limits")
	}
	if classifier.MaxInputTokens > int64(model.ContextTokens) ||
		classifier.MaxOutputTokens > int64(model.ContextTokens)-classifier.MaxInputTokens {
		return errors.New("routing classifier token reservation exceeds model context")
	}
	return nil
}

// YAML reparsing represents a zero-valued float as an integer scalar. Preserve
// the declared MaxCost type when a higher-precedence scalar layer supplies a
// fractional ceiling.
func normalizeRoutingClassifierOverrideTypes(root *yaml.Node, overrides map[string]string) error {
	if _, ok := overrides["routing.classifier.max_cost"]; !ok {
		return nil
	}
	node := root
	for _, name := range []string{"routing", "classifier", "max_cost"} {
		found := false
		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Value == name {
				node, found = node.Content[i+1], true
				break
			}
		}
		if !found {
			return errors.New("invalid routing classifier override")
		}
	}
	if node.Kind != yaml.ScalarNode {
		return errors.New("invalid routing classifier override")
	}
	node.Tag = "!!float"
	return nil
}
