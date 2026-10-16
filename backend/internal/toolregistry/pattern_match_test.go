package toolregistry

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// MatchPattern 是工具名单（allowlist/denylist/skills）的统一通配语义：
// "*" 匹配一切、"prefix_*" 前缀匹配、其余归一化精确匹配。
// 本组测试钉死三类语义分支与归一化行为。
func TestMatchPatternSemantics(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		cand    string
		want    bool
	}{
		// 裸 * 匹配一切（含空名——由调用方上游拦截空名）。
		{"bare wildcard matches everything", "*", "anything", true},
		{"bare wildcard matches essential", "*", "subagent_spawn", true},
		{"bare wildcard matches empty", "*", "", true},
		// 前缀模式：匹配域前缀，不匹配其他域。
		{"prefix matches same domain", "subagent_*", "subagent_spawn", true},
		{"prefix matches another tool in domain", "subagent_*", "subagent_status", true},
		{"prefix rejects other domains", "subagent_*", "job_start", false},
		{"prefix rejects prefix-lookalike", "subagent_*", "subagentish", false},
		{"prefix rejects bare prefix", "subagent_*", "subagent_", true},
		// 精确匹配：归一化后比较（别名/大小写/连字符变体）。
		{"exact canonical", "view", "view", true},
		{"exact with case folding", "View", "view", true},
		{"exact with dash folding", "spawn-subagents", "spawn_subagents", true},
		{"exact via legacy alias", "background_task", "job_start", true},
		{"exact rejects different tool", "view", "grep", false},
		{"exact rejects misspelling", "vieww", "view", false},
		// 空模式不匹配任何东西（空名单 = 未声明，不是全匹配）。
		{"empty pattern matches nothing", "", "view", false},
		{"blank pattern matches nothing", "   ", "view", false},
		// 中缀 * 不受支持（只有尾缀 * 是通配）：按归一精确比较落空。
		{"infix asterisk is literal", "sub*agent", "subagent_spawn", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, MatchPattern(tt.pattern, tt.cand),
				"MatchPattern(%q, %q)", tt.pattern, tt.cand)
		})
	}
}

func TestMatchesAnyPattern(t *testing.T) {
	// 混合名单：通配 + 精确 + 别名共存。
	list := []string{"view", "job_*"}
	require.True(t, MatchesAnyPattern(list, "view"))
	require.True(t, MatchesAnyPattern(list, "job_start"))
	require.True(t, MatchesAnyPattern(list, "Job-Output"))
	require.False(t, MatchesAnyPattern(list, "shell"))

	// 空名单与全空白名单都表示"未声明"。
	require.False(t, MatchesAnyPattern(nil, "view"))
	require.False(t, MatchesAnyPattern([]string{"", "  "}, "view"))
}

func TestListDeclaresWildcard(t *testing.T) {
	require.True(t, ListDeclaresWildcard([]string{"*"}))
	require.True(t, ListDeclaresWildcard([]string{"view", "job_*"}))
	require.True(t, ListDeclaresWildcard([]string{"  *  "}))
	require.False(t, ListDeclaresWildcard([]string{"view", "grep"}))
	require.False(t, ListDeclaresWildcard(nil))
	require.False(t, ListDeclaresWildcard([]string{""}))
}
