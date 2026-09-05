package skills

import (
	"context"
	"reflect"
	"time"
)

// DraftFromWorkflows proposes an inactive skill from verified repeated workflow
// examples. Source provenance is derived from admitted examples, never from the
// generator's claims. The automatic-update switch must remain enabled at commit;
// this operation never validates for activation or changes the active version.
//
// Generators are trusted in-process callbacks, not sandboxed plugins. A host
// using a model-backed generator must enforce routing, privacy, redaction and
// resource/cost budgets itself. Callbacks must honor cancellation and support
// concurrent calls. The thirty-second deadline is cooperative: this method waits
// for a callback to return rather than abandoning a running goroutine.
func (s *FileStore) DraftFromWorkflows(ctx context.Context, key Key, examples []WorkflowExample, generator DraftGenerator) (Version, error) {
	if ctx == nil || s == nil || !s.permitted(key) {
		return Version{}, ErrInvalid
	}
	if ctx.Err() != nil {
		return Version{}, ctx.Err()
	}
	if s.readOnly || !s.automatic.Load() {
		return Version{}, ErrDisabled
	}
	if generator == nil || nilDraftGenerator(generator) {
		return Version{}, ErrValidation
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	snapshot, sessions, evidence, err := prepareWorkflows(key, examples)
	if err != nil {
		return Version{}, err
	}
	// Keep canonical metadata separate from the generator-owned request snapshot.
	sessions = append([]string(nil), sessions...)
	evidence = append([]string(nil), evidence...)
	if bounded.Err() != nil {
		return Version{}, bounded.Err()
	}
	if !s.automatic.Load() {
		return Version{}, ErrDisabled
	}
	draft, err := generateWorkflowDraft(bounded, generator, key, snapshot)
	if bounded.Err() != nil {
		return Version{}, bounded.Err()
	}
	if err != nil || draft.Key != key {
		return Version{}, ErrValidation
	}
	draft.SourceSessions = sessions
	draft.SourceEvidence = evidence
	if validateGeneratedDraft(draft) != nil {
		return Version{}, ErrValidation
	}
	version, err := s.Draft(bounded, draft, true)
	if err != nil {
		return Version{}, err
	}
	return version, nil
}

func nilDraftGenerator(generator DraftGenerator) bool {
	value := reflect.ValueOf(generator)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	}
	return false
}

func generateWorkflowDraft(ctx context.Context, generator DraftGenerator, key Key, examples []WorkflowExample) (draft Draft, err error) {
	defer func() {
		if recover() != nil {
			draft, err = Draft{}, ErrValidation
		}
	}()
	draft, err = generator.Generate(ctx, key, examples)
	if err != nil {
		return Draft{}, ErrValidation
	}
	return draft, nil
}
