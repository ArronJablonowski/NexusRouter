package webui

import (
	"github.com/ArronJablonowski/NexusRouter/remoteconfig"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var settingsDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

const MaxSettingsRootBytes = 4096

type ToolAccessSettings struct {
	MacMemoryPercent      float64                    `json:"mac_memory_percent"`
	MacSwapGrowthGB       float64                    `json:"mac_swap_growth_gb"`
	DNSLogging            string                     `json:"dns_logging"`
	RemoteAdvertisement   remoteconfig.Advertisement `json:"remote_advertisement"`
	SkillsEnabled         bool                       `json:"skills_enabled"`
	SkillsAutoDraft       bool                       `json:"skills_auto_draft"`
	SkillsRoot            string                     `json:"skills_root"`
	SkillsScope           string                     `json:"skills_scope"`
	ToolsEnabled          bool                       `json:"tools_enabled"`
	DelegateReadTools     bool                       `json:"delegate_read_tools"`
	ReadRoot              string                     `json:"read_root"`
	SpecialistsAllowCloud bool                       `json:"specialists_allow_cloud"`
}

func (s ToolAccessSettings) Validate() error {
	if !finiteNonnegative(s.MacMemoryPercent) || s.MacMemoryPercent > 100 || !finiteNonnegative(s.MacSwapGrowthGB) || s.MacSwapGrowthGB > 1024 {
		return ErrContract
	}
	if s.DNSLogging != "" && s.DNSLogging != "managed" && s.DNSLogging != "full" {
		return ErrContract
	}
	if s.RemoteAdvertisement.Validate() != nil {
		return ErrContract
	}
	if len(s.SkillsRoot) > 4096 || !utf8.ValidString(s.SkillsRoot) || strings.ContainsFunc(s.SkillsRoot, unicode.IsControl) || len(s.SkillsScope) > 64 {
		return ErrContract
	}
	if s.SkillsRoot != "" && (!filepath.IsAbs(s.SkillsRoot) || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`).MatchString(s.SkillsScope)) {
		return ErrContract
	}
	if s.SkillsEnabled && s.SkillsRoot == "" {
		return ErrContract
	}

	if len(s.ReadRoot) > MaxSettingsRootBytes || !utf8.ValidString(s.ReadRoot) || strings.ContainsFunc(s.ReadRoot, unicode.IsControl) {
		return ErrContract
	}
	if s.ToolsEnabled && !filepath.IsAbs(s.ReadRoot) || s.DelegateReadTools && !s.ToolsEnabled {
		return ErrContract
	}
	return nil
}

type SettingsInspection struct {
	Version         int                `json:"version"`
	Digest          string             `json:"digest"`
	Active          ToolAccessSettings `json:"active"`
	Saved           ToolAccessSettings `json:"saved"`
	RestartRequired bool               `json:"restart_required"`
}

func (s SettingsInspection) Validate() error {
	if s.Version != ContractVersion || !settingsDigestPattern.MatchString(s.Digest) || s.Active.Validate() != nil || s.Saved.Validate() != nil || s.RestartRequired != (s.Active != s.Saved) {
		return ErrContract
	}
	return encodedWithin(s, 16<<10)
}

type SettingsUpdateRequest struct {
	Version        int                `json:"version"`
	ExpectedDigest string             `json:"expected_digest"`
	Settings       ToolAccessSettings `json:"settings"`
}

func (r SettingsUpdateRequest) Validate() error {
	if r.Version != ContractVersion || !settingsDigestPattern.MatchString(r.ExpectedDigest) || r.Settings.Validate() != nil {
		return ErrContract
	}
	return encodedWithin(r, 16<<10)
}
