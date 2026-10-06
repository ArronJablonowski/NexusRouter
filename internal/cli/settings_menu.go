package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"go.yaml.in/yaml/v3"
)

// settingsFields derives the menu from Settings so newly added configuration
// fields cannot silently disappear from the headless administration surface.
type settingsField struct {
	name string
	typ  reflect.Type
}

func settingsFields(t reflect.Type) []settingsField {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var fields []settingsField
	if t.Kind() != reflect.Struct {
		return fields
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if f.IsExported() && name != "" && name != "-" {
			fields = append(fields, settingsField{name, f.Type})
		}
	}
	return fields
}

func runSettingsMenu(args []string, input io.Reader, out, errs io.Writer) int {
	fs := flag.NewFlagSet("settings", flag.ContinueOnError)
	fs.SetOutput(errs)
	home, _ := os.UserHomeDir()
	path := fs.String("config", filepath.Join(home, ".NexusRouter", "config", "config.yaml"), "configuration file to edit")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	original, err := os.ReadFile(*path)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(errs, "Cannot read configuration")
		return 1
	}
	if exists {
		info, e := os.Lstat(*path)
		if e != nil || !info.Mode().IsRegular() || len(original) > config.MaxFileBytes {
			fmt.Fprintln(errs, "Configuration must be a regular file under 1 MiB")
			return 1
		}
	}
	opts := config.Options{}
	if exists {
		opts.ProjectFile = *path
	}
	settings, err := config.Load(opts)
	if err != nil {
		fmt.Fprintln(errs, "Configuration is invalid; use config validate to diagnose before editing")
		return 1
	}
	data, _ := json.Marshal(settings)
	var draft map[string]any
	draftDecoder := json.NewDecoder(bytes.NewReader(data))
	draftDecoder.UseNumber()
	_ = draftDecoder.Decode(&draft)
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), config.MaxFileBytes)
	read := func() (string, bool) {
		if !scanner.Scan() {
			return "", false
		}
		return strings.TrimSpace(scanner.Text()), true
	}
	var pathParts []string
	types := []reflect.Type{reflect.TypeOf(config.Settings{})}
	dirty := false
	fmt.Fprintf(out, "NexusRouter settings — %s\nEditing this file plus defaults; environment/launch overrides are not saved.\nValues are hidden; use config show for a redacted view. Input is echoed by your terminal: use credential references, never secret values.\n", *path)
	for {
		fields := settingsFields(types[len(types)-1])
		fmt.Fprintf(out, "\nSettings / %s\n", strings.Join(pathParts, " / "))
		for i, f := range fields {
			fmt.Fprintf(out, "%d. %s (%s)\n", i+1, f.name, f.typ)
		}
		fmt.Fprintln(out, "b Back | s Save | q Discard and quit")
		choice, ok := read()
		if !ok {
			fmt.Fprintln(out, "No changes saved.")
			return 0
		}
		switch choice {
		case "q":
			fmt.Fprintln(out, "No changes saved.")
			return 0
		case "b":
			if len(pathParts) > 0 {
				pathParts = pathParts[:len(pathParts)-1]
				types = types[:len(types)-1]
			}
			continue
		case "s":
			if !dirty {
				fmt.Fprintln(out, "No changes to save.")
				continue
			}
			backup, e := saveSettingsMenu(*path, original, exists, draft)
			if e != nil {
				fmt.Fprintln(errs, e)
				continue
			}
			fmt.Fprintf(out, "Saved. Backup: %s\nRunning services are unchanged. Restart at an idle boundary to apply startup settings.\n", backup)
			return 0
		}
		n, e := strconv.Atoi(choice)
		if e != nil || n < 1 || n > len(fields) {
			fmt.Fprintln(out, "Choose a listed number.")
			continue
		}
		f := fields[n-1]
		t := f.typ
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() == reflect.Struct {
			fmt.Fprintln(out, "Enter opens this section; 'null' disables an optional section.")
			action, ok := read()
			if !ok {
				return 0
			}
			if action == "null" && f.typ.Kind() == reflect.Pointer {
				setSettingsValue(draft, append(append([]string{}, pathParts...), f.name), nil)
				dirty = true
				continue
			}
			if action != "" {
				fmt.Fprintln(out, "Invalid section action.")
				continue
			}
			pathParts = append(pathParts, f.name)
			types = append(types, f.typ)
			continue
		}
		fmt.Fprintf(out, "New %s: strings are plain text; other values use JSON. Arrays/objects replace the whole collection. :cancel leaves it unchanged.\n", f.name)
		value, ok := read()
		if !ok {
			return 0
		}
		if value == ":cancel" {
			continue
		}
		var raw []byte
		if t.Kind() == reflect.String {
			raw, _ = json.Marshal(value)
		} else {
			raw = []byte(value)
		}
		typed := reflect.New(f.typ)
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(typed.Interface()) != nil {
			fmt.Fprintln(out, "Invalid value for this field type.")
			continue
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			fmt.Fprintln(out, "Enter one JSON value.")
			continue
		}
		var parsed any
		valueDecoder := json.NewDecoder(bytes.NewReader(raw))
		valueDecoder.UseNumber()
		_ = valueDecoder.Decode(&parsed)
		setSettingsValue(draft, append(append([]string{}, pathParts...), f.name), parsed)
		dirty = true
		fmt.Fprintln(out, "Staged.")
	}
}
func setSettingsValue(root map[string]any, path []string, value any) {
	for _, key := range path[:len(path)-1] {
		child, ok := root[key].(map[string]any)
		if !ok {
			child = map[string]any{}
			root[key] = child
		}
		root = child
	}
	root[path[len(path)-1]] = value
}

