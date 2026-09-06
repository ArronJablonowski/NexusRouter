package runtime

import "errors"

// SkillContextUse records the host's fresh skill tier admitted into the initial
// task context. It does not describe inherited history, prove model dispatch or
// semantic use, or grant validation/tool authority. Nil means unrecorded legacy
// context. Complete with no references means no fresh skills; incomplete means
// attribution was unavailable and must not be treated as a negative cohort.
type SkillContextUse struct {
	Version    int              `json:"version"`
	Complete   bool             `json:"complete"`
	References []SkillReference `json:"references"`
}

// SkillReference pins an immutable, host-validated selected version. It is
// provenance, never a claim extracted from user/model message content.
type SkillReference struct {
	Scope   string `json:"scope"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

func (s *SkillContextUse) Validate() error {
	if s == nil {
		return nil
	}
	if s.Version != 1 || len(s.References) > 16 || !s.Complete && len(s.References) != 0 {
		return errors.New("invalid skill context attribution")
	}
	seen := make(map[string]bool, len(s.References))
	for _, ref := range s.References {
		key := ref.Scope + "/" + ref.Name
		if !skillContextIdentifier(ref.Scope) || !skillContextIdentifier(ref.Name) || !skillContextHex(ref.Version, 32) || !skillContextHex(ref.Digest, 64) || seen[key] {
			return errors.New("invalid skill context attribution")
		}
		seen[key] = true
	}
	return nil
}

// Clone returns an independently owned record, preserving nil legacy absence.
func (s *SkillContextUse) Clone() *SkillContextUse {
	if s == nil {
		return nil
	}
	copy := *s
	copy.References = append([]SkillReference(nil), s.References...)
	return &copy
}

func skillContextIdentifier(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for i, c := range []byte(s) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || i > 0 && (c == '_' || c == '-')) {
			return false
		}
	}
	return true
}

func skillContextHex(s string, size int) bool {
	if len(s) != size {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
