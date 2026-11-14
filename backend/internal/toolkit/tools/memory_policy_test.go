package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

func TestMemoryToolsPolicyMatchesDefinitionMetadata(t *testing.T) {
	resolver := runtimepolicy.DefaultCapabilityResolver{}
	readOnly := runtimepolicy.NewToolExecutionPolicy(nil, true)
	child := runtimepolicy.NewCapabilityScopedToolExecutionPolicy(nil, runtimepolicy.ReadOnlyChildCapabilities())
	offline := runtimepolicy.NewCapabilityScopedToolExecutionPolicy(nil,
		runtimepolicy.CapabilitiesComplementOf([]runtimepolicy.Capability{runtimepolicy.CapNetwork}))
	for _, tc := range []struct {
		tool  toolkit.Tool
		write bool
	}{
		{NewMemorySearchTool(nil), false},
		{NewMemoryReadTool(nil), false},
		{NewMemorySessionContextTool(nil), false},
		{NewMemoryRecentTool(nil), false},
		{NewMemoryStatsTool(nil), false},
		{NewMemoryWhyTool(nil), false},
		{NewMemoryWhyNotTool(nil), false},
		{NewMemoryAddTool(nil), true},
		{NewMemoryCorrectTool(nil), true},
		{NewMemoryForgetTool(nil), true},
	} {
		t.Run(tc.tool.Name(), func(t *testing.T) {
			name := tc.tool.Name()
			declared, ok := runtimepolicy.TaxonomyFromMetadata(name, tc.tool.(*memoryTool).DefinitionMetadata())
			require.True(t, ok)
			fallback, ok := runtimepolicy.LookupToolTaxonomy(name)
			require.True(t, ok)
			require.Equal(t, declared, fallback, "name-only derivation must match the tool's live metadata")
			want := []runtimepolicy.Capability{runtimepolicy.CapReadOnly}
			if tc.write {
				want = []runtimepolicy.Capability{runtimepolicy.CapWriteFS}
				require.Error(t, readOnly.AllowTool(name))
				require.Error(t, child.AllowTool(name))
			} else {
				require.NoError(t, readOnly.AllowTool(name))
				require.NoError(t, child.AllowTool(name))
			}
			require.Equal(t, want, resolver.Resolve(runtimepolicy.EvalRequest{ToolName: name}))
			require.NoError(t, offline.AllowTool(name), "local memory tools do not require network capability")
		})
	}
}
