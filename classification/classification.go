// Package classification defines the provider-neutral auxiliary intent
// classification boundary. It deliberately has no storage or tool authority.
package classification

import (
	"context"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

const DecisionVersion = 1

var (
	ErrInvalidConfig   = errors.New("classification: invalid configuration")
	ErrInvalidInput    = errors.New("classification: invalid input")
	ErrInvalidResponse = errors.New("classification: invalid response")
	ErrProvider        = errors.New("classification: provider failure")
)

// Input is the transient task material presented to a classifier. Callers must
// apply privacy and admission policy before constructing it. Neither the Input
// nor the model's raw response is part of the durable classification contract.
type Input struct {
	Task    string
	Context string
}

// Decision is the only model-authored classification data released to callers.
// ModelClassifier returns capabilities in sorted, duplicate-free order.
type Decision struct {
	Version      int      `json:"version"`
	Domain       string   `json:"domain"`
	Capabilities []string `json:"capabilities"`
}

// Result contains bounded accounting metadata and the validated decision. It
// never contains a prompt, raw model output, or provider error prose.
type Result struct {
	Decision Decision         `json:"decision"`
	Usage    *providers.Usage `json:"usage,omitempty"`
	Elapsed  time.Duration    `json:"elapsed"`
}

// Classifier is replaceable and has no persistence or side-effect authority.
type Classifier interface {
	Classify(context.Context, Input) (Result, error)
}
