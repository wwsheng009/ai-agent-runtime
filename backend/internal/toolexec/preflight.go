package toolexec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/observability"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// PreflightRequest is the schema-driven input for tool-agnostic preflight checks.
type PreflightRequest struct {
	ToolName    string
	ToolCallID  string
	Args        map[string]interface{}
	InputSchema map[string]interface{}
	Metadata    map[string]interface{}
	// WorkspaceRoot is the session/tool base path used to resolve relative
	// content paths (mirrors toolkit SetBasePath). When empty, relative paths
	// fall back to process cwd via filepath.Abs.
	WorkspaceRoot string
	// PathExists optionally overrides filesystem checks (tests).
	// It receives the resolved absolute/candidate path after WorkspaceRoot join.
	PathExists func(path string) bool
}

// PreflightDecision describes whether execution should proceed.
type PreflightDecision struct {
	Allow   bool
	Digest  string
	Attempt int
	// Args is the compact source for attempted_args metadata (not a full dump).
	Args       map[string]interface{}
	Error      string
	ErrorCode  string
	Retryable  bool
	NextAction string
	Diagnostic toolresult.Diagnostic
	Circuit    bool
	// SoftEmpty is true when preflight short-circuits an identical empty-success
	// digest. Allow is false so tools are not re-executed, but Error stays empty
	// and the result is treated as a successful empty disposition.
	SoftEmpty bool
	// SkipEmptyReplayCache is true when tool metadata declares that empty results
	// are volatile (for example polling/state reads) and must not be replayed.
	SkipEmptyReplayCache bool
	Preflight            string
	// PathCandidates are nearby filesystem siblings that may correct a missing path.
	// Generic model recovery signal — not tool-specific.
	PathCandidates []string
	// PathAutoHealed is true when a unique high-confidence nearby path replaced
	// a missing read-only path so the tool can run instead of hard-failing.
	PathAutoHealed bool
	// OriginalPath / ResolvedPath record the auto-heal rewrite for observability.
	OriginalPath string
	ResolvedPath string
}

