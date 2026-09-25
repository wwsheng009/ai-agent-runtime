# Profile 配置参考

> 状态：已实施（2026-09-25）。语义与边界见 [design.md](./design.md)；命令用法见 [usage.md](./usage.md)。
> 所有路径相对 profile 根目录（即 `<profile-root>/`）；所有键名以本文为准。

配置分布在三处：

| # | 位置 | 作用 | 谁写 |
| --- | --- | --- | --- |
| 1 | 全局/项目配置文件 `profiles` 段 | 拓扑：默认 profile、注册项、default root、auto 路由 | 人 / `profile create --use` / `--set-default` / `profile set-default` |
| 2 | `<profile-root>/profile.yaml` + `agents/<id>/…` | 场景声明：工具面、技能、MCP、prompt、agent | 人 / CLI / TUI / Web 编辑器 |
| 3 | `<workspace>/.aicli/profile` | 项目绑定指针（只读发现） | 人（随仓库提交） |

配置文件查找顺序（未显式 `-c/--config` 时，**按高→低优先级取第一个存在的文件**）：
`./.aicli/config.yaml`（项目级）→ `$HOME/.aicli/config.yaml`（用户级）→ `./aicli.yaml` → `./config.yaml` → `./configs/config.yaml`。
默认是**单文件语义**：只有命中的那一个文件参与，低层不补键；`AICLI_CONFIG_MERGE=on` 时才按层合并（低→高，
高层只覆盖自己显式写的键，显式 `null` 删除下层键）。本文件讲的 `profiles` 段读取同样受此约定约束。

---

## 1. `profiles` 配置段

```yaml
profiles:
  root: ~/.aicli/profiles              # default root；env: PROFILES_ROOT
  default_profile: coding              # 默认 profile；env: DEFAULT_PROFILE（新会话生效）
  items:                               # 注册项：名字 → 目录（优先级最高的发现来源）
    coding:
      root: ./examples/profiles/coding
  auto:                                # 可选；仅 --profile auto 使用（FR-11）
    fallback: minimal                  # 规则未命中时的兜底；空 = 内置 executor
    rules:                             # 非空即整体替换内置映射；按声明顺序短路，首个命中胜出
      - profile: reviewer
        keywords: [review, audit]
```

| 键 | 类型 | 说明 |
| --- | --- | --- |
| `profiles.root` | string | default root：`<root>/<name>/` 下含 `profile.yaml` 即被 `profile list` 发现 |
| `profiles.default_profile` | string | 无 `--profile`、无会话绑定时的默认选择；等价 env `DEFAULT_PROFILE` |
| `profiles.items.<name>.root` | string | 注册项（优先级最高）；`create --use` 会写这里 |
| `profiles.auto.fallback` | string | auto 路由未命中的兜底 profile |
| `profiles.auto.rules[]` | `{profile, keywords[]}` | 首轮提示词命中 keywords 即路由到 profile；非空即整体替换内置映射 |

内置 auto 启发式（未配置 `profiles.auto` 时）：`write/implement/fix/add/edit/refactor/patch/update/change`
→ `executor`；`plan/design/compare/proposal/approach` → `planner`；`search/inspect/understand/locate/find/investigate`
→ `explore`；未命中 → `executor`。

环境变量：`PROFILES_ROOT`、`DEFAULT_PROFILE`（生效时 `profile list` 会在输出中标注）。

---

## 2. `profile.yaml`（完整示例 + 语义）

```yaml
profile:
  name: coding              # 必填：profile list/show/validate 的标识
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
  mode: replace             # replace（默认）| append；非法取值报错，不回退

agents:                     # 按 agent 的内联覆盖（provider / model / tools）
  explore:
    tools:
      allowlist: [view, grep, glob, ls, shell]
      denylist: [write, edit, apply_patch, append_write, multiedit]
```

语义要点：

- **只收窄**：denylist / exclude 优先；安全域（密钥、端点、foldertrust、`BlockUntrustedMCP`）不可被覆盖或关闭。
- **零变化**：不声明某项 = 沿用原有全量行为。
- **错误不猜**：`prompts.mode` 等取值非法时**报错**，不降级为别的语义。
- `agents.<id>` 的 id 必须与 `agents/<id>/` 目录同名才指向同一个 agent（见 §3）。

---

## 3. `agents/<id>/` 目录

```
agents/<id>/
├── agent.yaml                  # 可选：portable agent definition（角色/权限/沙箱）
├── prompts/role.md             # 可选：角色提示词（进入 prompt 组合）
├── tools/policy.yaml           # 可选：工具策略（ToolPolicySpec 文件形式）
├── skills/                     # 可选：profile 自带技能
└── workspace/workspace.yaml    # 可选：workspace 级 provider/model/tools 覆盖
```

### 3.1 `<id>` 是什么

`agents/<id>/` 的目录名，即该 profile 内的 agent 句柄（同一 profile 内唯一，如 `default`、`explore`）。
它决定装配哪套 agent 定义。选择顺序：

