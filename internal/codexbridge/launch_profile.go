package codexbridge

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// launchProfile accepts only the pinned CLI metadata shape. It does not launch
// a process or establish effective configuration/capability isolation.
func launchProfile(version, inventory []byte) (features []string, args []string, err error) {
	if len(version) > 128 || !utf8.Valid(version) || len(inventory) > 64<<10 || !utf8.Valid(inventory) {
		return nil, nil, ErrLaunchObservation
	}
	expectedRows := 0
	switch strings.TrimSpace(string(version)) {
	case "codex-cli 0.153.4":
		expectedRows = 135
	case "codex-cli 0.159.3":
		expectedRows = 152
	default:
		return nil, nil, ErrLaunchObservation
	}
	rows := strings.Split(strings.TrimSpace(string(inventory)), "\n")
	if len(rows) != expectedRows {
		return nil, nil, ErrLaunchObservation
	}
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		fields := strings.Fields(row)
		if len(fields) < 2 || len(fields) > 4 || len(fields[0]) > 128 || (!namePattern.MatchString(fields[0]) && !(expectedRows == 152 && fields[0] == "guardianv2.thread_context")) || seen[fields[0]] {
			return nil, nil, ErrLaunchObservation
		}
		stage := strings.Join(fields[1:len(fields)-1], " ")
		value := fields[len(fields)-1]
		if value != "true" && value != "false" {
			return nil, nil, ErrLaunchObservation
		}
		switch stage {
		case "deprecated", "experimental", "removed", "stable", "under development":
		default:
			return nil, nil, ErrLaunchObservation
		}
		name := fields[0]
		seen[name] = true
		if name == "apps_mcp_path_override" || name == "guardianv2.thread_context" {
			if stage != "removed" || value != "false" {
				return nil, nil, ErrLaunchObservation
			}
			continue
		}
		features = append(features, name)
	}
	for _, name := range []string{"apps_mcp_path_override", "skip_host_skill_discovery", "code_mode_host", "shell_tool", "plugins", "hooks", "apps"} {
		if !seen[name] {
			return nil, nil, ErrLaunchObservation
		}
	}
	sort.Strings(features)
	overrides := make([]string, 0, len(features))
	for _, name := range features {
		value := "false"
		if name == "skip_host_skill_discovery" || name == "code_mode_host" {
			value = "true"
		}
		overrides = append(overrides, name+"="+value)
	}
	args = []string{"app-server", "--strict-config", "--listen", "stdio://", "-c", "features={" + strings.Join(overrides, ",") + "}"}
	for _, override := range []string{`mcp_servers={}`, `plugins={}`, `project_doc_max_bytes=0`, `notify=[]`, `web_search="disabled"`} {
		args = append(args, "-c", override)
	}
	return features, args, nil
}