// ApplyPreflight validates required args, consults the failure circuit, and
// optionally checks read-like path existence when schema/metadata imply it.
func ApplyPreflight(memory *Memory, req PreflightRequest) PreflightDecision {
	toolName := strings.TrimSpace(req.ToolName)
	// Models sometimes emit empty/null path placeholders as the literal characters
	// `""` / `null` (live residual: grep path=""). Normalize those to true empty
	// before digest/required/path checks so optional path tools can default to
	// workspace root instead of hard-failing with root-directory noise candidates.
	if clearPlaceholderPathLikeArgs(req.Args) {
		// Keep decision.Args pointing at the mutated map below.
	}
	// Models routinely send schema-mismatched JSON types that tool
	// implementations already tolerate (timeout=120 as bare seconds, commands
	// as a single string). Normalize those before digest/type checks so
	// preflight does not reject what the tool would have accepted.
	normalizeCoercibleSchemaArgs(req.InputSchema, req.Args)
	digest := ArgsDigest(toolName, req.Args)
	attempt := 1
	if memory != nil {
		attempt = memory.BeginAttempt(toolName, digest)
	}

	decision := PreflightDecision{
		Allow:                true,
		Digest:               digest,
		Attempt:              attempt,
		Args:                 req.Args,
		SkipEmptyReplayCache: emptyReplayCacheDisabled(req.Metadata),
	}

	if missing := missingRequiredArgs(req.InputSchema, req.Args); len(missing) > 0 {
		decision.Allow = false
		decision.ErrorCode = string(runtimeerrors.ErrToolInvalidArgs)
		decision.Error = fmt.Sprintf("missing required argument(s): %s", strings.Join(missing, ", "))
		decision.Retryable = false
		decision.NextAction = "Correct the tool arguments using the current schema, then call it again."
		decision.Preflight = "required_args"
		decision.Diagnostic = toolresult.Diagnose(toolName, req.ToolCallID, decision.Error, map[string]interface{}{
			toolresult.MetadataErrorCodeKey:  decision.ErrorCode,
			toolresult.MetadataRetryableKey:  false,
			toolresult.MetadataNextActionKey: decision.NextAction,
		})
		observability.RecordToolPreflight(decision.Preflight, false)
		return decision
	}

	if invalid := invalidSchemaArgs(req.InputSchema, req.Args); len(invalid) > 0 {
		decision.Allow = false
		decision.ErrorCode = string(runtimeerrors.ErrToolInvalidArgs)
		decision.Error = fmt.Sprintf("invalid argument type(s): %s", strings.Join(invalid, "; "))
		decision.Retryable = false
		decision.NextAction = "Correct the tool argument types using the current schema, then call it again. Do not retry the same invalid types unchanged."
		decision.Preflight = "arg_types"
		decision.Diagnostic = toolresult.Diagnose(toolName, req.ToolCallID, decision.Error, map[string]interface{}{
			toolresult.MetadataErrorCodeKey:  decision.ErrorCode,
			toolresult.MetadataRetryableKey:  false,
			toolresult.MetadataNextActionKey: decision.NextAction,
		})
		observability.RecordToolPreflight(decision.Preflight, false)
		return decision
	}

	if memory != nil {
		if record, open := memory.LookupFailure(toolName, digest); open && record != nil {
			decision.Allow = false
			decision.Circuit = true
			decision.ErrorCode = record.ErrorCode
			decision.Error = record.Error
			if decision.Error == "" {
				decision.Error = fmt.Sprintf("repeated terminal failure for %s (%s)", toolName, record.ErrorCode)
			}
			decision.Retryable = false
			decision.NextAction = strengthenNoRetryAction(record.NextAction, record.ErrorCode)
			decision.Preflight = "circuit_open"
			// Restore path candidates stored with the original terminal failure so
			// circuit-open replays still surface recovery hints to the model.
			if len(record.PathCandidates) > 0 {
				decision.PathCandidates = append([]string(nil), record.PathCandidates...)
			} else if decision.ErrorCode == string(runtimeerrors.ErrToolPathNotFound) {
				// Best-effort recompute when older records lack stored candidates.
				if pathValue := firstMissingPathHint(req); pathValue != "" {
					if hints := suggestNearbyPathCandidatesForRequest(pathValue, req); len(hints) > 0 {
						decision.PathCandidates = hints
						if !strings.Contains(strings.ToLower(decision.NextAction), "nearby candidates") {
							decision.NextAction = fmt.Sprintf(
								"%s Nearby candidates: %s.",
								strings.TrimRight(decision.NextAction, "."),
								strings.Join(hints, ", "),
							)
						}
					}
				}
			}
			diagMeta := map[string]interface{}{
				toolresult.MetadataErrorCodeKey:  decision.ErrorCode,
				toolresult.MetadataRetryableKey:  false,
				toolresult.MetadataNextActionKey: decision.NextAction,
			}
			if len(decision.PathCandidates) > 0 {
				diagMeta[MetadataPathCandidatesKey] = decision.PathCandidates
			}
			decision.Diagnostic = toolresult.Diagnose(toolName, req.ToolCallID, decision.Error, diagMeta)
			observability.RecordToolPreflight(decision.Preflight, false)
			return decision
		}
		// Soft empty negative cache: identical empty-success digests short-circuit
		// without re-executing. Still a success disposition (Error empty).
		if !decision.SkipEmptyReplayCache {
			if record, open := memory.LookupEmpty(toolName, digest); open && record != nil {
				decision.Allow = false
				decision.SoftEmpty = true
				decision.Retryable = false
				decision.Preflight = "empty_replay"
				decision.NextAction = strengthenEmptyReplayAction(record.NextAction, record.Count)
				if decision.NextAction == "" {
					decision.NextAction = strengthenEmptyReplayAction("", record.Count)
				}
				diagMeta := map[string]interface{}{
					toolresult.MetadataEmptyResultKey: true,
					toolresult.MetadataOutcomeKey:     toolresult.OutcomeEmpty,
					toolresult.MetadataRetryableKey:   false,
					toolresult.MetadataNextActionKey:  decision.NextAction,
					MetadataEmptyReplayKey:            true,
				}
				if len(record.AttemptedArgs) > 0 {
					diagMeta[MetadataAttemptedArgsKey] = cloneStringInterfaceMap(record.AttemptedArgs)
				} else if compact := toolresult.CompactAttemptedArgs(req.Args); len(compact) > 0 {
					diagMeta[MetadataAttemptedArgsKey] = compact
				}
				// Soft empty is a successful empty disposition: Diagnose with empty err.
				decision.Diagnostic = toolresult.Diagnose(toolName, req.ToolCallID, "", diagMeta)
				decision.Diagnostic.EmptyResult = true
				decision.Diagnostic.Outcome = toolresult.OutcomeEmpty
				decision.Diagnostic.NextAction = decision.NextAction
				observability.RecordToolPreflight(decision.Preflight, false)
				return decision
			}
		}
	}

	if pathErr, pathValue := preflightMissingReadPath(req); pathErr != "" {
		// Unique high-confidence sibling (case / extension / close typo): rewrite
		// the single missing content path and allow the read-like tool to run.
		// Multi-path batches stay on the deny/partial path to avoid silent wrong-file reads.
		if pathValue != "" && canAutoHealSingleMissingContentPath(req) {
			if healedPath, hints, ambiguous := uniqueHighConfidencePathCandidate(pathValue, req); healedPath != "" {
				// Honor PathExists hooks (tests / virtual FS). Only rewrite when the
				// healed candidate is considered present by the same checker used for
				// the original miss — never invent a path that still fails existence.
				if pathExistsChecker(req)(healedPath) && rewritePathLikeArgs(req.Args, pathValue, healedPath) {
					decision.Allow = true
					decision.PathAutoHealed = true
					decision.OriginalPath = pathValue
					decision.ResolvedPath = healedPath
					decision.PathCandidates = hints
					decision.Preflight = "path_auto_heal"
					decision.Args = req.Args
					decision.Digest = ArgsDigest(toolName, req.Args)
					if memory != nil {
						decision.Attempt = memory.BeginAttempt(toolName, decision.Digest)
					}
					decision.NextAction = fmt.Sprintf(
						"Path auto-healed: %s → %s (unique high-confidence nearby match). Proceed with resolved path; prefer the corrected path on future calls.",
						pathValue,
						healedPath,
					)
					observability.RecordToolPreflight(decision.Preflight, true)
					return decision
				}
			} else if ambiguous && len(hints) > 0 {
				// Multiple high-confidence siblings — deny with explicit pick guidance
				// so the model does not invent a third path or blind-retry the miss.
				decision.Allow = false
				decision.ErrorCode = string(runtimeerrors.ErrToolPathNotFound)
				decision.Error = fmt.Sprintf("%s: %s (ambiguous candidates: %s)", pathErr, pathValue, strings.Join(hints, ", "))
				decision.Retryable = false
				decision.PathCandidates = hints
				decision.Preflight = "path_existence"
				decision.NextAction = fmt.Sprintf(
					"Path not found: %s. Ambiguous nearby matches (no unique auto-heal): %s. Pick exactly one confirmed path_candidates entry (or ls/glob to disambiguate), then retry. Do not invent a third path or retry the same missing path unchanged.",
					pathValue,
					strings.Join(hints, ", "),
				)
				diagMeta := map[string]interface{}{
					toolresult.MetadataErrorCodeKey:  decision.ErrorCode,
					toolresult.MetadataRetryableKey:  false,
					toolresult.MetadataNextActionKey: decision.NextAction,
					MetadataPathCandidatesKey:        decision.PathCandidates,
				}
				decision.Diagnostic = toolresult.Diagnose(toolName, req.ToolCallID, decision.Error, diagMeta)
				observability.RecordToolPreflight(decision.Preflight, false)
				return decision
			}
		}

		decision.Allow = false
		decision.ErrorCode = string(runtimeerrors.ErrToolPathNotFound)
		decision.Error = pathErr
		decision.Retryable = false
		decision.NextAction = "Verify the path and working directory, correct them, then call the tool again. Do not retry the same missing path unchanged."
		decision.Preflight = "path_existence"
		if pathValue != "" {
			decision.Error = fmt.Sprintf("%s: %s", pathErr, pathValue)
			hints := suggestNearbyPathCandidatesForRequest(pathValue, req)
			parentHint := existingParentPathHint(pathValue, req)
			// Ranked sibling miss is common for invented leaves. When the parent
			// exists, still surface a short non-noise sibling sample so the model
			// can pick a real name without an extra ls/glob round-trip.
			parentOnly := false
			if len(hints) == 0 && parentHint != "" {
				parentOnly = true
				hints = []string{parentHint}
				if siblings := listParentSiblingDiscoveryHints(pathValue, parentHint, req); len(siblings) > 0 {
					hints = append(hints, siblings...)
				}
			}
			if len(hints) > 0 {
				decision.PathCandidates = hints
				if parentOnly {
					if len(hints) > 1 {
						decision.NextAction = fmt.Sprintf(
							"Path not found: %s. Parent directory exists: %s. Sample siblings under the parent are listed in path_candidates after the parent; pick one confirmed entry (or ls/glob under the parent for more), then retry. Do not invent a new leaf or retry the same missing path unchanged.",
							pathValue,
							parentHint,
						)
					} else {
						decision.NextAction = fmt.Sprintf(
							"Path not found: %s. Parent directory exists: %s. Use ls/glob under the parent (or glob for the basename), pick a confirmed path from path_candidates, then retry. Do not retry the same missing path unchanged.",
							pathValue,
							parentHint,
						)
					}
				} else if len(hints) >= 2 {
					decision.NextAction = fmt.Sprintf(
						"Path not found: %s. Multiple nearby candidates: %s. Pick exactly one path_candidates entry (or ls/glob under the parent to confirm), then call the tool again. Do not invent a new path or retry the same missing path unchanged.",
						pathValue,
						strings.Join(hints, ", "),
					)
				} else {
					decision.NextAction = fmt.Sprintf(
						"Path not found: %s. Nearby candidates: %s. Prefer a path_candidates entry (or ls/glob under the parent), then call the tool again. Do not retry the same missing path unchanged.",
						pathValue,
						strings.Join(hints, ", "),
					)
				}
				decision.Error = fmt.Sprintf("%s: %s (candidates: %s)", pathErr, pathValue, strings.Join(hints, ", "))
			} else if parentHint != "" {
				decision.NextAction = fmt.Sprintf(
					"Path not found: %s. Parent directory exists: %s. Use ls/glob under the parent to discover the correct name, then retry. Do not retry the same missing path unchanged.",
					pathValue,
					parentHint,
				)
			}
		}
		diagMeta := map[string]interface{}{
			toolresult.MetadataErrorCodeKey:  decision.ErrorCode,
			toolresult.MetadataRetryableKey:  false,
			toolresult.MetadataNextActionKey: decision.NextAction,
		}
		if len(decision.PathCandidates) > 0 {
			diagMeta[MetadataPathCandidatesKey] = decision.PathCandidates
		}
		decision.Diagnostic = toolresult.Diagnose(toolName, req.ToolCallID, decision.Error, diagMeta)
		observability.RecordToolPreflight(decision.Preflight, false)
		return decision
	}

	observability.RecordToolPreflight(observability.PreflightReasonAllow, true)
	return decision
}

// AttachPreflightMetadata writes stable invocation metadata used by events and model contracts.
func AttachPreflightMetadata(metadata map[string]interface{}, decision PreflightDecision) {
	if metadata == nil {
		return
	}
	metadata[MetadataArgumentsDigestKey] = decision.Digest
	metadata[MetadataAttemptKey] = decision.Attempt
	if decision.SkipEmptyReplayCache {
		metadata[runtimetypes.ToolMetadataEmptyReplayCacheKey] = false
	}
	if decision.Preflight != "" {
		metadata[MetadataPreflightKey] = decision.Preflight
	}
	if decision.Circuit {
		metadata[MetadataCircuitOpenKey] = true
	}
	if decision.PathAutoHealed {
		metadata["path_auto_healed"] = true
		if decision.OriginalPath != "" {
			metadata["original_path"] = decision.OriginalPath
		}
		if decision.ResolvedPath != "" {
			metadata["resolved_path"] = decision.ResolvedPath
		}
		if len(decision.PathCandidates) > 0 {
			metadata[MetadataPathCandidatesKey] = append([]string(nil), decision.PathCandidates...)
		}
		if decision.NextAction != "" {
			metadata[toolresult.MetadataNextActionKey] = decision.NextAction
		}
	}
	if decision.SoftEmpty {
		metadata[MetadataEmptyReplayKey] = true
		metadata[toolresult.MetadataEmptyResultKey] = true
		metadata[toolresult.MetadataOutcomeKey] = toolresult.OutcomeEmpty
	}
	// Always keep a compact args summary under tool_invocation so recovery
	// paths can promote it. Only surface top-level attempted_args on blocked
	// preflight so ordinary success metadata stays compact.
	compactArgs := toolresult.CompactAttemptedArgs(decision.Args)
	if !decision.Allow {
		if decision.ErrorCode != "" {
			metadata[toolresult.MetadataErrorCodeKey] = decision.ErrorCode
		}
		// Soft empty is still a successful disposition; keep retryable false
		// only as a "do not re-issue unchanged" signal without error_code.
		metadata[toolresult.MetadataRetryableKey] = decision.Retryable
		if decision.NextAction != "" {
			metadata[toolresult.MetadataNextActionKey] = decision.NextAction
		}
		if len(decision.PathCandidates) > 0 {
			metadata[MetadataPathCandidatesKey] = append([]string(nil), decision.PathCandidates...)
		}
		if len(compactArgs) > 0 {
			if _, exists := metadata[MetadataAttemptedArgsKey]; !exists {
				metadata[MetadataAttemptedArgsKey] = compactArgs
			}
		}
	}
	invocation := map[string]interface{}{
		"arguments_digest": decision.Digest,
		"attempt":          decision.Attempt,
		"circuit_open":     decision.Circuit,
		"preflight":        decision.Preflight,
	}
	if decision.SoftEmpty {
		invocation[MetadataEmptyReplayKey] = true
	}
	if len(decision.PathCandidates) > 0 {
		invocation[MetadataPathCandidatesKey] = append([]string(nil), decision.PathCandidates...)
	}
	if decision.PathAutoHealed {
		invocation["path_auto_healed"] = true
		if decision.OriginalPath != "" {
			invocation["original_path"] = decision.OriginalPath
		}
		if decision.ResolvedPath != "" {
			invocation["resolved_path"] = decision.ResolvedPath
		}
	}
	if len(compactArgs) > 0 {
		invocation[MetadataAttemptedArgsKey] = compactArgs
	}
	metadata[MetadataInvocationKey] = invocation
}

