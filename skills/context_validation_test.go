package skills

import (
	"strings"
	"testing"
	"time"
)

func contextFixture() (Metadata, Version) {
	v := Version{ID: strings.Repeat("a", 32), CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Draft: sample()}
	m := Metadata{Key: v.Draft.Key, Version: v.ID, Digest: digest(v), Description: v.Draft.Description, Tags: append([]string{}, v.Draft.Tags...)}
	return m, v
}

func TestValidateContextVersionAndMetadata(t *testing.T) {
	m, v := contextFixture()
	if err := ValidateContextMetadata(m, "project", "go"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateContextVersion(m, v, "project", "go"); err != nil {
		t.Fatal(err)
	}
	for _, year := range []int{1970, 2260} {
		v.CreatedAt = time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
		m.Digest = digest(v)
		if err := ValidateContextVersion(m, v, "project", "go"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestValidateContextRejectsMetadataMismatch(t *testing.T) {
	for _, mode := range []string{"scope", "name", "version", "digest", "digestcase", "privacy", "invalidprivacy", "description", "tags", "domain", "manytags"} {
		t.Run(mode, func(t *testing.T) {
			m, v := contextFixture()
			domain := "go"
			switch mode {
			case "scope":
				m.Key.Scope = "other"
			case "name":
				m.Key.Name = "../name"
			case "version":
				m.Version = "bad"
			case "digest":
				m.Digest = strings.Repeat("z", 64)
			case "digestcase":
				m.Digest = strings.ToUpper(m.Digest)
			case "privacy":
				m.Privacy = PrivacyPublic
			case "invalidprivacy":
				m.Privacy = "cloud_allowed"
			case "description":
				m.Description = "different"
			case "tags":
				m.Tags = []string{"go", "other"}
			case "domain":
				domain = "other"
			case "manytags":
				m.Tags = make([]string, 4097)
			}
			if err := ValidateContextVersion(m, v, "project", domain); err == nil {
				t.Fatal("accepted mismatch")
			}
		})
	}
}

func TestValidateContextRejectsMalformedOrOversizedDraft(t *testing.T) {
	for _, mode := range []string{"id", "parent", "timezero", "timeearly", "timelate", "description", "steps", "manyitems", "large", "combined", "escaped", "utf8", "digest", "key"} {
		t.Run(mode, func(t *testing.T) {
			m, v := contextFixture()
			switch mode {
			case "id":
				v.ID = strings.Repeat("b", 32)
			case "parent":
				v.Parent = "invalid"
			case "timezero":
				v.CreatedAt = time.Time{}
			case "timeearly":
				v.CreatedAt = time.Date(1969, 1, 1, 0, 0, 0, 0, time.UTC)
			case "timelate":
				v.CreatedAt = time.Date(2261, 1, 1, 0, 0, 0, 0, time.UTC)
			case "description":
				v.Draft.Description = strings.Repeat("x", 1025)
			case "steps":
				v.Draft.Steps = nil
			case "manyitems":
				v.Draft.Steps = make([]string, 4097)
			case "large":
				v.Draft.Configuration = strings.Repeat("x", maxFile+1)
			case "combined":
				v.Draft.Configuration = strings.Repeat("x", maxFile/2)
				v.Draft.Steps = []string{strings.Repeat("x", maxFile/2)}
			case "escaped":
				v.Draft.Configuration = strings.Repeat("<", maxFile/2)
			case "utf8":
				v.Draft.Configuration = string([]byte{255})
			case "digest":
				v.Draft.Steps = []string{"different"}
			case "key":
				v.Draft.Key.Name = "other"
			}
			// Most fixtures carry a correct hash, proving structure/size checks
			// reject them independently of digest mismatch.
			if mode != "digest" {
				m.Digest = digest(v)
			}
			if err := ValidateContextVersion(m, v, "project", "go"); err == nil {
				t.Fatal("accepted malformed draft")
			}
		})
	}
}
