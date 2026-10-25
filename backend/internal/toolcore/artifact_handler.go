package toolcore

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

const (
	// artifactReadMinLimitBytes keeps a page useful even when the configured
	// budget is unusually tight.
	artifactReadMinLimitBytes = 1024
	// artifactReadHeaderReserveBytes is the room the rendered page needs for its
	// framing, charged against the same budget.
	artifactReadHeaderReserveBytes = 512
	// artifactReadBudgetBytes is this tool's declared model-visible window. It
	// lives here rather than in toolcore/output because that package imports
	// toolcore; output re-exports the same number via ToolBudget("artifact_read").
	artifactReadBudgetBytes = 32 * 1024
	// Metadata keys, mirroring toolresult's wire names. artifact_handler cannot
	// import the output package without creating an import cycle.
	keySkipRenderTruncation = "skip_render_truncation"
	keyModelVisibleBudget   = "model_visible_budget_bytes"
)

// ArtifactOriginProvider is an optional Runtime extension declaring which store
// owns bare `art_…` ids on this surface. Without it, bare ids default to local.
type ArtifactOriginProvider interface {
	ArtifactOrigin() ResultOrigin
}

// NewArtifactReadHandler builds the shared artifact_read Handler. The surface
// supplies the schema/description and injects a Runtime whose Artifacts() sink
// serves the requested scope.
func NewArtifactReadHandler(def Definition) Handler {
	if def.Name == "" {
		def.Name = "artifact_read"
	}
	if def.Version == "" {
		def.Version = "1.0.0"
	}
	return &artifactReadHandler{def: def}
}

type artifactReadHandler struct {
	def Definition
}

func (h *artifactReadHandler) Definition() Definition { return h.def }

