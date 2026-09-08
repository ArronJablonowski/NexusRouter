package config

import "time"

// DecayHalfLife resolves an exact domain/profile override, falling back to the
// global routing half-life. Settings validation guarantees one match at most.
func (r Routing) DecayHalfLife(domain, profile string) (time.Duration, error) {
	for _, override := range r.DecayOverrides {
		if override.Domain == domain && override.Profile == profile {
			return Duration(override.HalfLife)
		}
	}
	return Duration(r.HalfLife)
}
