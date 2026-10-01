package remote

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"net"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ServiceTemplateSpec contains only paths and explicit host settings. Rendering
// never reads credentials, writes files, installs a service or opens a socket.
// Both templates are per-user services, not privileged system services.
type ServiceTemplateSpec struct {
	Platform         string
	Executable       string
	WorkingDirectory string
	OwnerDirectory   string
	Instance         string
	Listen           string
	Config           string
	Journal          string
	Trust            string
	Certificate      string
	Key              string
	CA               string
}

func servicePath(value string) bool {
	return utf8.ValidString(value) && strings.HasPrefix(value, "/") && path.Clean(value) == value && value != "/" && len(value) <= 4096 && !strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) || r == 0xfffe || r == 0xffff })
}

// RenderServiceTemplate emits either a launchd agent plist or a systemd user
// unit. It does not enable discovery or SSH server installation. Operators must
// separately validate filesystem permissions, credentials and runtime readiness.
func RenderServiceTemplate(s ServiceTemplateSpec) ([]byte, error) {
	if (s.Platform != "launchd" && s.Platform != "systemd") || !id(s.Instance) {
		return nil, ErrInvalid
	}
	for _, p := range []string{s.Executable, s.WorkingDirectory, s.OwnerDirectory, s.Config, s.Journal, s.Trust, s.Certificate, s.Key, s.CA} {
		if !servicePath(p) {
			return nil, ErrInvalid
		}
	}
	host, port, err := net.SplitHostPort(s.Listen)
	number, e := strconv.Atoi(port)
	if err != nil || e != nil || net.ParseIP(host) == nil || number < 1 || number > 65535 {
		return nil, ErrInvalid
	}
	if s.Platform == "systemd" && (strings.TrimSpace(s.WorkingDirectory) != s.WorkingDirectory || strings.HasSuffix(s.WorkingDirectory, "\\")) {
		return nil, ErrInvalid
	}
	args := []string{s.Executable, "remote", "serve", "--instance", s.Instance, "--listen", s.Listen, "--config", s.Config, "--journal", s.Journal, "--trust", s.Trust, "--cert", s.Certificate, "--key", s.Key, "--ca", s.CA}
	if s.Platform == "launchd" {
		return renderLaunchAgent(s, args), nil
	}
	return renderSystemdUser(s, args), nil
}
func renderLaunchAgent(s ServiceTemplateSpec, args []string) []byte {
	var b bytes.Buffer
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict>\n")
	text := func(value string) {
		b.WriteString("<string>")
		_ = xml.EscapeText(&b, []byte(value))
		b.WriteString("</string>\n")
	}
	b.WriteString("<key>Label</key>\n")
	text("com.nexusrouter.remote." + s.Instance)
	b.WriteString("<key>ProgramArguments</key><array>\n")
	for _, arg := range args {
		text(arg)
	}
	b.WriteString("</array>\n<key>WorkingDirectory</key>\n")
	text(s.WorkingDirectory)
	b.WriteString("<key>EnvironmentVariables</key><dict><key>DARWIN_PROCESS_OWNER_DIR</key>\n")
	text(s.OwnerDirectory)
	b.WriteString("</dict>\n")
	b.WriteString("<key>Umask</key><integer>63</integer>\n<key>RunAtLoad</key><true/>\n<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>\n<key>ThrottleInterval</key><integer>30</integer>\n<key>ExitTimeOut</key><integer>30</integer>\n</dict></plist>\n")
	return b.Bytes()
}

// systemd values are quoted without a shell. Percent specifiers are doubled;
// ExecStart uses the colon prefix to disable environment expansion.
// Environment= also does not expand dollar references.
func systemdQuote(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	value = strings.ReplaceAll(value, "%", "%%")
	return "\"" + value + "\""
}
func renderSystemdUser(s ServiceTemplateSpec, args []string) []byte {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = systemdQuote(arg)
	}
	quoted[0] = systemdQuote(":" + s.Executable)
	return []byte(fmt.Sprintf("[Unit]\nDescription=NexusRouter remote instance %s\nStartLimitIntervalSec=300\nStartLimitBurst=5\n\n[Service]\nType=simple\nWorkingDirectory=%s\nEnvironment=%s\nExecStart=%s\nUMask=0077\nRestart=on-failure\nRestartSec=30\nTimeoutStopSec=30\nKillMode=control-group\n\n[Install]\nWantedBy=default.target\n", s.Instance, strings.ReplaceAll(s.WorkingDirectory, "%", "%%"), systemdQuote("DARWIN_PROCESS_OWNER_DIR="+s.OwnerDirectory), strings.Join(quoted, " ")))
}
