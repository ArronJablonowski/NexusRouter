package remote

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/internal/processaudit"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// SSH carries the existing mutually authenticated protocol over OpenSSH's
// direct-tcpip channel. SSH grants transport access, never router authority.
// The SSH host is the endpoint's concrete IP, not a configurable jump host.
type SSH struct {
	User           string `json:"user"`
	Port           int    `json:"port"`
	IdentityFile   string `json:"identity_file"`
	KnownHostsFile string `json:"known_hosts_file"`
}

func (s SSH) Validate() error {
	if !id(s.User) || strings.HasPrefix(s.User, "-") || s.Port < 1 || s.Port > 65535 {
		return ErrInvalid
	}
	for _, path := range []string{s.IdentityFile, s.KnownHostsFile} {
		// OpenSSH expands tokens and parses filename lists in configuration options.
		// Accept literal absolute paths only; never evaluate caller-provided syntax.
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "%$~\"'\\") || strings.ContainsFunc(path, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return ErrInvalid
		}
	}
	return nil
}
func (s SSH) arguments(address string) ([]string, error) {
	if s.Validate() != nil {
		return nil, ErrInvalid
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil {
		return nil, ErrInvalid
	}
	for _, path := range []string{s.IdentityFile, s.KnownHostsFile} {
		st, e := os.Lstat(path)
		if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
			return nil, ErrDenied
		}
	}
	args := []string{"-F", os.DevNull, "-T", "-N", "-p", strconv.Itoa(s.Port), "-l", s.User, "-i", s.IdentityFile}
	for _, option := range []string{
		"BatchMode=yes", "StrictHostKeyChecking=yes", "UserKnownHostsFile=" + s.KnownHostsFile,
		"GlobalKnownHostsFile=" + os.DevNull, "UpdateHostKeys=no", "VerifyHostKeyDNS=no",
		"IdentitiesOnly=yes", "IdentityAgent=none", "CertificateFile=none",
		"PreferredAuthentications=publickey", "PasswordAuthentication=no", "KbdInteractiveAuthentication=no",
		"ForwardAgent=no", "ForwardX11=no", "ControlMaster=no", "ControlPath=none",
		"ProxyCommand=none", "ProxyJump=none", "PermitLocalCommand=no",
		"ClearAllForwardings=yes", "ExitOnForwardFailure=yes", "ConnectionAttempts=1",
		"ConnectTimeout=5", "ServerAliveInterval=5", "ServerAliveCountMax=2",
	} {
		args = append(args, "-o", option)
	}
	return append(args, "-W", address, host), nil
}
func (s SSH) dial(ctx context.Context, address string) (net.Conn, error) {
	args, err := s.arguments(address)
	if err != nil {
		return nil, err
	}
	child, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(child, "ssh", args...)
	command.WaitDelay = time.Second
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, ErrUnavailable
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		stdin.Close()
		cancel()
		return nil, ErrUnavailable
	}
	command.Stderr = io.Discard // Do not return remote banners, paths or auth diagnostics.
	if err = processaudit.Start(command); err != nil {
		stdin.Close()
		stdout.Close()
		cancel()
		return nil, ErrUnavailable
	}
	local, bridge := net.Pipe()
	connection := &sshConnection{Conn: local, cancel: cancel, bridge: bridge}
	go func() { _, _ = io.Copy(stdin, bridge); stdin.Close() }()
	go func() { _, _ = io.Copy(bridge, stdout); bridge.Close() }()
	go func() { _ = processaudit.Wait(command); stdin.Close(); stdout.Close(); bridge.Close(); cancel() }()
	return connection, nil
}

type sshConnection struct {
	net.Conn
	cancel context.CancelFunc
	bridge net.Conn
	once   sync.Once
}

func (c *sshConnection) Close() error {
	c.once.Do(func() { c.cancel(); c.bridge.Close(); c.Conn.Close() })
	return nil
}
