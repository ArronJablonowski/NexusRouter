package app

// PresentationTextSink receives already-redacted top-level assistant text only
// after the matching lifecycle marker commits. Implementations must be
// non-blocking; the daemon installs a bounded drop-only fan-out.
type PresentationTextSink func(taskID, sessionID, text string)

// InstallPresentationTextSink is construction-only. Call it before sharing the
// service with a dispatcher or request goroutine.
func InstallPresentationTextSink(service *Service, sink PresentationTextSink) error {
	if service == nil || sink == nil {
		return ErrAdmission
	}
	service.presentationTextSink = sink
	return nil
}

func deliverPresentationText(sink PresentationTextSink, task, session, text string) {
	if sink == nil || text == "" {
		return
	}
	// A presentation observer is never part of execution success. The daemon's
	// sink is non-blocking; containment additionally makes observer panic a drop.
	defer func() { _ = recover() }()
	sink(task, session, text)
}
