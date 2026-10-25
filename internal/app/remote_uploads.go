package app

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/attachments"
	"path/filepath"
	"strings"
)

// PrepareRemoteUploads uses the commanding host's bounded readers. Originals
// remain private downloads there; only sanitized text and bounded analysis images cross hosts.
func (s *Service) PrepareRemoteUploads(ctx context.Context, prompt string, files []attachments.File) (string, error) {
	// Keep the complete analysis batch within the existing byte-conservative
	// context floor. Do not discard images or weaken the estimator; originals
	// stay untouched and only their bounded analysis copies are resized.
	images := 0
	for _, file := range files {
		switch strings.ToLower(filepath.Ext(file.Name)) {
		case ".png", ".jpg", ".jpeg", ".gif":
			images++
		}
	}
	limit := 4 << 10
	if images > 0 {
		limit /= images
	}
	inputs, err := prepareChatInputsWithImageLimit(ctx, files, limit)
	if err != nil {
		return "", err
	}
	secrets, err := memorySecretsSafely(s.settings, s.secret)
	if err != nil {
		return "", err
	}
	return redactUploadContent(attachments.Encode(prompt, inputs), secrets)
}
