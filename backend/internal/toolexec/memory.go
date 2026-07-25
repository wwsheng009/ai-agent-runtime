package toolexec

import (
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

const (
	// DefaultTerminalFailureThreshold opens the circuit after N identical
	// non-retryable failures for the same tool+args digest within one run.
	DefaultTerminalFailureThreshold = 2
	// DefaultEmptyReplayThreshold short-circuits identical empty-success
	// digests after N observations within one run. Soft: returns the prior
	// empty evidence without re-executing the tool.
	DefaultEmptyReplayThreshold = 2

	MetadataArgumentsDigestKey = "arguments_digest"
	MetadataAttemptKey         = "attempt"
	MetadataCircuitOpenKey     = "circuit_open"
	MetadataPreflightKey       = "preflight"
	MetadataInvocationKey      = "tool_invocation"
	// MetadataEmptyReplayKey marks a soft empty-success short-circuit.
	MetadataEmptyReplayKey = "empty_replay"
	// MetadataPathCandidatesKey carries nearby path suggestions for missing-path recovery.
	// Prefer toolresult.MetadataPathCandidatesKey; kept as a stable alias for callers.
	MetadataPathCandidatesKey = toolresult.MetadataPathCandidatesKey
	// MetadataAttemptedArgsKey aliases the model-facing compact args summary key.
	MetadataAttemptedArgsKey = toolresult.MetadataAttemptedArgsKey
)

// FailureRecord stores the last terminal failure for a tool invocation fingerprint.
type FailureRecord struct {
	ToolName   string
	Digest     string
	ErrorCode  string
	Error      string
	Retryable  bool
	NextAction string
	// PathCandidates preserves nearby path suggestions across circuit opens so
	// the 3rd+ identical call still surfaces recovery hints to the model.
	PathCandidates []string
	Count          int
	LastSeen       time.Time
	Open           bool
}

// EmptyRecord stores repeated empty-success outcomes for soft negative caching.
// Unlike FailureRecord this never hard-fails the call; once the threshold is
// reached, preflight may short-circuit by replaying the empty disposition.
type EmptyRecord struct {
	ToolName      string
	Digest        string
	NextAction    string
	AttemptedArgs map[string]interface{}
	Count         int
	LastSeen      time.Time
	Open          bool
}

// Memory is a session/run scoped negative cache and circuit for tool invocations.
// It is intentionally process-local and not a global process cache.
type Memory struct {
	mu             sync.Mutex
	threshold      int
	emptyThreshold int
	failures       map[string]*FailureRecord
	empties        map[string]*EmptyRecord
	attempts       map[string]int
}

// NewMemory creates run-scoped tool execution memory.
func NewMemory(threshold int) *Memory {
	if threshold <= 0 {
		threshold = DefaultTerminalFailureThreshold
	}
	return &Memory{
		threshold:      threshold,
		emptyThreshold: DefaultEmptyReplayThreshold,
		failures:       make(map[string]*FailureRecord),
		empties:        make(map[string]*EmptyRecord),
		attempts:       make(map[string]int),
	}
}

// WithEmptyThreshold overrides the empty-success soft-cache threshold.
func (m *Memory) WithEmptyThreshold(threshold int) *Memory {
	if m == nil {
		return nil
	}
	if threshold <= 0 {
		threshold = DefaultEmptyReplayThreshold
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emptyThreshold = threshold
	return m
}

func (m *Memory) key(toolName, digest string) string {
	return strings.TrimSpace(toolName) + "\x00" + strings.TrimSpace(digest)
}

// BeginAttempt increments the attempt counter for the digest and returns the attempt number.
func (m *Memory) BeginAttempt(toolName, digest string) int {
	if m == nil {
		return 1
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := m.key(toolName, digest)
	m.attempts[key]++
	return m.attempts[key]
}

// LookupFailure returns a previous terminal failure when the circuit is open.
func (m *Memory) LookupFailure(toolName, digest string) (*FailureRecord, bool) {
	if m == nil {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.failures[m.key(toolName, digest)]
	if !ok || record == nil || !record.Open {
		return nil, false
	}
	clone := *record
	if len(record.PathCandidates) > 0 {
		clone.PathCandidates = append([]string(nil), record.PathCandidates...)
	}
	return &clone, true
}

// LookupEmpty returns a previous empty-success when the soft negative cache is open.
func (m *Memory) LookupEmpty(toolName, digest string) (*EmptyRecord, bool) {
	if m == nil {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.empties[m.key(toolName, digest)]
	if !ok || record == nil || !record.Open {
		return nil, false
	}
	clone := *record
	if len(record.AttemptedArgs) > 0 {
		clone.AttemptedArgs = cloneStringInterfaceMap(record.AttemptedArgs)
	}
	return &clone, true
}

// RecordSuccess clears terminal-failure circuit state for the digest.
// Empty soft-cache entries are left intact so identical empty digests can still
// short-circuit; callers that produce real (non-empty) success should use
// RecordNonEmptySuccess instead.
func (m *Memory) RecordSuccess(toolName, digest string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.failures, m.key(toolName, digest))
}

// RecordNonEmptySuccess clears both terminal-failure and empty soft-cache state.
func (m *Memory) RecordNonEmptySuccess(toolName, digest string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := m.key(toolName, digest)
	delete(m.failures, key)
	delete(m.empties, key)
}

// RecordFailure records an outcome and opens the circuit for terminal classes.
func (m *Memory) RecordFailure(toolName, digest string, diagnostic toolresult.Diagnostic, errText string) *FailureRecord {
	if m == nil {
		return nil
	}
	if diagnostic.OK || diagnostic.Retryable {
		return nil
	}
	if !isTerminalCircuitErrorCode(diagnostic.ErrorCode) {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	key := m.key(toolName, digest)
	// A hard failure supersedes any soft empty cache for the same digest.
	delete(m.empties, key)
	record := m.failures[key]
	if record == nil || record.ErrorCode != diagnostic.ErrorCode {
		record = &FailureRecord{
			ToolName:  strings.TrimSpace(toolName),
			Digest:    strings.TrimSpace(digest),
			ErrorCode: diagnostic.ErrorCode,
		}
		m.failures[key] = record
	}
	record.Error = strings.TrimSpace(errText)
	record.Retryable = diagnostic.Retryable
	if next := strings.TrimSpace(diagnostic.NextAction); next != "" {
		record.NextAction = next
	}
	// Preserve the richest path candidate set seen so far for this digest.
	if len(diagnostic.PathCandidates) > 0 {
		record.PathCandidates = mergeUniqueStrings(record.PathCandidates, diagnostic.PathCandidates)
	}
	record.Count++
	record.LastSeen = time.Now().UTC()
	if record.Count >= m.threshold {
		record.Open = true
	}
	clone := *record
	if len(record.PathCandidates) > 0 {
		clone.PathCandidates = append([]string(nil), record.PathCandidates...)
	}
	return &clone
}

// RecordEmpty records a successful empty result for soft negative caching.
// After emptyThreshold identical empty outcomes the soft cache opens and
// preflight may short-circuit without re-executing the tool.
func (m *Memory) RecordEmpty(toolName, digest string, diagnostic toolresult.Diagnostic) *EmptyRecord {
	if m == nil {
		return nil
	}
	if !diagnostic.OK {
		return nil
	}
	if !diagnostic.EmptyResult && diagnostic.Outcome != toolresult.OutcomeEmpty {
		return nil
	}
	// Partial batches are not empty-success and must not soft-cache.
	if diagnostic.PartialFailure || diagnostic.Outcome == toolresult.OutcomePartial {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	key := m.key(toolName, digest)
	// Empty soft cache only applies when there is no open hard circuit.
	if failure, ok := m.failures[key]; ok && failure != nil && failure.Open {
		return nil
	}
	record := m.empties[key]
	if record == nil {
		record = &EmptyRecord{
			ToolName: strings.TrimSpace(toolName),
			Digest:   strings.TrimSpace(digest),
		}
		m.empties[key] = record
	}
	if next := strings.TrimSpace(diagnostic.NextAction); next != "" {
		record.NextAction = next
	}
	if len(diagnostic.AttemptedArgs) > 0 {
		record.AttemptedArgs = cloneStringInterfaceMap(diagnostic.AttemptedArgs)
	}
	record.Count++
	record.LastSeen = time.Now().UTC()
	if record.Count >= m.emptyThreshold {
		record.Open = true
	}
	clone := *record
	if len(record.AttemptedArgs) > 0 {
		clone.AttemptedArgs = cloneStringInterfaceMap(record.AttemptedArgs)
	}
	return &clone
}

func isTerminalCircuitErrorCode(code string) bool {
	switch strings.TrimSpace(code) {
	case "TOOL_INVALID_ARGS", "TOOL_PATH_NOT_FOUND", "AGENT_PERMISSION",
		"TOOL_NOT_FOUND", "TOOL_NOT_REGISTERED", "JOB_NOT_FOUND",
		"APPROVAL_EXPIRED", "API_UNAUTHORIZED", "API_BAD_REQUEST",
		"VALIDATION_FAILED", "CONFIG_INVALID", "WRITE_PRECONDITION_FAILED":
		return true
	default:
		return false
	}
}

func mergeUniqueStrings(existing, extra []string) []string {
	if len(extra) == 0 {
		return append([]string(nil), existing...)
	}
	out := make([]string, 0, len(existing)+len(extra))
	seen := map[string]struct{}{}
	for _, item := range existing {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	for _, item := range extra {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func cloneStringInterfaceMap(in map[string]interface{}) map[string]interface{} {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
