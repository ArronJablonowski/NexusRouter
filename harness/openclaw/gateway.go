package openclaw

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/harness/internal/textgateway"
	"github.com/ArronJablonowski/NexusRouter/providers"
)

const MaxRecordBytes = textgateway.MaxRecordBytes

type gatewayConfig = textgateway.Config

func startGateway(ctx context.Context, c gatewayConfig) (string, string, func() (textgateway.Completion, error), func(), error) {
	return textgateway.Start(ctx, c)
}
func contextMessages(m []providers.Message) ([]byte, error) { return textgateway.ContextMessages(m) }
