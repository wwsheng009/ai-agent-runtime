# Profile 架构与运行时

> 状态：已实施（2026-09-25）。本文描述当前 HEAD 的实际结构；设计与取舍见 [design.md](./design.md)，用法见 [usage.md](./usage.md)。
> 单一实现点：`backend/internal/profile/`。任何“第二种解析/合并/校验口径”都不是本架构的一部分。

---

## 1. 定位与依赖方向

profile 是**会话装配期的输入**：它在一次会话（和每个 turn 的组装点）决定工具面、技能可见性、MCP 服务器、
提示词组合与路径。依赖方向是单向的——上层读 profile，profile 不反向依赖会话/运行时。

```
                    ┌─────────────────────────────────────────────┐
  声明（磁盘）      │  profile.yaml + agents/<id>/…               │
                    │  config profiles.*   .aicli/profile(指针)   │
                    └───────────────┬─────────────────────────────┘
                                    │ 只读加载/校验/合并
                    ┌───────────────▼─────────────────────────────┐
  解析（单一实现）  │  internal/profile：LoadProfile / Resolve     │
                    │  layer 发现 / merge / prompts_gate / estimate │
                    └───────┬───────────────┬───────────────┬──────┘
                            │               │               │
        ┌───────────────────▼──┐   ┌────────▼────────┐  ┌───▼─────────────────┐
  消费  │ cmd/aicli（CLI/TUI） │   │ api/skills      │  │ frontend（设置页/   │
        │ 启动装配 + /profile  │   │ REST + 会话切换 │  │ composer，只读展示）│
        └──────────────────────┘   └─────────────────┘  └─────────────────────┘
```

信任结论由 `internal/foldertrust` 提供（工作区是否可信），profile 层只消费它，不自己判定。

---

## 2. 代码地图

### 2.1 `backend/internal/profile/`（唯一实现点）

| 文件 | 职责 |
| --- | --- |
| `spec.go` / `loader.go` | `profile.yaml`、`agents/<id>/agent.yaml`、workspace 覆盖的结构与可选加载 |
| `paths.go` / `filesystem.go` | profile 目录内的路径约定与存在性工具 |
| `layer.go` | 层根：`LayerRoot("user"\|"project")`、`LayerRootForWorkspace(layer, workspace)`、层枚举 `LayerProfiles*` |
| `registry.go` / `registry_config.go` | config `profiles.items` / `profiles.root` 注册项发现 |
| `merge.go` / `resolved.go` / `resolver.go` | 合并与解析：产出 `ResolvedAgent`（工具面/skills/mcp/prompts/provider/model/路径） |
| `overrides.go` | 工作区/agent 级覆盖（`WorkspaceSpec`、内联 `agents.<id>`） |
| `validate.go` | 校验器与错误（`ErrAgentUnresolved` 等）、agent id 解析 `resolveAgentID` |
| `prompts_gate.go` | D29 提示词门控（`ApplyProjectPromptGate` / `EvaluateProjectPromptGate`） |
| `binding.go` | 项目绑定只读发现（FR-14 第一阶段）：`ProjectProfileBinding` / `LoadProjectProfileBinding` |
| `autoroute.go` | `--profile auto` 的首轮提示词路由（FR-11，`RouteProfileForPrompt`） |
| `saveas.go` | 从会话固化差分（D24/D36）的声明构造 |
| `transfer.go` | 导入/导出（zip）与不覆盖同名的约束 |
| `estimate.go` | token 估算的单一实现点（固定 bytes/4 向上取整） |
| `reference_validation.go` / `rewrite.go` / `name.go` | 引用检查、重命名/移动、名称规则 |
| `templates.go` + `templates/{coding,docs,minimal,review}/` | 内置模板（随二进制嵌入；`consistency_test.go` 保证模板可解析/双消费者可读、`examples/profiles/*` 与 schema 一致） |

### 2.2 CLI / TUI

| 位置 | 内容 |
| --- | --- |
| `cmd/aicli/commands/profile.go` | `aicli profile` 命令组入口 |
| `profile_list.go` / `profile_show.go` / `profile_validate.go` / `profile_create.go` / `profile_export.go` / `profile_import.go` | 各子命令 |
| `chat_profile_command.go` | TUI `/profile` 命令面（分派子命令；不做第二套切换逻辑） |
| `chat_profile_switch.go` / `chat_profile_summary.go` / `chat_profile_overlay.go` | 切换执行核心与报告、启动摘要、覆盖层 |
| `chat_profile_lifecycle*.go` | `create/duplicate/save-as/rename/move/delete/export/import` 的会话内入口 |
| `chat_profile_resume.go` / `chat_profile_editor.go` / `chat_profile_completion.go` | resume 恢复绑定、`$EDITOR`、补全 |

