//go:build !darwin && !linux

package githubpublish

import "errors"

func duplicateCredentialDescriptor(int) (int, error) {
	return -1, errors.New("credential descriptor transport is unsupported")
}

func closeCredentialDescriptor(int) {}
