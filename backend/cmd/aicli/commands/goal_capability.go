package commands

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclitools"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
)

func goalCapabilities() []aiclitools.Capability {
	return runtimegoal.Capabilities()
}

func goalCapabilityRegistry() *aiclitools.Registry {
	return runtimegoal.CapabilityRegistry()
}

func goalCapabilityByName(name string) (aiclitools.Capability, bool) {
	return goalCapabilityRegistry().Get(name)
}

func goalCapabilityMetadata() map[string]interface{} {
	return runtimegoal.CapabilityMetadata()
}

func goalEmptyParameters() map[string]interface{} {
	return runtimegoal.EmptyParameters()
}

func updateGoalParameters() map[string]interface{} {
	return runtimegoal.UpdateParameters()
}
