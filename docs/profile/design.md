# Profile 设计与决策

> 状态：已实施（2026-09-25）。本文记录**为什么这样设计**与**硬边界**；结构描述见 [architecture.md](./architecture.md)。
> 任何与本文冲突的早期设计稿（`docs/multi-agents/profile/*`、`docs/plan/*` 的历史段落）以本文 + 代码为准。

---

## 1. 设计目标与非目标

**目标**

1. 一次会话的场景化裁剪：工具面、技能可见性、MCP、提示词、路径在一个声明文件里表达。
2. **只收窄、不改安全基线**：profile 能做的事永远小于等于无 profile 时的全量行为。
3. 零变化兼容：不声明 profile 的路径行为与引入本体系之前逐字节一致。
4. 单一实现点：解析 / 合并 / 校验 / 估算各只有一处实现，预览与运行同源。
5. 显式优先：用户显式选择（`--profile`、会话内切换、请求级 profile）永不被隐式来源覆盖。

**非目标（明确不做）**

- 不做“按仓库自动切换 profile”的隐式魔法（见 §9 后置项）。
- 不允许 profile 放宽权限、绕过 `foldertrust`、覆盖密钥/端点等安全域。
- 不把 profile 当成第二套 agent 路由（subagent 难度档位是另一套机制）。
- 不在设置页推断“当前会话”去替用户切换（apply 必须显式给 `session_id`）。

---

## 2. 关键不变量

| # | 不变量 | 落地方式 |
| --- | --- | --- |
| I1 | 只收窄 | deny/exclude 恒优先；安全域不可覆盖；`permission_mode` 未信任时只允许安全档位 |
| I2 | 零变化 | 未声明项 = 沿用原行为；无绑定文件 = 无 error、零变化；旧响应缺字段按缺失兼容 |
| I3 | 错误不猜 | 解析不了就报错：不回落、不静默降级、不默认挑第一个候选 |
| I4 | 显式优先 | §3 的解析顺序；项目绑定不参与隐式解析 |
| I5 | 单一实现点 | `internal/profile` + `applySessionProfileSwitch`；CLI/TUI/REST/前端都调用它们 |
| I6 | 发现即只读 | list/show/validate/preview/绑定卡片不写盘、不切换、不改默认 |
| I7 | 切换可解释 | Switch Report 如实回传生效差异、在途状态与 cache 代价；provider/model/permission 只报告 |
| I8 | 失败零改动 | 写回与切换失败时状态不变（原子写 + 校验前置） |

---

## 3. 选择与解析优先级（冻结）

从高到低：

| 序 | 来源 | 说明 |
| --- | --- | --- |
| 1 | runtime 请求级 `profile` | 单次请求带来的显式选择 |
| 2 | 会话已有显式绑定 | 会话元数据里的 `ProfileRef`（`--profile` 启动或会话内切换写入） |
| 3 | CLI `--profile <ref>` | 启动期显式选择 |
| 4 | `/profile use <ref>`（TUI）/ `/profile <ref>`（Web composer） | 会话内显式切换 |
| 5 | 项目绑定的显式应用 | 走 3/4 同一通路；绑定本身不自动生效 |
| 6 | `profiles.default_profile`（env `DEFAULT_PROFILE`） | 兜底默认 |
| — | 无 profile | 全量行为（零变化） |

补充：`--profile auto`（FR-11）在启动期按首轮提示词路由到具体 profile，路由结果即会话绑定；
纯交互式 `chat` / `agent stdio` 没有首轮提示词时**启动即显式报错**，不猜。

---

## 4. 错误语义

| 场景 | 行为 |
| --- | --- |
| 没有 `.aicli/profile` | 无绑定：`present=false`、无 error、零变化 |
| 绑定文件 YAML 非法 / 字段非法 | `present=true, valid=false`，明确报错（列表响应里带 `error`，不整页失败） |
| 绑定目标 profile 不存在 | 明确“目标缺失”，**不回退** user / config root / default |
| `profile.yaml` 解析失败 / `profile.name` 缺失 / 技能或 MCP 引用不存在 / prompt 不可读 / `prompts.mode` 非法 | error 级：`profile validate` 退出码 1；运行期同样报错 |
| 声明冗余/可疑（deny 引用不存在、工具名未登记、prompt 空文件等） | warning 级：退出码 0，如实提示 |
| agent 无法确定（多个候选且无 `default_agent` / 未显式 `--agent`） | `ErrAgentUnresolved`，不默认挑一个 |
| 写回时 `expected_mtime` 与磁盘不一致 | 409，不合并、不静默覆盖 |
| `apply` 未给 `session_id` / 会话不存在 | 400 / 404，服务端不推断“当前会话” |
| 列表的 `workspace` 参数本身非法（不存在/非目录） | 400（与“绑定文件内容有错”刻意区分：前者重试永远不会成功） |
| 未信任工作区的 project profile prompt | 不视为错误：正常返回 + `prompt_suppressed` + 报告提示 |

---

## 5. D29：按资源类型分级门控（信任）

未信任工作区加载 project profile 时：

| 资源类型 | 处理 | 理由 |
| --- | --- | --- |
| tools / skills / MCP 的收窄声明 | 正常生效 | 收窄只减少能力，不引入外部内容 |
| `permission_mode` | 只允许安全档位 | 权限档位属于安全基线，不能由未信任仓库放宽 |
| prompts / agent prompts | **不应用**，并在状态、启动摘要、Switch Report、前端卡片显式提示 | 提示词是无信任边界下的指令注入面 |
| 安全域（密钥、端点、foldertrust、`BlockUntrustedMCP`） | 不可覆盖/关闭 | 安全基线不参与 profile 合并 |

