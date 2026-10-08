package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/processaudit"
)

// launchctl output may contain credentials. It stays in memory and is never
// returned in diagnostics or logged. Bound both elapsed time and output size.
func webLaunchOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "/bin/launchctl", args...)
	var output webLaunchBuffer
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := processaudit.Run(cmd); err != nil {
		return "", errWebInstallation
	}
	return output.buffer.String(), nil
}

type webLaunchBuffer struct{ buffer bytes.Buffer }

func (b *webLaunchBuffer) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 1<<20 {
		return 0, errWebInstallation
	}
	return b.buffer.Write(p)
}
func discoverWebInstallations() ([]webInstallation, error) {
	if runtime.GOOS != "darwin" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := webLaunchOutput(ctx, "list")
	if err != nil {
		return nil, err
	}
	return discoverWebLaunchServices(raw, func(label string) (string, error) {
		return webLaunchOutput(ctx, "print", "gui/"+strconv.Itoa(os.Getuid())+"/"+label)
	})
}
func discoverWebLaunchServices(list string, read func(string) (string, error)) ([]webInstallation, error) {
	var result []webInstallation
	count := 0
	for _, line := range strings.Split(list, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || !(strings.HasPrefix(fields[2], "com.nexusrouter.") || strings.HasPrefix(fields[2], "com.darwinrouter.")) {
			continue
		}
		if fields[0] == "-" {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			return nil, errWebInstallation
		}
		count++
		if count > 32 {
			return nil, errWebInstallation
		}
		raw, err := read(fields[2])
		if err != nil {
			return nil, err
		}
		service, ok, err := parseWebLaunchService(fields[2], raw)
		if err != nil {
			return nil, err
		}
		if ok {
			result = append(result, service)
		}
	}
	return result, nil
}
func parseWebLaunchService(label, raw string) (webInstallation, bool, error) {
	service := webInstallation{label: label}
	var args, env, credentials []string
	working, section := "", ""
	for _, line := range strings.Split(raw, "\n") {
		// launchctl's top-level fields have one tab; nested resource state and
		// inherited/default environment blocks do not describe this service.
		if strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "\t\t") {
			field := strings.TrimSpace(line)
			if field == "arguments = {" || field == "environment = {" {
				section = field
				continue
			}
			if field == "}" {
				section = ""
				continue
			}
			if strings.HasPrefix(field, "working directory = ") {
				working = strings.TrimPrefix(field, "working directory = ")
			}
		}
		value := strings.TrimSpace(line)
		if section == "arguments = {" {
			args = append(args, value)
		}
		if section == "environment = {" {
			key, value, ok := strings.Cut(value, " => ")
			if ok {
				env = append(env, key+"="+value)
				if key == "NEXUS_API_TOKEN" || key == "DARWIN_API_TOKEN" {
					credentials = append(credentials, key+"="+value)
				}
			}
		}
	}
	if len(args) < 2 || args[1] != "serve" {
		return service, false, nil
	}
	if name := filepath.Base(args[0]); name != "nexus" && name != "darwin" {
		return service, false, nil
	}
	for i := 2; i < len(args); i++ {
		value := ""
		if args[i] == "--config" {
			i++
			if i >= len(args) {
				return service, false, errWebInstallation
			}
			value = args[i]
		} else if strings.HasPrefix(args[i], "--config=") {
			value = strings.TrimPrefix(args[i], "--config=")
		}
		if value != "" {
			if service.path != "" {
				return service, false, errWebInstallation
			}
			service.path = value
		}
	}
	if service.path == "" {
		return service, false, errWebInstallation
	}
	if !filepath.IsAbs(service.path) {
		if !filepath.IsAbs(working) {
			return service, false, errWebInstallation
		}
		service.path = filepath.Join(working, service.path)
	}
	service.path = filepath.Clean(service.path)
	service.env = config.Environment(env)
	// Use only this service's environment, never another launchd job's token.
	service.token = webLaunchToken(strings.Join(credentials, "\n"))
	return service, true, nil
}
