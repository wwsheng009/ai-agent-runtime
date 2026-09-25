package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// NewProfileCommand 创建 `aicli profile` 命令组（实施方案 Batch 2 / FR-1）：
// list / show / validate / create。
//
// 注意命名区分（设计文档 D11）：本命令组处理的是"运行 profile"（profile 目录 =
// profile.yaml + agents/... 的场景化裁剪）；"subagent 路由难度档位"
// （AICLISubagentRouteProfile）是另一个概念，用户可见文案中称为"路由档位"。
func NewProfileCommand(getConfig func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "管理场景化 profile（list/show/validate/create/export/import）",
		Long: `管理运行 profile：profile.yaml + agents/<id>/ 的场景化裁剪声明。

子命令：
  list      列出可用 profile（config 注册项 / default root 下的目录 / user|project 层目录 / 显式路径）
  show      解析并展示一个 profile 的最终生效面（工具/skills/mcp/prompt/paths）
  validate  校验 profile 声明（语法/必填/工具名/skill/mcp 引用/prompt 可读性）
  create    从内置模板（coding|review|minimal|docs）生成 profile 目录
  export    导出 profile 为 zip 包（分享/分发；只读，不写 profile/配置/会话）
  import    导入目录或 zip 包为新 profile（先 validate、绝不自动激活、不覆盖同名）

优先级：--profile flag > config profiles.default_profile（可经 DEFAULT_PROFILE
env 提供）> 无 profile（全量，零变化）。
--profile auto：按首轮提示词自动路由到具体 profile（--prompt/--message，
exec 也可用 stdin；规则见 config profiles.auto，未配置时为内置启发式）。
启动期无提示词（纯交互式 chat / agent stdio）时显式报错，不猜、不静默降级。`,
	}
	cmd.AddCommand(newProfileListCommand(getConfig))
	cmd.AddCommand(newProfileShowCommand(getConfig))
	cmd.AddCommand(newProfileValidateCommand(getConfig))
	cmd.AddCommand(newProfileCreateCommand(getConfig))
	cmd.AddCommand(newProfileExportCommand(getConfig))
	cmd.AddCommand(newProfileImportCommand(getConfig))
	return cmd
}

// resolveProfileRegistryForCLI 构建与 `aicli chat --profile` 相同的 registry，
// 避免出现"第二套解析逻辑"。
func resolveProfileRegistryForCLI(cfg *config.Config) *profilesys.Registry {
	if cfg == nil {
		return profilesys.NewRegistry("")
	}
	return profilesys.NewRegistryFromProfilesConfig(cfg.Profiles)
}

// resolveProfileStateForCLI 复用 chat 的解析路径（registry + 全局层输入），
// 保证 show/validate 与真实会话看到的是同一个结果。
func resolveProfileStateForCLI(cfg *config.Config, ref, agent string) (*chatProfileState, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("profile reference is required")
	}
	state, err := resolveChatProfileState(cfg, &chatCommandOptions{
		ProfileFlag: ref,
		AgentFlag:   strings.TrimSpace(agent),
	})
	if err != nil {
		return nil, err
	}
	if state == nil || !state.Active() {
		return nil, fmt.Errorf("profile %q could not be resolved", ref)
	}
	return state, nil
}

// loadProfileSpecForCLI 读取 profile.yaml 原始声明（供 show/validate 展示与校验）。
func loadProfileSpecForCLI(root string) (*profilesys.ProfileSpec, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("profile root is required")
	}
	return profilesys.LoadProfile(root)
}

// annotateProfileResolveError 把 agents/<id>/agent.yaml 的解析失败升级为可执行
// 提示：该文件同时被 profile 解析器（profile.AgentSpec：tools 为映射）与
// portable agentdef（tools 为列表）读取，两种形状无法共存，因此 tools/
// disallowed_tools 不能写在 agent.yaml 里。
func annotateProfileResolveError(err error) error {
	if err == nil {
		return nil
	}
	if !strings.Contains(err.Error(), "agent.yaml") {
		return err
	}
	return fmt.Errorf("%w\n提示：agents/<id>/agent.yaml 同时被 profile 解析器与 portable agentdef 读取，"+
		"两者对 tools 的形状要求不同（映射 vs 列表）；请勿在 agent.yaml 中声明 tools/disallowed_tools，"+
		"工具策略请写到 profile.yaml 的 agents.<id>.tools 或 agents/<id>/tools/policy.yaml", err)
}

func profileYAMLPath(root string) string {
	return filepath.Join(strings.TrimSpace(root), "profile.yaml")
}

func profileRootHasProfileYAML(root string) bool {
	root = strings.TrimSpace(root)
	if root == "" {
		return false
	}
	info, err := os.Stat(profileYAMLPath(root))
	return err == nil && !info.IsDir()
}

// sortedProfileItemNames 返回 config profiles.items 的稳定顺序键。
func sortedProfileItemNames(profiles *config.ProfilesConfig) []string {
	if profiles == nil || len(profiles.Items) == 0 {
		return nil
	}
	names := make([]string, 0, len(profiles.Items))
	for name := range profiles.Items {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
