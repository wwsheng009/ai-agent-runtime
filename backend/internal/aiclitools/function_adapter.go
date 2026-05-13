package aiclitools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
)

type SessionContextFactory func(ctx context.Context) ToolSessionContext

type capabilityFunction struct {
	cap     Capability
	factory SessionContextFactory
}

func FunctionFromCapability(cap Capability, factory SessionContextFactory) functions.Function {
	return &capabilityFunction{cap: cap, factory: factory}
}

func (f *capabilityFunction) Name() string {
	return f.cap.Name
}

func (f *capabilityFunction) Description() string {
	return f.cap.Description
}

func (f *capabilityFunction) Parameters() map[string]interface{} {
	return cloneMap(f.cap.Parameters)
}

func (f *capabilityFunction) DefinitionMetadata() map[string]interface{} {
	return cloneMap(f.cap.Metadata)
}

func (f *capabilityFunction) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	output, _, err := f.ExecuteWithMeta(ctx, args)
	return output, err
}

func (f *capabilityFunction) ExecuteWithMeta(ctx context.Context, args map[string]interface{}) (string, map[string]interface{}, error) {
	if f == nil || f.cap.Execute == nil {
		return "", nil, fmt.Errorf("capability %q is not executable", f.Name())
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var session ToolSessionContext
	if f.factory != nil {
		session = f.factory(ctx)
	}
	result, err := f.cap.Execute(ctx, session, args)
	if err != nil {
		return "", nil, err
	}
	output, err := stringifyToolOutput(result.Output)
	if err != nil {
		return "", nil, err
	}
	return output, cloneMap(result.Metadata), nil
}

var _ functions.Function = (*capabilityFunction)(nil)
var _ functions.FunctionWithMetadata = (*capabilityFunction)(nil)
var _ functions.FunctionDefinitionMetadataProvider = (*capabilityFunction)(nil)

func stringifyToolOutput(output interface{}) (string, error) {
	switch value := output.(type) {
	case nil:
		return "", nil
	case string:
		return value, nil
	default:
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshal tool output: %w", err)
		}
		return string(data), nil
	}
}

func cloneMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]interface{}, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
