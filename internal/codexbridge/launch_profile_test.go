package codexbridge

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func profileInventory() string {
	rows := []string{"apps_mcp_path_override removed false", "skip_host_skill_discovery experimental false", "shell_tool stable true", "plugins experimental true", "hooks experimental true", "apps experimental true", "code_mode_host stable true"}
	for len(rows) < 135 {
		rows = append(rows, fmt.Sprintf("feature_%03d under development false", len(rows)))
	}
	return strings.Join(rows, "\n")
}

func TestLaunchProfile(t *testing.T) {
	features, args, err := launchProfile([]byte("codex-cli 0.153.4\n"), []byte(profileInventory()))
	if err != nil || len(features) != 134 || !sort.StringsAreSorted(features) || len(args) != 16 {
		t.Fatal("pinned inventory rejected")
	}
	if !reflect.DeepEqual(args[:5], []string{"app-server", "--strict-config", "--listen", "stdio://", "-c"}) ||
		strings.Contains(args[5], "apps_mcp_path_override") || !strings.Contains(args[5], "skip_host_skill_discovery=true") || !strings.Contains(args[5], "code_mode_host=true") || strings.Count(args[5], "=true") != 2 || strings.Count(args[5], "=false") != 132 {
		t.Fatal("incorrect feature override profile")
	}
	want := []string{"-c", `mcp_servers={}`, "-c", `plugins={}`, "-c", `project_doc_max_bytes=0`, "-c", `notify=[]`, "-c", `web_search="disabled"`}
	if !reflect.DeepEqual(args[6:], want) {
		t.Fatal("incorrect fixed launch arguments")
	}
	rows := strings.Split(profileInventory(), "\n")
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	f2, a2, err := launchProfile([]byte("codex-cli 0.153.4"), []byte(strings.Join(rows, "\n")))
	if err != nil || !reflect.DeepEqual(features, f2) || !reflect.DeepEqual(args, a2) {
		t.Fatal("inventory ordering changed launch profile")
	}
}

func TestLaunchProfileRejectsMetadata(t *testing.T) {
	for _, version := range []string{"", "codex-cli 0.153.5", "codex-cli 0.153.4 extra", "\xff", strings.Repeat(" ", 129) + "codex-cli 0.153.4"} {
		f, a, err := launchProfile([]byte(version), []byte(profileInventory()))
		if err != ErrLaunchObservation || f != nil || a != nil {
			t.Fatal("unsupported version admitted")
		}
	}
	inventory := profileInventory()
	cases := []string{"", "\xff", inventory + "\nextra stable false", strings.Join(strings.Split(inventory, "\n")[1:], "\n"), strings.Repeat("x", 64<<10+1)}
	for _, row := range []string{"feature_007 stable", "feature_007 stable false extra", "feature_007 unknown false", "feature_007 under development extra false", "feature_007 stable maybe", "feature_007\x00 stable false", "shell_tool stable false", strings.Repeat("a", 129) + " stable false"} {
		cases = append(cases, strings.Replace(inventory, "feature_007 under development false", row, 1))
	}
	for _, name := range []string{"apps_mcp_path_override", "skip_host_skill_discovery", "code_mode_host", "shell_tool", "plugins", "hooks", "apps"} {
		cases = append(cases, strings.Replace(inventory, name+" ", "unexpected_name ", 1))
	}
	cases = append(cases, strings.Replace(inventory, "apps_mcp_path_override removed false", "apps_mcp_path_override stable false", 1), strings.Replace(inventory, "apps_mcp_path_override removed false", "apps_mcp_path_override removed true", 1))
	for i, raw := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			f, a, err := launchProfile([]byte("codex-cli 0.153.4"), []byte(raw))
			if err != ErrLaunchObservation || f != nil || a != nil {
				t.Fatal("malformed inventory did not fail statically")
			}
		})
	}
}

func TestLaunchProfile01593(t *testing.T) {
	rows := strings.Split(profileInventory(), "\n")
	rows = append(rows, "guardianv2.thread_context removed false")
	for len(rows) < 152 {
		rows = append(rows, fmt.Sprintf("new_feature_%03d stable false", len(rows)))
	}
	inventory := strings.Join(rows, "\n")
	features, args, err := launchProfile([]byte("codex-cli 0.159.3"), []byte(inventory))
	if err != nil || len(features) != 150 || strings.Contains(args[5], "guardianv2.thread_context") || strings.Count(args[5], "=true") != 2 {
		t.Fatal("invalid new profile", err)
	}
	for _, bad := range []string{strings.Replace(inventory, "guardianv2.thread_context removed false", "guardianv2.thread_context stable false", 1), strings.Replace(inventory, "guardianv2.thread_context removed false", "other.dotted removed false", 1), inventory + "\nextra stable false"} {
		if _, _, err := launchProfile([]byte("codex-cli 0.159.3"), []byte(bad)); err == nil {
			t.Fatal("malformed inventory accepted")
		}
	}
}
