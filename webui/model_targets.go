package webui

// ModelTargets exposes configured identities only; it is not a health or dispatch grant.
type ModelTarget struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	Local bool   `json:"local"`
}
type ModelTargets struct {
	Version  int           `json:"version"`
	Hostname string        `json:"hostname"`
	Models   []ModelTarget `json:"models"`
}

func (p ModelTargets) Validate() error {
	if p.Version != 1 || !boundedPrintable(p.Hostname, 1, 253) || p.Models == nil || len(p.Models) > 256 {
		return ErrContract
	}
	seen := map[string]bool{}
	for _, m := range p.Models {
		if !ValidModelID(m.ID) || m.ID == "auto" || seen[m.ID] || !boundedPrintable(m.Model, 1, 512) {
			return ErrContract
		}
		seen[m.ID] = true
	}
	return encodedWithin(p, 256<<10)
}
