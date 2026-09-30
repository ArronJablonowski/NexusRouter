package v1

import "context"

import "github.com/ArronJablonowski/NexusRouter/memory"

type MemoryExport = memory.ExportSnapshot
type MemoryExporter = memory.Exporter

// ExportMemory returns one complete, bounded, redacted snapshot of configured
// factual memory, including expired facts. It never enables retrieval or models.
func (c *Client) ExportMemory(ctx context.Context) (MemoryExport, error) {
	if !c.valid(ctx) {
		return MemoryExport{}, ErrAdmission
	}
	return c.service.ExportMemory(ctx)
}