// RecordOutcome updates circuit / empty soft-cache memory from a completed tool invocation.
func RecordOutcome(memory *Memory, toolName, digest, toolErr string, metadata map[string]interface{}) toolresult.Diagnostic {
	diagnostic := toolresult.Diagnose(toolName, "", toolErr, metadata)
	if memory == nil || strings.TrimSpace(digest) == "" {
		return diagnostic
	}
	if diagnostic.OK {
		if disabled, ok := runtimetypes.BoolMetadataValue(metadata, runtimetypes.ToolMetadataEmptyReplayCacheKey); ok && !disabled {
			// Volatile tools may be empty now and non-empty moments later. Never
			// retain empty evidence as a negative cache entry; hard-failure
			// circuits below remain available for true terminal errors.
			memory.RecordNonEmptySuccess(toolName, digest)
			return diagnostic
		}
		if diagnostic.EmptyResult || diagnostic.Outcome == toolresult.OutcomeEmpty {
			// Soft negative cache for identical empty successes. Do not clear
			// via RecordSuccess (that would wipe the soft cache we just built).
			// Also clear hard-failure circuit state for this digest.
			memory.RecordSuccess(toolName, digest)
			if record := memory.RecordEmpty(toolName, digest, diagnostic); record != nil && record.Open && metadata != nil {
				metadata[MetadataEmptyReplayKey] = true
				if next := strengthenEmptyReplayAction(diagnostic.NextAction, record.Count); next != "" {
					metadata[toolresult.MetadataNextActionKey] = next
					diagnostic.NextAction = next
				}
			}
			return diagnostic
		}
		// Real non-empty success: clear both hard circuit and empty soft cache.
		memory.RecordNonEmptySuccess(toolName, digest)
		return diagnostic
	}
	if record := memory.RecordFailure(toolName, digest, diagnostic, toolErr); record != nil && record.Open {
		if metadata != nil {
			metadata[MetadataCircuitOpenKey] = true
			// Keep the richest path candidates on the open circuit record and
			// surface them on the current response metadata when present.
			if len(record.PathCandidates) > 0 {
				if existing := toolresult.ExtractPathCandidates(metadata); len(existing) == 0 {
					metadata[MetadataPathCandidatesKey] = append([]string(nil), record.PathCandidates...)
				}
				if len(diagnostic.PathCandidates) == 0 {
					diagnostic.PathCandidates = append([]string(nil), record.PathCandidates...)
				}
			}
			if next := strengthenNoRetryAction(diagnostic.NextAction, diagnostic.ErrorCode); next != "" {
				metadata[toolresult.MetadataNextActionKey] = next
				diagnostic.NextAction = next
				diagnostic.Retryable = false
			}
		}
	}
	return diagnostic
}

func emptyReplayCacheDisabled(metadata map[string]interface{}) bool {
	enabled, ok := runtimetypes.BoolMetadataValue(metadata, runtimetypes.ToolMetadataEmptyReplayCacheKey)
	return ok && !enabled
}

func strengthenNoRetryAction(existing, code string) string {
	base := strings.TrimSpace(existing)
	if base == "" {
		base = "Inspect the error details, correct the cause, and retry only when the operation is safe."
	}
	if strings.Contains(strings.ToLower(base), "do not retry unchanged") {
		return base
	}
	switch strings.TrimSpace(code) {
	case "TOOL_INVALID_ARGS", "TOOL_PATH_NOT_FOUND", "AGENT_PERMISSION", "TOOL_NOT_FOUND", "JOB_NOT_FOUND":
		return base + " Do not retry unchanged arguments; change inputs or choose a different tool."
	default:
		return base + " Do not retry unchanged."
	}
}

func strengthenEmptyReplayAction(existing string, count int) string {
	base := strings.TrimSpace(existing)
	if base == "" {
		base = "This identical tool call previously returned a successful empty result. Treat that as valid evidence; broaden/change inputs or proceed instead of retrying unchanged."
	}
	lower := strings.ToLower(base)
	if strings.Contains(lower, "do not retry unchanged") || strings.Contains(lower, "broaden") {
		if count > DefaultEmptyReplayThreshold && !strings.Contains(lower, "replayed") {
			return fmt.Sprintf("%s Identical empty result has been observed %d times; do not re-issue unchanged.", strings.TrimRight(base, "."), count)
		}
		return base
	}
	if count > DefaultEmptyReplayThreshold {
		return fmt.Sprintf("%s Identical empty result has been observed %d times; do not re-issue unchanged.", strings.TrimRight(base, "."), count)
	}
	return base + " Do not retry unchanged."
}

// firstMissingPathHint extracts the first content path from args for circuit-open
// candidate recomputation when stored candidates are missing.
func firstMissingPathHint(req PreflightRequest) string {
	candidates := collectContentReadPathCandidates(req.InputSchema, req.Args)
	if len(candidates) == 0 {
		candidates = collectReadPathCandidates(req.InputSchema, req.Args)
	}
	exists := pathExistsChecker(req)
	for _, candidate := range candidates {
		path := strings.TrimSpace(candidate)
		if path == "" || path == "." || path == "./" || strings.Contains(path, "://") {
			continue
		}
		if !exists(path) {
			return path
		}
	}
	if len(candidates) > 0 {
		return strings.TrimSpace(candidates[0])
	}
	return ""
}

func missingRequiredArgs(schema map[string]interface{}, args map[string]interface{}) []string {
	required := schemaRequiredFields(schema)
	if len(required) == 0 {
		return nil
	}
	if args == nil {
		args = map[string]interface{}{}
	}
	missing := make([]string, 0, len(required))
	for _, field := range required {
		value, ok := args[field]
		if !ok || isEmptyArgValue(value) {
			missing = append(missing, field)
		}
	}
	return missing
}

func schemaRequiredFields(schema map[string]interface{}) []string {
	if len(schema) == 0 {
		return nil
	}
	raw, ok := schema["required"]
	if !ok || raw == nil {
		return nil
	}
	switch typed := raw.(type) {
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if name := strings.TrimSpace(fmt.Sprint(item)); name != "" && name != "<nil>" {
				out = append(out, name)
			}
		}
		return out
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if name := strings.TrimSpace(item); name != "" {
				out = append(out, name)
			}
		}
		return out
	default:
		return nil
	}
}

// invalidSchemaArgs returns human-readable schema type/enum mismatches for
// present arguments. Checks are schema-driven and tool-agnostic.
func invalidSchemaArgs(schema map[string]interface{}, args map[string]interface{}) []string {
	if len(schema) == 0 || len(args) == 0 {
		return nil
	}
	props := schemaProperties(schema)
	if len(props) == 0 {
		return nil
	}
	// Stable order for deterministic model-facing errors.
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)

	var issues []string
	for _, name := range names {
		value, present := args[name]
		if !present || isEmptyArgValue(value) {
			// Missing/empty values are handled by required-arg preflight.
			continue
		}
		prop := asObjectMap(props[name])
		if prop == nil {
			continue
		}
		if issue := describeSchemaValueIssue(name, prop, value); issue != "" {
			issues = append(issues, issue)
		}
	}
	return issues
}

