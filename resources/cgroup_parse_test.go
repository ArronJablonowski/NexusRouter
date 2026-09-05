package resources

import (
	"errors"
	"strings"
	"testing"
)

func TestFindUnifiedCgroup(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		legacy, bad      bool
	}{
		{"root", "0::/\n", "/", false, false},
		{"nested", "0::/user.slice/app.scope\n", "/user.slice/app.scope", false, false},
		{"hybrid", "3:cpu,cpuacct:/tasks\n4:memory:/tasks\n0::/new\n", "/new", true, false},
		{"legacy memory", "4:memory:/tasks\n", "", true, false},
		{"other legacy", "4:name=systemd:/tasks\n", "", false, false},
		{"colon and space", "0::/group:with space", "/group:with space", false, false},
		{"empty", "", "", false, true},
		{"oversize", strings.Repeat("x", 64<<10+1), "", false, true},
		{"duplicate", "0::/a\n0::/b\n", "", false, true},
		{"zero controllers", "0:memory:/a", "", false, true},
		{"nonzero empty controllers", "1::/a", "", false, true},
		{"malformed", "0:/a", "", false, true},
		{"bad hierarchy", "-1:memory:/a", "", false, true},
		{"overflow hierarchy", "18446744073709551616:memory:/a", "", false, true},
		{"relative", "0::a", "", false, true},
		{"parent traversal", "0::/../a", "", false, true},
		{"middle traversal", "0::/a/../b", "", false, true},
		{"dot", "0::/a/./b", "", false, true},
		{"double slash", "0:://a", "", false, true},
		{"trailing slash", "0::/a/", "", false, true},
		{"control", "0::/a\x00b", "", false, true},
		{"invalid UTF8", "0::/\xff", "", false, true},
		{"blank line", "0::/a\n\n", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, legacy, err := findUnifiedCgroup([]byte(tc.body))
			if tc.bad {
				if !errors.Is(err, ErrProfile) || got != "" || legacy {
					t.Fatalf("got %q %v %v", got, legacy, err)
				}
			} else if err != nil || got != tc.want || legacy != tc.legacy {
				t.Fatalf("got %q %v %v; want %q %v", got, legacy, err, tc.want, tc.legacy)
			}
		})
	}
}

func TestFindCgroupMount(t *testing.T) {
	mount := func(root, point string) string {
		return "30 20 0:28 " + root + " " + point + " rw,nosuid shared:1 future:value - cgroup2 cgroup rw\n"
	}
	for _, tc := range []struct {
		name, body, group, point, root string
		bad                            bool
	}{
		{"root", mount("/", "/sys/fs/cgroup"), "/a/b", "/sys/fs/cgroup", "/", false},
		{"subtree", mount("/a", "/sys/fs/cgroup"), "/a/b", "/sys/fs/cgroup", "/a", false},
		{"exact subtree", mount("/a", "/sys/fs/cgroup"), "/a", "/sys/fs/cgroup", "/a", false},
		{"escapes", mount(`/a\040b`, `/sys/a\040b\134c`), "/a b/job", `/sys/a b\c`, "/a b", false},
		{"unrelated mount", "1 1 0:1 / / rw - ext4 /dev/a rw\n" + mount("/", "/cg"), "/a", "/cg", "/", false},
		{"unrelated subtree", mount("/other", "/other") + mount("/a", "/cg"), "/a/b", "/cg", "/a", false},
		{"duplicate", mount("/", "/cg") + mount("/a", "/other"), "/a/b", "", "", true},
		{"component mismatch", mount("/a", "/cg"), "/ab", "", "", true},
		{"absent", "1 1 0:1 / / rw - ext4 /dev/a rw\n", "/a", "", "", true},
		{"empty", "", "/a", "", "", true},
		{"oversize", strings.Repeat("x", 64<<10+1), "/a", "", "", true},
		{"bad group", mount("/", "/cg"), "/a/../b", "", "", true},
		{"root traversal", mount("/a/../b", "/cg"), "/b", "", "", true},
		{"point traversal", mount("/", "/cg/../else"), "/a", "", "", true},
		{"point relative", mount("/", "cg"), "/a", "", "", true},
		{"tab escape", mount("/", `/cg\011a`), "/a", "", "", true},
		{"newline escape", mount("/", `/cg\012a`), "/a", "", "", true},
		{"unknown escape", mount("/", `/cg\057a`), "/a", "", "", true},
		{"short escape", mount("/", `/cg\04`), "/a", "", "", true},
		{"literal control", mount("/", "/cg\ta"), "/a", "", "", true},
		{"bad separator", "30 20 0:28 / /cg rw cgroup2 cgroup rw", "/a", "", "", true},
		{"missing super options", "30 20 0:28 / /cg rw - cgroup2 cgroup", "/a", "", "", true},
		{"bad device", "30 20 :28 / /cg rw - cgroup2 cgroup rw", "/a", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			point, root, err := findCgroupMount([]byte(tc.body), tc.group)
			if tc.bad {
				if !errors.Is(err, ErrProfile) || point != "" || root != "" {
					t.Fatalf("got %q %q %v", point, root, err)
				}
			} else if err != nil || point != tc.point || root != tc.root {
				t.Fatalf("got %q %q %v; want %q %q", point, root, err, tc.point, tc.root)
			}
		})
	}
}
