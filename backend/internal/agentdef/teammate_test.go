package agentdef

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsPortableAgentName(t *testing.T) {
	assert.False(t, IsPortableAgentName(""))
	assert.False(t, IsPortableAgentName("team_teammate"))
	assert.False(t, IsPortableAgentName("Team_Teammate"))
	assert.False(t, IsPortableAgentName("team-teammate"))
	assert.True(t, IsPortableAgentName("explore"))
	assert.True(t, IsPortableAgentName("general"))
}

func TestTeammatePermissionModeExplore(t *testing.T) {
	mode := TeammatePermissionMode("explore", DiscoverOptions{})
	assert.Equal(t, "plan", mode)
}

func TestTeammatePermissionModeEmptyAndSynthetic(t *testing.T) {
	assert.Empty(t, TeammatePermissionMode("", DiscoverOptions{}))
	assert.Empty(t, TeammatePermissionMode("team_teammate", DiscoverOptions{}))
	assert.Empty(t, TeammatePermissionMode("not-a-real-agent-xyz", DiscoverOptions{}))
}

func TestPortableSessionDefaultsExplore(t *testing.T) {
	defaults, ok := PortableSessionDefaults("explore", DiscoverOptions{})
	require.True(t, ok)
	assert.Equal(t, "plan", defaults.PermissionMode)
	require.True(t, defaults.HasReadOnly)
	assert.True(t, defaults.ReadOnly)
}

func TestResolvePortableBindingGeneral(t *testing.T) {
	binding, err := ResolvePortableBinding("general", DiscoverOptions{})
	require.NoError(t, err)
	require.NotNil(t, binding)
	assert.Equal(t, "default", string(binding.PermissionMode))
}