func describeSchemaValueIssue(name string, prop map[string]interface{}, value interface{}) string {
	expectedTypes := schemaDeclaredTypes(prop)
	got := jsonSchemaValueKind(value)
	if len(expectedTypes) > 0 && !valueMatchesSchemaTypes(value, expectedTypes) {
		return fmt.Sprintf("%s expected %s, got %s", name, strings.Join(expectedTypes, "|"), got)
	}
	if enums := schemaEnumValues(prop); len(enums) > 0 && !valueInEnum(value, enums) {
		return fmt.Sprintf("%s must be one of [%s], got %s", name, strings.Join(enums, ", "), compactEnumObserved(value))
	}
	// Shallow items type check for homogeneous primitive/object arrays.
	if got == "array" {
		if items := asObjectMap(prop["items"]); items != nil {
			itemTypes := schemaDeclaredTypes(items)
			if len(itemTypes) > 0 {
				for i, item := range asInterfaceSlice(value) {
					if isEmptyArgValue(item) {
						continue
					}
					if !valueMatchesSchemaTypes(item, itemTypes) {
						// Bash-style batch tools accept bare string items even
						// when items declares object (single-command shorthand
						// in parseBashCommandBatch). Mirror that tolerance in
						// preflight instead of rejecting what the tool would
						// have handled; the nested object-property check below
						// safely skips non-object items.
						if containsSchemaType(itemTypes, "object") && jsonSchemaValueKind(item) == "string" {
							continue
						}
						return fmt.Sprintf("%s[%d] expected %s, got %s", name, i, strings.Join(itemTypes, "|"), jsonSchemaValueKind(item))
					}
					// One-level object property types (e.g. files[].file_path).
					if containsSchemaType(itemTypes, "object") {
						if obj := asObjectMap(item); obj != nil {
							if nestedProps := schemaProperties(items); len(nestedProps) > 0 {
								nestedNames := make([]string, 0, len(nestedProps))
								for nestedName := range nestedProps {
									nestedNames = append(nestedNames, nestedName)
								}
								sort.Strings(nestedNames)
								for _, nestedName := range nestedNames {
									nestedVal, ok := obj[nestedName]
									if !ok || isEmptyArgValue(nestedVal) {
										continue
									}
									nestedProp := asObjectMap(nestedProps[nestedName])
									if nestedProp == nil {
										continue
									}
									nestedExpected := schemaDeclaredTypes(nestedProp)
									if len(nestedExpected) > 0 && !valueMatchesSchemaTypes(nestedVal, nestedExpected) {
										return fmt.Sprintf("%s[%d].%s expected %s, got %s", name, i, nestedName, strings.Join(nestedExpected, "|"), jsonSchemaValueKind(nestedVal))
									}
								}
							}
						}
					}
				}
			}
		}
	}
	return ""
}

func schemaProperties(schema map[string]interface{}) map[string]interface{} {
	if len(schema) == 0 {
		return nil
	}
	raw, ok := schema["properties"]
	if !ok || raw == nil {
		return nil
	}
	props, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}
	return props
}

func schemaDeclaredTypes(prop map[string]interface{}) []string {
	if len(prop) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(t string) {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			return
		}
		if _, ok := seen[t]; ok {
			return
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}

	switch typed := prop["type"].(type) {
	case string:
		add(typed)
	case []interface{}:
		for _, item := range typed {
			add(fmt.Sprint(item))
		}
	case []string:
		for _, item := range typed {
			add(item)
		}
	}

	// anyOf / oneOf: union of nested declared types (e.g. string|integer).
	for _, key := range []string{"anyOf", "oneOf"} {
		for _, branch := range asInterfaceSlice(prop[key]) {
			if nested := asObjectMap(branch); nested != nil {
				for _, t := range schemaDeclaredTypes(nested) {
					add(t)
				}
			}
		}
	}
	return out
}

func schemaEnumValues(prop map[string]interface{}) []string {
	if len(prop) == 0 {
		return nil
	}
	raw, ok := prop["enum"]
	if !ok || raw == nil {
		return nil
	}
	var out []string
	switch typed := raw.(type) {
	case []interface{}:
		for _, item := range typed {
			if s := compactEnumObserved(item); s != "" {
				out = append(out, s)
			}
		}
	case []string:
		for _, item := range typed {
			if s := strings.TrimSpace(item); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func valueMatchesSchemaTypes(value interface{}, expected []string) bool {
	if len(expected) == 0 {
		return true
	}
	got := jsonSchemaValueKind(value)
	for _, want := range expected {
		switch want {
		case "string":
			if got == "string" {
				return true
			}
		case "integer":
			if isIntegerLikeValue(value) {
				return true
			}
		case "number":
			if isNumberLikeValue(value) {
				return true
			}
		case "boolean":
			if isBooleanLikeValue(value) {
				return true
			}
		case "array":
			if got == "array" {
				return true
			}
		case "object":
			if got == "object" {
				return true
			}
		case "null":
			if value == nil {
				return true
			}
		default:
			// Unknown schema type: do not over-block.
			if got == want {
				return true
			}
		}
	}
	return false
}

func containsSchemaType(types []string, want string) bool {
	want = strings.ToLower(strings.TrimSpace(want))
	for _, t := range types {
		if strings.ToLower(strings.TrimSpace(t)) == want {
			return true
		}
	}
	return false
}

func jsonSchemaValueKind(value interface{}) string {
	switch value.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "integer"
	case float32, float64:
		// Distinguish whole numbers for clearer diagnostics when possible.
		if isIntegerLikeValue(value) && !isNonIntegerFloat(value) {
			return "integer"
		}
		return "number"
	case jsonNumber:
		if isIntegerLikeValue(value) {
			return "integer"
		}
		return "number"
	case []interface{}, []string, []map[string]interface{}:
		return "array"
	case map[string]interface{}:
		return "object"
	default:
		// Fallback: detect slices/maps via fmt kind is brittle; use type string.
		typeName := fmt.Sprintf("%T", value)
		if strings.HasPrefix(typeName, "[]") {
			return "array"
		}
		if strings.HasPrefix(typeName, "map[") {
			return "object"
		}
		return typeName
	}
}

// jsonNumber mirrors encoding/json.Number without importing encoding/json here.
// Values unmarshaled with UseNumber may arrive as this alias via interface{}.
type jsonNumber interface {
	String() string
	Float64() (float64, error)
	Int64() (int64, error)
}

func isNonIntegerFloat(value interface{}) bool {
	switch typed := value.(type) {
	case float32:
		return float64(typed) != float64(int64(typed))
	case float64:
		return typed != float64(int64(typed))
	default:
		return false
	}
}

func isIntegerLikeValue(value interface{}) bool {
	switch typed := value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	case float32:
		return float64(typed) == float64(int64(typed))
	case float64:
		return typed == float64(int64(typed))
	case string:
		s := strings.TrimSpace(typed)
		if s == "" {
			return false
		}
		if _, err := strconv.ParseInt(s, 10, 64); err == nil {
			return true
		}
		// Accept whole-number decimals like "10.0".
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f == float64(int64(f))
		}
		return false
	case jsonNumber:
		if _, err := typed.Int64(); err == nil {
			return true
		}
		if f, err := typed.Float64(); err == nil {
			return f == float64(int64(f))
		}
		return false
	default:
		return false
	}
}

func isNumberLikeValue(value interface{}) bool {
	if isIntegerLikeValue(value) {
		return true
	}
	switch typed := value.(type) {
	case float32, float64:
		return true
	case string:
		s := strings.TrimSpace(typed)
		if s == "" {
			return false
		}
		_, err := strconv.ParseFloat(s, 64)
		return err == nil
	case jsonNumber:
		_, err := typed.Float64()
		return err == nil
	default:
		return false
	}
}

func isBooleanLikeValue(value interface{}) bool {
	switch typed := value.(type) {
	case bool:
		return true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "false", "1", "0", "yes", "no", "on", "off":
			return true
		default:
			return false
		}
	case int:
		return typed == 0 || typed == 1
	case int8:
		return typed == 0 || typed == 1
	case int16:
		return typed == 0 || typed == 1
	case int32:
		return typed == 0 || typed == 1
	case int64:
		return typed == 0 || typed == 1
	case float64:
		return typed == 0 || typed == 1
	case float32:
		return typed == 0 || typed == 1
	default:
		return false
	}
}

func valueInEnum(value interface{}, enums []string) bool {
	observed := compactEnumObserved(value)
	if observed == "" {
		return false
	}
	for _, allowed := range enums {
		if strings.EqualFold(strings.TrimSpace(allowed), observed) {
			return true
		}
	}
	return false
}

