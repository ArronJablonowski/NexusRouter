package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

var errTaskPrompt = errors.New("task prompt unavailable or invalid")

// readTaskPrompt borrows the caller's input, preserving its contents and file
// ownership. Pollable input waits without an idle timeout until EOF or context
// cancellation. Custom readers and regular-file reads remain cooperative.
func readTaskPrompt(ctx context.Context, input io.Reader) (text string, err error) {
	defer func() {
		if recover() != nil {
			text = ""
			err = errTaskPrompt
			if ctx != nil {
				err = errors.Join(err, ctx.Err())
			}
		}
	}()
	if ctx == nil || input == nil {
		return "", errTaskPrompt
	}
	if ctx.Err() != nil {
		return "", errors.Join(errTaskPrompt, ctx.Err())
	}
	reader, cleanup, prepareErr := prepareChatInput(ctx, input)
	if prepareErr != nil {
		return "", errors.Join(errTaskPrompt, ctx.Err())
	}
	defer func() {
		if cleanup() != nil {
			text = ""
			err = errors.Join(errTaskPrompt, err, ctx.Err())
		}
	}()
	body, readErr := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if ctx.Err() != nil {
		return "", errors.Join(errTaskPrompt, ctx.Err())
	}
	if readErr != nil {
		if errors.Is(readErr, context.Canceled) {
			return "", errors.Join(errTaskPrompt, context.Canceled)
		}
		if errors.Is(readErr, context.DeadlineExceeded) {
			return "", errors.Join(errTaskPrompt, context.DeadlineExceeded)
		}
		return "", errTaskPrompt
	}
	if len(body) > 1<<20 || !utf8.Valid(body) || strings.TrimSpace(string(body)) == "" {
		return "", errTaskPrompt
	}
	return string(body), nil
}
