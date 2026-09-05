//go:build !darwin && !linux

package processguard

import "context"

type Observation struct{ State State }

func (*Observation) Close() error { return nil }

func Current(ctx context.Context) (Reference, error) {
	if err := checkContext(ctx); err != nil {
		return Reference{}, err
	}
	return Reference{}, ErrUnavailable
}

func Probe(ctx context.Context, _ Reference) (*Observation, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	return nil, ErrUnavailable
}
