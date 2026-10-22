package toolcore

import (
	"context"
	"fmt"
	"strings"
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
	limit := intArg(call.Args, "limit", 64*1024)
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
	metadata := map[string]any{
		"artifact_id":  ref.ID,
		"offset":       offset,
		"bytes":        len(chunk.Data),
		"total_bytes":  chunk.Size,
		"eof":          chunk.EOF,
		"is_truncated": !chunk.EOF,
	}
	if !chunk.EOF {
		metadata["suggested_next_offset"] = offset + len(chunk.Data)
	}
	result := &Result{
		Success:    true,
		OutputKind: KindText,
		Content:    string(chunk.Data),
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