func compactEnumObserved(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case float32:
		return compactEnumObserved(float64(typed))
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case jsonNumber:
		return strings.TrimSpace(typed.String())
	default:
		s := strings.TrimSpace(fmt.Sprint(value))
		if s == "" || s == "<nil>" {
			return ""
		}
		return s
	}
}

func asObjectMap(value interface{}) map[string]interface{} {
	if value == nil {
		return nil
	}
	if m, ok := value.(map[string]interface{}); ok {
		return m
	}
	return nil
}

func asInterfaceSlice(value interface{}) []interface{} {
	switch typed := value.(type) {
	case nil:
		return nil
	case []interface{}:
		return typed
	case []string:
		out := make([]interface{}, len(typed))
		for i, item := range typed {
			out[i] = item
		}
		return out
	case []map[string]interface{}:
		out := make([]interface{}, len(typed))
		for i, item := range typed {
			out[i] = item
		}
		return out
	default:
		return nil
	}
}

func isEmptyArgValue(value interface{}) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		// Treat placeholder tokens ("" / null / undefined) as empty so required
		// path checks and stringValues collection stay consistent with
		// clearPlaceholderPathLikeArgs normalization.
		return normalizePathArgPlaceholder(typed) == ""
	case []interface{}:
		return len(typed) == 0
	case []string:
		return len(typed) == 0
	case map[string]interface{}:
		return len(typed) == 0
	default:
		return false
	}
}

// normalizeCoercibleSchemaArgs rewrites top-level args whose runtime JSON
// types differ from the declared schema in ways tool implementations already
// tolerate. It mutates args in place and reports whether anything changed.
//
// Supported coerce directions (mirrors tool-layer tolerance):
//   - declared "string", got integer/number → formatted decimal string
//     (models send timeout=120 as bare seconds; parseShellCommandTimeout
//     already parses plain numbers as seconds via ParseFloat)
//   - declared "array", got string → JSON array text parsed when valid,
//     otherwise wrapped as a single-item array (models send commands as one
//     command string; parseBashCommandBatch already accepts that shape)
func normalizeCoercibleSchemaArgs(schema, args map[string]interface{}) bool {
	if len(schema) == 0 || len(args) == 0 {
		return false
	}
	props := schemaProperties(schema)
	if len(props) == 0 {
		return false
	}
	changed := false
	for name, value := range args {
		if value == nil || isEmptyArgValue(value) {
			continue
		}
		prop := asObjectMap(props[name])
		if prop == nil {
			continue
		}
		declared := schemaDeclaredTypes(prop)
		if len(declared) == 0 {
			continue
		}
		got := jsonSchemaValueKind(value)
		switch {
		case containsSchemaType(declared, "string") &&
			!containsSchemaType(declared, "integer") &&
			!containsSchemaType(declared, "number") &&
			(got == "integer" || got == "number"):
			// number → string, preserving the exact decimal text so duration
			// parsers can reinterpret it as seconds.
			args[name] = numericArgString(value)
			changed = true
		case containsSchemaType(declared, "array") && got == "string":
			text := strings.TrimSpace(value.(string))
			if text == "" {
				continue
			}
			var parsed []interface{}
			if json.Unmarshal([]byte(text), &parsed) == nil {
				args[name] = parsed
			} else {
				args[name] = []interface{}{text}
			}
			changed = true
		}
	}
	return changed
}

// numericArgString formats a numeric JSON value without scientific notation
// (120 → "120", 120.0 → "120", 30.5 → "30.5").
func numericArgString(value interface{}) string {
	switch typed := value.(type) {
	case int:
		return strconv.Itoa(typed)
	case int8:
		return strconv.FormatInt(int64(typed), 10)
	case int16:
		return strconv.FormatInt(int64(typed), 10)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint:
		return strconv.FormatUint(uint64(typed), 10)
	case uint8:
		return strconv.FormatUint(uint64(typed), 10)
	case uint16:
		return strconv.FormatUint(uint64(typed), 10)
	case uint32:
		return strconv.FormatUint(uint64(typed), 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 64)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case jsonNumber:
		return typed.String()
	default:
		return fmt.Sprintf("%v", value)
	}
}

// clearPlaceholderPathLikeArgs rewrites path-like args that are model
// placeholders for "no path" into true empty strings (or drops them from
// string slices). Returns whether any rewrite happened.
//
// Live residual (2026-07-26): grep path="" (two quote chars) was treated as a
// missing relative path, ranked workspace-root siblings (.agents/.backups/.git)
// as path_candidates, and blocked an otherwise valid workspace-wide search.
func clearPlaceholderPathLikeArgs(args map[string]interface{}) bool {
	if len(args) == 0 {
		return false
	}
	changed := false
	for key, raw := range args {
		if !isPathLikeKey(key) {
			// Still rewrite nested files[] path fields below.
			if key != "files" {
				continue
			}
		}
		switch typed := raw.(type) {
		case string:
			if normalized := normalizePathArgPlaceholder(typed); normalized != typed {
				args[key] = normalized
				changed = true
			}
		case []string:
			localChanged := false
			next := make([]string, 0, len(typed))
			for _, item := range typed {
				normalized := normalizePathArgPlaceholder(item)
				if normalized == "" {
					if item != "" {
						localChanged = true
					}
					continue
				}
				if normalized != item {
					localChanged = true
				}
				next = append(next, normalized)
			}
			if localChanged {
				args[key] = next
				changed = true
			}
		case []interface{}:
			localChanged := false
			// files[] objects vs path string arrays share []interface{} shape.
			if key == "files" {
				for _, item := range typed {
					obj, ok := item.(map[string]interface{})
					if !ok {
						continue
					}
					for _, nk := range []string{"file_path", "path", "filepath"} {
						if v, exists := obj[nk]; exists {
							if s, ok := v.(string); ok {
								if normalized := normalizePathArgPlaceholder(s); normalized != s {
									obj[nk] = normalized
									localChanged = true
								}
							}
						}
					}
				}
				if localChanged {
					args[key] = typed
					changed = true
				}
				continue
			}
			next := make([]interface{}, 0, len(typed))
			for _, item := range typed {
				s, ok := item.(string)
				if !ok {
					next = append(next, item)
					continue
				}
				normalized := normalizePathArgPlaceholder(s)
				if normalized == "" {
					if s != "" {
						localChanged = true
					}
					continue
				}
				if normalized != s {
					localChanged = true
					next = append(next, normalized)
				} else {
					next = append(next, s)
				}
			}
			if localChanged {
				args[key] = next
				changed = true
			}
		}
	}
	return changed
}

