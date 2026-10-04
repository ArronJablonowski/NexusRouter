// Package scheduleview provides bounded read-only operating-system schedule metadata.
package scheduleview

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Entry struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Source   string `json:"source"`
	Schedule string `json:"schedule"`
	State    string `json:"state"`
}
type Page struct {
	Version     int       `json:"version"`
	ObservedAt  time.Time `json:"observed_at"`
	Items       []Entry   `json:"items"`
	Limitations []string  `json:"limitations"`
}

func (p Page) Validate() error {
	if p.Version != 1 || p.ObservedAt.IsZero() || len(p.Items) > 512 || len(p.Limitations) > 16 {
		return fmt.Errorf("invalid schedule page")
	}
	for _, e := range p.Items {
		for _, v := range []string{e.ID, e.Name, e.Source, e.Schedule, e.State} {
			if len(v) > 1024 || strings.ContainsAny(v, "\x00\r\n") {
				return fmt.Errorf("invalid entry")
			}
		}
	}
	return nil
}

type limited struct{ bytes.Buffer }

func (b *limited) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, fmt.Errorf("output limit")
	}
	return b.Buffer.Write(p)
}
func command(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, name, args...)
	var out limited
	c.Stdout = &out
	err := c.Run()
	return out.Bytes(), err
}
func Read(ctx context.Context) Page {
	p := Page{Version: 1, ObservedAt: time.Now().UTC(), Items: []Entry{}, Limitations: []string{}}
	add := func(e Entry) {
		if len(p.Items) < 512 {
			p.Items = append(p.Items, e)
		}
	}
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		for _, dir := range []string{filepath.Join(home, "Library/LaunchAgents"), "/Library/LaunchAgents", "/Library/LaunchDaemons", "/System/Library/LaunchAgents", "/System/Library/LaunchDaemons"} {
			files, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, f := range files {
				if ctx.Err() != nil {
					break
				}
				if !strings.HasSuffix(f.Name(), ".plist") {
					continue
				}
				path := filepath.Join(dir, f.Name())
				data, err := readFile(path)
				if err != nil || len(data) > 1<<20 {
					continue
				}
				if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("<?xml")) {
					data, err = command(ctx, "/usr/bin/plutil", "-convert", "xml1", "-o", "-", path)
					if err != nil {
						continue
					}
				}
				d := xml.NewDecoder(bytes.NewReader(data))
				var value any
				for {
					t, e := d.Token()
					if e != nil {
						break
					}
					if start, ok := t.(xml.StartElement); ok && start.Name.Local == "dict" {
						value, _ = plistValue(d, start)
						break
					}
				}
				m, ok := value.(map[string]any)
				if !ok {
					continue
				}
				schedule := ""
				if v, ok := m["StartInterval"]; ok {
					schedule = fmt.Sprint("Every ", v, " seconds")
				}
				if v, ok := m["StartCalendarInterval"]; ok {
					b, _ := json.Marshal(v)
					schedule = "Calendar " + string(b)
				}
				if m["RunAtLoad"] == true {
					if schedule != "" {
						schedule += "; "
					}
					schedule += "At load"
				}
				if schedule == "" {
					continue
				}
				name, _ := m["Label"].(string)
				if name == "" {
					name = f.Name()
				}
				state := "Configured; runtime state unknown"
				if m["Disabled"] == true {
					state = "Disabled in plist; runtime override unknown"
				}
				add(Entry{ID: path, Name: name, Source: filepath.Base(dir), Schedule: schedule, State: state})
			}
		}
		p.Limitations = append(p.Limitations, "Launchd configuration is shown; loaded state and overrides are not inferred.")
	} else if runtime.GOOS == "linux" {
		for _, scope := range []string{"system", "user"} {
			args := []string{"list-timers", "--all", "--output=json", "--no-pager"}
			if scope == "user" {
				args = append([]string{"--user"}, args...)
			}
			body, err := command(ctx, "systemctl", args...)
			var rows []map[string]any
			if err != nil || json.Unmarshal(body, &rows) != nil {
				p.Limitations = append(p.Limitations, scope+" systemd timers unavailable")
				continue
			}
			for _, r := range rows {
				unit, _ := r["unit"].(string)
				if unit == "" {
					continue
				}
				next := "Next run unavailable"
				if n, ok := r["next"].(float64); ok && n > 0 {
					next = "Next " + time.UnixMicro(int64(n)).UTC().Format(time.RFC3339)
				}
				add(Entry{ID: scope + ":" + unit, Name: unit, Source: scope + " systemd timer", Schedule: next, State: "Listed by systemd"})
			}
		}
	} else {
		p.Limitations = append(p.Limitations, "Operating-system scheduler is unsupported on this platform")
	}
	data, err := command(ctx, "crontab", "-l")
	if err == nil {
		readCron(data, "Current user crontab", false, add)
	} else {
		p.Limitations = append(p.Limitations, "Current-user crontab is absent or inaccessible")
	}
	if data, err = readFile("/etc/crontab"); err == nil {
		readCron(data, "System crontab", true, add)
	}
	if files, e := os.ReadDir("/etc/cron.d"); e == nil {
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			if data, e := readFile(filepath.Join("/etc/cron.d", f.Name())); e == nil {
				readCron(data, "cron.d/"+f.Name(), true, add)
			}
		}
	}
	p.Limitations = append(p.Limitations, "Only readable system schedules and the router service account’s schedules are included; other users’ private schedules are not inspected.")
	if len(p.Items) == 512 {
		p.Limitations = append(p.Limitations, "512-entry display limit reached")
	}
	if ctx.Err() != nil {
		p.Limitations = append(p.Limitations, "Inspection deadline reached; inventory is partial")
	}
	return p
}
func readCron(data []byte, source string, system bool, add func(Entry)) {
	if len(data) > 1<<20 {
		return
	}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		n := 5
		if strings.HasPrefix(f[0], "@") {
			n = 1
		}
		min := n + 1
		if system {
			min++
		}
		if len(f) < min || strings.Contains(f[0], "=") {
			continue
		}
		add(Entry{ID: fmt.Sprintf("%s:%d", source, i+1), Name: fmt.Sprintf("%s entry %d", source, i+1), Source: source, Schedule: strings.Join(f[:n], " "), State: "Configured"})
	}
}
func plistValue(d *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "dict":
		m := map[string]any{}
		key := ""
		for {
			t, e := d.Token()
			if e != nil {
				return nil, e
			}
			switch v := t.(type) {
			case xml.EndElement:
				return m, nil
			case xml.StartElement:
				if v.Name.Local == "key" {
					if e = d.DecodeElement(&key, &v); e != nil {
						return nil, e
					}
				} else {
					x, e := plistValue(d, v)
					if e != nil {
						return nil, e
					}
					m[key] = x
				}
			}
		}
	case "array":
		a := []any{}
		for {
			t, e := d.Token()
			if e != nil {
				return nil, e
			}
			switch v := t.(type) {
			case xml.EndElement:
				return a, nil
			case xml.StartElement:
				x, e := plistValue(d, v)
				if e != nil {
					return nil, e
				}
				a = append(a, x)
			}
		}
	case "true", "false":
		if e := d.Skip(); e != nil {
			return nil, e
		}
		return start.Name.Local == "true", nil
	default:
		var s string
		if e := d.DecodeElement(&s, &start); e != nil && e != io.EOF {
			return nil, e
		}
		return s, nil
	}
}

func readFile(path string) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if len(b) > 1<<20 {
		return nil, fmt.Errorf("schedule file too large")
	}
	return b, e
}
