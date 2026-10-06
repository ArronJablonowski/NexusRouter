package webui

import "time"

type DependencyInventory struct {
	Version    int          `json:"version"`
	ObservedAt time.Time    `json:"observed_at"`
	Items      []Dependency `json:"items"`
}
type Dependency struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Scope    string `json:"scope"`
	Location string `json:"location"`
	Note     string `json:"note"`
}

func (p DependencyInventory) Validate() error {
	if p.Version != 1 || !validBrowserTime(p.ObservedAt) || len(p.Items) > 1024 {
		return ErrContract
	}
	seen := map[string]bool{}
	for _, x := range p.Items {
		if !validID(x.ID) || seen[x.ID] {
			return ErrContract
		}
		seen[x.ID] = true
		for _, v := range []string{x.Name, x.Status, x.Scope, x.Location, x.Note} {
			if requireText(v, 4096) != nil {
				return ErrContract
			}
		}
	}
	return nil
}