// normalizePathArgPlaceholder maps model-emitted empty/null path tokens to "".
// True empty / "." remain caller's choice; only placeholder *literals* collapse.
func normalizePathArgPlaceholder(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	// Literal empty-string spellings models paste when they mean "omit path".
	switch trimmed {
	case `""`, `''`, "``":
		return ""
	}
	lower := strings.ToLower(trimmed)
	switch lower {
	case "null", "none", "undefined", "<nil>", "nil", "n/a", "na":
		return ""
	}
	// Quoted empty after one unwrap: "\"\"" already handled; also "null".
	if len(trimmed) >= 2 {
		if (trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"') ||
			(trimmed[0] == '\'' && trimmed[len(trimmed)-1] == '\'') {
			inner := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
			if inner == "" {
				return ""
			}
			switch strings.ToLower(inner) {
			case "null", "none", "undefined", "<nil>", "nil", "n/a", "na":
				return ""
			}
		}
	}
	return path
}

func preflightMissingReadPath(req PreflightRequest) (string, string) {
	if !shouldPreflightPaths(req.Metadata, req.InputSchema, req.Args) {
		return "", ""
	}
	exists := pathExistsChecker(req)
	// Prefer content targets (file_path / paths / files[]) over execution roots
	// (cwd/workdir). A present workdir must not soft-allow a missing single file.
	candidates := collectContentReadPathCandidates(req.InputSchema, req.Args)
	if len(candidates) == 0 {
		candidates = collectReadPathCandidates(req.InputSchema, req.Args)
	}
	var firstMissing string
	existing := 0
	checked := 0
	for _, candidate := range candidates {
		path := strings.TrimSpace(candidate)
		if path == "" || path == "." || path == "./" {
			continue
		}
		// Skip obvious non-filesystem values (URLs, pure globs without separators).
		if strings.Contains(path, "://") {
			continue
		}
		checked++
		if !exists(path) {
			if firstMissing == "" {
				firstMissing = path
			}
			continue
		}
		existing++
	}
	if firstMissing == "" || checked == 0 {
		return "", ""
	}
	// Multi-path batches: when at least one content target exists, allow the tool
	// to run so it can return outcome=partial + failed_items instead of a hard
	// preflight deny that forces the model to re-issue the whole batch.
	if existing > 0 {
		return "", ""
	}
	return "path not found", firstMissing
}

func shouldPreflightPaths(metadata, schema, args map[string]interface{}) bool {
	// Explicit opt-out / opt-in via definition metadata.
	if enabled, ok := runtimetypes.BoolMetadataValue(metadata, "path_preflight"); ok {
		return enabled
	}
	// Never preflight path existence for write/mutation-shaped argument sets.
	if hasMutationLikeArgs(args) {
		return false
	}
	// Prefer safe retry_class as a generic read-like signal.
	if class, ok := metadataString(metadata, runtimetypes.ToolMetadataRetryClassKey); ok {
		switch class {
		case runtimetypes.ToolRetryClassSafe:
			return schemaHasPathLikeProperty(schema) || argsHavePathLikeKeys(args)
		case runtimetypes.ToolRetryClassNever,
			runtimetypes.ToolRetryClassIdempotencyKeyRequired,
			runtimetypes.ToolRetryClassCompensatable:
			return false
		}
	}
	// Without retry_class, only preflight when schema properties look path-like
	// and mutation-like properties are absent from the schema.
	if schemaHasMutationLikeProperty(schema) {
		return false
	}
	return schemaHasPathLikeProperty(schema)
}

func metadataString(metadata map[string]interface{}, key string) (string, bool) {
	if len(metadata) == 0 {
		return "", false
	}
	raw, ok := metadata[key]
	if !ok || raw == nil {
		return "", false
	}
	value := strings.TrimSpace(fmt.Sprint(raw))
	if value == "" || value == "<nil>" {
		return "", false
	}
	return strings.ToLower(value), true
}

func hasMutationLikeArgs(args map[string]interface{}) bool {
	if len(args) == 0 {
		return false
	}
	for key, value := range args {
		if isMutationLikeKey(key) && !isEmptyArgValue(value) {
			return true
		}
	}
	return false
}

func schemaHasMutationLikeProperty(schema map[string]interface{}) bool {
	for _, name := range schemaPropertyNames(schema) {
		if isMutationLikeKey(name) {
			return true
		}
	}
	return false
}

func schemaHasPathLikeProperty(schema map[string]interface{}) bool {
	for _, name := range schemaPropertyNames(schema) {
		if isPathLikeKey(name) {
			return true
		}
	}
	return false
}

func argsHavePathLikeKeys(args map[string]interface{}) bool {
	for key := range args {
		if isPathLikeKey(key) {
			return true
		}
	}
	return false
}

func schemaPropertyNames(schema map[string]interface{}) []string {
	if len(schema) == 0 {
		return nil
	}
	raw, ok := schema["properties"]
	if !ok || raw == nil {
		return nil
	}
	props, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	return names
}

func collectReadPathCandidates(schema map[string]interface{}, args map[string]interface{}) []string {
	if len(args) == 0 {
		return nil
	}
	// Prefer schema-declared path-like properties; fall back to arg keys.
	keys := schemaPropertyNames(schema)
	if len(keys) == 0 {
		for key := range args {
			keys = append(keys, key)
		}
	}
	out := make([]string, 0, 4)
	seen := map[string]struct{}{}
	for _, key := range keys {
		if !isPathLikeKey(key) {
			continue
		}
		for _, path := range stringValues(args[key]) {
			path = normalizePathArgPlaceholder(path)
			if path == "" {
				continue
			}
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			out = append(out, path)
		}
	}
	// Nested files[].file_path style batches used by generic multi-file readers.
	if rawFiles, ok := args["files"]; ok {
		for _, item := range asObjectSlice(rawFiles) {
			for _, key := range []string{"file_path", "path", "filepath"} {
				if path := normalizePathArgPlaceholder(fmt.Sprint(item[key])); path != "" && path != "<nil>" {
					if _, ok := seen[path]; ok {
						continue
					}
					seen[path] = struct{}{}
					out = append(out, path)
				}
			}
		}
	}
	return out
}

// collectContentReadPathCandidates returns path-like targets that represent tool
// content inputs (file_path / paths / files[]), excluding execution roots such as
// cwd/workdir. Used by multi-path soft-allow so a present workdir never masks a
// missing single-file target.
func collectContentReadPathCandidates(schema map[string]interface{}, args map[string]interface{}) []string {
	if len(args) == 0 {
		return nil
	}
	keys := schemaPropertyNames(schema)
	if len(keys) == 0 {
		for key := range args {
			keys = append(keys, key)
		}
	}
	out := make([]string, 0, 4)
	seen := map[string]struct{}{}
	for _, key := range keys {
		if !isPathLikeKey(key) || isExecutionRootPathKey(key) {
			continue
		}
		for _, path := range stringValues(args[key]) {
			path = normalizePathArgPlaceholder(path)
			if path == "" {
				continue
			}
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			out = append(out, path)
		}
	}
	if rawFiles, ok := args["files"]; ok {
		for _, item := range asObjectSlice(rawFiles) {
			for _, key := range []string{"file_path", "path", "filepath"} {
				if path := normalizePathArgPlaceholder(fmt.Sprint(item[key])); path != "" && path != "<nil>" {
					if _, ok := seen[path]; ok {
						continue
					}
					seen[path] = struct{}{}
					out = append(out, path)
				}
			}
		}
	}
	return out
}

func stringValues(value interface{}) []string {
	switch typed := value.(type) {
	case string:
		// Drop empty + placeholder path tokens so existence preflight never ranks
		// workspace-root siblings for path=""/null.
		if normalizePathArgPlaceholder(typed) == "" {
			return nil
		}
		return []string{typed}
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if normalizePathArgPlaceholder(item) == "" {
				continue
			}
			out = append(out, item)
		}
		return out
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && normalizePathArgPlaceholder(text) != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func asObjectSlice(value interface{}) []map[string]interface{} {
	switch typed := value.(type) {
	case []map[string]interface{}:
		return typed
	case []interface{}:
		out := make([]map[string]interface{}, 0, len(typed))
		for _, item := range typed {
			if obj, ok := item.(map[string]interface{}); ok {
				out = append(out, obj)
			}
		}
		return out
	default:
		return nil
	}
}

func isPathLikeKey(key string) bool {
	name := strings.ToLower(strings.TrimSpace(key))
	switch name {
	case "path", "file_path", "filepath", "filename", "file", "dir", "directory",
		"cwd", "workdir", "work_dir", "working_directory", "target_path", "source_path",
		"paths", "files":
		return true
	default:
		return strings.HasSuffix(name, "_path") || strings.HasSuffix(name, "_dir") || strings.HasSuffix(name, "_file")
	}
}

// isExecutionRootPathKey reports cwd/workdir-style keys that locate the process
// root rather than a content target. Soft multi-path preflight ignores these so
// a present workdir cannot mask a missing file_path.
func isExecutionRootPathKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "cwd", "workdir", "work_dir", "working_directory", "working_dir":
		return true
	default:
		return false
	}
}

func isMutationLikeKey(key string) bool {
	name := strings.ToLower(strings.TrimSpace(key))
	switch name {
	case "content", "contents", "patch", "diff", "old_string", "new_string",
		"command", "commands", "script", "stdin", "body", "data", "bytes",
		"prompt", "edits", "operations", "write", "append", "create", "delete":
		return true
	default:
		return false
	}
}

// pathExistsChecker returns a function that checks path existence using the
// request's WorkspaceRoot for relative paths (matching toolkit SetBasePath).
// Custom PathExists hooks still receive the resolved path.
func pathExistsChecker(req PreflightRequest) func(path string) bool {
	root := strings.TrimSpace(req.WorkspaceRoot)
	if root != "" && !filepath.IsAbs(root) {
		if abs, err := filepath.Abs(root); err == nil {
			root = abs
		}
	}
	hook := req.PathExists
	return func(path string) bool {
		resolved := resolvePreflightPath(path, root)
		if hook != nil {
			// Prefer hook on resolved path; also accept original relative form so
			// existing unit tests that stub by logical path keep working.
			if hook(resolved) || (resolved != path && hook(path)) {
				return true
			}
			return false
		}
		return defaultPathExists(resolved)
	}
}

// resolvePreflightPath joins relative targets onto the session workspace root
// the same way toolkit tools resolve via SetBasePath. Absolute paths are kept.
func resolvePreflightPath(path, workspaceRoot string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return trimmed
	}
	if filepath.IsAbs(trimmed) {
		return filepath.Clean(trimmed)
	}
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		return trimmed
	}
	return filepath.Clean(filepath.Join(root, trimmed))
}

