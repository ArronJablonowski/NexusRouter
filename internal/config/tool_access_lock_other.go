//go:build !darwin && !linux

package config

// Unsupported platforms cannot safely provide cross-process compare/replace.
func lockProjectUpdate(string) (func(), error) { return nil, ErrConfigWrite }