1. 显式 `--agent <id>`（如 `aicli chat --profile coding --agent explore`、`aicli profile show coding --agent explore`）；
2. `profile.yaml` 的 `profile.default_agent`；
3. 若 `agents:` 键与 `agents/` 目录名合计只有**一个**候选 → 用它；
4. 否则报错 `ErrAgentUnresolved: explicit agent or profile.default_agent is required`（不默认挑一个）。

`default_agent` 必须是已声明/已存在的 id，否则解析期报错。会话里可见于启动摘要与 `/profile status`。

### 3.2 `agent.yaml` 合法字段与硬约束

合法且生效：`name`、`description`、`model`、`provider`、`permission_mode`、`completion_requirement`、
`sandbox`（`read-only` / `workspace` / `strict` / `off`）、`prompt_mode`、`system_prompt`，加 `prompts/role.md`。

**不要写 `tools` / `disallowed_tools`**：`agent.yaml` 同时被两个消费者读取，形状冲突且无法共存——

| 消费者 | 读取目的 | `tools` 形状 |
| --- | --- | --- |
| profile 解析器（`profile.AgentSpec`） | provider/model/工具策略合并 | 映射（`allowlist`/`denylist`/`read_only`/`sandbox`） |
| portable agentdef（`agentdef.AdaptProfileAgent`） | 角色定义（body/权限/沙箱） | 列表（`[view, grep, ...]`） |

工具策略写到 ① `profile.yaml` 的 `agents.<id>.tools`（推荐），或 ② `agents/<id>/tools/policy.yaml`。
`aicli profile validate` 命中冲突时直接给出这条提示。

---

## 4. 项目绑定文件 `<workspace>/.aicli/profile`

```yaml
profile: coding        # 唯一允许的字段；非空单段安全 ref
```

允许：单段名字（`[A-Za-z0-9._-]` 形态的目录名/注册名）。
拒绝：空 ref；`.`、`..`；绝对路径、Windows 驱动器路径、UNC；含 `/` 或 `\`；嵌入对象（`profile: {…}`）；
未知顶层字段；重复字段；非 mapping YAML；空文档；指向 workspace 之外的自定义 root。

语义：**只读发现**，不自动激活、不改 `profiles.default_profile`；目标必须存在且是本工作区的 project 层
profile（`<workspace>/.aicli/profiles/<ref>/profile.yaml`）；目标缺失**不回退**其他来源；文件不存在 = 无绑定（零变化）。

关联：仓库里另有 `.aicli/profiles/<name>/`（项目层 profile 目录）。`foldertrust` 把 `.aicli/profiles`
识别为 `ConfigKindProfiles`，未信任工作区时 project profile 的提示词按 D29 扣留。

---

## 5. 层、写回与并发

| 层 | 位置 | 用途 |
| --- | --- | --- |
| user | `<home>/.aicli/profiles/<name>/` | 个人常用 profile |
| project | `<workspace>/.aicli/profiles/<name>/` | 随仓库走；服务端按会话 workspace 计算 |
| 注册项 / default root | `profiles.items.<name>.root` / `profiles.root/<name>` | 任意路径 / 统一收拢 |

- 发现同名优先级：config 注册项 > `profiles.root` > project 层 > user 层。
- 写回点只有 profile 目录内文件与配置文件的 `profiles` 段；共用写锁与分层写路由；失败时状态零改动。
- REST 写回带 `expected_mtime`：不一致返回 409，不合并、不静默覆盖。
- 导入（zip/目录）：先 validate、不覆盖同名、绝不自动激活；导出只读且有大小上限（256 文件 / 单文件 2MiB / 总 8MiB）。

---

## 6. 校验分级与估算

| 级别 | 含义 | 退出码 |
| --- | --- | --- |
| error | 声明无法被满足：`profile.yaml` 解析失败、`profile.name` 缺失、技能 allowlist 引用不存在、`mcp.use_servers` 引用不存在、prompt 文件不可读、`prompts.mode` 非法 | 1 |
| warning | 被满足但冗余/可疑：deny/exclude 引用不存在、工具名未登记（可能是 MCP/动态工具）、prompt 文件为空、声明了 `prompts.mode` 但无 prompt 文件 | 0 |

`aicli profile validate`、REST `/validate`、写回前检查复用同一校验器，不存在第二套口径。

token 估算来自单一实现点 `backend/internal/profile/estimate.go`（固定 bytes/4 向上取整），是**估算**而非精确
分词；展示层必须带“估算”标注，启动摘要与前端只消费同一结果。

---

## 7. 参考样例与内置模板

- 内置模板（随二进制嵌入，`aicli profile create` 使用）：`backend/internal/profile/templates/{coding,docs,minimal,review}/`。
- 参考样例：`examples/profiles/coding/`（含 `default` 与 `explore` 两个 agent）。
- 一致性由测试保证：`backend/internal/profile/consistency_test.go` 校验“每个模板渲染后 = 对应样例目录”，
  样例与模板漂移会直接失败。