func defaultPathExists(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return false
	}
	// Prefer the path as given (already workspace-resolved when applicable).
	if _, err := os.Stat(trimmed); err == nil {
		return true
	}
	// Fall back to process-cwd absolute form for bare relative paths when no
	// workspace root was supplied.
	if !filepath.IsAbs(trimmed) {
		if abs, err := filepath.Abs(trimmed); err == nil {
			if _, err := os.Stat(abs); err == nil {
				return true
			}
		}
	}
	return false
}

const maxNearbyPathCandidates = 5

// minPathAutoHealScore is the confidence floor for silently rewriting a missing
// read path. Scores come from suggestNearbyPathCandidates ranking:
// case-only=100, same-stem different-ext=70, close typo with same ext≈60-80.
const minPathAutoHealScore = 70

// canAutoHealSingleMissingContentPath is true only for single-target read-like
// path checks. Multi-path batches keep partial/deny semantics.
func canAutoHealSingleMissingContentPath(req PreflightRequest) bool {
	if !shouldPreflightPaths(req.Metadata, req.InputSchema, req.Args) {
		return false
	}
	// Never rewrite mutation-shaped tools even if path preflight somehow ran.
	if hasMutationLikeArgs(req.Args) || schemaHasMutationLikeProperty(req.InputSchema) {
		return false
	}
	candidates := collectContentReadPathCandidates(req.InputSchema, req.Args)
	if len(candidates) == 0 {
		candidates = collectReadPathCandidates(req.InputSchema, req.Args)
	}
	// Only auto-heal when exactly one content path is present (after trim).
	count := 0
	for _, candidate := range candidates {
		path := strings.TrimSpace(candidate)
		if path == "" || path == "." || path == "./" || strings.Contains(path, "://") {
			continue
		}
		count++
		if count > 1 {
			return false
		}
	}
	return count == 1
}

// uniqueHighConfidencePathCandidate returns a single nearby sibling when ranking
// is unambiguous and above minPathAutoHealScore. hints always carries the ranked
// list for metadata even when no unique heal is selected.
//
// The third return value is true when multiple high-confidence siblings are
// close enough that silent rewrite would be unsafe (model must pick one).
func uniqueHighConfidencePathCandidate(missing string, req PreflightRequest) (healed string, hints []string, ambiguous bool) {
	missing = strings.TrimSpace(missing)
	if missing == "" {
		return "", nil, false
	}
	root := strings.TrimSpace(req.WorkspaceRoot)
	if root != "" && !filepath.IsAbs(root) {
		if abs, err := filepath.Abs(root); err == nil {
			root = abs
		}
	}
	resolved := resolvePreflightPath(missing, root)
	scored := rankNearbyPathCandidates(resolved)
	if len(scored) == 0 && resolved != missing {
		scored = rankNearbyPathCandidates(missing)
	}
	if len(scored) == 0 {
		return "", nil, false
	}

	// Present candidates in the same shape the model used (relative vs absolute).
	hints = make([]string, 0, len(scored))
	for _, item := range scored {
		hints = append(hints, presentPathCandidate(item.path, missing, root))
	}

	top := scored[0]
	if top.score < minPathAutoHealScore {
		return "", hints, false
	}
	// Ambiguous: another candidate within 10 points of the top score.
	if len(scored) > 1 && scored[1].score >= top.score-10 && scored[1].score >= minPathAutoHealScore {
		return "", hints, true
	}
	return presentPathCandidate(top.path, missing, root), hints, false
}

func presentPathCandidate(candidate, originalMissing, workspaceRoot string) string {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return candidate
	}
	if filepath.IsAbs(originalMissing) || strings.TrimSpace(workspaceRoot) == "" {
		return candidate
	}
	rel, err := filepath.Rel(workspaceRoot, candidate)
	if err == nil && rel != "" && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	// If candidate was already relative (rank used original missing dir), keep it.
	if !filepath.IsAbs(candidate) {
		return filepath.ToSlash(candidate)
	}
	return candidate
}

// rewritePathLikeArgs replaces exact path occurrences in path-like args.
// Mutates args in place and returns whether any rewrite happened.
func rewritePathLikeArgs(args map[string]interface{}, from, to string) bool {
	if len(args) == 0 {
		return false
	}
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	if from == "" || to == "" || from == to {
		return false
	}
	changed := false

	rewriteString := func(value string) (string, bool) {
		if strings.TrimSpace(value) == from {
			return to, true
		}
		return value, false
	}

	for key, raw := range args {
		if !isPathLikeKey(key) || isExecutionRootPathKey(key) {
			// Still allow nested files[] rewrites below.
			if key != "files" {
				continue
			}
		}
		switch typed := raw.(type) {
		case string:
			if next, ok := rewriteString(typed); ok {
				args[key] = next
				changed = true
			}
		case []string:
			localChanged := false
			for i, item := range typed {
				if next, ok := rewriteString(item); ok {
					typed[i] = next
					localChanged = true
				}
			}
			if localChanged {
				args[key] = typed
				changed = true
			}
		case []interface{}:
			localChanged := false
			for i, item := range typed {
				switch nested := item.(type) {
				case string:
					if next, ok := rewriteString(nested); ok {
						typed[i] = next
						localChanged = true
					}
				case map[string]interface{}:
					for _, nk := range []string{"file_path", "path", "filepath"} {
						if v, exists := nested[nk]; exists {
							if s, ok := v.(string); ok {
								if next, ok := rewriteString(s); ok {
									nested[nk] = next
									localChanged = true
								}
							}
						}
					}
				}
			}
			if localChanged {
				args[key] = typed
				changed = true
			}
		}
	}
	return changed
}

// suggestNearbyPathCandidatesForRequest resolves the missing path against the
// session workspace root before listing siblings, then rewrites candidates back
// to the same shape the model originally used (relative vs absolute).
func suggestNearbyPathCandidatesForRequest(missing string, req PreflightRequest) []string {
	missing = strings.TrimSpace(missing)
	if missing == "" {
		return nil
	}
	root := strings.TrimSpace(req.WorkspaceRoot)
	if root != "" && !filepath.IsAbs(root) {
		if abs, err := filepath.Abs(root); err == nil {
			root = abs
		}
	}
	resolved := resolvePreflightPath(missing, root)
	hints := suggestNearbyPathCandidates(resolved, req.PathExists)
	if len(hints) == 0 {
		// Fall back to original form (process-cwd relative) for legacy behavior.
		if resolved != missing {
			hints = suggestNearbyPathCandidates(missing, req.PathExists)
		}
		return hints
	}
	if filepath.IsAbs(missing) || root == "" {
		return hints
	}
	// Prefer workspace-relative candidates when the model supplied a relative path.
	out := make([]string, 0, len(hints))
	for _, hint := range hints {
		rel, err := filepath.Rel(root, hint)
		if err == nil && rel != "" && !strings.HasPrefix(rel, "..") {
			out = append(out, filepath.ToSlash(rel))
			continue
		}
		out = append(out, hint)
	}
	return out
}

// suggestNearbyPathCandidates lists sibling filesystem entries near a missing path
// and ranks generic typo / case / extension-preserving alternatives. This is a
// model recovery hint only — it never invents tool-specific rewrites.
//
// pathExists is reserved for tests that stub existence checks; listing still uses
// the real parent directory so candidates are grounded in actual FS contents.
func suggestNearbyPathCandidates(missing string, pathExists func(string) bool) []string {
	_ = pathExists
	scored := rankNearbyPathCandidates(missing)
	if len(scored) == 0 {
		return nil
	}
	limit := maxNearbyPathCandidates
	if len(scored) < limit {
		limit = len(scored)
	}
	out := make([]string, 0, limit)
	seen := map[string]struct{}{}
	for _, item := range scored[:limit] {
		if _, ok := seen[item.path]; ok {
			continue
		}
		seen[item.path] = struct{}{}
		out = append(out, item.path)
	}
	return out
}

type nearbyPathScore struct {
	path  string
	score int
}

