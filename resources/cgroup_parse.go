package resources

import (
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// findUnifiedCgroup reads the kernel's hierarchy-ID:controllers:path records.
// Cgroup v2 has the unique hierarchy ID 0 and an empty controller list.
// Namespace-relative paths containing '..' cannot be resolved safely here.
func findUnifiedCgroup(body []byte) (string, bool, error) {
	if !validCgroupInput(body) {
		return "", false, ErrProfile
	}
	group := ""
	legacy := false
	seen := map[uint64]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 || !cgroupDecimal(parts[0]) || !canonicalCgroupPath(parts[2]) {
			return "", false, ErrProfile
		}
		id, err := strconv.ParseUint(parts[0], 10, 64)
		if err != nil || seen[id] {
			return "", false, ErrProfile
		}
		seen[id] = true
		if id == 0 {
			if parts[0] != "0" || parts[1] != "" {
				return "", false, ErrProfile
			}
			group = parts[2]
			continue
		}
		for _, controller := range strings.Split(parts[1], ",") {
			if controller == "" || strings.ContainsAny(controller, " /\\") {
				return "", false, ErrProfile
			}
			legacy = legacy || controller == "memory"
		}
	}
	return group, legacy, nil
}

// findCgroupMount matches mountinfo's filesystem root to the process's cgroup
// path, using component boundaries. Multiple matching bind mounts are ambiguous
// and deliberately fail closed; no shortest/longest-path guess is made.
func findCgroupMount(body []byte, groupPath string) (string, string, error) {
	if !validCgroupInput(body) || !canonicalCgroupPath(groupPath) {
		return "", "", ErrProfile
	}
	point, root := "", ""
	for _, line := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
		fields := strings.Split(line, " ")
		separator := -1
		for i, field := range fields {
			if field == "" {
				return "", "", ErrProfile
			}
			if field == "-" && i >= 6 && separator < 0 {
				separator = i
			}
		}
		if separator < 6 || len(fields) != separator+4 || !cgroupDecimal(fields[0]) || !cgroupDecimal(fields[1]) {
			return "", "", ErrProfile
		}
		device := strings.Split(fields[2], ":")
		if len(device) != 2 || !cgroupDecimal(device[0]) || !cgroupDecimal(device[1]) {
			return "", "", ErrProfile
		}
		if fields[separator+1] != "cgroup2" {
			continue
		}
		candidateRoot, ok := decodeCgroupMountPath(fields[3])
		candidatePoint, pointOK := decodeCgroupMountPath(fields[4])
		if !ok || !pointOK {
			return "", "", ErrProfile
		}
		if candidateRoot != "/" && groupPath != candidateRoot && !strings.HasPrefix(groupPath, candidateRoot+"/") {
			continue
		}
		if point != "" {
			return "", "", ErrProfile
		}
		point, root = candidatePoint, candidateRoot
	}
	if point == "" {
		return "", "", ErrProfile
	}
	return point, root, nil
}

func validCgroupInput(body []byte) bool {
	if len(body) == 0 || len(body) > 64<<10 || !utf8.Valid(body) {
		return false
	}
	for _, r := range string(body) {
		if r != '\n' && unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func canonicalCgroupPath(value string) bool {
	if value == "" || !utf8.ValidString(value) || !path.IsAbs(value) || path.Clean(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func cgroupDecimal(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
}

func decodeCgroupMountPath(value string) (string, bool) {
	var decoded strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' {
			decoded.WriteByte(value[i])
			continue
		}
		if i+3 >= len(value) {
			return "", false
		}
		switch value[i+1 : i+4] {
		case "040":
			decoded.WriteByte(' ')
		case "134":
			decoded.WriteByte('\\')
		default:
			// Standard tab/newline escapes (011/012) remain unsafe paths.
			return "", false
		}
		i += 3
	}
	result := decoded.String()
	return result, canonicalCgroupPath(result)
}
