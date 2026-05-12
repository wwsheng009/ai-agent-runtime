package commands

import "github.com/spf13/cobra"

func registerExecFlags(cmd *cobra.Command) {
	registerExecSharedFlags(cmd, nil)
}

func registerExecSharedFlags(cmd *cobra.Command, exclude map[string]bool) {
	flags := cmd.Flags()
	has := func(name string) bool {
		return exclude != nil && exclude[name]
	}

	flags.String("profile", "", "profile 名称或目录路径")
	flags.String("agent", "", "profile 内 agent 标识")
	flags.StringP("model", "m", "", "指定模型名称")
	flags.StringP("provider", "P", "", "指定 provider 名称")
	flags.IntP("max-tokens", "t", 0, "最大输出 tokens（0=使用模型默认值）")
	if !has("prompt") {
		flags.StringP("prompt", "p", "", "提示词（可与 stdin 组合使用）")
	}
	flags.Bool("stream", false, "使用流式模型输出")
	flags.String("reasoning-effort", "", "当前模型支持的 reasoning_effort 值")
	flags.String("runtime-mode", "", "执行宿主模式（local|server|auto）")
	flags.String("runtime-server", "", "runtime-server 地址或模式别名（server|auto|local|http://127.0.0.1:8101）")

	flags.Bool("json", false, "以 JSONL 事件流格式输出")
	flags.String("output", "", "输出格式（text|json）")
	flags.StringP("output-last-message", "o", "", "将最后消息写入文件")
	flags.String("output-schema", "", "最终 assistant 消息的 JSON Schema 文件路径或内联 JSON")
	flags.Bool("envelope", false, "JSON 输出使用 envelope 结构")

	flags.Bool("ephemeral", false, "不持久化会话文件")
	flags.String("session-dir", "", "chat 会话持久化目录")
	flags.String("user", "", "chat 会话用户 ID")
	if !has("title") {
		flags.String("title", "", "设置当前 exec 会话标题")
	}
	if !has("image") {
		flags.StringSliceP("image", "i", nil, "附加图片文件路径")
	}
	flags.String("request-timeout", "", "单次 LLM 请求超时（如 60s, 2m）")
	flags.Duration("timeout", 0, "整次 exec 执行超时时间（如 5m, 30s），0 表示无限制")

	flags.String("permission-mode", "default", "权限模式（default|accept_edits|plan|bypass_permissions）")
	flags.String("approval-reuse", "session_readonly_shell", "审批复用策略（off|session_readonly_shell|team_readonly_shell）")
	flags.Bool("yolo", false, "快捷模式：等价于 --permission-mode bypass_permissions")

	flags.Bool("disable-tools", false, "禁用 tools/skills 暴露")
	flags.StringSlice("skills-dir", nil, "附加外部 skills 目录（可重复指定）")
	flags.Int("skills-top-k", 0, "暴露给模型的候选 skills 数量（0=使用配置默认值）")
	flags.String("skills-mode", "auto", "skills 暴露模式（auto|prefer|only）")
	flags.Bool("skills-debug", false, "打印 skill route 候选和暴露结果")

	flags.StringSliceP("config-override", "C", nil, "配置覆盖 key=value（-c 已被 root --config 使用）")

	flags.Bool("debug-http", false, "HTTP 调试输出")
	flags.Bool("fail-fast", false, "禁用自动重试")
}
