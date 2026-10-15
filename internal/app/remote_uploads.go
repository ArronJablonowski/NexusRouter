package app

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/attachments"
)

// PrepareRemoteUploads uses the commanding host's bounded readers. Originals
// remain private downloads there; only sanitized text and bounded analysis images cross hosts.
func (s *Service) PrepareRemoteUploads(ctx context.Context, prompt string, files []attachments.File) (string, error) {
	inputs, err := prepareChatInputs(ctx, files)
	if err != nil {
		return "", err
	}
	secrets, err := memorySecretsSafely(s.settings, s.secret)
	if err != nil {
		return "", err
	}
	return redactUploadContent(attachments.Encode(prompt, inputs), secrets)
}