// rankNearbyPathCandidates returns all scored sibling candidates sorted by score desc.
func rankNearbyPathCandidates(missing string) []nearbyPathScore {
	missing = strings.TrimSpace(missing)
	if missing == "" {
		return nil
	}

	dir, base := filepath.Dir(missing), filepath.Base(missing)
	// Placeholder / empty basenames must never rank workspace-root noise.
	if base == "" || base == "." || base == string(filepath.Separator) ||
		normalizePathArgPlaceholder(base) == "" {
		return nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if abs, absErr := filepath.Abs(missing); absErr == nil {
			dir = filepath.Dir(abs)
			base = filepath.Base(abs)
			entries, err = os.ReadDir(dir)
		}
		if err != nil {
			return nil
		}
	}

	wantLower := strings.ToLower(base)
	wantExt := strings.ToLower(filepath.Ext(base))
	// Treat pure dotfiles (".backups") carefully: filepath.Ext(".backups") == ".backups"
	// which would leave an empty stem and make strings.Contains(wantStem, "") always true.
	wantStem := strings.TrimSuffix(wantLower, wantExt)
	if wantStem == "" {
		wantStem = wantLower
		wantExt = ""
	}

	candidates := make([]nearbyPathScore, 0, 8)
	for _, entry := range entries {
		name := entry.Name()
		if name == "" || name == base {
			continue
		}
		// Drop VCS/backup noise unless the model was already looking for a dotfile.
		if isPathSuggestionNoiseName(name) && !strings.HasPrefix(wantLower, ".") {
			continue
		}
		nameLower := strings.ToLower(name)
		nameExt := strings.ToLower(filepath.Ext(name))
		nameStem := strings.TrimSuffix(nameLower, nameExt)
		if nameStem == "" {
			nameStem = nameLower
			nameExt = ""
		}

		score := 0
		switch {
		case nameLower == wantLower:
			// Case-only mismatch is the strongest correction signal.
			score = 100
		case wantExt != "" && nameExt == wantExt && nameStem == wantStem:
			score = 95
		case wantExt != "" && nameExt == wantExt:
			dist := runeEditDistance(wantStem, nameStem)
			if dist <= 2 && dist < max(1, len(wantStem)/2+1) {
				score = 80 - dist*10
			}
		case nameStem == wantStem && wantStem != "":
			// Missing/extra extension only (ui → ui.tsx).
			score = 70
		case separatorFoldedPathStemEqual(wantStem, nameStem):
			// providertoken ↔ provider_token / aiSites ↔ ai_sites: models often
			// drop or invent _/- separators. Strong enough for unique auto-heal.
			score = 85
		default:
			dist := runeEditDistance(wantLower, nameLower)
			if dist <= 2 && dist < max(1, len(wantLower)/2+1) {
				score = 60 - dist*10
			} else if wantStem != "" && nameStem != "" &&
				(strings.Contains(nameLower, wantStem) || strings.Contains(wantStem, nameStem)) {
				// Require a meaningful stem length so tiny/empty fragments never match.
				if len([]rune(nameStem)) >= 2 && len([]rune(wantStem)) >= 2 {
					score = 40
				}
			}
		}
		// Separator-folded equality can still upgrade a weaker contains/typo score
		// when the only difference is _/- (without demoting stronger exact ranks).
		if score > 0 && score < 85 && separatorFoldedPathStemEqual(wantStem, nameStem) {
			score = 85
		}
		if score <= 0 {
			continue
		}
		// Prefer keeping the caller's path style (relative vs absolute).
		candidatePath := filepath.Join(dir, name)
		if !filepath.IsAbs(missing) {
			// Reconstruct with the original parent string so relative inputs stay relative.
			candidatePath = filepath.Join(filepath.Dir(missing), name)
			if filepath.Dir(missing) == "." {
				candidatePath = name
			}
		}
		candidates = append(candidates, nearbyPathScore{path: candidatePath, score: score})
	}

	if len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].path < candidates[j].path
	})
	return candidates
}

// isPathSuggestionNoiseName filters sibling basenames that commonly dominate empty
// directories in this repo (and similar workspaces) but almost never correct a
// missing source path. Kept as a small denylist — not a full ignore-file parser.
func isPathSuggestionNoiseName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return true
	}
	lower := strings.ToLower(name)
	switch lower {
	case ".backups", ".backup", ".git", ".svn", ".hg", ".ds_store",
		"__pycache__", "node_modules", ".idea", ".vscode", ".turbo", ".cache":
		return true
	}
	// Generic backup/cache dirs: ".backups-2026", "backups", etc.
	if strings.HasPrefix(lower, ".backup") || lower == "backups" {
		return true
	}
	return false
}

// separatorFoldedPathStemEqual reports whether two basenames/stems are equal after
// dropping _ and - separators (and case). Models frequently invent or drop these
// when recalling package/dir names (providertoken vs provider_token).
// Requires a minimum folded length so tiny stems never auto-heal.
func separatorFoldedPathStemEqual(a, b string) bool {
	fa := foldPathStemSeparators(a)
	fb := foldPathStemSeparators(b)
	if fa == "" || fb == "" || fa != fb {
		return false
	}
	// Avoid matching very short accidental collisions (e.g. "a" / "a_").
	if len([]rune(fa)) < 4 {
		return false
	}
	return true
}

func foldPathStemSeparators(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '_' || r == '-' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

const maxParentSiblingDiscoveryHints = 4

// listParentSiblingDiscoveryHints returns a short sample of real non-noise
// siblings under an existing parent when ranked typo candidates were empty.
// First path_candidates entry remains the parent itself; these are discovery
// hints only (never auto-heal targets by themselves).
func listParentSiblingDiscoveryHints(missing, parentHint string, req PreflightRequest) []string {
	missing = strings.TrimSpace(missing)
	parentHint = strings.TrimSpace(parentHint)
	if missing == "" || parentHint == "" {
		return nil
	}
	root := strings.TrimSpace(req.WorkspaceRoot)
	if root != "" && !filepath.IsAbs(root) {
		if abs, err := filepath.Abs(root); err == nil {
			root = abs
		}
	}
	resolvedParent := resolvePreflightPath(parentHint, root)
	entries, err := os.ReadDir(resolvedParent)
	if err != nil {
		// Fall back to the presented parent form (relative) when resolve differs.
		entries, err = os.ReadDir(parentHint)
		if err != nil {
			return nil
		}
		resolvedParent = parentHint
	}
	wantBase := filepath.Base(missing)
	out := make([]string, 0, maxParentSiblingDiscoveryHints)
	seen := map[string]struct{}{}
	for _, entry := range entries {
		name := entry.Name()
		if name == "" || name == wantBase || isPathSuggestionNoiseName(name) {
			continue
		}
		// Prefer the caller's relative/absolute style via presentPathCandidate.
		candidatePath := filepath.Join(resolvedParent, name)
		presented := presentPathCandidate(candidatePath, missing, root)
		if presented == "" {
			continue
		}
		if _, ok := seen[presented]; ok {
			continue
		}
		// Skip duplicates of the parent itself.
		if pathCandidatesEqual(presented, parentHint) {
			continue
		}
		seen[presented] = struct{}{}
		out = append(out, presented)
		if len(out) >= maxParentSiblingDiscoveryHints {
			break
		}
	}
	return out
}

// existingParentPathHint returns the parent directory of a missing path when that
// parent itself exists on disk (so models can ls/glob instead of inventing leaves).
func existingParentPathHint(missing string, req PreflightRequest) string {
	missing = strings.TrimSpace(missing)
	if missing == "" || missing == "." || missing == "./" {
		return ""
	}
	root := strings.TrimSpace(req.WorkspaceRoot)
	if root != "" && !filepath.IsAbs(root) {
		if abs, err := filepath.Abs(root); err == nil {
			root = abs
		}
	}
	resolved := resolvePreflightPath(missing, root)
	parent := filepath.Dir(resolved)
	if parent == "" || parent == "." || parent == string(filepath.Separator) {
		return ""
	}
	// Avoid suggesting drive roots / filesystem roots as "discovery" targets.
	if parent == filepath.Dir(parent) {
		return ""
	}
	exists := pathExistsChecker(req)
	if !exists(parent) {
		// Fall back to original missing's parent form for relative inputs.
		origParent := filepath.Dir(missing)
		if origParent != "" && origParent != "." && exists(origParent) {
			return presentPathCandidate(origParent, missing, root)
		}
		return ""
	}
	// Confirm parent is a directory when possible.
	if info, err := os.Stat(parent); err == nil && !info.IsDir() {
		return ""
	}
	return presentPathCandidate(parent, missing, root)
}

func pathCandidatesEqual(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if strings.EqualFold(a, b) {
		return true
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) ||
		strings.EqualFold(filepath.ToSlash(filepath.Clean(a)), filepath.ToSlash(filepath.Clean(b)))
}

// runeEditDistance is a small Levenshtein distance over runes for short basenames.
func runeEditDistance(a, b string) int {
	if a == b {
		return 0
	}
	ar := []rune(a)
	br := []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	// Bound work for long names; path hints are only useful for short basenames.
	if len(ar) > 64 || len(br) > 64 {
		if utf8.RuneCountInString(a) == 0 {
			return len(br)
		}
		return absInt(len(ar)-len(br)) + 3
	}
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := 0; j <= len(br); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			curr[j] = minInt(del, minInt(ins, sub))
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
