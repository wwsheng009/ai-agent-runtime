package runtimeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
)

type toolCatalogToolJSON struct {
	Name       string   `json:"name"`
	Aliases    []string `json:"aliases"`
	Group      string   `json:"group"`
	Exposure   []string `json:"exposure"`
	Essential  bool     `json:"essential"`
	WriteLike  bool     `json:"write_like"`
	ShellLike  bool     `json:"shell_like"`
	Delegation bool     `json:"delegation"`
	PlanWrite  bool     `json:"plan_write"`
}

type toolCatalogGroupJSON struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Protected bool   `json:"protected"`
	ToolCount int    `json:"tool_count"`
}

type toolCatalogJSON struct {
	Source string                 `json:"source"`
	Count  int                    `json:"count"`
	Tools  []toolCatalogToolJSON  `json:"tools"`
	Groups []toolCatalogGroupJSON `json:"groups"`
}

// decodeToolCatalog 走真实路由请求 /api/runtime/tools/catalog 并解码响应。
func decodeToolCatalog(t *testing.T) toolCatalogJSON {
	t.Helper()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	// 路径里没有会话 id：目录端点必须能无会话独立响应（前端 profile 编辑器用）。
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/tools/catalog", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var decoded toolCatalogJSON
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&decoded))
	return decoded
}

// TestListBuiltinToolCatalog_ProjectsRegistrySingleSource 钉住 §2.2-B 的口径：
// 目录只列规范名（别名不另立条目）、每条都有分组与暴露面、与注册表逐一对应。
func TestListBuiltinToolCatalog_ProjectsRegistrySingleSource(t *testing.T) {
	decoded := decodeToolCatalog(t)
	entries := toolregistry.Entries()

	require.Equal(t, "toolregistry", decoded.Source)
	require.Equal(t, len(entries), decoded.Count)
	require.Len(t, decoded.Tools, len(entries))

	wantNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		wantNames = append(wantNames, entry.Name)
	}
	names := make([]string, 0, len(decoded.Tools))
	for _, tool := range decoded.Tools {
		require.NotEmpty(t, tool.Group, "每个 builtin 必须有分组")
		require.NotEmpty(t, tool.Exposure, "每个 builtin 必须有暴露面声明")
		names = append(names, tool.Name)
	}
	require.Equal(t, wantNames, names, "目录的规范名集合与顺序必须与注册表一致")

	// 别名只是同一身份的兼容拼写，不得各自成为目录条目。
	for _, alias := range []string{"bash", "execute_shell_command", "read", "rg"} {
		require.NotContains(t, names, alias, "别名 %q 不应作为独立条目出现", alias)
	}
}

// TestListBuiltinToolCatalog_AliasesRideWithCanonical 钉住别名的挂载点与类声明：
// 前端要把旧拼写解释成同一个工具，就必须能从规范名条目上看到别名。
func TestListBuiltinToolCatalog_AliasesRideWithCanonical(t *testing.T) {
	decoded := decodeToolCatalog(t)
	byName := make(map[string]toolCatalogToolJSON, len(decoded.Tools))
	for _, tool := range decoded.Tools {
		byName[tool.Name] = tool
	}

	shell, ok := byName["shell"]
	require.True(t, ok, "注册表必须有 shell")
	require.Equal(t, "shell", shell.Group)
	require.True(t, shell.ShellLike)
	require.False(t, shell.Essential)
	require.ElementsMatch(t, []string{"bash", "execute_shell_command"}, shell.Aliases)

	view, ok := byName["view"]
	require.True(t, ok, "注册表必须有 view")
	require.Equal(t, "read", view.Group)
	require.ElementsMatch(t, []string{"read"}, view.Aliases)

	grep, ok := byName["grep"]
	require.True(t, ok, "注册表必须有 grep")
	require.ElementsMatch(t, []string{"rg"}, grep.Aliases)

	// essential 是控制面语义（取代 policy 的硬编码名单），必须如实投影。
	teamSpawn, ok := byName["team_spawn"]
	require.True(t, ok, "注册表必须有 team_spawn")
	require.True(t, teamSpawn.Essential, "控制面工具必须标 essential")
	require.True(t, teamSpawn.Delegation)
}

// TestListBuiltinToolCatalog_GroupCountsComeFromSameEntries 钉住分组账本同源：
// 各组 tool_count 之和必须等于条目总数（两组数字不得来自两份名单）。
func TestListBuiltinToolCatalog_GroupCountsComeFromSameEntries(t *testing.T) {
	decoded := decodeToolCatalog(t)

	require.NotEmpty(t, decoded.Groups)
	protected := map[string]bool{}
	sum := 0
	for _, group := range decoded.Groups {
		require.NotEmpty(t, group.ID)
		require.NotEmpty(t, group.Label, "组 %q 必须有展示名", group.ID)
		protected[group.ID] = group.Protected
		sum += group.ToolCount
	}
	require.Equal(t, decoded.Count, sum, "各组 tool_count 之和必须等于条目总数")

	// profile-tool-groups-design.md §3.3：读/写/命令执行是基本盘，不可整组关闭。
	require.True(t, protected["read"])
	require.True(t, protected["edit"])
	require.True(t, protected["shell"])
	require.False(t, protected["orchestration"])
}
