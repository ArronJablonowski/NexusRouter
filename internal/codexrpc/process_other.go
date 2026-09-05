//go:build !darwin && !linux

package codexrpc

// Other platforms require explicit pipe/lifetime qualification first.
func processPlatformSupported() bool { return false }
