package textgateway

import "github.com/ArronJablonowski/NexusRouter/providers"

// Transcript returns an owned copy of the verified generated conversation after
// final completion. It excludes initial host context and is for native-output
// reconciliation only; it supplies no new inference or tool authority.
func (g *AgentGateway) Transcript() ([]providers.Message, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fault != nil || g.final == nil || g.pending != 0 {
		return nil, ErrProjection
	}
	return copyAgentMessages(g.messages[len(g.config.Messages):])
}
