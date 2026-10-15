package commands

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSkillTurnDoesNotChangeRequestToolSurface pins the tools-pin abolition
// (2026-10-15): /skill is knowledge guidance, not capability authorization.
// The materialized pin must never carry tool definitions, and overlaying it on
// the stable selection must keep the function set identical unless the skill
// itself declares allowed/disallowed-tools (narrow-only).
func TestSkillTurnDoesNotChangeRequestToolSurface(t *testing.T) {
	stable := &aicliFunctionSelection{
		Mode:               "auto",
		IncludeBuiltin:     true,
		BuiltinFunctions:   []string{"read_file", "bash", "write"},
		SkillFunctions:     []string{"skill__imagegen"},
		FinalFunctionNames: []string{"read_file", "bash", "write", "skill__imagegen"},
		Schemas: []map[string]interface{}{
			{"name": "read_file"}, {"name": "bash"}, {"name": "write"}, {"name": "skill__imagegen"},
		},
	}
	baseline, err := json.Marshal(stable.FinalFunctionNames)
	require.NoError(t, err)
	baselineSchemas, err := json.Marshal(stable.Schemas)
	require.NoError(t, err)

	// A pin without allow/disallow declarations (guide-only skill) must leave
	// the function surface byte-identical.
	guideOnly := &skillTurnPin{
		Guide:           "Skill program guide:\n- skill: skill__imagegen",
		PinnedFunctions: []string{"skill__imagegen"},
	}
	overlaid := overlayPinnedFunctions(stable, guideOnly)
	after, err := json.Marshal(overlaid.FinalFunctionNames)
	require.NoError(t, err)
	require.JSONEq(t, string(baseline), string(after),
		"/skill without tool declarations must not change the request function surface")
	afterSchemas, err := json.Marshal(overlaid.Schemas)
	require.NoError(t, err)
	require.JSONEq(t, string(baselineSchemas), string(afterSchemas),
		"/skill without tool declarations must not change request schemas")

	// A pin whose skill declares allowed-tools may only narrow (subset), never add.
	restricting := &skillTurnPin{
		Guide:            "guide",
		PinnedFunctions:  []string{"skill__imagegen"},
		AllowedFunctions: []string{"bash", "skill__imagegen"},
	}
	narrowed := overlayPinnedFunctions(stable, restricting)
	require.NotContains(t, narrowed.FinalFunctionNames, "read_file")
	require.NotContains(t, narrowed.FinalFunctionNames, "write")
	require.Contains(t, narrowed.FinalFunctionNames, "bash")
	require.Contains(t, narrowed.FinalFunctionNames, "skill__imagegen")
	require.Len(t, narrowed.Schemas, 2)
}
