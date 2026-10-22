package toolkit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolschema"
)

// ToolcoreHandler adapts a legacy toolkit.Tool to the surface-independent
// Handler contract. The adapter is intentionally one-way at execution time:
// legacy tools keep their existing behavior while new surfaces consume one
// definition.
type ToolcoreHandler struct {
	in  Tool
	def toolcore.Definition
}

// NewToolcoreHandler converts a legacy Tool. The schema is canonicalized once,
// so both interfaces expose byte-identical parameters.
func NewToolcoreHandler(tool Tool) (*ToolcoreHandler, error) {
	if tool == nil {
		return nil, fmt.Errorf("tool is nil")
	}
	schema, err := SchemaFromParameters(tool.Parameters())
	if err != nil {
		return nil, fmt.Errorf("tool %q schema: %w", tool.Name(), err)
	}
	def := toolcore.Definition{
		Name:        tool.Name(),
		Description: tool.Description(),
		Version:     tool.Version(),
		InputSchema: schema,
		DirectCall:  tool.CanDirectCall(),
	}
	if provider, ok := tool.(ToolDefinitionMetadataProvider); ok {
		def.Metadata = provider.DefinitionMetadata()
	}
	return &ToolcoreHandler{in: tool, def: def}, nil
}

func (h *ToolcoreHandler) Definition() toolcore.Definition {
	if h == nil {
		return toolcore.Definition{}
	}
	return h.def
}

func (h *ToolcoreHandler) Execute(ctx context.Context, call *toolcore.Call) (*toolcore.Result, error) {
	if h == nil || h.in == nil {
		return nil, fmt.Errorf("tool handler is nil")
	}
	var args map[string]any
	if call != nil {
		args = call.Args
	}
	result, err := h.in.Execute(ctx, args)
	if err != nil {
		return &toolcore.Result{
			Success:    false,
			OutputKind: toolcore.KindText,
			Content:    err.Error(),
			Err:        err,
		}, nil
	}
	return ResultFromToolkit(result), nil
}

// ToolkitTool adapts a core Handler back to the legacy interface so existing
// hosts can consume migrated tools without a flag day.
type ToolkitTool struct {
	in  toolcore.Handler
	def toolcore.Definition
}

func NewToolkitTool(handler toolcore.Handler) (*ToolkitTool, error) {
	if handler == nil {
		return nil, fmt.Errorf("tool handler is nil")
	}
	def := handler.Definition()
	if err := toolcore.ValidateDefinition(def); err != nil {
		return nil, err
	}
	return &ToolkitTool{in: handler, def: def}, nil
}

func (t *ToolkitTool) Name() string        { return t.def.Name }
func (t *ToolkitTool) Description() string { return t.def.Description }
func (t *ToolkitTool) Version() string     { return t.def.Version }
func (t *ToolkitTool) CanDirectCall() bool { return t.def.DirectCall }

func (t *ToolkitTool) Parameters() map[string]interface{} {
	parameters, err := ParametersFromSchema(t.def.InputSchema)
	if err != nil {
		return map[string]interface{}{}
	}
	return parameters
}

func (t *ToolkitTool) DefinitionMetadata() map[string]interface{} {
	out := make(map[string]interface{}, len(t.def.Metadata))
	for key, value := range t.def.Metadata {
		out[key] = value
	}
	if len(t.def.Capabilities) > 0 {
		capabilities := make([]string, 0, len(t.def.Capabilities))
		for _, capability := range t.def.Capabilities {
			capabilities = append(capabilities, string(capability))
		}
		out["capabilities"] = capabilities
	}
	out["contract_version"] = toolcore.ContractVersion
	return out
}

func (t *ToolkitTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	result, err := t.in.Execute(ctx, &toolcore.Call{Args: params})
	if err != nil {
		return nil, err
	}
	return ResultToToolkit(result), nil
}

// ParametersFromSchema converts the canonical raw schema into the legacy map.
func ParametersFromSchema(raw json.RawMessage) (map[string]interface{}, error) {
	if len(raw) == 0 {
		return map[string]interface{}{}, nil
	}
	var schema map[string]interface{}
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, err
	}
	return schema, nil
}

// SchemaFromParameters canonicalizes a legacy parameter map into raw JSON.
func SchemaFromParameters(parameters map[string]interface{}) (json.RawMessage, error) {
	canonical, _, err := toolschema.CanonicalizeAndValidate(parameters)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// ResultFromToolkit maps a legacy result into the core result. A nil result is
// represented as an empty success rather than a nil pointer.
func ResultFromToolkit(result *ToolResult) *toolcore.Result {
	if result == nil {
		return &toolcore.Result{Success: true, OutputKind: toolcore.KindEmpty}
	}
	kind := result.NormalizedOutputKind()
	out := &toolcore.Result{
		Success:    result.Success,
		OutputKind: kind,
		Content:    result.Content,
		Data:       append([]byte(nil), result.Data...),
		MIMEType:   result.MIMEType,
		Metadata:   cloneAnyMap(result.Metadata),
		Err:        result.Error,
	}
	if result.Error != nil && !result.Success {
		out.ErrorCode = errorCodeFromMetadata(out.Metadata)
	}
	if out.Metadata != nil {
		if code, ok := out.Metadata["error_code"].(string); ok {
			out.ErrorCode = toolcore.NormalizeErrorCode(code)
		}
		if action, ok := out.Metadata["next_action"].(string); ok {
			out.NextAction = action
		}
		if retriable, ok := out.Metadata["retriable"].(bool); ok {
			out.Retriable = retriable
		}
	}
	return out
}

// ResultToToolkit maps a core result back to the legacy shape.
func ResultToToolkit(result *toolcore.Result) *ToolResult {
	if result == nil {
		return &ToolResult{Success: true, OutputKind: toolcore.KindEmpty}
	}
	metadata := cloneAnyMap(result.Metadata)
	if metadata == nil && (result.ErrorCode != "" || result.NextAction != "" || result.Retriable) {
		metadata = map[string]interface{}{}
	}
	if result.ErrorCode != "" {
		metadata["error_code"] = result.ErrorCode
	}
	if result.NextAction != "" {
		metadata["next_action"] = result.NextAction
	}
	if result.Retriable {
		metadata["retriable"] = true
	}
	return &ToolResult{
		Success:    result.Success,
		OutputKind: result.OutputKind,
		Content:    result.Content,
		Data:       append([]byte(nil), result.Data...),
		MIMEType:   result.MIMEType,
		Error:      result.Err,
		Metadata:   metadata,
	}
}

func cloneAnyMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func errorCodeFromMetadata(metadata map[string]interface{}) string {
	if len(metadata) == 0 {
		return ""
	}
	code, _ := metadata["error_code"].(string)
	return toolcore.NormalizeErrorCode(strings.TrimSpace(code))
}
