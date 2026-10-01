// Package remoteconfig defines inert remote host configuration shared by the
// runtime and operator settings. Validation performs no network or file access.
package remoteconfig

import (
	"errors"
	"regexp"
	"strings"
)

type Advertisement struct {
	Enabled   bool   `yaml:"enabled" json:"enabled"`
	Interface string `yaml:"interface" json:"interface"`
	Name      string `yaml:"name" json:"name"`
	SSHPort   int    `yaml:"ssh_port" json:"ssh_port"`
}

var interfaceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)
var dnsLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func (a Advertisement) Validate() error {
	bad := errors.New("invalid remote advertisement settings")
	if a.SSHPort < 0 || a.SSHPort > 65535 || a.Interface != "" && !interfaceName.MatchString(a.Interface) || len(a.Name) > 253 {
		return bad
	}
	if a.Name != "" {
		for _, label := range strings.Split(a.Name, ".") {
			if !dnsLabel.MatchString(label) {
				return bad
			}
		}
	}
	if a.Enabled && (a.Interface == "" || a.Name == "") {
		return bad
	}
	return nil
}
