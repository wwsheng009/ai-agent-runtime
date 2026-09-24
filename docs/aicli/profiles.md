# 运行 profile 指南（`profile.yaml` + `agents/`）

> 状态：已实施（2026-09-24，实施方案 Batch 2：`aicli profile` 命令组 + 内置模板 + 估算单点）。
> 代码位置：`backend/internal/profile/`、`backend/cmd/aicli/commands/profile*.go`。

本页只讲**运行 profile**（一个目录：`profile.yaml` + `agents/<id>/`），它决定一次会话的
工具面、技能可见性、MCP 服务器、提示词组合与路径。

**命名区分**：`aicli profile` / `profile.yaml` 是"运行 profile"；`AICLISubagentRouteProfile`
是 subagent 的"**路由难度档位**"，与本页无关（见 [agents.md](./agents.md) 与 subagent 路由文档）。

---

## 1. 目录结构

```
<profile-root>/
├── profile.yaml                 # 必需：场景声明
└── agents/
    └── <agent-id>/
        ├── agent.yaml           # 可选：portable agent definition（角色/权限/沙箱）
        ├── prompts/role.md      # 可选：角色提示词（进入 prompt 组合）
        ├── tools/policy.yaml    # 可选：工具策略（ToolPolicySpec）
        ├── skills/              # 可选：profile 自带技能
        └── workspace/workspace.yaml  # 可选：workspace 级 provider/model/tools 覆盖
```

选择优先级：`--profile` flag > `config profiles.default_profile`（可经 `DEFAULT_PROFILE`
环境变量提供）> 无 profile（全量，行为与未引入 profile 时逐字节一致）。

profile 引用（`<profile>`）支持三种写法：注册名、`profiles.root` 下的目录名、profile 目录路径。

---

## 2. `profile.yaml` 字段

```yaml
profile:
  name: coding              # 必填：`profile list/show/validate` 的标识
  description: ...          # 可选：仅展示用，不影响解析
  default_agent: default    # 可选：未显式 --agent 时选中的 agent

providers:
  default_provider: openai  # 可选：profile 级默认 provider

tools:                      # 工具策略（只收窄，不能放宽安全域）
  allowlist: [view, grep]
  denylist: [shell]
  read_only: true           # 拒绝写操作
  sandbox: {mode: read-only}

skills:                     # 技能可见性（deny 恒优先于 allow；"*" 匹配全部）
  allowlist: [...]
  denylist: ["*"]

mcp:                        # MCP 服务器（exclude 恒优先于 use；"*" 匹配全部）
  use_servers: [docs]
  exclude_servers: [...]

prompts:
  mode: replace             # replace（默认）| append

agents:                     # 按 agent 的内联覆盖（provider/model/tools）
  explore:
    tools:
      allowlist: [view, grep, glob, ls, shell]
      denylist: [write, edit, apply_patch, append_write, multiedit]
```

语义要点：

- **只收窄**：denylist / exclude 优先；安全域（密钥、端点、foldertrust、`BlockUntrustedMCP`）
  不能被 profile 覆盖或关闭。
- **零变化**：不声明某项 = 沿用原有全量行为（未引入 profile 的路径行为不变）。
- **错误不猜**：`prompts.mode` 等取值非法时**报错**，不回退到别的语义。

---

## 3. `agents/<id>/agent.yaml` 的硬约束

`agent.yaml` 同时被两个消费者读取：

| 消费者 | 读取目的 | `tools` 的形状 |
| --- | --- | --- |
| profile 解析器（`profile.AgentSpec`） | provider/model/工具策略合并 | 映射（`allowlist`/`denylist`/`read_only`/`sandbox`） |
| portable agentdef（`agentdef.AdaptProfileAgent`） | 角色定义（body/权限/沙箱） | 列表（`[view, grep, ...]`） |

两者形状不同，**无法共存**：因此 `agent.yaml` 中**不要写 `tools` / `disallowed_tools`**。

工具策略请写到：

1. `profile.yaml` 的 `agents.<id>.tools`（推荐，见上例），或
2. `agents/<id>/tools/policy.yaml`（`ToolPolicySpec` 文件形式）。

`agent.yaml` 里合法且生效的字段（portable definition）：`name`、`description`、`model`、
`provider`、`permission_mode`、`completion_requirement`、`sandbox`（`read-only` / `workspace` /
`strict` / `off`）、`prompt_mode`、`system_prompt`，以及 `prompts/role.md`（角色正文）。

`aicli profile validate` 命中该冲突时会直接给出这条提示。

---

## 4. 命令

```powershell
# 列出可用 profile（config 注册项 / profiles.root 下的目录 / 显式路径三来源）
aicli profile list
aicli profile list .\profiles\review --output json

# 展示解析后的最终生效面：工具/skills/mcp/prompts/paths（与真实会话同一套解析逻辑）
aicli profile show coding
aicli profile show .\profiles\coding --agent explore --output json

# 校验声明（error 级问题退出码为 1，可直接用于 CI 门禁）
aicli profile validate coding

# 从内置模板生成（coding | review | minimal | docs）
aicli profile create my-coding --template coding --use --set-default
aicli profile create my-profile --dry-run
```

`create` 的目标目录：`--root` > `profiles.root/<name>` > `./profiles/<name>`；目标目录已存在
且非空时报错，`--force` 才覆盖。`--use` / `--set-default` 会更新配置文件（与其它写点共用同一把
写锁与分层写路由），失败时状态零改动；写回边界只在 profile 目录与配置文件内。

---

## 5. 校验分级

| 级别 | 含义 | 退出码 |
| --- | --- | --- |
| error | 声明无法被满足：`profile.yaml` 解析失败、`profile.name` 缺失、技能 allowlist 引用不存在、`mcp.use_servers` 引用不存在、prompt 文件不可读、`prompts.mode` 非法 | 1 |
| warning | 被满足但冗余/可疑：deny/exclude 引用不存在、工具名未登记（可能是 MCP/动态工具）、prompt 文件为空、声明了 `prompts.mode` 但无 prompt 文件 | 0 |

`aicli profile validate` 复用 `internal/profile` 的校验器，不存在第二套校验口径。

---

## 6. token 估算

`profile show` 展示的 token 数来自**单一实现点** `backend/internal/profile/estimate.go`
（固定 bytes/4 向上取整）。它是明示的**估算**，不是精确分词；展示层必须带"估算"标注，
chat 启动摘要与前端只消费同一结果，不各自换算。

---

## 7. 参考样例与内置模板

- 内置模板：`backend/internal/profile/templates/<name>/`（随二进制嵌入，`aicli profile create` 使用）。
- 参考样例：`examples/profiles/coding/`（含 default + explore 两个 agent）。
- 一致性由测试保证：`backend/internal/profile/consistency_test.go` 校验"每个模板渲染后
  可解析/可校验/可被两个消费者读取"，以及"每个样例与当前 schema 一致"。
