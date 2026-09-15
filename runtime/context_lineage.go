package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"github.com/ArronJablonowski/DarwinRouter/providers"
)

const (
	ContextLineageVersion = 1
	maxContextEpochs      = 256
	maxContextToolCallIDs = 100_000
)

var ErrInvalidContextLineage = errors.New("invalid context lineage")

// ContextCompactionEpoch is one ordered shortening of a session context. TaskID
// identifies the task whose journal activated the checkpoint; ActivationSequence
// identifies the exact task-local activation boundary.
type ContextCompactionEpoch struct {
	Version            int               `json:"version"`
	TaskID             string            `json:"task_id"`
	ActivationSequence int64             `json:"activation_sequence"`
	Compaction         ContextCompaction `json:"compaction"`
}

// ContextLineage is the bounded context identity carried between continuation
// tasks. ToolCallIDs is the sorted set of every call identity observed before or
// during the epochs, including identities no longer present in provider context.
type ContextLineage struct {
	Version     int                      `json:"version"`
	Epochs      []ContextCompactionEpoch `json:"epochs"`
	ToolCallIDs []string                 `json:"tool_call_ids,omitempty"`
	Digest      string                   `json:"digest"`
}

func (l ContextLineage) CanonicalDigest() (string, error) {
	if l.validateStructure() != nil {
		return "", ErrInvalidContextLineage
	}
	l.Digest = ""
	body, err := json.Marshal(l)
	if err != nil || len(body) > MaxContextCompactionPlanBytes {
		return "", ErrInvalidContextLineage
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func (l ContextLineage) Validate() error {
	digest, err := l.CanonicalDigest()
	if err != nil || !validCompactionPlanDigest(l.Digest) || digest != l.Digest {
		return ErrInvalidContextLineage
	}
	return nil
}

func (l ContextLineage) validateStructure() error {
	if l.Version != ContextLineageVersion || len(l.Epochs) == 0 || len(l.Epochs) > maxContextEpochs || len(l.ToolCallIDs) > maxContextToolCallIDs {
		return ErrInvalidContextLineage
	}
	if !validContextToolCallIDs(l.ToolCallIDs) {
		return ErrInvalidContextLineage
	}
	for i, epoch := range l.Epochs {
		if epoch.Version != ContextLineageVersion || epoch.TaskID == "" || len(epoch.TaskID) > 128 || epoch.ActivationSequence < 1 || epoch.Compaction.Validate(epoch.Compaction.SourceTaskID) != nil {
			return ErrInvalidContextLineage
		}
		if i > 0 {
			previous := l.Epochs[i-1]
			if epoch.TaskID == previous.TaskID && epoch.ActivationSequence <= previous.ActivationSequence {
				return ErrInvalidContextLineage
			}
		}
		for _, id := range epoch.Compaction.SourceToolCallIDs {
			position := sort.SearchStrings(l.ToolCallIDs, id)
			if position == len(l.ToolCallIDs) || l.ToolCallIDs[position] != id {
				return ErrInvalidContextLineage
			}
		}
	}
	return nil
}

// ExtendContextLineage appends one epoch and unions call identities without
// mutating caller-owned values. The source task must extend the immediately
// preceding epoch when one exists.
func ExtendContextLineage(inherited *ContextLineage, task string, sequence int64, compaction *ContextCompaction, messages []providers.Message) (*ContextLineage, error) {
	if task == "" || sequence < 1 || compaction == nil || compaction.Validate(compaction.SourceTaskID) != nil {
		return nil, ErrInvalidContextLineage
	}
	lineage := ContextLineage{Version: ContextLineageVersion}
	if inherited != nil {
		if inherited.Validate() != nil {
			return nil, ErrInvalidContextLineage
		}
		body, _ := json.Marshal(inherited)
		if json.Unmarshal(body, &lineage) != nil {
			return nil, ErrInvalidContextLineage
		}
		lineage.Digest = ""
	}
	lineage.Epochs = append(lineage.Epochs, ContextCompactionEpoch{Version: ContextLineageVersion, TaskID: task, ActivationSequence: sequence, Compaction: *compaction})
	ids := make(map[string]struct{}, len(lineage.ToolCallIDs))
	for _, id := range lineage.ToolCallIDs {
		ids[id] = struct{}{}
	}
	for _, id := range compaction.SourceToolCallIDs {
		ids[id] = struct{}{}
	}
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			ids[call.ID] = struct{}{}
		}
	}
	lineage.ToolCallIDs = lineage.ToolCallIDs[:0]
	for id := range ids {
		lineage.ToolCallIDs = append(lineage.ToolCallIDs, id)
	}
	sort.Strings(lineage.ToolCallIDs)
	digest, err := lineage.CanonicalDigest()
	if err != nil {
		return nil, err
	}
	lineage.Digest = digest
	body, err := json.Marshal(lineage)
	if err != nil {
		return nil, ErrInvalidContextLineage
	}
	var owned ContextLineage
	if json.Unmarshal(body, &owned) != nil {
		return nil, ErrInvalidContextLineage
	}
	return &owned, nil
}

// ContextSourceStateDigest binds visible messages and the complete lineage.
// It is used by version-two checkpoints; version one remains message-only.
func ContextSourceStateDigest(messages []providers.Message, lineage *ContextLineage) (string, error) {
	if lineage != nil && lineage.Validate() != nil || providers.ValidateMessages(messages) != nil {
		return "", ErrInvalidContextLineage
	}
	body, err := json.Marshal(struct {
		Version  int                 `json:"version"`
		Messages []providers.Message `json:"messages"`
		Lineage  *ContextLineage     `json:"lineage"`
	}{2, messages, lineage})
	if err != nil || len(body) > MaxContextCompactionPlanBytes {
		return "", ErrInvalidContextLineage
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// ContextSourceToolCallIDs returns the canonical complete call-identity set for
// a source snapshot, including identities retired by earlier epochs.
func ContextSourceToolCallIDs(messages []providers.Message, lineage *ContextLineage) ([]string, error) {
	if lineage != nil && lineage.Validate() != nil || providers.ValidateMessages(messages) != nil {
		return nil, ErrInvalidContextLineage
	}
	set := map[string]struct{}{}
	if lineage != nil {
		for _, id := range lineage.ToolCallIDs {
			set[id] = struct{}{}
		}
	}
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			set[call.ID] = struct{}{}
		}
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if !validContextToolCallIDs(ids) {
		return nil, ErrInvalidContextLineage
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return ids, nil
}

func validContextToolCallIDs(ids []string) bool {
	if len(ids) > maxContextToolCallIDs {
		return false
	}
	previous := ""
	for _, id := range ids {
		if id == "" || id <= previous {
			return false
		}
		previous = id
	}
	return true
}
