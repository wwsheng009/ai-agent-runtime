package output

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// TrustOptions supplies the local side of the trust calculation. Tool and
// HardCap come from local configuration, never from remote metadata.
type TrustOptions struct {
	Tool          string
	ToolWindow    int
	RemoteHardCap int
}

// SanitizedResult is the local trust projection of one remote result.
type SanitizedResult struct {
	Body     string
	Metadata map[string]any
	Budget   int
	Folded   bool
}

// SanitizeRemoteResult enforces design §9.3/§9.6. It never trusts a remote
// declaration to widen the local window, and it resolves artifact ids through
// the origin's target namespace.
func SanitizeRemoteResult(origin toolcore.ResultOrigin, body string, metadata map[string]any, options TrustOptions) SanitizedResult {
	result := SanitizedResult{Body: body}
	if !origin.IsRemote() {
		result.Metadata = cloneMap(metadata)
		return result
	}
	if MetadataSize(metadata) > MaxMetadataBytes {
		result.Metadata = map[string]any{KeyMetadataRejected: "meta_too_large"}
		metadata = nil
	}

	clean := allowlist(metadata)
	declared := metadataInt(clean[KeyModelVisibleBudget])
	budget := ResolveCap(options.Tool, declared, options.ToolWindow, options.RemoteHardCap)
	hardCap := HardCap(options.Tool, options.ToolWindow, options.RemoteHardCap)

	tailBytes := metadataInt(clean[KeyReservedTailBytes])
	tailStart := verifiedTailStart(body, tailBytes)
	if tailStart < 0 {
		delete(clean, KeyReservedTailBytes)
		tailBytes = 0
	}

	skip := metadataBool(clean[KeySkipRenderTruncation])
	skipAllowed := false
	if skip {
		switch {
		case len(body) <= budget:
			skipAllowed = true
		case tailBytes > 0 && tailStart >= 0:
			prefixBytes := len(body) - tailBytes
			skipAllowed = prefixBytes <= budget && len(body) <= hardCap
		}
	}

	folded := false
	if len(body) > hardCap || (!skipAllowed && len(body) > budget) {
		body, _, folded = FoldHeadTail(body, budget)
		delete(clean, KeyReservedTailBytes)
		tailBytes = 0
	}
	if !skipAllowed {
		delete(clean, KeySkipRenderTruncation)
	}
	if folded {
		clean[KeyIsTruncated] = true
		clean[KeyTruncated] = true
		clean[KeySkipRenderTruncation] = true
		clean[KeyModelVisibleBudget] = budget
	}

	if _, ok := clean[KeyModelVisibleBudget]; ok {
		clean[KeyModelVisibleBudget] = budget
	} else if folded {
		clean[KeyModelVisibleBudget] = budget
	}
	if tailBytes > 0 && tailStart >= 0 {
		clean[KeyReservedTailBytes] = tailBytes
	}

	sanitizeArtifactRef(origin, clean)
	result.Body = body
	result.Metadata = clean
	result.Budget = budget
	result.Folded = folded
	return result
}

func sanitizeArtifactRef(origin toolcore.ResultOrigin, metadata map[string]any) {
	rawRef, _ := metadata[KeyArtifactRef].(string)
	rawID, _ := metadata[KeyArtifactID].(string)
	candidate := strings.TrimSpace(rawRef)
	if candidate == "" {
		candidate = strings.TrimSpace(rawID)
	}
	if candidate == "" {
		return
	}
	ref, err := toolcore.ResolveBareArtifactRef(candidate, origin)
	if err != nil {
		delete(metadata, KeyArtifactRef)
		delete(metadata, KeyArtifactID)
		metadata[KeyMetadataRejected] = "artifact_scope"
		return
	}
	metadata[KeyArtifactRef] = ref.String()
	metadata[KeyArtifactID] = ref.ID
}

func allowlist(metadata map[string]any) map[string]any {
	clean := make(map[string]any)
	if len(metadata) == 0 {
		return clean
	}
	for _, key := range []string{
		KeySkipRenderTruncation,
		KeyModelVisibleBudget,
		KeyReservedTailBytes,
		KeyIsTruncated,
		KeySuggestedNextOffset,
		KeyArtifactRef,
		KeyArtifactID,
		KeyOutputKind,
		KeyErrorCode,
		KeyNextAction,
		KeyRetriable,
		KeyTruncated,
		"output_window_bytes",
		"output_window_total_bytes",
		"output_window_head_bytes",
		"output_window_tail_bytes",
		"output_window_omitted_start",
		"output_window_omitted_end",
		"output_window_omitted_bytes",
	} {
		if value, ok := metadata[key]; ok {
			clean[key] = value
		}
	}
	if nested, ok := metadata[KeyToolMetadata].(map[string]any); ok {
		filtered := allowlist(nested)
		if len(filtered) > 0 {
			clean[KeyToolMetadata] = filtered
		}
	}
	return clean
}

func metadataInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float32:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func metadataBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "y":
			return true
		}
	case int:
		return typed != 0
	case float64:
		return typed != 0
	}
	return false
}

func cloneMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

// verifiedTailStart checks the structural tail contract: the declared byte
// count must match a line-leading, tag-delimited block that closes at EOF.
func verifiedTailStart(text string, declared int) int {
	if declared <= 0 || declared > len(text) {
		return -1
	}
	anchor := -1
	if blockOpensAt(text, 0) {
		anchor = 0
	}
	for i := 0; i+1 < len(text); i++ {
		if text[i] == '\n' && blockOpensAt(text, i+1) {
			anchor = i + 1
		}
	}
	if anchor < 0 {
		return -1
	}
	actual := len(text) - anchor
	const slack = 16
	if declared > actual+slack || declared < actual-slack {
		return -1
	}
	tail := text[anchor:]
	if !blockClosesTail(tail) {
		return -1
	}
	return anchor
}

func blockOpensAt(text string, offset int) bool {
	if offset < 0 || offset+1 >= len(text) || text[offset] != '<' {
		return false
	}
	next := text[offset+1]
	return (next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z')
}

func blockClosesTail(tail string) bool {
	trimmed := strings.TrimSpace(tail)
	if !strings.HasSuffix(trimmed, ">") {
		return false
	}
	name := tagName(trimmed)
	if name == "" {
		return false
	}
	return strings.Contains(trimmed, "</"+name+">") || strings.Contains(trimmed, "/>")
}

func tagName(text string) string {
	if !strings.HasPrefix(text, "<") {
		return ""
	}
	end := 1
	for end < len(text) {
		c := text[end]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '-' || c == ':' {
			end++
			continue
		}
		break
	}
	if end == 1 {
		return ""
	}
	return text[1:end]
}