### 2.3 REST / 前端

| 位置 | 内容 |
| --- | --- |
| `internal/api/skills/profiles_handlers.go` | 路由注册与端点族注释（**端点表的事实来源**） |
| `profiles_store.go` | 列表/解析视图数据 + `project_binding` 填充 + `is_bound` 标注 |
| `profiles_write_handlers.go` | 写回（PUT，含 `expected_mtime` 冲突检测）、`apply`、`default` |
| `profiles_lifecycle_handlers.go` / `profiles_transfer_handlers.go` / `profiles_saveas_handlers.go` | 重命名/移动/删除、导入导出、从会话固化 |
| `session_profile_switch.go` | **唯一会话切换执行核心** `applySessionProfileSwitch` 与 Switch Report |
| `profile_support.go` / `profiles_view_groups.go` | workspace/binding 一致性适配、视图分组 |
| `frontend/src/types/runtime/profiles.ts`、`api/runtime/profiles/*` | 类型与 API 归一化（旧响应缺字段按 `null` 兼容） |
| `frontend/.../sections/modes/profiles*.tsx`、`profiles/*card*.tsx` | 设置页列表/编辑器/生命周期对话框/项目绑定卡片/信任提示 |
| `frontend/src/hooks/workspace/composer/use-composer-profile-command.ts` | composer `/profile` 与 `/profile save-as` 执行分支 |

---

## 3. 目录与层模型

```
<home>/.aicli/profiles/<name>/          ← user 层
<workspace>/.aicli/profiles/<name>/     ← project 层（服务端按会话 workspace 计算）
<profiles.root>/<name>/                 ← default root
<config profiles.items.<name>.root>     ← 注册项（任意路径）
```

发现来源与同名优先级（`profile list` 会标注 `source`）：

| 序 | 来源 | 说明 |
| --- | --- | --- |
| 1 | config 注册项 `profiles.items` | 显式注册，优先级最高 |
| 2 | `profiles.root` 下的目录 | 含 `profile.yaml` 的子目录 |
| 3 | project 层 | `<workspace>/.aicli/profiles/<name>` |
| 4 | user 层 | `<home>/.aicli/profiles/<name>` |
| + | 显式路径 | 命令参数里直接给出的目录 |

**服务端必须用 workspace 计算 project 层**：`LayerRootForWorkspace("project", workspace)`；不得使用
服务进程 cwd 的无参 `LayerRoot("project")`，否则多工作区会互相串 profile。

---

## 4. 解析管线

```
profile.yaml ──LoadProfile──▶ ProfileSpec
                                 │
agents/<id>/agent.yaml ─────────┤（可选；portable 角色定义）
profile.yaml 内联 agents.<id> ──┤（可选；工具/provider/model 覆盖）
agents/<id>/workspace/workspace.yaml ──▶ WorkspaceSpec（可选）
                                 ▼
                    Resolve(ResolveOptions{Root, Agent})
                                 ▼
                        ResolvedAgent（生效面）
   tools / skills / mcp / prompts / provider / model / paths / estimate
```

关键点：

1. **agent id 解析**（`validate.go` 的 `resolveAgentID`）：① 显式 `--agent <id>` → ② `profile.default_agent`
   → ③ 若 `agents:` 键与 `agents/` 目录名合计只有**一个**候选则用它 → ④ 否则报错
   `ErrAgentUnresolved: explicit agent or profile.default_agent is required`（不猜、不默认挑一个）。
2. **合并只收窄**：工具/技能/MCP 的 allow/deny、exclude 语义里 deny/exclude 恒优先；不声明 = 沿用全量行为。
   provider / model 走“就近覆盖”链（workspace spec → agent.yaml → 内联 agents.<id> → profile 级默认）。
3. **路径解析**（`paths.go`）：runtime config / MCP 配置 / 技能目录按 profile → agent → workspace 顺序落到
   实际存在的文件，缺失即回落到既有全局来源。
4. **提示词组合**：`prompts.mode: replace|append`（非法取值报错，不回退）；agent 角色正文来自
   `agents/<id>/prompts/role.md`。未信任工作区的 project profile 提示词由 `prompts_gate.go` 扣留（见 §7）。
5. **单一校验器**：`ValidateProfileSpec` 同时服务 `aicli profile validate`、REST `/validate` 与写回前的检查；
   `profile show` 与真实会话消费同一解析结果，不允许“预览一套、运行一套”。

---

## 5. 会话生命周期：装配、切换、失效

### 5.1 装配

- 启动期：`--profile <ref>` / `profiles.default_profile` / `--profile auto` 解析出具体 profile →
  `Resolve` → 用生效面装配 agent（工具面、技能、MCP、提示词、路径）。
