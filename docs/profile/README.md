# Profile 文档集（架构 / 设计 / 使用 / 配置）

> 状态：已实施（2026-09-25 建档；覆盖 profile 体系 + 项目绑定 FR-14 第一阶段）。
> 代码位置：`backend/internal/profile/`、`backend/cmd/aicli/commands/profile*.go`、`backend/cmd/aicli/commands/chat_profile*.go`、`backend/internal/api/skills/profiles_*.go`、`frontend/src/**/profile*`。
> 权威性：本目录描述**当前 HEAD 的实际行为**；与既有设计稿冲突时以本目录 + 代码为准。

本目录是 profile（运行 profile）的完整文档集：一个 profile 是一个目录（`profile.yaml` + `agents/<id>/`），
决定一次会话的工具面、技能可见性、MCP 服务器、提示词组合与路径。

---

## 1. 文档地图

| 文档 | 回答什么问题 | 主要读者 |
| --- | --- | --- |
| [architecture.md](./architecture.md) | 它由哪些模块组成、一次 turn 里 profile 如何被解析/生效、切换与缓存如何失效、多 workspace 如何隔离 | 需要改代码或排障的人 |
| [design.md](./design.md) | 为什么这样设计：优先级冻结、错误语义、信任门控分级、绑定契约、写回边界、后置项与非目标 | 评审者、安全与协议相关改动 |
| [usage.md](./usage.md) | 怎么用：CLI / TUI / Web 三条入口的完整命令、典型流程、命令语法差异、排错表 | 日常使用者 |
| [configuration.md](./configuration.md) | 怎么写配置：`profiles` 配置段、`profile.yaml` 全字段、`agents/<id>/` 目录、`.aicli/profile` 绑定文件、校验与估算 | 配置作者 |

快速上手（5 分钟）：

```powershell
aicli profile create coding --template coding --dry-run   # 先看会生成什么
aicli profile create coding --template coding --use       # 落盘 + 注册到 config profiles.items
aicli profile validate coding                             # error 级问题退出码 1
aicli profile show coding --agent explore                 # 只读预览最终生效面
aicli chat --profile coding                               # 带 profile 启动（最高优先级入口之一）
```

---

## 2. 术语

| 术语 | 含义 |
| --- | --- |
| profile / 运行 profile | 一个目录：`profile.yaml`（必需）+ `agents/<id>/`（可选）。**不是** subagent 的“难度档位” |
| ref / 引用 | profile 的标识：注册名、`profiles.root` 下的目录名、或 profile 目录路径（三种写法） |
| 层 / layer | profile 的存放位置：`user`（`<home>/.aicli/profiles`）、`project`（`<workspace>/.aicli/profiles`） |
| agent id | `agents/<id>/` 的目录名，也是 `profile.yaml` 中 `agents.<id>` 的键；决定装配哪套 agent 定义 |
| 生效面 | 解析后的结果：工具面 / skills / MCP / prompts / provider / model / 路径 |
| 会话绑定 | 会话元数据里记录当前 profile 引用（`sessionmeta.ProfileRef`）；切换写入它 |
| 项目绑定 | 工作区里的指针文件 `<workspace>/.aicli/profile`（只读发现，不自动激活） |
| D29 门控 | 按资源类型分级的信任门控：未信任工作区时，project profile 的 prompt 不注入、只收窄声明仍生效 |

---

## 3. 三层事实来源（避免认知分裂）

1. **代码**：`backend/internal/profile/` 是唯一的解析/校验/合并实现点；不存在第二套口径。
2. **本目录**：架构与契约的叙述版；含“已实施 / 后置”的明确标注。
3. **历史设计稿**（仅作背景，部分内容已过时）：
   - [../multi-agents/profile/profile_system_implementation.md](../multi-agents/profile/profile_system_implementation.md)（早期实施方案，含 Proposed 段落）
   - [../multi-agents/profile/profile_workspace_agent_design.md](../multi-agents/profile/profile_workspace_agent_design.md)（早期层级模型）
   - [../multi-agents/profile/aicli_profile_loading_flow.md](../multi-agents/profile/aicli_profile_loading_flow.md)（早期加载流程）
   - [../aicli/profiles.md](../aicli/profiles.md)（面向用户的快速指南，字段与命令的最短路径）
   - [../plan/profile-scenario-implementation-plan-20260924.md](../plan/profile-scenario-implementation-plan-20260924.md)（FR/批次/验收口径）
   - [../plan/profile-scenario-context-pruning-plan-20260924.md](../plan/profile-scenario-context-pruning-plan-20260924.md)（设计基线，含 D29 分级门控表）
   - [../plan/fr14-profile-binding-implementation-plan-20260925.md](../plan/fr14-profile-binding-implementation-plan-20260925.md)（项目绑定第一阶段）

---

## 4. 维护约定

- 行为改了，先改代码与测试，再同步本目录与 `docs/aicli/profiles.md`；不得只改叙述。
- 任何“未实施 / 后置”的表述必须显式写成“后置”并给出来源文档，避免被读成承诺。
- 新增 REST 端点时同步 [architecture.md](./architecture.md) §6 的端点表（表头即 `profiles_handlers.go` 的端点注释）。
- 新增决策编号（Dxx）时在 [design.md](./design.md) §8 登记，并写清“只报告/不隐式应用”这类边界。