同一口径贯穿：发现（`prompt_suppressed`）、应用到会话（report warning）、列表/卡片展示必须来自
`EvaluateProjectPromptGate`，不允许第二套判断。

---

## 6. 项目绑定契约（FR-14 第一阶段）

**位置**：`<workspace>/.aicli/profile`，与 `<workspace>/.aicli/profiles/<ref>/` 并列。
**格式**：pointer-only，唯一允许字段 `profile: <单段安全 ref>`。

拒绝清单（任一命中即 `valid=false`）：

- 空 ref；`.`、`..`；
- 绝对路径、Windows 驱动器路径、UNC 路径、含 `/` 或 `\` 的值；
- 嵌入完整 profile（`profile: { ... }`）；
- 未知顶层字段、重复字段、非 mapping YAML、空文档；
- 指向 workspace 之外的自定义 root（本阶段绑定只能解析为该 workspace 的 project 层 profile）。

**为什么 pointer-only**：绑定文件是仓库内容，可能来自任意 clone。把“选择哪个 profile”与“profile 内容”
拆开后，仓库只能引用**本工作区已存在**的 profile，无法把配置内容塞进指针文件；配合“只读发现 + 显式应用”，
未信任仓库无法凭一个文件改变会话行为。

**错误语义与可见性**：见 §4；列表端点在给出 `workspace` 时返回 `project_binding`（`present/valid/ref/source/
layer/workspace_path/path/profile_root/error/prompt_suppressed/prompt_suppression_reason`），命中的条目带
`is_bound`；**`is_default` 不受影响**。

**明确后置**（本阶段不实现）：启动期自动激活绑定、按会话/请求自动解析多 workspace 的绑定、未信任仓库
通过配置打开“自动使用项目绑定”。

---

## 7. 写回边界与并发

- 写回点只有两类：profile 目录内文件、以及配置文件的 `profiles` 段（`--use` / `--set-default` / 设为默认 / 删除清空 default）。
- 共用同一把写锁与分层写路由（D5/D15）；**失败时状态零改动**。
- REST 写回支持 `expected_mtime`：与客户端读到的 mtime 不一致即 409（R24/V25），不做合并。
- 导入（zip/目录）先 `validate`，不覆盖同名、绝不自动激活；导出只读。
- 配置文件被分层路由到归属层时，回执会提示“写入未落在预期路径，用 `aicli config path` 确认”。

---

## 8. 决策登记

| 编号 | 决策 | 位置 |
| --- | --- | --- |
| D5 | `profiles` 配置段的结构化写回（零值字段=不改动对应键） | `internal/agentconfig/profiles_persistence.go` |
| D8 / FR-9 | spawn 子会话在装配点快照父会话 profile 绑定（父未绑定则 no-op） | `internal/sessionmeta/sessionmeta.go` |
| D15 | 写回边界：只在 profile 目录与配置文件内；共用写锁与分层写路由 | CLI `profile create --help` |
| D23 | 切换的结构化 Switch Report（差异、生效时机、cache 提示、warnings） | `session_profile_switch.go` |
| D24 / D36 | 从会话固化差分（save-as）：差分与落盘在后端；prompt/权限模式不在 `profile.yaml` 内，报告逐项明示 | `saveas.go`、`profiles_saveas_handlers.go` |
| D26 | `apply` 只影响显式 `session_id` 的会话（A12：default 与别的会话不变） | `profiles_write_handlers.go` |
| D28 / G5 | 导入先 validate、不覆盖同名、绝不自动激活；导出只读 | `profiles_transfer_handlers.go` |
| D29 | 按资源类型分级门控（§5） | `prompts_gate.go`、`internal/foldertrust` |
| D30 | provider / model / permission_mode 差异只报告，不隐式应用 | `session_profile_switch.go` |
| FR-11 | `--profile auto` 首轮提示词路由；无提示词的纯交互启动显式报错 | `autoroute.go` |
| FR-14 | 项目绑定：只读发现 + 显式应用（§6） | `binding.go`、`profiles_store.go` |
| A3 | 在途 turn 的冻结前缀由存储层与活体 agent 配置保护；切换立即落地会话状态、下一轮生效 | `session_profile_switch.go` |
| R20 | `/profile` 仅在宿主确认后端能力（`session_switch` 能力广告）后注册 | `composer-builtin-commands.ts` |

---

## 9. 后置项与兼容性承诺

**后置（未实施，不得按“已支持”宣传）**

1. 项目绑定的自动默认激活（启动即用绑定 profile）。
2. 多 workspace 自动解析（服务端自行挑选工作区）。
3. 未信任仓库通过配置“默认开启”项目绑定。
4. 设置页直接 apply 绑定（需要会话上下文；当前卡片只给会话内命令指引）。

**兼容性承诺**

- 无 `profile` 相关声明时：工具面、提示词、路径与引入前一致。
- 旧前端对新增字段（如 `project_binding`）缺失时应正常渲染（不显示绑定卡片、不发额外请求）。
- 新字段一律可选；不得把绑定 ref 当作 `default` 展示或参与默认解析。