func (h *artifactReadHandler) Execute(ctx context.Context, call *Call) (*Result, error) {
	if call == nil || call.Runtime == nil {
		return artifactFailure(ErrInternal, "fix the runtime binding, then retry", fmt.Errorf("runtime is not configured")), nil
	}
	refValue := stringArg(call.Args, "artifact_ref")
	if strings.TrimSpace(refValue) == "" {
		refValue = stringArg(call.Args, "artifact_id")
	}
	refValue = strings.TrimSpace(refValue)
	if refValue == "" {
		return artifactFailure(ErrInvalidArgument, "pass artifact_ref or artifact_id", fmt.Errorf("artifact_ref/artifact_id is required")), nil
	}
	// A target-internal read never accepts a namespace: the caller already
	// selected this target, and accepting remote:<other>: here would turn the
	// tool into a cross-scope router.
	if strings.HasPrefix(refValue, "remote:") {
		return artifactFailure(ErrArtifactScopeDenied, "pass the bare art_… id", fmt.Errorf("remote namespace is not accepted by a target-internal artifact_read")), nil
	}
	// A model often pastes the whole pointer tail ("art_… size=… kind=…")
	// instead of the bare id; the token is extracted the same way the builtin
	// tool has always done it, so both surfaces accept the same spellings.
	refValue = normalizeArtifactID(refValue)
	if !ValidArtifactID(refValue) {
		return artifactFailure(ErrInvalidArgument, "pass the art_… id from the tool output", fmt.Errorf("invalid artifact id: %q", refValue)), nil
	}
	if call.Runtime.Artifacts() == nil {
		return artifactFailure(ErrUnsupported, "enable artifacts on this surface", fmt.Errorf("artifact store is not configured")), nil
	}
	origin := ResultOrigin{}
	if provider, ok := call.Runtime.(ArtifactOriginProvider); ok {
		origin = provider.ArtifactOrigin()
	}
	ref, err := ResolveBareArtifactRef(refValue, origin)
	if err != nil {
		return artifactFailure(ErrArtifactScopeDenied, "pass the bare art_… id for this surface", err), nil
	}
	offset := intArg(call.Args, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	// The page size is this tool's own contract, bounded here rather than by the
	// output shaper: a page must fit the artifact window together with its
	// framing, and a caller asking for more gets the largest page that still
	// does. An explicit small limit is honored as-is - a caller asking for 40
	// bytes wants 40 bytes, and the floor below belongs to the budget-derived
	// maximum, not to the request (the builtin tool has always read it that way).
	limit := intArg(call.Args, "limit", 0)
	if limit <= 0 {
		limit = artifactReadMaxLimit(call)
	}
	if maxLimit := artifactReadMaxLimit(call); limit > maxLimit {
		limit = maxLimit
	}
	chunk, err := call.Runtime.Artifacts().Open(ctx, ref, offset, limit)
	if err != nil {
		code := CodeOf(err)
		if code == "" {
			code = ErrInvalidArgument
		}
		nextAction := NextActionOf(err)
		if nextAction == "" {
			nextAction = "check the artifact id and window"
		}
		return artifactFailure(code, nextAction, err), nil
	}

	// The page is rendered the way the builtin tool has always rendered it: a
	// self-describing header, then the window itself. The header is what a model
	// pages with - a successful result reaches the model as its body, so the
	// window bounds, the total size and the continuation offset must be in the
	// text, not only in metadata a remote model never sees.
	start := int(chunk.Offset)
	end := start + len(chunk.Data)
	total := int(chunk.Size)
	var builder strings.Builder
	fmt.Fprintf(
		&builder,
		"artifact %s | tool=%s | total_bytes=%d | window=[%d,%d) | eof=%t",
		ref.ID, artifactToolLabel(chunk.SourceTool), total, start, end, chunk.EOF,
	)
	if !chunk.EOF {
		fmt.Fprintf(&builder, " | next_offset=%d", end)
	}
	if chunk.SourceToolCallID != "" {
		builder.WriteString(" | tool_call_id=")
		builder.WriteString(chunk.SourceToolCallID)
	}
	builder.WriteString("\n\n")
	builder.WriteString(string(chunk.Data))

	metadata := map[string]any{
		"artifact_source_id":     ref.ID,
		"artifact_tool_name":     chunk.SourceTool,
		"artifact_total_bytes":   total,
		"artifact_window_offset": start,
		"artifact_window_bytes":  len(chunk.Data),
		"artifact_eof":           chunk.EOF,
		// The record holds the complete capture: this is a page of it, not a
		// truncated copy, so a caller may keep paging to the end.
		"artifact_has_full_bytes": true,
		// This tool owns its window: every page is sized against its own budget and
		// it publishes its own continuation contract (artifact_eof /
		// artifact_next_offset). Declare the render-layer opt-out and the window
		// itself, so the page is never folded - and if the opt-out is ever lost,
		// the render layer still folds at this tool's window, not at the backstop.
		keySkipRenderTruncation: true,
		keyModelVisibleBudget:   artifactReadBudgetBytes,
	}
	if !chunk.EOF {
		metadata["artifact_next_offset"] = end
	}
	if chunk.SourceToolCallID != "" {
		metadata["artifact_source_tool_call_id"] = chunk.SourceToolCallID
	}
	result := &Result{
		Success:    true,
		OutputKind: KindText,
		Content:    builder.String(),
		Metadata:   metadata,
	}
	if shaper := call.Runtime.Output(); shaper != nil {
		shaped, shapeErr := shaper.Shape(h.def.Name, result)
		if shapeErr != nil {
			return artifactFailure(ErrInternal, "retry the call", shapeErr), nil
		}
		if shaped != nil {
			result = shaped
		}
	}
	return result, nil
}

// artifactToolLabel names the capture's origin in the page header. A sink that
// cannot report the source tool leaves it empty and the header says "unknown"
// rather than inventing a name.
func artifactToolLabel(toolName string) string {
	if label := strings.TrimSpace(toolName); label != "" {
		return label
	}
	return "unknown"
}

// artifactIDPattern matches the artifact record id namespace (art_<32 hex>)
// anywhere in a caller-supplied string, so a model may paste the whole pointer
// tail ("art_… size=… kind=…") instead of the bare id.
var artifactPointerIDPattern = regexp.MustCompile(`(?i)art_[0-9a-f]{32}`)

// normalizeArtifactID accepts either a bare id or a pasted pointer tail and
// returns the art_<32hex> token when one is present.
func normalizeArtifactID(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if match := artifactPointerIDPattern.FindString(trimmed); match != "" {
		return strings.ToLower(match)
	}
	return strings.Trim(trimmed, "`\"'<>;,.")
}

func stringArg(args map[string]any, key string) string {
	if len(args) == 0 {
		return ""
	}
	value, _ := args[key].(string)
	return value
}

func intArg(args map[string]any, key string, fallback int) int {
	if len(args) == 0 {
		return fallback
	}
	switch value := args[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	case float32:
		return int(value)
	default:
		return fallback
	}
}

// artifactFailure keeps the stable code in the rendered body: the legacy MCP
// projection renders Result.Err, and the previous remote tool emitted
// "CODE: message (next: …)".
func artifactFailure(code, nextAction string, err error) *Result {
	result := Error(code, nextAction, err)
	content := fmt.Sprintf("%s: %s", code, result.Content)
	if nextAction != "" {
		content += fmt.Sprintf(" (next: %s)", nextAction)
	}
	result.Content = content
	result.Err = fmt.Errorf("%s", content)
	return result
}

// artifactReadMaxLimit is the largest page one call may return: the tool's own
// window minus its framing. Both the default and any caller-supplied limit are
// bounded by it, so a response never exceeds what the tool declared.
func artifactReadMaxLimit(call *Call) int {
	budget := artifactReadBudgetBytes
	if call != nil && call.Runtime != nil {
		if shaper := call.Runtime.Output(); shaper != nil {
			if provider, ok := shaper.(interface{ Budget(string) int }); ok {
				// A surface may narrow the window; it can never widen it past
				// the tool's declared budget.
				if remote := provider.Budget("artifact_read"); remote > 0 && remote < budget {
					budget = remote
				}
			}
		}
	}
	limit := budget - artifactReadHeaderReserveBytes
	if limit < artifactReadMinLimitBytes {
		limit = artifactReadMinLimitBytes
	}
	return limit
}