- 会话绑定：解析结果写入会话身份（`sessionmeta.ProfileRef` 及展示用字段），启动摘要与 `/profile status` 读同一来源。
- 子会话：spawn 时按 FR-9/D8 在装配点**快照**父会话的 profile 绑定（`sessionmeta.CopyProfileBinding`），
  父会话未绑定时是 no-op（零变化）。

### 5.2 会话内切换（唯一执行核心）

所有会话内切换（TUI `/profile use`、Web composer `/profile <ref>`、REST `POST /profiles/{ref}/apply`）
都走 `applySessionProfileSwitch`：

```
用户显式切换
   │  1. 解析目标 ref（失败即报错，状态零改动）
   ▼
applySessionProfileSwitch
   ├─ 写会话身份：sessionmeta.ProfileRef = <ref>
   ├─ 删除 prompt 冻结锚点（下次 compose 重新组合 head）
   ├─ 驱逐空闲 actor ⇒ 下一轮按新 profile 重建 agent/工具面
   └─ 产出 Switch Report（结构化回执）
```

语义要点：

- **下一轮生效**：在途 turn 的冻结前缀由存储层与活体 agent 配置保护（`in_flight_turn`），切换本身立即落地会话状态。
- **只报告、不隐式应用**（D30）：provider / model / permission_mode 差异以 `warnings` 呈现，报告里对应
  `changed.*` 恒为 `false`——切换不会偷偷换模型或放宽权限。
- **prompt cache 代价显式化**：提示词前缀变化会使 provider prompt cache 重建，`cache_notice` 如实说明。
- **失败零改动**：解析不了就报错；不猜、不静默降级到别的 profile。

### 5.3 Switch Report 字段（`session_profile_switch.go`）

| 字段 | 含义 |
| --- | --- |
| `from` / `to` | 切换前后的 ref |
| `changed` | 生效面差异：tools/skills/mcp 的 added/removed + prompt/provider/model/permission_mode 布尔（后三者恒 false，见 D30） |
| `effective_at` | 生效时机（通常 `next_turn`） |
| `cache_notice` | 提示词前缀变化带来的缓存重建说明 |
| `warnings` | 差异与风险提示（含 D29 提示词扣留、权限档位变化等，只报告） |
| `in_flight_turn` | 切换时是否存在在途 turn |
| `anchor_cleared` | prompt 冻结锚点是否被删除 |

### 5.4 发现/编辑不参与解析

`profile list/show/validate/preview`、设置页列表与项目绑定卡片都是**只读**：不写 `profiles.default_profile`、
不切换任何会话、不参与 §5.2 之外的隐式解析。项目绑定的应用也必须由用户在会话内显式触发（见 §6）。

---

## 6. 项目绑定（FR-14 第一阶段）

`<workspace>/.aicli/profile` 是一个 **pointer-only** 文件：

```yaml
profile: coding
```

- 只读发现：`LoadProjectProfileBinding(workspace)` 返回 `Present/Valid/Ref/Root/Layer/Path/Error` 等 metadata；
  目标必须是该 workspace 的 project 层 profile（`LayerRootForWorkspace` 唯一计算）。
- 不自动激活：绑定**不**改变 `profiles.default_profile`，也**不**在 `profileRef == ""` 时被偷偷插入解析链；
  应用方式与普通 profile 相同（会话内 `/profile use <ref>` / composer `/profile <ref>` / REST apply）。
- 错误语义：文件不存在 = 无绑定（零变化）；文件非法或目标缺失 = `present=true, valid=false` 且**明确报错**，
  不回退到 user/config/default；绑定错误留在列表响应的 `project_binding.error`，不把整页列表打成空页。
- 可见性：REST `GET /api/runtime/profiles?workspace=…` 返回 `project_binding`，命中的列表项带 `is_bound`；
  前端设置页显示只读卡片（ref/工作区/来源/指针文件/目标目录/状态 + D29 扣留提示）。

后置项（**未实施**）：自动默认激活、多工作区自动解析、未信任仓库通过配置打开自动 profile。
见 [design.md](./design.md) §6。

---

## 7. 信任门控（D29）

`internal/foldertrust` 把仓库内 `.aicli/profiles` 识别为 `ConfigKindProfiles`；解析 project profile 时，
提示词走 `ApplyProjectPromptGate`，扣留结论与列表/报告的 `prompt_suppressed` 同源。

| 资源类型 | 未信任工作区时的行为 |
| --- | --- |
| tools / skills / MCP 的收窄声明 | 正常生效（收窄不构成信任风险） |
| `permission_mode` | 只允许安全档位；不得放宽安全基线 |
| prompts / agent prompts | **不应用**，并在状态、启动摘要、Switch Report 与前端卡片提示 |
| 安全域（密钥、端点、foldertrust、`BlockUntrustedMCP`） | 不可被 profile 覆盖或关闭 |

