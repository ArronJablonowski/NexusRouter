package remote

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func serviceFixture() ServiceTemplateSpec {
	return ServiceTemplateSpec{Platform: "launchd", Executable: "/opt/Nexus Router/nexus", WorkingDirectory: "/private/runtime", OwnerDirectory: "/private/shared-admission", Instance: "node-a", Listen: "192.168.1.20:8443", Config: "/private/config.yaml", Journal: "/private/journal", Trust: "/private/peers.json", Certificate: "/private/cert.pem", Key: "/private/key.pem", CA: "/private/ca.pem"}
}
func TestServiceTemplatesPreserveLiteralPathsAndNoImplicitNetworkPolicy(t *testing.T) {
	s := serviceFixture()
	s.Config = "/private/A&B <node> \"quoted\" $HOME %n.yaml"
	body, err := RenderServiceTemplate(s)
	if err != nil {
		t.Fatal(err)
	}
	decoder := xml.NewDecoder(strings.NewReader(string(body)))
	found := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == "string" {
			var v string
			if err := decoder.DecodeElement(&v, &start); err != nil {
				t.Fatal(err)
			}
			if v == s.Config {
				found = true
			}
		}
	}
	if !found || strings.Contains(string(body), "--advertise") || !strings.Contains(string(body), "DARWIN_PROCESS_OWNER_DIR") {
		t.Fatal(string(body))
	}
	s.Platform = "systemd"
	s.WorkingDirectory = "/private/work space $HOME %n"
	body, err = RenderServiceTemplate(s)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, required := range []string{`ExecStart=":/opt/Nexus Router/nexus" "remote" "serve"`, `"/private/A&B <node> \"quoted\" $HOME %%n.yaml"`, "WorkingDirectory=/private/work space $HOME %%n\n", "UMask=0077", "KillMode=control-group", "Restart=on-failure", `Environment="DARWIN_PROCESS_OWNER_DIR=/private/shared-admission"`} {
		if !strings.Contains(text, required) {
			t.Fatalf("missing %q: %s", required, text)
		}
	}
	if strings.Contains(text, "/bin/sh") || strings.Contains(text, "--advertise") || strings.Contains(text, "User=root") {
		t.Fatal("unexpected authority", text)
	}
}
func TestServiceTemplateRejectsUnsafeOrIncompleteConfiguration(t *testing.T) {
	for name, change := range map[string]func(*ServiceTemplateSpec){"relative": func(s *ServiceTemplateSpec) { s.Executable = "nexus" }, "newline": func(s *ServiceTemplateSpec) { s.Config = "/config\nExecStart=/bad" }, "clean": func(s *ServiceTemplateSpec) { s.Key = "/private/../key" }, "owner": func(s *ServiceTemplateSpec) { s.OwnerDirectory = "" }, "platform": func(s *ServiceTemplateSpec) { s.Platform = "windows" }, "instance": func(s *ServiceTemplateSpec) { s.Instance = "node\n<key>" }, "dns": func(s *ServiceTemplateSpec) { s.Listen = "example.com:8443" }, "port": func(s *ServiceTemplateSpec) { s.Listen = "127.0.0.1:0" }, "root": func(s *ServiceTemplateSpec) { s.WorkingDirectory = "/" }} {
		t.Run(name, func(t *testing.T) {
			s := serviceFixture()
			change(&s)
			if _, err := RenderServiceTemplate(s); err == nil {
				t.Fatal("accepted invalid template")
			}
		})
	}
}
func TestLaunchdTemplateNativePlistDecoding(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native plutil qualification is macOS only")
	}
	s := serviceFixture()
	s.Config = "/private/A&B <node> \"quoted\" $HOME %n.yaml"
	body, err := RenderServiceTemplate(s)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "remote.plist")
	if err := os.WriteFile(p, body, 0600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("/usr/bin/plutil", "-convert", "json", "-o", "-", "--", p).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	var parsed struct {
		Label                string
		ProgramArguments     []string
		WorkingDirectory     string
		EnvironmentVariables map[string]string
		Umask                int
		RunAtLoad            bool
	}
	if err := json.Unmarshal(output, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Label != "com.nexusrouter.remote.node-a" || parsed.Umask != 63 || !parsed.RunAtLoad || parsed.EnvironmentVariables["DARWIN_PROCESS_OWNER_DIR"] != s.OwnerDirectory || parsed.WorkingDirectory != s.WorkingDirectory || len(parsed.ProgramArguments) != 19 || parsed.ProgramArguments[0] != s.Executable || parsed.ProgramArguments[8] != s.Config {
		t.Fatal(parsed)
	}
}
