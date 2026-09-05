//go:build !darwin && !linux

package releasepack

func publish(source, target string) error { return ErrInvalid }
