package commands

import (
	"context"
	"fmt"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclitools"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
)

const (
	getGoalFunctionName    = runtimegoal.GetToolName
	updateGoalFunctionName = runtimegoal.UpdateToolName
)

type goalFunction struct {
	session *ChatSession
	name    string
}

func registerGoalFunctions(session *ChatSession) {
	catalog := ensureFunctionCatalog(session)
	if catalog == nil {
		return
	}
	for _, cap := range goalCapabilities() {
		if !cap.SupportsPath(aiclitools.ExposureShared) {
			continue
		}
		catalog.RegisterFunction(aiclitools.FunctionFromCapability(cap, func(ctx context.Context) aiclitools.ToolSessionContext {
			return newChatToolSessionContext(session, aiclitools.ExposureShared)
		}))
	}
}

func (f *goalFunction) Name() string {
	return f.name
}

func (f *goalFunction) Description() string {
	if cap, ok := goalCapabilityByName(f.name); ok {
		return cap.Description
	}
	return "Goal function"
}

func (f *goalFunction) Parameters() map[string]interface{} {
	if cap, ok := goalCapabilityByName(f.name); ok {
		return cap.Parameters
	}
	return goalEmptyParameters()
}

func (f *goalFunction) DefinitionMetadata() map[string]interface{} {
	return goalCapabilityMetadata()
}

func (f *goalFunction) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	cap, ok := goalCapabilityByName(f.name)
	if !ok {
		return "", fmt.Errorf("unknown goal function %q", f.name)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := cap.Execute(ctx, newChatToolSessionContext(f.session, aiclitools.ExposureShared), args)
	if err != nil {
		return "", err
	}
	output, _ := result.Output.(string)
	return output, nil
}

var _ functions.Function = (*goalFunction)(nil)
var _ functions.FunctionDefinitionMetadataProvider = (*goalFunction)(nil)

func executeGetGoalFunction(session *ChatSession) (string, error) {
	fn := &goalFunction{session: session, name: getGoalFunctionName}
	return fn.Execute(context.Background(), nil)
}

func executeUpdateGoalFunction(session *ChatSession, args map[string]interface{}) (string, error) {
	fn := &goalFunction{session: session, name: updateGoalFunctionName}
	return fn.Execute(context.Background(), args)
}
