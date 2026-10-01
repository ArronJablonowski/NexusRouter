//go:build !darwin && !linux

package remote

func lockReviewQueue(string) (func(), error) { return nil, ErrUnavailable }
