package cli

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"regexp"
	"strings"

	"github.com/ArronJablonowski/DarwinRouter/skills"
)

const skillsUsage = "usage: darwin skills list|show|history|state|draft|rollback --root path --scope id [--name id] [--version id] [--expected-version id] [--expected-revision sha256] [--limit 100]\nDraft reads a skills.Draft JSON object from stdin. Activation requires trusted validator integration and is unavailable."

var skillIdentifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func skillVersionID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 16 && len(id) == 32
}

func skillsError(w io.Writer, message string, code int) int {
	if _, err := io.WriteString(w, message+"\n"); err != nil {
		return 1
	}
	return code
}

func runSkills(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return skillsError(stderr, skillsUsage, 2)
	}
	command := args[0]
	if command == "learning" {
		return runSkillLearning(args[1:], stdout, stderr)
	}
	switch command {
	case "list", "show", "history", "state", "draft", "rollback":
	default:
		return skillsError(stderr, skillsUsage, 2)
	}
	seen := map[string]bool{}
	for i := 1; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "-") {
			return skillsError(stderr, skillsUsage, 2)
		}
		key, _, equal := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if key == "" || seen[key] {
			return skillsError(stderr, skillsUsage, 2)
		}
		seen[key] = true
		if !equal {
			i++ // All supported flags consume exactly one value.
		}
	}
	f := flag.NewFlagSet("skills", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	root := f.String("root", "", "explicit private store directory")
	scope := f.String("scope", "", "exact scope")
	name := f.String("name", "", "skill name")
	version := f.String("version", "", "version to inspect; default active")
	expected := f.String("expected-version", "", "current active version required for rollback")
	revision := f.String("expected-revision", "", "optional exact activation revision required for rollback")
	limit := f.Int("limit", 100, "maximum active metadata entries")
	if f.Parse(args[1:]) != nil || f.NArg() != 0 || *root == "" || !skillIdentifier.MatchString(*scope) {
		return skillsError(stderr, skillsUsage, 2)
	}
	if (command == "show" || command == "history" || command == "state" || command == "rollback") && !skillIdentifier.MatchString(*name) {
		return skillsError(stderr, skillsUsage, 2)
	}
	invalidFlag := false
	f.Visit(func(v *flag.Flag) {
		if v.Name == "name" && (command == "list" || command == "draft") ||
			v.Name == "version" && command != "show" ||
			v.Name == "expected-version" && command != "rollback" ||
			v.Name == "expected-revision" && command != "rollback" ||
			v.Name == "limit" && command != "list" {
			invalidFlag = true
		}
	})
	if invalidFlag || *limit < 1 || *limit > 100 || (*version != "" && !skillVersionID(*version)) || (command == "rollback" && !skillVersionID(*expected)) {
		return skillsError(stderr, skillsUsage, 2)
	}
	key := skills.Key{Scope: *scope, Name: *name}
	state := skills.ActivationState{Version: 1, Key: key, Active: *expected, Revision: *revision}
	if seen["expected-revision"] && state.Validate() != nil {
		return skillsError(stderr, skillsUsage, 2)
	}
	var draft skills.Draft
	if command == "draft" {
		b, err := io.ReadAll(io.LimitReader(stdin, (256<<10)+1))
		if err != nil || len(b) > 256<<10 {
			return skillsError(stderr, "skills: invalid draft input", 2)
		}
		decoder := json.NewDecoder(bytes.NewReader(b))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&draft) != nil || decoder.Decode(new(any)) != io.EOF || draft.Key.Scope != *scope || !validSkillDraft(draft) {
			return skillsError(stderr, "skills: invalid draft input or scope", 2)
		}
	}
	var store *skills.FileStore
	var err error
	if command == "draft" {
		store, err = skills.Open(*root, []string{*scope})
	} else {
		store, err = skills.OpenReadOnly(*root, []string{*scope})
		if err == nil && command == "rollback" {
			err = store.Close()
			if err == nil {
				store, err = skills.Open(*root, []string{*scope})
			}
		}
	}
	if err != nil {
		return skillsError(stderr, "skills: store unavailable or invalid", 1)
	}
	ctx := context.Background()
	var result any
	switch command {
	case "list":
		var metadata []skills.Metadata
		metadata, err = store.Discover(ctx, *scope, nil, *limit)
		if metadata == nil {
			metadata = []skills.Metadata{}
		}
		result = metadata
	case "show":
		result, err = store.Load(ctx, key, *version)
	case "history":
		result, err = store.History(ctx, key)
	case "state":
		result, err = store.ActivationState(ctx, key)
	case "draft":
		result, err = store.Draft(ctx, draft, false)
	case "rollback":
		if seen["expected-revision"] {
			err = store.RollbackAt(ctx, state, false)
		} else {
			err = store.Rollback(ctx, key, *expected, false)
		}
		result = struct {
			Key            skills.Key `json:"key"`
			RolledBackFrom string     `json:"rolled_back_from"`
		}{key, *expected}
	}
	err = errors.Join(err, store.Close())
	if err != nil {
		if errors.Is(err, skills.ErrConflict) {
			return skillsError(stderr, "skills: activation state changed; inspect before retrying", 1)
		}
		return skillsError(stderr, "skills: operation failed; requested record unavailable or invalid", 1)
	}
	if json.NewEncoder(stdout).Encode(result) != nil {
		return 1
	}
	return 0
}

// Validate before opening a mutable store, so malformed input cannot create it.
func validSkillDraft(d skills.Draft) bool {
	if !skillIdentifier.MatchString(d.Key.Scope) || !skillIdentifier.MatchString(d.Key.Name) || d.Description == "" || len(d.Description) > 1024 || len(d.Steps) == 0 || len(d.SourceSessions) == 0 || len(d.ValidationCases) == 0 {
		return false
	}
	for _, list := range [][]string{d.SourceSessions, d.Tags, d.RequiredTools} {
		for _, v := range list {
			if !skillIdentifier.MatchString(v) {
				return false
			}
		}
	}
	for _, list := range [][]string{d.Steps, d.ValidationCases, d.Risks} {
		for _, v := range list {
			if v == "" {
				return false
			}
		}
	}
	return true
}
