package app

import (
	"context"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
	"os"
)

func (s *Service) BrowserModelTargets(ctx context.Context) (contract.ModelTargets, error) {
	if ctx.Err() != nil {
		return contract.ModelTargets{}, ctx.Err()
	}
	host, err := os.Hostname()
	if err != nil {
		return contract.ModelTargets{}, err
	}
	p := contract.ModelTargets{Version: 1, Hostname: host, Models: []contract.ModelTarget{}}
	for _, m := range s.settings.Models {
		p.Models = append(p.Models, contract.ModelTarget{ID: m.ID, Model: m.Model, Local: m.Locality == "local"})
	}
	return p, p.Validate()
}
