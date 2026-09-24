package config

import "errors"

// EvidenceFallback supplies a bounded prior for an otherwise unmeasured task
// category. It never rewrites evidence or changes the executing task's key.
// Matching is exact, model/provider are preserved, and mappings are not chained.
type EvidenceFallback struct {
	Domain        string `yaml:"domain" json:"domain"`
	Profile       string `yaml:"profile" json:"profile"`
	SourceDomain  string `yaml:"source_domain" json:"source_domain"`
	SourceProfile string `yaml:"source_profile" json:"source_profile"`
}

func (r Routing) validateEvidenceFallbacks() error {
	if len(r.EvidenceFallbacks) > 128 {
		return errors.New("too many routing evidence fallbacks")
	}
	seen := map[[2]string]bool{}
	for _, v := range r.EvidenceFallbacks {
		key := [2]string{v.Domain, v.Profile}
		if !identifier.MatchString(v.Domain) || !identifier.MatchString(v.Profile) || !identifier.MatchString(v.SourceDomain) || !identifier.MatchString(v.SourceProfile) || seen[key] || (v.Domain == v.SourceDomain && v.Profile == v.SourceProfile) {
			return errors.New("invalid or duplicate routing evidence fallback")
		}
		seen[key] = true
	}
	return nil
}

func (r Routing) EvidenceSource(domain, profile string) (string, string, bool) {
	for _, v := range r.EvidenceFallbacks {
		if v.Domain == domain && v.Profile == profile {
			return v.SourceDomain, v.SourceProfile, true
		}
	}
	return "", "", false
}