func saveSettingsMenu(path string, original []byte, exists bool, draft map[string]any) (string, error) {
	// A private exclusive lock prevents concurrent menu saves. Recheck bytes before
	// replacement as web settings and external editors need not take this lock.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", fmt.Errorf("cannot create configuration directory")
	}
	lock, err := os.OpenFile(path+".menu.lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", fmt.Errorf("cannot lock configuration; ensure its parent directory exists and no other editor is saving")
	}
	_ = lock.Close()
	defer os.Remove(path + ".menu.lock")
	check := func() bool {
		info, e := os.Lstat(path)
		if !exists {
			return os.IsNotExist(e)
		}
		if e != nil || !info.Mode().IsRegular() {
			return false
		}
		current, e := os.ReadFile(path)
		return e == nil && bytes.Equal(current, original)
	}
	if !check() {
		return "", fmt.Errorf("configuration changed; quit and reopen the menu")
	}
	raw, err := json.Marshal(draft)
	if err != nil {
		return "", fmt.Errorf("cannot encode draft")
	}
	var typed config.Settings
	if json.Unmarshal(raw, &typed) != nil {
		return "", fmt.Errorf("invalid draft types")
	}
	encoded, err := yaml.Marshal(typed)
	if err != nil || len(encoded) > config.MaxFileBytes {
		return "", fmt.Errorf("configuration exceeds size limit")
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".nexus-settings-*")
	if err != nil {
		return "", fmt.Errorf("cannot prepare configuration")
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err = temp.Write(encoded); err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil || closeErr != nil {
		return "", fmt.Errorf("cannot write configuration")
	}
	if _, err = config.Load(config.Options{ProjectFile: name}); err != nil {
		return "", fmt.Errorf("configuration validation failed; staged edits retained; check configuration field constraints")
	}
	backup := "none (new file)"
	if exists {
		b, e := os.CreateTemp(filepath.Dir(path), "nexus-config-backup-*")
		if e != nil {
			return "", fmt.Errorf("cannot create backup")
		}
		backup = b.Name()
		_, e = b.Write(original)
		if e == nil {
			e = b.Sync()
		}
		ce := b.Close()
		if e != nil || ce != nil {
			return "", fmt.Errorf("cannot persist backup")
		}
	}
	if !check() {
		return "", fmt.Errorf("configuration changed; original preserved")
	}
	if os.Rename(name, path) != nil {
		return "", fmt.Errorf("cannot install configuration")
	}
	dir, e := os.Open(filepath.Dir(path))
	if e == nil {
		e = dir.Sync()
		_ = dir.Close()
	}
	if e != nil {
		return backup, fmt.Errorf("configuration saved but directory sync failed; reopen before further edits")
	}
	return backup, nil
}