信任动作在设置页确认（写本机信任清单），撤销走 CLI `/trust`；信任后已在运行的会话需 `/profile reload`
才能完整应用。

---

## 8. 多 workspace 隔离

- 会话侧的 workspace 取自会话元数据（含 worktree 路径），项目层根一律
  `LayerRootForWorkspace("project", workspace)`。
- 列表/发现端点要求显式 `workspace` 参数：参数本身非法（不存在/不是目录）返回 400；绑定文件内容问题仍是
  200 + `project_binding.error`（前者重试永远不会成功，后者是工作区里的事实，应展示）。
- 无 `workspace` 参数时字段省略，旧调用零变化（继续走无参层发现）。

---

## 9. REST 端点（事实来源：`profiles_handlers.go` 顶部注释）

| 方法 | 路径 | 语义 |
| --- | --- | --- |
| GET | `/api/runtime/profiles` | 列表（多来源 + 默认标注 + 解析状态；带 `workspace` 时含 `project_binding`） |
| GET | `/api/runtime/profiles/{ref}` | 解析后视图（工具面/skills/mcp/prompts/agents/overrides/估算） |
| PUT | `/api/runtime/profiles/{ref}` | 写回 `profile.yaml`（校验 + 原子写 + `expected_mtime` 冲突 409） |
| POST | `/api/runtime/profiles/{ref}/validate` | 校验（与 CLI `profile validate` 同一实现） |
| POST | `/api/runtime/profiles/{ref}/preview` | 只读预览（不写盘） |
| POST | `/api/runtime/profiles/{ref}/default` | 设为默认（新会话；写 `profiles.default_profile`） |
| POST | `/api/runtime/profiles/{ref}/apply` | 应用到**显式 `session_id`** 的会话（未知会话 404；服务端不推断“当前会话”） |
| POST | `/api/runtime/profiles` | 创建（模板 / 复制 / 从会话固化三模式合一） |
| POST | `/api/runtime/profiles/import` | 导入 zip 包（先 validate、绝不自动激活、不覆盖同名） |
| POST | `/api/runtime/profiles/{ref}/export` | 导出 zip（只读，不写盘） |
| POST | `/api/runtime/profiles/{ref}/duplicate` | 复制 |
| POST | `/api/runtime/profiles/{ref}/rename` / `/move` | 重命名（同层）/ 层级移动（user↔project） |
| DELETE | `/api/runtime/profiles/{ref}` | 删除（引用检查 + `?force=true`） |
| GET | `/api/runtime/profiles/{ref}/references` | 只读引用清单 |

会话内切换另走 `POST /api/runtime/sessions/{id}/runtime/commands`（`type=set_profile`），与 CLI/TUI
共用 `applySessionProfileSwitch`。

---

## 10. 可观测性

| 面 | 内容 |
| --- | --- |
| 启动摘要 | 当前 profile、来源、生效面摘要、被扣留的提示词提示 |
| `/profile status` | ref、来源、路由归因（`auto → <profile>`）、生效摘要 |
| Switch Report | §5.3；CLI/TUI/Web 回执同源 |
| 用量与元数据 | 会话元数据记录 profile 绑定（`ProfileRef` 及展示字段），spawn 快照继承 |
| 前端 | 设置页列表/卡片自带 `source/layer/valid/error/prompt_suppressed`；`/web/api` 调试端点可回读会话状态 |

---

## 11. 测试与验收地图

| 范围 | 位置 |
| --- | --- |
| 解析/合并/校验/层发现/绑定 | `backend/internal/profile/*_test.go`（含 `binding_test.go`、层发现 workspace 变体） |
| 信任门控 | `backend/internal/foldertrust/*_test.go`、`prompts_gate` 相关测试 |
| CLI/TUI 命令面与切换 | `backend/cmd/aicli/commands/`（`TestProfile*`、`TestChatWebInvoke*`、profile 生命周期测试） |
| REST + 多 workspace | `backend/internal/api/skills/profiles_*_test.go`、`session_profile_inheritance_test.go` |
| 模板/样例一致性 | `backend/internal/profile/consistency_test.go`（模板渲染后可解析 + 样例符合当前 schema） |
| 前端 | `frontend/src/**/profiles*.test.tsx`、`composer-profile-options.test.ts`、`use-composer-command-executor.profile*.test.tsx` |
| 端到端（独立进程） | `artifacts/profile-io-e2e/`（harness + report）、`docs/e2e/debug-guide.md` |
