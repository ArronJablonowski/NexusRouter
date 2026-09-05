package skills

// WorkflowSource binds a learning example to a completed journal snapshot and
// its current accepted evaluation. It is sensitive source material, not a model
// instruction or authority to execute its steps. The storage selector derives
// these fields; application adapters must enforce privacy and redact before use.
type WorkflowSource struct {
	Example                                      WorkflowExample
	Privacy                                      string
	EvaluationID, EvaluationDigest, SourceDigest string
	SourceSequence                               int64
}

// ValidateWorkflowExamples exposes the same bounded admission used by drafting
// for host adapters selecting examples from durable history.
func ValidateWorkflowExamples(key Key, examples []WorkflowExample) error {
	_, _, _, err := prepareWorkflows(key, examples)
	return err
}
