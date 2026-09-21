# aicli 配置加载与保存架构梳理

> 范围：启动时 provider/model/reasoning effort 的解析链、运行中 `/provider` `/model` 等切换命令的持久化、保存后的重新加载时机。
> 代码版本基准：2026-09-19 主干。所有路径相对 `backend/`（未标注则位于 `cmd/aicli/commands/` 或 `internal/agentconfig/`）。

## 一、目录布局总览

| 位置 | 角色 | 典型内容 |
|---|---|---|
| `~/.aicli/`（用户层，decision D4 默认写目标） | 全局用户配置 + 运行数据 | `config.yaml`、`runtime.yaml`、`.env`、`auth.json`、`presets.yaml`、`model_cards.yaml`、`workspace_directories.yaml` |
| `./.aicli/`（项目层，最高优先级） | 项目级覆盖，仅显式创建（`aicli init --project`）才出现 | `config.yaml`、`runtime.yaml`、`mcp.yaml`、`agent-worktrees/`、`memory/` |
| `~/.aicli/workspace/<hash>/`（D5 workspace 层） | 按 cwd 哈希隔离的 chat 偏好 | `chat-prefs.yaml` |
| `~/.aicli/sessions/`、`chat-logs/`、`cache/`、`data/`、`logs/`、`.backups/` | 运行时数据，非配置 | `session_history.sqlite` 等 |

## 二、启动加载：分层合并（decision D3）

### MergeMode

由环境变量 `AICLI_CONFIG_MERGE` 控制（`config_layers.go`）：

| 模式 | 行为 |
|---|---|
| `off`（默认） | 历史单文件行为：层栈中第一个存在的候选文件生效 |
| `dry-run` | 只计算合并结果，不落盘 |
| `on` | 从低到高逐层深合并，高层只覆盖其显式写的 key；显式 `null` 为 Helm 式"屏蔽下层" |

### bootstrap config 层栈（`ConfigLayerStack()`，从低到高）

```
1. ./configs/<name>        portable（随二进制/仓库附带）
2. ./<name>                legacy 散落文件
3. ./aicli.yaml            legacy 命名
4. ~/.aicli/<name>         user 层
5. ./.aicli/<name>         project 层（最高）
```

`runtime.yaml` 有独立层栈（`RuntimeConfigLayerStack()`）：`~/.aicli/runtime.yaml`（user）→ `./.aicli/runtime.yaml`（project，最高）。两层**合并**读取（`LoadMergedRuntimeConfigDocument`）：高层只覆盖其显式写的 key，低层其余设置保留；生效来源 = 最高存在层，写回落在最高可写层（全新安装创建 user 层）。

`configs/runtime.yaml` / `backend/configs/runtime.yaml` 是**开发目录布局，不再是隐式配置层**：CLI 解析顺序为「显式非约定覆盖 → `./.aicli/runtime.yaml` → `~/.aicli/runtime.yaml` → 空」，空表示使用内置默认值且不告警。只有调用方显式传入该文件时才读取（例如 runtime-server 的 `--config`）；旧模板里遗留的这两个约定值会被识别并忽略，不会报"未找到配置文件"。

### `.env` 发现（`bootstrap.go`）

与 config 层栈同目录派生，按最高优先级先到先得：

```
./.aicli/.env  →  ~/.aicli/.env  →  ./.env  →  configs/.env
```

godotenv **不覆盖已有进程环境变量**——因此 `~/.aicli/.env` 的 `PROVIDERS_DEFAULT=gemini_local` 能盖过 config.yaml 中 `${PROVIDERS_DEFAULT:-nvidia}` 占位符（参见 2026-09-19 gemini_local 排障记录 `docs/debug/tool-output-artifact-cascade-debug-20260919.md`）。

## 三、启动保存：写路由（decision D3 / D4）

- **写目标路由**（`config_write_route.go`）：merge 模式开启时，加载阶段记录 `ConfigOriginFiles`（每个 key 来自哪一层）。写入时按 key 的 origin 找归属层；跨层写入取最高层；"新 key 落到最高可写层"。安全三前提：merge on + 路径在层栈内 + 至少一个 key 有 origin。
- **默认写目标**（D4）：无显式路径时一律写 `~/.aicli/<config>`（`ResolveWritableConfigPath`）；project 文件只在显式创建时出现。runtime.yaml 的 fallback 同理（`RuntimeConfigWriteTarget`）。
- **文档级 diff 写回**（`config_merged_document.go`）：`ApplyMergedDocumentChanges` 把 merged 与 updated 的叶子差异写回各自拥有该 key 的层；消失的 key 写显式 `null` 屏蔽；不重写未触碰的层，保住 `${VAR}` 占位符和手工格式。`GET /config/document` 把 merged 视图同时喂给 CLI 与 Web UI，三方看到同一份有效配置。

