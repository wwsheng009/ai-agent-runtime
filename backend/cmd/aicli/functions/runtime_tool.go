package functions

import (
	"context"

	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

// RuntimeToolProvider executes unified runtime tools.
type RuntimeToolProvider interface {
	Execute(ctx context.Context, name string, args map[string]interface{}) (string, error)
}

// RuntimeToolProviderWithMetadata optionally returns richer tool metadata.
type RuntimeToolProviderWithMetadata interface {
	RuntimeToolProvider
	ExecuteWithMeta(ctx context.Context, name string, args map[string]interface{}) (string, map[string]interface{}, error)
}

// RuntimeToolFunction adapts runtime tools to the Function interface.
type RuntimeToolFunction struct {
	provider RuntimeToolProvider
	desc     runtimetools.ToolDescriptor
}

// NewRuntimeToolFunction creates a Function backed by the runtime tool manager.
func NewRuntimeToolFunction(provider RuntimeToolProvider, desc runtimetools.ToolDescriptor) *RuntimeToolFunction {
	return &RuntimeToolFunction{
		provider: provider,
		desc:     desc,
	}
}

// Name returns the tool name.
func (f *RuntimeToolFunction) Name() string {
	return f.desc.Name
}

// Description returns the tool description.
func (f *RuntimeToolFunction) Description() string {
	return f.desc.Description
}

// Parameters returns the tool schema.
func (f *RuntimeToolFunction) Parameters() map[string]interface{} {
	return f.desc.Parameters
}

// DefinitionMetadata returns optional extra definition metadata.
func (f *RuntimeToolFunction) DefinitionMetadata() map[string]interface{} {
	if f == nil {
		return nil
	}
	return f.desc.Metadata
}

// Execute runs the tool through the runtime provider.
func (f *RuntimeToolFunction) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	return f.provider.Execute(ctx, f.desc.Name, args)
}

// ExecuteWithMeta runs the tool and preserves metadata when the provider supports it.
func (f *RuntimeToolFunction) ExecuteWithMeta(ctx context.Context, args map[string]interface{}) (string, map[string]interface{}, error) {
	if provider, ok := f.provider.(RuntimeToolProviderWithMetadata); ok {
		return provider.ExecuteWithMeta(ctx, f.desc.Name, args)
	}
	output, err := f.Execute(ctx, args)
	return output, nil, err
}
