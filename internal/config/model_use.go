package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
)

// Model-use policy is separate from startup configuration so changes apply to
// new admissions without restarting a router or interrupting active work.
type ModelUsePolicy struct {
	Version  int             `json:"version"`
	Disabled map[string]bool `json:"disabled"`
}

var modelUseID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func ModelUseKey(host, model string) (string, error) {
	if !modelUseID.MatchString(host) || !modelUseID.MatchString(model) {
		return "", ErrConfigWrite
	}
	return host + "/" + model, nil
}
func ReadModelUse(database string) (ModelUsePolicy, error) {
	p := ModelUsePolicy{Version: 1, Disabled: map[string]bool{}}
	if database == "" || database == ":memory:" {
		return p, nil
	}
	path := database + ".model-use.json"
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return p, ErrConfigWrite
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	if json.Unmarshal(b, &p) != nil || p.Version != 1 || p.Disabled == nil {
		return p, ErrConfigWrite
	}
	return p, nil
}
func ModelUseAllowed(database, host, model string) bool {
	key, err := ModelUseKey(host, model)
	if err != nil {
		return false
	}
	p, err := ReadModelUse(database)
	return err == nil && !p.Disabled[key]
}
func UpdateModelUse(database, host, model string, enabled bool) (ModelUsePolicy, error) {
	key, err := ModelUseKey(host, model)
	if err != nil || database == "" || database == ":memory:" {
		return ModelUsePolicy{}, ErrConfigWrite
	}
	path := database + ".model-use.json"
	unlock, err := lockProjectUpdate(path)
	if err != nil {
		return ModelUsePolicy{}, err
	}
	defer unlock()
	p, err := ReadModelUse(database)
	if err != nil {
		return p, err
	}
	if enabled {
		delete(p.Disabled, key)
	} else {
		p.Disabled[key] = true
	}
	b, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".model-use-*")
	if err != nil {
		return p, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = writeAndSync(f, b); err != nil {
		return p, err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return p, err
	}
	return p, syncDirectory(filepath.Dir(path))
}