## 四、Chat 偏好的三级持久化

**优先级链**（`chat_preferences.go`）：

```
CLI flag > session 恢复上下文 > workspace 偏好 > 全局 aicli.chat > 交互式选择器 > provider 默认
```

| 层 | 文件 | 作用域 |
|---|---|---|
| 全局 | `~/.aicli/config.yaml` 的 `aicli.chat` | 所有 cwd 共享 |
| workspace | `~/.aicli/workspace/<hash>/chat-prefs.yaml` | 单个 cwd |
| session | session store 的 `Metadata.Context` | 单次会话恢复 |

**workspace 哈希**（`chat_persistence_workspace.go:174` `projectIDForPath`）：`sha256(cleaned cwd path)` 前 8 字节 hex，与 `providerops.HeaderTemplateProjectID` / gateway 路由共用同一算法，保证"哪个工作区"的定义全局一致。

**写入**（`chat_persistence.go` / `chat_persistence_workspace.go`）：`UpdateAICLIChatPreferences` 先走写路由（chat 段编辑回它的 origin 层），文件不存在时创建 starter config，原子写回，且只动 `aicli.chat` 段。加载失败只 warning，永不阻塞启动（`resolveWorkspaceChatPreferences`，`chat_preferences.go:31`）。

**关键防线**（`chat_preferences.go:97-102`）：session 恢复的 storedProvider 若已不在启用集合且无法按 protocol 兜底，不得直接返回失效名字——否则 `resolveProviderExecutionContext` 硬报 `provider not found` 使 chat 启动 exit 1。正确行为是降级继续走 workspace → config → default 链；workspace/config 偏好同样要过 `isEnabledProvider` 校验（:112-124）。

## 五、启动决策流（`chat_bootstrap.go:153` `prepareChatRuntimeState`）

1. 进程启动：从 `StartupDotEnvSearchPaths` 找第一个 `.env` 注入环境（不覆盖已有）
2. `ConfigLayerStack` 定位 config（off 模式取第一个存在的；on 模式深合并 + 记录 origins）
3. `resolveChatProviderChoice`（chat_preferences.go:80）解析 provider：flag → session（带失效降级）→ workspace prefs → `aicli.chat.default_provider` → 交互选择器 → `providers.default_provider` → 第一个启用 provider
4. `resolveProviderExecutionContext` 确定协议/adapter/baseURL
5. `resolveChatModelChoice` 解析 model（当 provider 是交互新选时，不短路旧 provider 的 `aicli.chat.default_model`）
6. 交互选择产生的新默认值回写 workspace 偏好（`persistChatPreferencesIfNeeded`，chat_preferences.go:282-311），保证"选一次、本目录记住"

## 六、运行中切换：一条命令的三段式处理

### (1) 命令入口（chat_model_switch.go）

| 入口 | 位置 | 行为 |
|---|---|---|
| `handleProviderCommand` | chat_model_switch.go:37 | 裸 `/provider` 打开 provider→model→reasoning 三级 picker；带参数直接应用；`/provider status` 只读打印 |
| `handleModelCommand` | chat_model_switch.go:79 | 裸 `/model` 只开 model picker；带参数直接应用 |

统一交互输出模式下先走结构化路径（`executeStructuredProviderCommand`；chat_command_result.go:233-243 注明 `/provider` `/model` 已完全迁移），返回 `ChatCommandResult`，`OpenModelPicker` 作为 effect 再开 picker。

### (2) 内存切换（`applyRuntimeModelSwitch`，chat_model_switch.go:171）

纯内存变更，立即生效：

| 行号 | 操作 |
|---|---|
| :184 | `config.ApplyModelMapping` 应用模型映射 |
| :193-205 | 按模型能力目录（`reasoningEffortCatalogForModel`）决定是否弹 reasoning 选择 |
| :212 | `session.Model = resolvedModel` |
| :213 | `session.ReasoningEffort = reasoningEffort` |
| :214 | `session.BaseURL = buildProviderURL(...)` 按 URL 模板重建 |
| :215 | `session.ContextWindowTokenCount = 0` 上下文窗口重置 |
| :219 | `syncRuntimeSessionFromChat` → 持久化到 session store |
| :220 | `refreshLocalRuntimeAfterModelSelection` → 刷新本地运行时 |

