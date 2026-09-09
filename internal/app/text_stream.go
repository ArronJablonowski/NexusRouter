package app

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

// RunTextStream emits provisional assistant text after the corresponding
// lifecycle marker commits. Text is incrementally redacted, not persisted as
// token deltas and not replayable. Intermediate assistant turns may be included;
// only a successful return establishes final task completion. Tool contents and
// delegated child streams are never forwarded. Callbacks are synchronous and
// must cooperate with cancellation; errors or panics cancel and join execution.
func (s *Service) RunTextStream(ctx context.Context, r Request, emit func(string) error) (Result, error) {
	if emit == nil {
		return Result{}, ErrAdmission
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var deliveryErr error
	r.textSink = func(text string) {
		if deliveryErr != nil || ctx.Err() != nil {
			return
		}
		defer func() {
			if recover() != nil {
				deliveryErr = ErrEventDelivery
				cancel()
			}
		}()
		if !utf8.ValidString(text) || emit(text) != nil {
			deliveryErr = ErrEventDelivery
			cancel()
		}
	}
	result, err := s.Run(ctx, r)
	return result, errors.Join(err, deliveryErr)
}

// textDelivery belongs to one execution journal. Unfinished redactor tails are
// discarded on failure. Matching spans assistant turns because clients combine
// their text; a secret split across two turns must not leak after concatenation.
type textDelivery struct {
	secrets  []string
	emit     func(string, bool)
	redactor *textRedactor
	utf8Tail string
}

func (d *textDelivery) accept(kind runtime.Kind, text string) {
	switch kind {
	case runtime.TurnStarted:
		if d.redactor == nil {
			d.redactor = newTextRedactor(d.secrets)
		}
	case runtime.ModelDelta:
		if d.redactor != nil {
			d.deliver(d.redactor.Write(text), false)
		}
	case runtime.TaskCompleted:
		if d.redactor != nil {
			d.deliver(d.redactor.Flush(), true)
			d.redactor = nil
		}
	case runtime.TaskFailed, runtime.TaskCanceled:
		d.redactor = nil
		d.utf8Tail = ""
	}
}

func (d *textDelivery) deliver(text string, final bool) {
	text = d.utf8Tail + text
	d.utf8Tail = ""
	end := len(text)
	if !final {
		// Only an incomplete terminal rune is retained. Any invalid complete
		// rune reaches the sink's validation and cancels execution fail-closed.
		for i := 0; i < len(text); {
			if !utf8.FullRuneInString(text[i:]) {
				end = i
				break
			}
			_, size := utf8.DecodeRuneInString(text[i:])
			i += size
		}
		d.utf8Tail = text[end:]
	}
	if end > 0 {
		d.emit(text[:end], final)
	}
}
