package scheduleview

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"strings"
	"testing"
)

func TestCronMetadataExcludesCommandsAndEnvironment(t *testing.T) {
	var entries []Entry
	readCron([]byte("TOKEN=private\n# comment\n*/5 * * * * /bin/runner --token private\n@reboot /bin/start private\n"), "Current user", false, func(e Entry) { entries = append(entries, e) })
	if len(entries) != 2 || entries[0].Schedule != "*/5 * * * *" || entries[1].Schedule != "@reboot" {
		t.Fatalf("metadata %#v", entries)
	}
	b, _ := json.Marshal(entries)
	if strings.Contains(string(b), "private") || strings.Contains(string(b), "runner") {
		t.Fatal("command or environment leaked")
	}
}
func TestPlistScheduleParse(t *testing.T) {
	d := xml.NewDecoder(bytes.NewBufferString(`<dict><key>Label</key><string>test</string><key>RunAtLoad</key><true/><key>StartCalendarInterval</key><dict><key>Hour</key><integer>3</integer></dict><key>ProgramArguments</key><array><string>private</string></array></dict>`))
	tok, _ := d.Token()
	v, e := plistValue(d, tok.(xml.StartElement))
	if e != nil {
		t.Fatal(e)
	}
	m := v.(map[string]any)
	if m["Label"] != "test" || m["RunAtLoad"] != true || m["StartCalendarInterval"].(map[string]any)["Hour"] != "3" {
		t.Fatal("plist parse")
	}
}