`refreshLocalRuntimeAfterModelSelection`（chat_actor_host.go:1264）通过 `SessionHub.StopContext` 停掉旧运行时上下文，下一个请求按新 model/baseURL 重建——即"切换后无需重启即生效"的机制。

### (3) 双层持久化

**第一层：session store（会话级，立即写）** — `syncRuntimeSessionFromChatMode`（chat_session.go:662）把当前 chat 态快照进 runtime session 的 `Metadata.Context`：

| key | 来源 | 行号 |
|---|---|---|
| `provider_name` / `provider_protocol` | session.ProviderName / GetProtocol() | :681-682 |
| `model` / `reasoning_effort` | session.Model / NormalizeReasoningEffort | :683-684 |
| `requested_*` / `effective_*`（provider/model/effort/permission） | 对应 session 字段，空值删除 | :685-702 |
| `stream` / `fast_mode` / `disable_tools` / `debug_mode` | session 开关 | :709-712 |
| token 计数族 | session 各计数器，0 值删除 | :715-744 |

随后 `SessionManager.Update`（不存在则 `Save` 兜底，chat_session.go:789-797）落库。**注意：这只在恢复该会话时生效，不构成跨会话默认值。**

**第二层：workspace 偏好（跨会话默认值，D5）** — `persistModelCommandPreferences`（chat_model_command.go:434）→ `persistChatPreferences`（chat_preferences.go:273）：

```go
config.SaveWorkspaceChatPreferences(config.AICLIChatPreferenceUpdate{
    DefaultProvider: ..., DefaultModel: ..., ReasoningEffort: ...,
})
```

写入 `~/.aicli/workspace/<hash>/chat-prefs.yaml`。**故意不写全局 `aicli.chat`**（:267-271 注释明确）——`/model` 切换只应改变当前工作目录的默认值。picker 路径同样持久化（chat_model_picker.go:403），失败只降级为 warning。

### 其他配置命令对照

| 命令 | 持久化函数 | 写入字段 | 位置 |
|---|---|---|---|
| `/model` `/provider` | persistChatPreferences | provider+model+reasoning | chat_model_command.go:434 |
| `/reasoning_effort` | saveReasoningEffortCommandPreference | reasoning_effort | chat_reasoning_command.go:247 |
| `/stream` | saveStreamCommandPreference | stream | chat_stream_command.go:139 |
| `/fast` | saveFastCommandPreference | fast_mode | chat_fast_command.go:140 |

共同模式：**写 workspace 偏好文件 + 同步内存镜像 `session.Config.AICLI.Chat`**（如 chat_fast_command.go:151-156），使运行中会话继续读到一致值。

## 七、重新加载的三个时机

| 时机 | 加载点 | 效果 |
|---|---|---|
| 会话内即时 | 切换命令直接改内存 + `SessionHub` 停旧上下文重建 | 无需任何"重载"，下一请求生效 |
| 同 workspace 下次启动 | `resolveChatProviderChoice`（chat_preferences.go:112）→ `LoadWorkspaceChatPreferences` 读 chat-prefs.yaml | 偏好按 flag > session > workspace > config 链生效，picker 不再弹 |
| `/resume` 恢复会话 | session 恢复分支读 `Metadata.Context` 的 storedProvider/model/effort（chat_preferences.go:84-107） | 带失效降级：provider 不在启用集合时退回偏好链，不硬报错 |

## 八、全链路总图

```
启动:  .env 注入 → config 层栈加载(记 origin) → 偏好链解析 provider/model/effort
       flag > session context > workspace chat-prefs.yaml > aicli.chat > 选择器 > 默认
运行中: /model /provider /stream /fast /reasoning_effort
       → 内存切换(立即生效) + SessionHub 上下文重建
       → session store (Metadata.Context, 会话级)
       → workspace chat-prefs.yaml (目录级默认值, 下次启动加载)
保存:  chat-prefs.yaml 原子写; 全局 config.yaml 仅经写路由/配置 TUI 改动
重载:  会话内免重载; 下次启动按同一偏好链自然读到新值
```

**分层职责总结**：全局 config.yaml 走分层合并 + 写路由（D3/D4）；chat 偏好走 workspace 哈希目录（D5）；session 上下文走 `Metadata.Context`（带失效降级）。三层各有明确职责，互不越界。
