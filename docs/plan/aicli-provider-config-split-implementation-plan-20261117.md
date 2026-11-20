# aicli Provider/Model 配置拆分实施方案（providers.yaml）

> 文档状态：Draft（待评审后实施）
> 日期：2026-11-17
> 范围：`~/.aicli/config.yaml` 中的 `providers` / `provider_groups` 拆分到独立文件
> 代码基线：当前主干（`backend/internal/agentconfig/config_layers.go`、`config_write_route.go`、`config_merged_document.go`）

## 实施状态（2026-11-17）

已完成：

- provider fragment 层栈、深合并、origins、SourcePath/DefaultWriteLayer 排除；
- 写路由（已有 provider origin、新 provider 子树、混合 section 保护）、fragment 最小骨架与 0600；
- `chatUpdateWriteKeys` 修复、global proxy 路由、跨文件删除引用阻断；
- runtime-server fragment 加载、配置文档 layers/origins、显式 `--config` 模式、watch/signature；
- aicli TUI 来源展示、micro web 快照 + 配置页 provider 来源显示、桌面 Frontend provider 文件 badge；
- `aicli config split-providers`（单 owner、默认 dry-run、`--keep-main`/`--prune-main`）；
- 核心测试：agentconfig full、runtimeserver full、runtimeapi full、cmd/aicli/commands full、cmd/runtime-server full、frontend tsc/build/vitest。

> 全量 `go test ./...` 仍有若干与本功能无关的失败（configwriteguard 既有未加锁写点、lsp Rust analyzer、agent loop、sqlite/mesh 等）；本功能受影响包与前端构建均已单独验证通过。

暂未完成：

- 详细 doctor provider-split 诊断；
- 多 owner / 非空目标合并迁移（当前迁移命令显式拒绝，需手工合并）。

---

## 0. 摘要

### 0.1 目标

把 provider/model 定义从通用配置中剥离出来，支持以下布局：

```text
~/.aicli/config.yaml      # 通用配置：aicli、profiles、gateway、supervision、log 等
~/.aicli/providers.yaml   # provider 定义：providers、provider_groups
./.aicli/config.yaml      # 项目级通用配置（可选）
./.aicli/providers.yaml   # 项目级 provider 定义（可选，优先级最高）
```

要求：

1. 文件不存在时，现有行为完全不变，零迁移成本。
2. 文件存在时，CLI、runtime-server、TUI、micro web client、桌面 Web 设置页看到同一份有效配置。
3. 所有 provider 写入（登录、启用/禁用、删除、模型刷新、代理、Web 编辑）落到正确的文件。
4. 通用配置写入（`aicli.chat`、`gateway`、主题等）不落进 `providers.yaml`。
5. 外部编辑 `providers.yaml` 能触发热重载/指纹刷新。
6. `--config` 显式路径和 `AICLI_CONFIG_MERGE=off` 继续保持单文件语义。

### 0.2 非目标（首版不做）

- 不做通用 `include:` 语法（任意文件、递归 include、跨文件锚点）。
- 不把 `aicli.chat.default_provider/default_model`、`aicli.subagents.routing`、`aicli.main_agent` 拆进 provider 文件。
- 不做每 provider 一个文件的 `providers.d/` 目录布局。
- 不改变 `runtime.yaml` 的独立层栈。
- 不改变 `model_cards.yaml` 的模型卡目录机制。
- 不改变 provider 凭据以 `auth.json` / `api_key_ref` / `auth_ref` 为主的现状。

### 0.3 结论

**可行，但必须新增“provider 专用补充文档层”。** 直接把 `providers.yaml` 加入 `ConfigLayerStack()` 会把一个文档片段误当成完整配置候选，污染 `--config` 解析、`.env` 发现、`ConfigFilePath`/`SourcePath`、starter 创建和写路由。正确做法是：

- `ConfigLayerStack()` 保持只代表“完整配置文件”的候选；
- 新增 `ProviderConfigLayerStack()`，只包含 `providers.yaml` 用户层/项目层；
- 合并文档时把 provider 层插入到同层主配置之后；
- `Layers`/`Origins` 包含 provider 层以支持写路由；
- `SourcePath`（即 `ConfigFilePath`）只从主配置层计算；
- 首版只允许 provider 文件承载 `providers` 与 `provider_groups` 两个顶层节点。

---

## 1. 现状与约束

### 1.1 当前配置职责

`agentconfig.Config`（`backend/internal/agentconfig/config.go:31-57`）是一个单文档结构，同时承载：

| 顶层键 | 内容 | 与本次拆分的关系 |
|---|---|---|
| `providers` | `default_provider`、全局 headers/proxy/timeout、`items`（含模型列表、模型能力） | 拆分主体 |
| `provider_groups` | provider 分组/负载均衡 | 建议随 providers 一起拆 |
| `aicli` | chat 偏好、主题、subagents routing、main_agent、model_cards 指针等 | 留在主配置 |
| `profiles` | profile 根目录 | 留在主配置 |
| `gateway` | 外部会话网关连接 | 留在主配置 |
| `retry` / `circuit_breaker` | provider 重试/熔断策略（顶层） | 首版留在主配置 |
| `log` / `supervision` / `skills_runtime` | 日志、监督、技能入口 | 留在主配置 |

模型相关字段分布在两处，必须在文档中区分：

- `providers.items.<name>.default_model`、`supported_models`、`model_capabilities`、`models_path`：provider 级模型定义，随 provider 文件走。
- `aicli.chat.default_model`、`aicli.subagents.routing.*`：全局/子 Agent 选路偏好，留在主配置。
- `~/.aicli/model_cards.yaml`：模型元数据目录，已有独立分层（见 `docs/config/aicli-config-load-save.md:59-83`），不重复造。

### 1.2 当前读取链路

```text
aicli main
  └─ ConfigLayerStack()                         # 固定候选名：configs/<name>、./<name>、./aicli.yaml、~/.aicli/<name>、./.aicli/<name>
      └─ InitGlobalConfigLayered(resolved, explicit)
          ├─ explicit 或 merge off → InitGlobalConfig(path)        # 单文件
          └─ merge on → initGlobalConfigMergedLayers()
              ├─ loadLayeredConfigDocument()                       # 每层 ${VAR} 展开 + 深合并 + origins
              ├─ unmarshalYAML(merged) → Config
              ├─ applySystemPresetLayer()                          # 预设作为基线，用户配置优先
              └─ cfg.ConfigFilePath = document.SourcePath          # 最后一个存在层
```

关键事实：

- `ConfigLayerStack()`（`config_layers.go:173-195`）只接受固定文件名；`~/.aicli/providers.yaml` 目前不会被读取。
- 分层合并默认开启：`ParseMergeMode("")` 返回 `MergeModeOn`（`config_layers.go:45-58`）。`docs/config/aicli-config-load-save.md:41` 仍写默认 `off`，属于文档滞后，应在本次一并修正。
- `loadLayeredDocumentFor` 的 `SourcePath` 取“最后一个存在层”（`config_layers.go:378-380`），这个值同时驱动 `ConfigFilePath`、`ensureWritableAICLIConfigPath` 和 `ReloadGlobalConfig`。
- `Config` 的 `ConfigLayers`/`ConfigOrigins`/`ConfigOriginFiles` 是诊断与写路由的基础（`config.go:48-56`）。
- 单文件模式（`--config` 或 merge off）不合并其它层（`config_layers.go:445-458`）。

runtime-server 侧：

- `LoadRuntimeAgentConfig` 仅在 `MergeModeOn` 且 `MergedConfigDocument.MultiLayer()` 为 true 时使用合并视图（`internal/runtimeserver/config_snapshot.go:64-86`）。
- 配置页 `LocalConfigDocumentService.LoadDocument` 同样走 `LoadMergedConfigDocument`（`internal/runtimeserver/config_document.go:309-336`）。
- 热重载签名 `ConfigSourceSignatureFor` 只覆盖 `ConfigLayerStack()` 与预设层（`internal/runtimeserver/config_external_reload.go:211-269`）。

### 1.3 当前写入链路

```text
CLI / TUI / chat slash / micro web / runtime config document
  └─ 具体持久化函数（UpdateProviderConfig / DeleteProvidersConfig / SetDefaultProviderConfig / ...）
      └─ routeConfigWritePath(configPath, keys...)
          ├─ merge off / 路径不在层栈 / 无 origin → 原 configPath
          └─ WriteTargetForKeys(keys...) → ConfigOriginFiles 中最高优先级的 owner 文件
              └─ updateConfigFileDocument() / applyDocumentChanges()
                  └─ 文件锁 + 读-改-写 + 原子写
```

已有的关键写点：

| 入口/函数 | 位置 | 是否已走路由 |
|---|---|---|
| `UpdateProviderConfig` | `provider_persistence.go:64-107` | 是（`routeConfigWritePath`） |
| `DeleteProvidersConfig` | `provider_management.go:140-306` | 是 |
| `SetProvidersEnabledConfig` | `provider_management.go:308-346` | 是 |
| `SetDefaultProviderConfig` | `provider_management.go:348-380` | 是 |
| `SetProviderProxyConfig` / `RemoveProviderProxyConfig` | `provider_proxy.go:115-.../281-...` | 是 |
| `SetGlobalProxyConfig` / `RemoveGlobalProxyConfig` | `provider_proxy.go:193/252` | **否（需补）** |
| `UpdateAICLIChatPreferences` | `chat_persistence.go:35-69` | 是（但 write keys 含 `providers.default_provider`，见风险） |
| Web 文档保存 | `internal/runtimeserver/config_document.go:161-287` | 是（`ApplyMergedDocumentChanges`） |

### 1.4 已有拆分先例

仓库已经有多处“把某一类配置单独放文件”的先例，说明方向一致：

| 文件 | 机制 | 结论 |
|---|---|---|
| `~/.aicli/auth.json` | 独立凭据存储，0600（`auth_store.go:169-175`） | 凭据继续留在这里 |
| `~/.aicli/model_cards.yaml` | 独立五层模型卡目录（`docs/config/aicli-config-load-save.md:59-83`） | 模型元数据不要重复造 |
| `~/.aicli/runtime.yaml` / `./.aicli/runtime.yaml` | 独立层栈（`RuntimeConfigLayerStack`） | 运行时执行参数已有独立机制 |
| `~/.aicli/workspace/<hash>/chat-prefs.yaml` | workspace 级 chat 偏好 | 全局 chat 偏好不要拆进 provider 文件 |
| `~/.aicli/presets.yaml` | 预设基线，用户配置覆盖 | 不适合承载可写 provider 定义 |

### 1.5 为什么不能直接新增文件

如果只是把 `providers:` 剪切到 `~/.aicli/providers.yaml`：

- 加载器不会读它，provider 直接消失；
- 如果把 `providers.yaml` 加进 `ConfigLayerStack()`：
  - `ResolveConfigPath` 可能把它选成“主配置”；
  - `EnsureStarterConfigFile` 可能把 starter 写到它；
  - `ConfigFilePath`/`SourcePath` 会变成它；
  - `ReloadGlobalConfig`、`ensureWritableAICLIConfigPath`、Web 配置页的目标路径全部错位；
  - `.env` 发现、错误提示、help 文案也会跟着漂移。

因此必须区分“完整配置文件候选”和“专用补充文档”。

---

## 2. 设计决策

### 2.1 文件与优先级

新增固定文件名 `providers.yaml`，候选路径：

```text
$HOME/.aicli/providers.yaml      # user providers
./.aicli/providers.yaml          # project providers
```

合并顺序（低 → 高）：

```text
1. ./configs/config.yaml                 portable
2. ./config.yaml                         legacy
3. ./aicli.yaml                          legacy
4. ~/.aicli/config.yaml                  user config
5. ~/.aicli/providers.yaml               user providers      ← 新增
6. ./.aicli/config.yaml                  project config
7. ./.aicli/providers.yaml               project providers   ← 新增
```

规则：

- 同层 provider 文件覆盖同层主配置中的 provider 键；
- 项目层整体高于用户层（项目主配置仍高于用户 provider 文件）；
- provider 文件不存在 = 该层不参与合并，行为完全不变；
- 不支持 `providers.yml` 别名（首版只认 `.yaml`）。
- 不提供 `./configs/providers.yaml` 这种 portable/仓库层：provider 文件只作为用户/项目显式配置来源，避免仓库附带文件成为隐式配置（与 `runtime.yaml` 不把 `configs/` 当层栈的原则一致）。

### 2.2 Fragment Schema

`providers.yaml` 不是一份完整配置，只允许两个顶层键：

```yaml
providers:
  default_provider: deepseek
  headers:
    X-Client: aicli
  proxy:
    enabled: true
    http: http://127.0.0.1:7890
  items:
    deepseek:
      enabled: true
      protocol: openai
      base_url: https://api.deepseek.com
      api_key_ref: deepseek
      default_model: deepseek-chat
      supported_models:
        - deepseek-chat
        - deepseek-reasoner
      model_capabilities:
        deepseek-chat:
          max_context_tokens: 65536
provider_groups:
  - name: pool
    strategy: round_robin
    providers:
      - name: deepseek
        weight: 1
        enabled: true
```

约束：

- 顶层出现 `aicli`、`gateway`、`log` 等其它键时，**加载报错**并提示文件路径与非法键名，避免误把通用配置塞进 provider 文件后产生跨文件写入；
- `${VAR}` / `${VAR:-default}` 由现有层加载逻辑自动展开；
- 显式 `null` 仍然遵循 Helm 式屏蔽下层语义；
- 数组按现有 merge 语义**整体替换**（不是拼接）：`supported_models`、`provider_groups` 等列表一旦在 provider 文件出现，就应以完整列表写入；`provider_groups` 必须与 providers 同文件完整定义。
- 不单独解码 fragment（必须与主文档合并后再解码/校验），否则 `provider_groups` 引用主配置里的 provider 会误报缺失。

### 2.3 层模型与 origins

在 `ConfigLayer` 上新增：

```go
const (
    LayerKindUserProviders    ConfigLayerKind = "user-providers"
    LayerKindProjectProviders ConfigLayerKind = "project-providers"
)

// Fragment 标记该层是专用补充文档，不参与 SourcePath/默认写层计算。
Fragment bool
// Section 可选：首版固定为 "providers"，用于 UI/诊断展示。
Section string
```

新增函数：

```go
func ProviderConfigLayerStack() []ConfigLayer          // user + project providers
func ProviderConfigLayerPaths() []string               // watch/fingerprint 用（导出给 runtimeserver/commands）
func configLayerStackWithFragments() []ConfigLayer     // 主层栈 + 同层插入 fragment
```

`configLayerStackWithFragments()` 的行为：

- 在 user 主配置候选之后插入 user providers；
- 在 project 主配置候选之后插入 project providers；
- fragment 的 `Present` 由 `fileExists` 决定；
- 保持 `ConfigLayerStack()` 不变，供 `ResolveConfigPath`、`.env` 发现、`isConfigLayerPath` 使用。

`MergedConfigDocument`：

- `Layers` = 合并层栈（含 fragment），供 origins/写路由使用；
- `PresentLayers` = 所有存在层（含 fragment），`MultiLayer()` 因此能感知 fragment；
- `SourcePath` = 最后一个存在的**非 fragment** 层；
- `DefaultWriteLayer` = 从非 fragment 层里选最高可写层（不得选 provider 文件）。

### 2.4 写路由规则

新增判定：

```go
func isProviderSubtreeKey(key string) bool {
    return key == "providers" ||
        strings.HasPrefix(key, "providers.") ||
        key == "provider_groups" ||
        strings.HasPrefix(key, "provider_groups.")
}
```

`routeConfigWritePath` / `ApplyMergedDocumentChanges.targetFileFor` 使用以下优先级：

1. **一次写入同时包含 provider 与非 provider key**：直接写主配置，禁止整批落到 fragment。
   - 这是防止 `aicli.chat` 被写进 provider 文件的关键规则；
   - `ApplyMergedDocumentChanges` 是按单个 key 分摊的，不受此规则影响。
2. **已存在 key**：写回 `ConfigOriginFiles` 记录的 owner 文件（现有逻辑）。
3. **新 provider 子树 key**：如果同层（user/project）provider fragment 存在且可写，写 fragment；否则写主配置。
4. **新非 provider key**：写主配置的 `DefaultWriteLayer`。
5. read-only 层（portable/仓库文件）永不接收写入，继续走可写层。

`MergedConfigDocument.WriteLayerKindFor` 也必须复用同一套 provider 子树规则：否则新 provider 的 diff 虽然写进了 fragment，配置页的 `path_layers` 仍然会显示主配置层，造成“显示写 A、实际写 B”。

“同层”的定义：以调用方传入的主配置路径所在层为基准（user 或 project）。例如：

- `ConfigFilePath = ~/.aicli/config.yaml`，存在 `~/.aicli/providers.yaml` → 新 provider 写 `~/.aicli/providers.yaml`；
- `ConfigFilePath = ./.aicli/config.yaml`，存在 `./.aicli/providers.yaml` → 新 provider 写 `./.aicli/providers.yaml`；
- 只有 user provider 文件、当前写项目主配置 → 新项目 provider 仍写项目主配置，不跨层写用户文件。
- legacy/portable 主配置没有对应的 fragment；当前活动路径是 legacy/portable 时，新 provider 回退到调用方传入的主配置。
- 若只有 `./.aicli/providers.yaml` 而 `./.aicli/config.yaml` 不存在，当前活动主配置是 user 层，新 user provider 不会跨层写 project fragment；需要项目级写入时，应同时创建项目主配置（或由后续版本提供更明确的“项目 provider 优先”策略）。

### 2.5 首次创建与权限

- 正常写入只在 fragment 已存在（或迁移命令创建）时路由到 fragment；文件不存在时保持写主配置，避免隐式创建。
- 为防竞态（加载后文件被删除）增加 fragment-aware 创建：
  - 目标为 provider fragment 且不存在时，创建最小骨架：

    ```yaml
    providers:
      items: {}
    ```

  - **不得**调用 `ensureStarterConfigAtPathLocked` 写全量 starter（`bootstrap.go:269-301`）。
- provider fragment 可能包含明文 `api_key`；创建时权限使用 `0600`。`writeFileAtomic` 当前默认 `0644`（`chat_persistence.go:239-242`），需要新增可指定 mode 的变体或在 fragment 写点显式 chmod。
- 推荐用户继续使用 `api_key_ref` / `auth_ref` 指向 `auth.json`，provider 文件只存引用。

### 2.6 安全开关

新增环境变量 `AICLI_PROVIDERS_FILE`：

| 值 | 行为 |
|---|---|
| 未设置 / `on` | 按默认路径发现并合并 provider 文件 |
| `off` / `0` / `false` | 完全忽略 provider 文件（读取与写入均不路由到 fragment） |

用途：滚动发布/事故回滚；不替代 `AICLI_CONFIG_MERGE=off`，后者仍然关闭所有分层合并。

---

## 3. 读取链路改造

### 3.1 aicli 启动

位置：`backend/cmd/aicli/main.go:325-357`。

现状：

```text
configPath := explicitConfigPath 或 ResolveConfigPath(DefaultConfigSearchPaths())
EnsureStarterConfigFile(configPath)
InitGlobalConfigLayered(configPath, explicitConfigPath)
```

改造后：

- `DefaultConfigSearchPaths()` / `ResolveConfigPath` / `EnsureStarterConfigFile` **保持只看主配置**；
- `InitGlobalConfigLayered` 在 merge on 且非 explicit 时加载 fragment；
- `cfg.ConfigFilePath` 仍是主配置路径；
- `cfg.ConfigLayers` 包含 fragment；
- `cfg.ConfigOrigins` 中 `providers.*` / `provider_groups.*` 指向 `user-providers` / `project-providers`。

验收：

- 只有 `providers.yaml`、没有主配置时，启动仍会创建主 starter（`EnsureStarterConfigFile` 行为不变），provider 从 fragment 加载；
- `--config <path>` 显式路径时 fragment 不参与（保持“显式选择即精确文件”）。

### 3.2 `loadLayeredConfigDocument`

位置：`internal/agentconfig/config_layers.go:308-382`。

改造：

```go
func loadLayeredConfigDocument() (*layeredConfigDocument, error) {
    return loadLayeredDocumentFor(configLayerStackWithFragments())
}
```

保持：

- 每层先展开环境变量，再 YAML 解析；
- `mergeMergeMapsMasked` 深合并；
- `collectConfigOrigins` 记录每个叶 key 的最高层；
- `SourcePath` 只在非 fragment 层中取最后一个 present；
- `FallbackKeys` / `OverriddenKeys` 包含 fragment 产生的覆盖，用于 doctor/UI 提示。

新增：

- fragment 顶层键白名单校验：`providers`、`provider_groups`；
- fragment 解析失败时报错信息包含文件路径；
- 可选：`OverriddenKeys` 中 `providers.*` / `provider_groups.*` 重复时输出 warning，提示用户清理主配置残留。

### 3.3 `InitGlobalConfigLayered` / `ReloadGlobalConfig`

位置：`config_layers.go:445-525`。

- `InitGlobalConfigLayered`：
  - explicit 或 merge off → 走 `InitGlobalConfig`，完全忽略 fragment（不变）；
  - merge on → `initGlobalConfigMergedLayers()` 使用含 fragment 的文档；
  - `cfg.ConfigFilePath = document.SourcePath`（主配置）；
  - `cfg.ConfigLayers = document.Layers`（含 fragment）；
  - `cfg.ConfigMergeMode = MergeModeOn`。
- `ReloadGlobalConfig(configPath)`：
  - `isConfigLayerPath(configPath)` 仍只看主层栈；
  - 主配置路径 → layered reload（包含 fragment）；
  - 自定义路径 → explicit 单文件（不变）。
- **显式模式一致性修复（必须）**：当前 `ReloadGlobalConfig` 只看路径是否属于层栈，如果用户用 `--config ~/.aicli/config.yaml`（路径恰好也是层候选），启动时是单文件，但后续写入触发的 reload 会走 layered，导致 provider fragment 突然生效/不生效。
  - 在 `Config` 上记录 `ConfigExplicitPath`（`yaml:"-"`），或在 agentconfig 包记录全局 explicit 路径；
  - `ReloadGlobalConfig` 先判断 `configPath == ConfigExplicitPath` → 走 `InitGlobalConfigLayered(configPath, configPath)` 的单文件分支；
  - 测试必须覆盖“显式路径等于主层路径 + fragment 存在”的启动/ reload 一致性。

### 3.4 runtime-server 运行时配置

位置：`internal/runtimeserver/config_snapshot.go:35-86`。

- `LoadMergedConfigDocument()` 返回的 `PresentLayers` 包含 fragment；
- `MultiLayer()` 为 true；
- `LoadRuntimeAgentConfig` 使用 `merged.MergedYAML`，因此运行时注册的 provider 与配置页展示一致；
- `ResolveAgentConfigSnapshotInfo` 的 `BasePath` 仍是主配置路径，不指向 fragment。

测试重点：只有 `config.yaml` + `providers.yaml` 两层时，runtime 必须看到 fragment 里的 provider。

**兼容性修复（必须）**：`LoadRuntimeAgentConfig(configPath)` 当前在 merge on + 多主层时无条件使用 `LoadMergedConfigDocument()`，会忽略 runtime-server 的显式 `--config`，与 CLI 的“显式路径 = 单文件”语义不一致。需要：

- 给 `LoadRuntimeAgentConfig` 增加显式模式参数（或 `AgentConfigLoadOptions{Path, Explicit}`）；
- runtime-server `serve --config <path>` 传入 `Explicit=true`，未指定 `--config` 时传 `false`；
- 显式模式下只读该文件，不加载其它主配置层与 fragment；预设基线仍按现有 `InitGlobalConfig` 单文件语义应用；
- `ConfigSourceSignatureFor` 也按同一 explicit 标志决定是否加入层栈/fragment 路径。

否则会出现“CLI `--config` 忽略 provider 文件，runtime-server `--config` 却加载 provider 文件”的入口分叉。

### 3.5 Web 配置文档服务

位置：`internal/runtimeserver/config_document.go:44-87、309-352`。

- `LoadDocument` 返回：
  - `Raw`：合并后的 YAML（不是单个文件）；
  - `Parsed`：合并后的 map；
  - `Layers`：含 fragment 的完整层栈；
  - `Origins`：provider key 指向 `user-providers` / `project-providers`；
  - `Warnings`：增加“provider 配置来自独立文件”的提示。
- `configDocumentLayers` 需要表达 fragment：
  - 方案 A：`Kind` 用 `user-providers` / `project-providers`，新增 `Fragment` / `Section` 可选字段；
  - 方案 B：`Kind` 保持 `user` / `project`，新增 `section: "providers"`。
  - 推荐 A+B：`Kind` 保留优先级语义，另加 `Section`/`Fragment` 给 UI 显示，避免破坏现有前端对 kind 的假设。
- `attachConfigDocumentPathLayers` 使用 `WriteLayerKindFor`，provider 变更的 `PathLayers` 应显示 provider 文件对应 kind。

### 3.6 热重载与指纹

位置：

- `internal/runtimeserver/config_external_reload.go:211-269`
- `backend/cmd/aicli/commands/chat_config_refresh.go:37-86`

改造：

- `configSourceWatchPaths` 增加 `agentconfig.ProviderConfigLayerPaths()`；
- **仅当 merge on、provider 文件未禁用（`AICLI_PROVIDERS_FILE!=off`）且当前不是显式 `--config` 路径时**才加入 provider 路径；显式配置模式忽略其它层与 fragment（预设仍按单文件语义参与），不应因 fragment 改动触发无效重载；
- 显式模式的签名集合应为「显式文件 + 预设层」；默认模式为「层栈 + fragment + 预设层」；
- `ConfigSourceSignatureFor` 自动覆盖 fragment 的存在性/大小/mtime；
- `chatConfigFingerprintForPath` 在 merge on 且 path 是主层时，追加所有 provider fragment 的指纹；
- `chatConfigFingerprintForPath` 还必须尊重 `ConfigExplicitPath`：显式 `--config` 等于层路径时不得因 fragment 变化触发 reload；
- `chatConfigPathIsLayer` 仍只判断主层，fragment 通过单独的 helper 判断；
- 外部编辑 `providers.yaml` 后：
  - CLI 下一 turn 触发 `refreshChatConfigIfChanged`；
  - runtime-server 外部重载器触发配置重载。

### 3.7 诊断

- `describeConfigLayers` 输出包含 `user-providers` / `project-providers`。
- `aicli doctor`（如已有配置层诊断入口）增加：
  - provider 文件路径是否存在；
  - 每个 provider key 的 origin；
  - 主配置与 provider 文件重复定义 warning；
  - `AICLI_PROVIDERS_FILE=off` 时的提示。
- `ConfigSearchSummary` 保持主配置搜索顺序，另加一行 `Providers: ...`，避免用户误以为 `providers.yaml` 也是完整配置候选。

---

## 4. 写入链路改造（CLI / TUI / micro web / frontend）

### 4.0 统一原则

所有写入入口都必须遵守：

1. 调用方传入的仍然是**主配置路径**（`ConfigFilePath` 或 `ConfigPath`），不直接传 provider 文件；
2. 由 `agentconfig` 的写路由决定最终目标文件；
3. 一次写入涉及多个 key 时，按 2.4 的规则选择唯一目标文件；
4. 写成功后按入口刷新内存配置；
5. provider 文件写入与主配置写入共用同一把跨进程文件锁。

### 4.1 核心路由改造

位置：`backend/internal/agentconfig/config_write_route.go`。

新增/调整：

```go
func (c *Config) providerFragmentForMainPath(mainPath string) string
func (c *Config) isProviderSubtreeKey(key string) bool
func (c *Config) allKeysAreProviderSubtree(keys []string) bool
```

`WriteTargetForKeys(keys ...string)` 保持现有签名，语义扩展：

```text
if merge off / len(keys)==0 -> ""
if keys 同时包含 provider 子树和非 provider 子树 -> ""   # 调用方保留主配置
if 任一 key 有 origin:
    取 origin 中最高层文件
    if 该文件是 fragment -> 返回 fragment
    if 该文件是主配置 -> 返回主配置
if 所有 key 都是 provider 子树:
    if 同层 provider fragment 存在且可写 -> 返回 fragment
返回 ""（调用方保留默认主配置路径）
```

注意：

- **不能再把 `len(ConfigOriginFiles)==0` 当作前置条件**：一个空的 `providers.yaml` 可能还没有任何 provider origin，但新增 provider 仍必须路由到它。origin 为空只表示“没有已有 key”，不能阻止 subtree 规则。
- 已有 key 的 origin 优先，不因为 fragment 存在就静默迁移；
- 新 provider 才进入 fragment；
- 混合 key（provider + 非 provider）不返回 fragment。
- 单次写入的多个 key 若分散在不同层，仍按现有策略取最高层；因此同一个 provider 的 `items.<name>` 与 `providers.default_provider` 应尽量在同一文件，迁移命令/doctor 需要提示这种跨文件拆分。

`providerUpdateWriteKeys`（`config_write_route.go:144-158`）保持“provider item + default_provider”；两者都属于 provider 子树，因此 `UpdateProviderConfig(SetDefaultProvider=true)` 可以整体写 provider 文件。

`chatUpdateWriteKeys`（`config_write_route.go:185-195`）必须修改：

```go
// 只列实际会写的 key；UpdateAICLIChatPreferences 只写 aicli.chat 节点。
func chatUpdateWriteKeys(update AICLIChatPreferenceUpdate) []string {
    keys := []string{"aicli.chat"}
    if update.Routing != nil {
        keys = append(keys, "aicli.chat.routing.main_agent", "aicli.chat.routing.sub_agent")
    }
    return keys
}
```

原因：原实现把 `providers.default_provider` 纳入路由，但 `applyAICLIChatPreferenceUpdate` 只修改 `aicli.chat.default_provider`，并不写 `providers.default_provider`。拆分后如果 provider 文件拥有 `providers.default_provider`，这个 over-approximation 会让整段 `aicli.chat` 写进 provider 文件。

### 4.2 provider 持久化/管理函数

| 函数 | 改动 | 说明 |
|---|---|---|
| `UpdateProviderConfig` | 依赖新路由 | 新增/更新 provider、模型列表、能力、API key、proxy、headers、默认模型 |
| `DeleteProvidersConfig` | 依赖新路由；fragment 缺失按空处理；**处理跨文件 default_provider** | 删除 provider；默认值清理与 `provider_groups` 级联需显式设计 |
| `SetProvidersEnabledConfig` | 依赖新路由 | 启用/禁用 |
| `SetDefaultProviderConfig` | 依赖新路由 | 设默认 provider |
| `SetProviderProxyConfig` / `RemoveProviderProxyConfig` | 已路由，依赖新目标 | provider 级 proxy |
| `SetGlobalProxyConfig` / `RemoveGlobalProxyConfig` | **新增 route 调用** | `providers.proxy`；当前未路由 |
| `provider login` / `buildProviderPersistenceUpdate` | 依赖 `UpdateProviderConfig` | 登录、自动导入 |
| `provider_auth` | 不改 | 继续写 `auth.json` |

`DeleteProvidersConfig` 的跨文件风险：

- 删除 provider 时，`providers.default_provider` 可能位于另一个文件（未迁移时在主配置，provider 在 fragment）。现有实现只在“目标文件”里检查并清理默认值（`provider_management.go:200-214`），会留下指向已删除 provider 的悬空默认值。
- 首版必须二选一：
  - **推荐**：按 `providers.default_provider` 的 origin 做一次独立写事务，清除或替换默认值；两文件按顺序加锁，失败返回已写/未写文件；
  - 或显式 blocked，提示用户先迁移/切换默认值，禁止静默成功。
- Web 删除当前传 `ClearDefault=true`（`web_config_handlers.go:853-856`），不能因为默认值在另一个文件而静默忽略。

`DeleteProvidersConfig` 的 `provider_groups` 级联：

- 首版约束：`provider_groups` 必须与 `providers` 位于同一文件（迁移命令保证）；
- 如果 `provider_groups` 仍在主配置而 provider 在 fragment，`removeProviderGroupReferences` 只扫目标文件会漏删；需要在 doctor 中给出 warning，或实现跨文件清理（见 8.3 风险）。
- Web 端删除当前不传 `Cascade`，只传 `ClearDefault`；`provider_groups` 引用会以 blocked 信息返回，保持现有交互。

### 4.3 chat 偏好写入

位置：`internal/agentconfig/chat_persistence.go:35-69`。

- `UpdateAICLIChatPreferences` 只写 `aicli.chat`；
- 目标文件由 `chatUpdateWriteKeys` 决定，修正后只写主配置；
- 不允许把 `aicli.chat` 写进 `providers.yaml`；
- workspace 级 `chat-prefs.yaml` 不受影响（`chat_persistence_workspace.go`）。
- 注意：移除 `providers.default_provider` 这个 over-approximation 会改变既有“多层配置下 chat 偏好落点”的部分行为（更符合实际写入键），需要现有 layered 写路由测试 + 新增测试共同锁定。

### 4.4 CLI / chat slash commands

| 入口 | 主要函数 | 拆分后行为 |
|---|---|---|
| `aicli provider add/update/remove/enable/default/proxy` | `provider_management.go` / `provider_proxy.go` | 已存在 provider → origin 文件；新 provider → 同层 fragment |
| `aicli login` / `provider login` | `runProviderLogin` → `UpdateProviderConfig` | provider 定义写 fragment；凭据写 `auth.json` |
| `aicli provider refresh-models` | `provider_refresh_models.go` → `UpdateProviderConfig` | `supported_models` / `model_capabilities` 写 fragment |
| chat `/provider refresh` | `chat_provider_refresh_command.go` | 同上 |
| chat `/model` 新增/删除模型 | `chat_model_picker.go:508/618` | 同上 |
| chat `/model` 选择默认偏好 | `SaveWorkspaceChatPreferences` | 写 workspace `chat-prefs.yaml`，不碰全局配置 |
| `aicli config`（只读） | `config.go` | 从合并后的 `cfg` 展示 |
| `aicli config` TUI | 见 4.5 | 通过 agentconfig 函数写 |
| `aicli config` JSON 输出 | `config.go` | 可增加 `config_layers` / provider 路径字段 |

### 4.5 aicli TUI 写入逻辑

位置：`backend/cmd/aicli/commands/config_tui.go`、`config_tui_shell.go`。

现状写点：

| 操作 | 行 | 函数 |
|---|---|---|
| 启用/禁用 provider | 154 | `SetProvidersEnabledConfig(t.cfg.ConfigFilePath, ...)` |
| 设默认 provider | 164 | `SetDefaultProviderConfig(t.cfg.ConfigFilePath, name)` |
| 编辑 provider 字段 | 267、489 | `UpdateProviderConfig(t.cfg.ConfigFilePath, update)` |
| provider proxy | 534/547/560/577/591 | `SetProviderProxyConfig` / `RemoveProviderProxyConfig` |
| 全局 proxy | 638/651/664/677/691 | `SetGlobalProxyConfig` / `RemoveGlobalProxyConfig` |
| chat 偏好 | 832 | `UpdateAICLIChatPreferences(t.cfg.ConfigFilePath, update)` |
| 重新加载 | 906 | `ReloadGlobalConfig(t.cfg.ConfigFilePath)` |

改造：

1. **显示层来源**
   - `config_tui.go:45` 的 `Config: ...` 后增加 `Providers: ...` / `Layers: ...`；
   - 若 provider 文件存在，显示其绝对路径；若不存在，显示 `(inline in config.yaml)`。
2. **写路径**
   - 所有写点继续传 `t.cfg.ConfigFilePath`；
   - `SetGlobalProxyConfig` / `RemoveGlobalProxyConfig` 内部补 `routeConfigWritePath(path, "providers.proxy")`；
   - `UpdateAICLIChatPreferences` 修正 write keys，确保 chat 偏好写主配置。
3. **刷新**
   - `configTUI.reload()` 继续使用主配置路径；`ReloadGlobalConfig` 的分层逻辑会重载 fragment；
   - 每次写后保留现有 reload 行为；增加失败时显示实际目标文件（便于判断写到了哪个文件）。
4. **错误提示**
   - 写入失败时错误文案包含目标文件路径（provider 文件或主配置），避免用户误以为写的是 `config.yaml`。

TUI 测试重点：

- 编辑已有 provider（origin 在 fragment）→ 断言 fragment 内容变化，主配置不变；
- 新增 provider（fragment 存在）→ 断言写入 fragment；
- 全局 proxy（fragment 存在）→ 断言写入 fragment；
- chat 默认偏好 → 断言写主配置，fragment 不含 `aicli:`；
- `Config:` 行显示 provider 文件路径。

### 4.6 micro web client 写入逻辑

位置：`backend/cmd/aicli/commands/web_config_handlers.go`、`backend/cmd/aicli/commands/web/js/*.js`。

#### 4.6.1 读取快照

`GET /web/api/config` → `HandleChatWebAPIConfig`（`web_config_handlers.go:593`）：

- 当前返回 `ConfigPath` + `Providers` + `Chat`；
- 改造后增加：
  - `providers_config_path`：当前 provider 文件路径（不存在时为空）；
  - `config_layers`：层栈（kind/path/present/fragment/section）；
  - `provider_origin`：每个 provider 的 origin 文件（可选，用于 UI 提示）。
- `ConfigPath` 仍为主配置路径。

#### 4.6.2 写入端点

| 端点 | Handler | 底层函数 | 拆分后行为 |
|---|---|---|---|
| `POST /web/api/config/providers` | `HandleChatWebAPIConfigProviders`（694） | `UpdateProviderConfig`（806） | 已有 provider → origin；新 provider → fragment |
| `POST /web/api/config/providers/delete` | `HandleChatWebAPIConfigProvidersDelete`（830） | `DeleteProvidersConfig`（853） | 删除 fragment 中的 provider |
| `POST /web/api/config/providers/enabled` | `HandleChatWebAPIConfigProvidersEnabled`（884） | `SetProvidersEnabledConfig`（907） | 写 provider 所在文件 |
| `POST /web/api/config/providers/fetch-models` | `HandleChatWebAPIConfigProvidersFetchModels`（938） | 只读配置 + 模型卡写回 | 不写 provider 文件；`write_back` 写 `model_cards.yaml` |
| `POST /web/api/config/providers/probe-models` | `HandleChatWebAPIConfigProvidersProbeModels`（1066） | 只读配置 + 模型卡写回 | 同上 |
| `POST /web/api/config/providers/auto-import` | `HandleChatWebAPIConfigProvidersAutoImport`（1194） | `runProviderLogin` → `UpdateProviderConfig` | provider 定义写 fragment；key 写 `auth.json` |
| `POST /web/api/config/chat` | `HandleChatWebAPIConfigChat`（1311） | `UpdateAICLIChatPreferences`（1339） | 只写主配置的 `aicli.chat` |

#### 4.6.3 写入流程

```text
web/js/provider-editor.js
  └─ POST /web/api/config/providers
      └─ HandleChatWebAPIConfigProviders
          ├─ chatWebConfigPath()                  # 返回主 ConfigFilePath
          ├─ UpdateProviderConfig(path, update)
          │   └─ routeConfigWritePath → fragment/main
          ├─ chatWebRefreshSessionConfig()
          │   └─ reloadChatConfigForModelCommand → ReloadGlobalConfig(main)（layered）
          ├─ chatWebInvalidateRuntimeProvider(name)
          └─ 200 {status: ok, masked?}
              └─ loadConfigAdmin() → GET /web/api/config
```

关键点：

- `chatWebConfigPath()`（`web_config_handlers.go:286-306`）必须继续返回主配置，不能返回 fragment；
- `chatWebRefreshSessionConfig()` 依赖 `session.Config.ConfigFilePath`；因此 `ConfigFilePath` 必须保持主配置；
- 删除/启用/保存后都调用 `loadConfigAdmin()` 重新拉快照，保证 UI 看到合并后的 provider 列表；
- API key 保存若为内联 key，会写 fragment；推荐 `api_key_ref` 模式，key 写 `auth.json`（现有逻辑已支持）；
- `web_config_handlers.go:79-82` 的注释“保存时整体写回用户 config.yaml”需要更新为“写回 provider 文件（若存在）”。

#### 4.6.4 micro web 前端测试

- 编辑已有 provider → 检查 fragment 文件，主配置无 provider 变化；
- 新增 provider → fragment 创建/更新；
- 删除 provider → fragment 删除，主配置默认值清理到正确文件；
- 启用/禁用 → fragment；
- 保存 chat 默认值 → 主配置 `aicli.chat`，fragment 不出现 `aicli:`；
- 刷新模型列表（fetch-models）→ 不修改 provider 文件，仅 `model_cards.yaml`；
- 自动导入 → provider 在 fragment，API key 在 `auth.json`。

### 4.7 桌面 Frontend 写入逻辑

位置：`frontend/src/components/workspace/settings/backend-config-settings-page/`。

#### 4.7.1 读取

`GET /api/runtime/config/document` → `LocalConfigDocumentService.LoadDocument`：

- 返回合并后的 `parsed` / `raw`；
- `layers` 包含 provider fragment；
- `origins` 中 provider key → `user-providers`/`project-providers`；
- 前端 `RuntimeConfigDocument`（`frontend/src/types/runtime/config.ts`）当前没有 `layers`/`origins` 字段，需要新增：
  - `layers?: RuntimeConfigDocumentLayer[]`；
  - `origins?: Record<string, string>`；
  - `RuntimeConfigDocumentLayer` 包含 `kind/path/present/read_only?/section?/fragment?`；
  - `RuntimeConfigDocumentRuntimeImpact` 增加 `path_layers?: Record<string, string>`（后端已返回，前端类型当前缺失）。

#### 4.7.2 编辑与保存

前端不直接写文件，而是维护一个完整的 `draftParsed`：

| 域 | 文件 | 写入的 key |
|---|---|---|
| providers | `domains/providers.ts` | `providers.default_provider`、`providers.items.<name>`、`supported_models`、`model_capabilities` |
| provider groups | `domains/provider-groups.ts` | `provider_groups` |
| network proxy | `domains/network-proxy.ts` | `providers.proxy` |
| provider queue | `domains/provider-queue.ts` | `provider_queue`（本次不拆） |
| agent routing | `domains/agent-routing.ts` | `aicli.subagents.routing` 等（留在主配置） |

保存流程（`use-config-actions.ts`）：

```text
用户编辑
  └─ setDraftParsed(...)
      └─ 保存
          ├─ mode=source → POST {mode:"raw", raw}
          └─ mode=structured → POST {mode:"structured", parsed}
              └─ LocalConfigDocumentService.SaveDocument
                  ├─ loadEffectiveDocument(合并视图)
                  ├─ validate + decode
                  ├─ analyzeConfigDocumentRuntimeImpact
                  ├─ attachConfigDocumentPathLayers
                  └─ ApplyMergedDocumentChanges(merged, nextParsed, fallback=主配置)
                      └─ 每个 changed path → targetFileFor → 逐文件写
                          ├─ provider 键 → fragment
                          └─ 其它键 → 主配置
```

前端需要配合的改动：

1. `RuntimeConfigDocumentLayer` 类型增加可选 `section`/`fragment`；
2. 设置页的“层来源”展示增加 “Providers 文件” 标签/i18n；
3. provider 新建（`handleSaveProvider`）会写入新的 `providers.items.<name>`：
   - 后端 `targetFileFor` 必须按 2.4 规则路由到 fragment，前端无需额外指定路径；
4. provider 删除（`handleDeleteProvider`）会生成删除 diff：
   - 后端按 origin 删除 fragment 中的节点；
   - 若同时清空 `providers.default_provider`，该 key 的 origin 决定目标文件；
5. proxy / provider_groups 的写入走同一套 diff 路由；
6. source/raw 模式保存的是**合并文档**，后端仍按 key 分摊写回；UI warning 要说明“不是写回单个文件”，避免用户误以为会整体覆盖某个文件；
7. impact 面板可显示 `path_layers`，让用户看到 provider 变更落在哪个文件（可选增强）。

#### 4.7.3 前端测试

- `use-config-actions` 单测：structured save 携带完整 parsed 文档；source save 携带 raw；
- `providers.ts` 单测：新建/删除/默认 provider 修改；
- `provider-groups.ts` / `network-proxy.ts` 单测；
- 后端 `config_document_test.go` 覆盖 layers/origins/PathLayers；
- 前端类型检查 `tsc` 与 `vitest`。

### 4.8 Web 文档保存的层分摊

`ApplyMergedDocumentChanges`（`config_merged_document.go:115-151`）已有能力：

- 对每个叶子差异调用 `targetFileFor`；
- 目标文件不存在时创建目录 + 原子写；
- 删除的 key 写显式 `null` 屏蔽下层；
- 未触碰的文件不重写，保留 `${VAR}` 与注释。

本次需要补：

- `targetFileFor` 的 provider 子树规则（新 key）；
- 每次写每个目标文件独立取锁，避免嵌套锁；
- 若一次保存涉及 fragment 与主配置，按文件分组写入；失败时返回已写文件与失败文件信息（可选增强）；
- `configDocumentLayers` 展示 fragment；
- `Warnings` 增加 provider 文件提示。

### 4.9 并发与原子性

- 所有写点继续使用 `LockConfigFileWrite`；
- `ApplyMergedDocumentChanges` 先释放文档锁，再逐层取锁（现有逻辑）；
- provider fragment 与主配置是不同文件，同一逻辑写入跨文件时无法做到全局原子；采用“先写 provider 文件，再写主配置”的顺序，并在失败时返回明确错误；
- 迁移命令按文件逐个加锁，禁止同时持有两个文件锁；
- 所有写入使用 `writeFileAtomic`；fragment 首次创建使用 0600。

### 4.10 错误处理与用户提示

| 场景 | 行为 |
|---|---|
| provider 文件 YAML 非法 | 启动/加载失败，错误包含文件路径与解析位置 |
| provider 文件根节点不是 mapping | 加载失败，提示必须为 YAML mapping |
| provider 文件含非法顶层键 | 加载失败，提示只允许 `providers`/`provider_groups` |
| provider 文件不存在 | 视为未启用拆分，回退主配置，零告警 |
| fragment 目标文件在写入前被删除 | fragment-aware 创建最小骨架，或回退主配置并 warning |
| 新旧文件都定义同一 provider | 高层胜出；doctor/配置页 warning 提示清理 |
| provider 文件写失败（权限/磁盘） | 写操作失败，保留主配置；不静默降级 |
| `--config` 显式路径且 fragment 存在 | 忽略 fragment，help/文档说明；可选 warning |

## 5. 实施步骤

> 建议按阶段提交，每阶段都能独立回滚。P0/P1 只读能力仅用于内部验证，不对外发布；P2 写路由完成后才具备对外可用性。

### 5.0 实施总览

| 阶段 | 内容 | 估算 | 可独立交付 |
|---|---|---|---|
| Phase 0 | 测试基线、常量、契约 | 0.5 人日 | 是（无行为变化） |
| Phase 1 | fragment 只读合并 + 诊断 | 1–1.5 人日 | 内部验证 |
| Phase 2 | 写路由 + 首次创建 + 删除/默认/proxy | 1–1.5 人日 | 是（核心可用） |
| Phase 3 | TUI / micro web / frontend / CLI 适配 | 1–1.5 人日 | 是 |
| Phase 4 | 热重载 / watch / doctor | 0.5 人日 | 是 |
| Phase 5 | 迁移命令 | 0.5–1 人日 | 是 |
| Phase 6 | 文档、灰度、回滚、全量测试 | 0.5–1 人日 | 发布门槛 |
| **合计** | 完整可用 + 测试矩阵 | **5–7.5 人日** | |

> 若只做“读取 + 核心写路由 + 最小测试”的内部 MVP，可压缩到约 2–3 人日；但不能跳过 Phase 2 的写路由直接对外发布。

依赖顺序：

```text
P0 → P1 → P2 → P3 → P4 → P5 → P6
              └─ P3 的 TUI / micro web / frontend 可在 P2 完成后并行
```

### 5.1 改动文件总览

| 层 | 文件（新增标 *） | 职责 |
|---|---|---|
| agentconfig | `config_layers.go`、`config_write_route.go`、`config_merged_document.go`、`config_file_write.go`、`config.go` | 层栈、origins、写路由、fragment 创建、显式模式 |
| agentconfig | `provider_management.go`、`provider_proxy.go`、`provider_persistence.go` | provider 写点与跨文件删除 |
| runtimeserver | `config_snapshot.go`、`config_document.go`、`config_external_reload.go` | runtime 加载、配置文档 API、watch/signature |
| commands | `config_tui.go`、`web_config_handlers.go`、`chat_config_refresh.go`、`config.go`、`config_path_write.go` | TUI、micro web、CLI、热重载 |
| commands | `config_split_providers.go` * | 迁移命令 |
| frontend | `frontend/src/types/runtime/config.ts`、`backend-config-settings-page/**` | 类型、层展示、provider/proxy/groups 保存 |
| micro web assets | `backend/cmd/aicli/commands/web/js/*.js` | 来源文件/错误提示（可选） |
| docs | `docs/config/aicli-config-load-save.md`、`docs/aicli/install.md`、`docs/aicli/faq.md` | 使用说明与兼容矩阵 |

### Phase 0：基线与契约（0.5 人日）

| 步骤 | 文件 | 交付物 |
|---|---|---|
| P0-1 | `internal/agentconfig/config_layers_test.go` / 新增 `provider_fragment_test.go` | 测试 helper：隔离 HOME/cwd、创建主配置与 provider 文件、断言 origins/target |
| P0-2 | `aiclipaths` / `agentconfig` | 常量 `ProviderConfigFileName = "providers.yaml"`、`ProviderConfigEnvVar = "AICLI_PROVIDERS_FILE"` |
| P0-3 | `docs/plan/...` | 本文档评审通过，确认首版范围 |
| P0-4 | 测试基线 | 记录当前 `go test ./internal/agentconfig/... ./cmd/aicli/commands/... ./internal/runtimeserver/...` 全绿结果 |

验收：

- provider 文件不存在时，所有现有测试与手工路径行为不变；
- `AICLI_PROVIDERS_FILE=off` 时，即使文件存在也不读取、不写路由。

### Phase 1：只读合并（1–1.5 人日）

| 步骤 | 文件 | 具体改动 |
|---|---|---|
| P1-1 | `internal/agentconfig/config_layers.go` | `ConfigLayer` 增加 `Fragment`、`Section`；新增 `ProviderConfigLayerStack()`、`ProviderConfigLayerPaths()` |
| P1-2 | 同上 | 新增 `configLayerStackWithFragments()`：在主层栈的 user/project 位置插入 fragment |
| P1-3 | 同上 | `loadLayeredConfigDocument()` 使用 combined stack；`SourcePath` 只取非 fragment；`FallbackKeys`/`OverriddenKeys` 包含 fragment |
| P1-4 | 同上 | fragment 顶层键白名单：只允许 `providers`、`provider_groups`；否则返回带文件路径的错误 |
| P1-5 | `internal/agentconfig/config_merged_document.go` | `MultiLayer()` 计入 fragment；`DefaultWriteLayer` 只从非 fragment 层选 |
| P1-6 | `internal/agentconfig/config.go` | 诊断字段保持（无需新增公开字段，必要时增加 `ProviderConfigPath` 辅助方法） |
| P1-7 | `internal/runtimeserver/config_snapshot.go` | 确认 `LoadRuntimeAgentConfig` 在 fragment 存在时使用 merged view |
| P1-8 | `internal/runtimeserver/config_document.go` | `Layers`/`Origins` 包含 fragment；`configDocumentLayers` 增加 section/fragment 信息 |
| P1-9 | `internal/agentconfig/config_layers_test.go` | 新增：用户/项目 fragment 合并、同层覆盖、项目层高于用户层、`null` 屏蔽、`${VAR}` 展开 |
| P1-10 | 新增 `provider_fragment_test.go` | 断言：`ConfigFilePath` 是主配置，不是 provider 文件；`Origins` 正确；非法顶层键报错 |
| P1-11 | `internal/agentconfig/config_layers.go` / `config.go` | 记录显式 `--config` 模式；修正 `ReloadGlobalConfig` 显式路径等于层路径时误走 layered 的问题 |
| P1-12 | `internal/runtimeserver/config_snapshot.go` / `cmd/runtime-server/main.go` | `LoadRuntimeAgentConfig` 增加 explicit 模式；runtime-server `--config` 只读该文件；`ConfigSourceSignatureFor` 同步 |

建议先实现一个“read-only MVP”做内部验证：

- 只让 `providers.yaml` 被发现、合并、展示；
- 写操作仍全部走原主配置（Phase 2 前不改写路由）；
- 通过 `aicli config` / runtime config document API 验证 provider 列表与 origins。

> **该阶段不要对外发布。** 用户一旦编辑 provider，写入仍会落到主配置；如果 provider 文件已经定义了同名 key，编辑看起来会“不生效”（fragment 覆盖主配置）。P1 只用于内部验证加载/合并/展示，P2 的写路由必须紧随其后。

验收：

```text
~/.aicli/config.yaml      = 通用配置
~/.aicli/providers.yaml   = providers + provider_groups
```

- `aicli chat` 能使用 fragment 中的 provider；
- `GET /api/runtime/config/document` 的 `layers` 含 provider 文件；
- `origins["providers.items.x"]` 指向 user-providers；
- `ConfigFilePath` / `SourcePath` 仍是 `~/.aicli/config.yaml`；
- runtime-server 的 provider 列表与配置页一致。

### Phase 2：写路由与首次创建（1–1.5 人日）

| 步骤 | 文件 | 具体改动 |
|---|---|---|
| P2-1 | `internal/agentconfig/config_write_route.go` | 新增 `isProviderSubtreeKey`、`allKeysAreProviderSubtree`、`providerFragmentForMainPath` |
| P2-2 | 同上 | `WriteTargetForKeys`：已有 origin 优先；新 provider key 路由 fragment；混合 key 不路由 fragment |
| P2-3 | 同上 | `chatUpdateWriteKeys` 移除 `providers.default_provider` |
| P2-4 | `internal/agentconfig/config_merged_document.go` | `targetFileFor` 复用新规则；新 provider key → fragment；`DefaultWriteLayer` 不选 fragment；`WriteLayerKindFor` 复用 provider 子树规则 |
| P2-5 | `internal/agentconfig/provider_proxy.go` | `SetGlobalProxyConfig` / `RemoveGlobalProxyConfig` 增加 `routeConfigWritePath(..., "providers.proxy")` |
| P2-6 | `internal/agentconfig/config_file_write.go` | 新增 fragment-aware 创建（最小骨架 + 0600），避免 `ensureStarterConfigAtPathLocked` 写全量 starter |
| P2-7 | `internal/agentconfig/provider_management.go` | `DeleteProvidersConfig` 对缺失 fragment 容忍；跨文件 `providers.default_provider` 独立清理或 blocked；`provider_groups` 同文件约束/警告 |
| P2-8 | `internal/agentconfig/provider_persistence.go` | 确认新 provider/模型写入使用路由；必要时显式传 provider 子树 keys |
| P2-9 | 新增 `provider_fragment_write_test.go` | 覆盖：已有 provider、新 provider、删除、启用/禁用、默认值、proxy、全局 proxy、混合 key、fragment 缺失 |
| P2-10 | `internal/agentconfig/chat_persistence_test.go` | 断言 chat 偏好只写主配置，provider 文件不出现 `aicli:` |

验收：

- `UpdateProviderConfig` 修改已有 provider → fragment；
- 新增 provider（fragment 存在）→ fragment；
- `SetDefaultProviderConfig` / provider login → fragment；
- `SetGlobalProxyConfig` → fragment；
- `UpdateAICLIChatPreferences` → 主配置；
- 混合 key 写入 → 主配置；
- 文件锁内读-改-写，无 `.tmp` 残留、无部分写入。

### Phase 3：入口适配（TUI / micro web / frontend / CLI）（1–1.5 人日）

#### 3A. aicli TUI

| 步骤 | 文件 | 改动 |
|---|---|---|
| P3A-1 | `cmd/aicli/commands/config_tui.go` | 头部显示 `Providers:` 路径与层来源 |
| P3A-2 | 同上 | 全局 proxy 写点验证路由；失败信息带目标文件 |
| P3A-3 | `config_tui_test.go` | 覆盖 provider 编辑/新增、全局 proxy、chat 偏好、reload |

#### 3B. micro web client

| 步骤 | 文件 | 改动 |
|---|---|---|
| P3B-1 | `cmd/aicli/commands/web_config_handlers.go` | `chatWebConfigSnapshot` 增加 `providers_config_path`、`config_layers`；更新注释（provider 文件） |
| P3B-2 | 同上 | 各 handler 继续传主配置路径；依赖 Phase 2 路由 |
| P3B-3 | 同上 | `fetch-models` / `probe-models` 明确只写 `model_cards.yaml`，不写 provider 文件 |
| P3B-4 | `web/js/config-admin.js` / `provider-editor.js` | 如需显示来源文件/层提示，读取新字段；错误 toast 显示目标文件 |
| P3B-5 | `web_config_handlers_test.go` | 覆盖保存/删除/启用/chat 默认/自动导入的落盘文件 |

#### 3C. 桌面 Frontend

| 步骤 | 文件 | 改动 |
|---|---|---|
| P3C-1 | `frontend/src/types/runtime/config.ts` | `RuntimeConfigDocument` 增加 `layers`/`origins`；新增 `RuntimeConfigDocumentLayer`（含可选 `section`/`fragment`） |
| P3C-2 | `backend-config-settings-page/` 层展示 | 增加 providers 层标签/i18n；impact 面板可显示 `path_layers` |
| P3C-3 | `domains/providers.ts` | 无需改写入路径；确认新建/删除 provider diff 正确 |
| P3C-4 | `domains/network-proxy.ts` | 确认 `providers.proxy` 走 diff 路由 |
| P3C-5 | `domains/provider-groups.ts` | 确认 `provider_groups` 路由；文档提示与 providers 同文件 |
| P3C-6 | `use-config-actions.ts` | source 模式 warning 文案：保存的是合并文档，后端按层分摊 |
| P3C-7 | 前端测试 | `vitest`：provider domain、proxy、provider groups、save actions；`tsc` 类型检查 |

#### 3D. CLI / chat

| 步骤 | 文件 | 改动 |
|---|---|---|
| P3D-1 | `cmd/aicli/commands/config.go` | 结构化输出增加 `config_layers` / provider 路径（可选） |
| P3D-2 | `provider_login.go` / `chat_model_picker.go` / `chat_provider_refresh_command.go` | 确认写路由；补测试 |
| P3D-3 | `cmd/aicli/commands/config_path_write.go` | 确认 `ensureWritableAICLIConfigPath` 永远拿主配置；不把 fragment 当 starter 目标 |

### Phase 4：热重载与诊断（0.5 人日）

| 步骤 | 文件 | 改动 |
|---|---|---|
| P4-1 | `internal/runtimeserver/config_external_reload.go` | `configSourceWatchPaths` 在 merge on、provider 文件未禁用且非显式 `--config` 时加入 provider 文件路径 |
| P4-2 | `cmd/aicli/commands/chat_config_refresh.go` | fingerprint 在 merge on、provider 文件未禁用且 path 为主配置层时加入 provider 文件 |
| P4-3 | `internal/agentconfig/config_layers.go` | `describeConfigLayers` 显示 fragment |
| P4-4 | `cmd/aicli/commands/doctor*` | 诊断：provider 文件存在性、origin、重复定义、`AICLI_PROVIDERS_FILE=off` |
| P4-5 | 测试 | 外部编辑 fragment 触发 runtime/CLI 重载；签名变化；fingerprint 变化 |

### Phase 5：迁移命令（0.5–1 人日）

新增命令：

```text
aicli config split-providers [--write] [--keep-main|--prune-main] [--project] [--merge] [--json]
```

| Flag | 默认 | 语义 |
|---|---|---|
| `--write` | false（dry-run） | 实际写盘；不传只输出计划 |
| `--keep-main` | true | 保留主配置副本，便于回滚 |
| `--prune-main` | false | 与 `--keep-main` 互斥；从主配置 owner 层删除对应 key |
| `--project` | false | 迁移到项目层 `./.aicli/providers.yaml`；默认 user 层 |
| `--merge` | false | 目标 provider 文件已存在且非空时，允许合并而不是拒绝 |
| `--json` | false | 输出结构化计划/结果 |

> `--keep-main` 与 `--prune-main` 互斥；未显式传 `--prune-main` 时默认 `--keep-main=true`。

行为：

1. 读取合并视图与 origins；
2. 计算需要迁移的 `providers` / `provider_groups` key；
3. 默认 `--dry-run`（不写盘），输出计划：
   - 目标文件路径；
   - 将写入的 provider 名；
   - 将从哪些主配置层移除哪些 key；
   - 冲突/跳过项；
4. `--write`：
   - 先写 `providers.yaml`（0600，原子写）；
   - 重新加载校验；
   - 默认 `--keep-main`：保留主配置副本（provider 文件更高，立即生效，便于回滚）；
   - `--prune-main`（显式）：从主配置的 owner 层删除对应 key；
   - 失败时保留备份文件（`.bak`）并输出已写/未写文件；
5. `--project` 指定项目层；默认 user 层；
6. `--json` 输出结构化结果。

约束：

- 目标 provider 文件已存在且非空时，默认拒绝覆盖；`--merge` 显式合并；
- 不同主配置层拥有同一 provider key 时，按 origins 逐层处理；
- 迁移命令本身不修改 `auth.json`；
- 迁移后 doctor 提示重复定义直至 `--prune-main` 完成。

### Phase 6：文档、灰度与回滚（0.5–1 人日）

文档：

- 更新 `docs/config/aicli-config-load-save.md`：
  - 修正 merge 默认值（代码默认 on）；
  - 增加 provider 文件层栈、优先级、写路由、迁移命令；
- 更新 `docs/aicli/install.md` / `docs/aicli/faq.md`：
  - provider 文件示例；
  - `api_key_ref` 推荐；
  - `--config` / merge off 的兼容说明；
- 更新 help 文案：
  - `ConfigSearchSummary` 增加 provider 文件说明（不混入主搜索顺序）；
  - `aicli config --help` 提示 `split-providers`。

灰度：

1. 只读合并版本先发布，观察 origins/诊断；
2. 写路由版本默认启用，但保留 `AICLI_PROVIDERS_FILE=off`；
3. 迁移命令默认 dry-run；
4. 回滚：删除 `providers.yaml` 或将 `AICLI_PROVIDERS_FILE=off`，主配置仍保留（`--keep-main`）时立即恢复。

---

## 6. 关键实现伪代码

### 6.1 层栈插入

```go
func configLayerStackWithFragments() []ConfigLayer {
    main := ConfigLayerStack()
    out := make([]ConfigLayer, 0, len(main)+2)
    for _, layer := range main {
        out = append(out, layer)
        // 在 user 主配置候选结束后插入 user providers；
        // 在 project 主配置候选结束后插入 project providers。
        if shouldInsertUserProviders(layer, main) {
            out = append(out, providerLayer(LayerKindUserProviders, userProvidersPath()))
        }
        if shouldInsertProjectProviders(layer, main) {
            out = append(out, providerLayer(LayerKindProjectProviders, projectProvidersPath()))
        }
    }
    return out
}
```

> 实现时不要依赖循环内“最后一个 name”判断出错；可以先按 `Kind` 分组构造，再显式拼接：`portable -> legacy -> user main -> user providers -> project main -> project providers`。

### 6.2 SourcePath / DefaultWriteLayer

```go
func sourcePathFor(layers []ConfigLayer) string {
    for i := len(layers) - 1; i >= 0; i-- {
        if layers[i].Present && !layers[i].Fragment {
            return layers[i].Path
        }
    }
    return ""
}

func writableMainLayer(layers []ConfigLayer) (ConfigLayer, bool) {
    for i := len(layers) - 1; i >= 0; i-- {
        if layers[i].Present && !layers[i].ReadOnly && !layers[i].Fragment {
            return layers[i], true
        }
    }
    // fallback 到 user 主配置候选
    ...
}
```

### 6.3 路由判定

```go
func (c *Config) WriteTargetForKeys(keys ...string) string {
    if c == nil || c.ConfigMergeMode != MergeModeOn || len(keys) == 0 {
        return ""
    }
    // 混合 key（provider + 非 provider）永远走主配置，避免 aicli.chat 落 fragment。
    if hasProviderAndNonProviderKeys(keys) {
        return ""
    }

    target := ""
    for _, key := range keys {
        file := c.originFileFor(key)
        if file == "" {
            continue
        }
        if target == "" || c.layerComesAfter(file, target) {
            target = file
        }
    }
    if target != "" {
        return target
    }

    // 新 key：只有全部属于 provider 子树时才考虑 fragment。
    // 注意：即使 ConfigOriginFiles 为空（空 providers.yaml + 全新 provider），
    // 也必须允许 subtree 规则把新 provider 路由到已存在的 fragment。
    if !c.allKeysAreProviderSubtree(keys) {
        return ""
    }
    // mainPath 由调用方传入的 configPath 推断层，或由 ConfigFilePath 推断
    return c.providerFragmentForMainPath(c.ConfigFilePath)
}
```

> 注意：`WriteTargetForKeys` 当前没有 mainPath 参数，需要给 `routeConfigWritePath` 增加内部 helper，或在 `Config` 上记录当前主路径。推荐新增 `WriteTargetForKeysInFile(mainPath string, keys ...string)`，保持旧函数兼容。

### 6.4 文档 diff 目标

```go
func (m *MergedConfigDocument) targetFileFor(dottedPath, fallback string) string {
    if target := m.router().WriteTargetForKeysInFile(fallback, dottedPath); target != "" {
        return target
    }
    if !m.isReadOnlyLayer(fallback) {
        return fallback
    }
    return m.DefaultWriteLayer.Path
}
```

对 `providers.items.<new>`、`providers.proxy`、`provider_groups`：
- 如果同层 fragment 存在 → 返回 fragment；
- 否则返回 fallback（主配置）。

### 6.5 fragment 首次创建

```go
func ensureProviderFragmentAtPathLocked(path string) error {
    if fileExists(path) {
        return nil
    }
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
        return err
    }
    skeleton := []byte("providers:\n  items: {}\n")
    return writeFileAtomicMode(path, skeleton, 0o600)
}
```

只在“路由目标确定为 fragment 且文件恰好缺失”时调用；不得复用 `ensureStarterConfigAtPathLocked`。

---

## 7. 测试验证

### 7.1 测试分层

| 层级 | 范围 | 工具 |
|---|---|---|
| L1 单元测试 | agentconfig 层栈、origins、写路由、fragment 创建 | `go test` |
| L2 命令/处理器测试 | TUI、CLI provider 命令、micro web handlers、config document handlers | `go test` |
| L3 运行时集成 | runtime-server 加载、热重载签名、配置文档 API | `go test` + 临时 HOME/cwd |
| L4 前端 | 设置页 provider/proxy/groups 域、保存 action、类型 | `vitest` / `tsc` / `eslint` |
| L5 手工 E2E | CLI/TUI/micro web/frontend 四条入口 + 真实文件检查 | 手工/脚本 |

### 7.2 必测用例

#### A. 加载与优先级

| ID | 场景 | 预期 |
|---|---|---|
| T-L1 | user `config.yaml` + user `providers.yaml` | provider 从 fragment 加载；`ConfigFilePath` = `config.yaml` |
| T-L2 | 同层 `config.yaml` 与 `providers.yaml` 都定义 `providers.items.p` 的不同字段 | 深合并；provider 文件字段覆盖同名字段 |
| T-L3 | user providers + project config 都定义同一 provider | project config 胜出（项目层高于用户层） |
| T-L4 | project providers 显式 `providers.items.p: null` | 合并结果删除该 provider（屏蔽下层） |
| T-L5 | provider 文件含 `${PROVIDER_KEY}` / `${VAR:-default}` | 展开后再解码；未定义变量保持现有语义 |
| T-L6 | provider 文件顶层含 `aicli:` | 加载失败，错误包含文件路径和非法键 |
| T-L7 | 只有 `providers.yaml`、没有主配置 | 启动创建主 starter；provider 从 fragment 加载；`ConfigFilePath` = 主 starter |
| T-L8 | user 主配置目录不可写、project provider 可写 | 写路由不落到只读/不存在层；错误清晰 |
| T-L9 | `--config <主配置路径>` 且 fragment 存在 | fragment 被忽略；启动与写入后 reload 都保持显式单文件（覆盖 `ReloadGlobalConfig` 歧义） |
| T-L10 | `AICLI_CONFIG_MERGE=off` 且 fragment 存在 | fragment 被忽略 |
| T-L11 | `AICLI_PROVIDERS_FILE=off` 且 fragment 存在 | fragment 被忽略（读写都是） |
| T-L12 | `MergedConfigDocument.MultiLayer()` | fragment 存在时为 true；runtime 使用合并视图 |
| T-L13 | `provider_groups` 同时存在于主配置与 fragment | 数组整体替换（fragment 版本生效），不会拼接；迁移需完整复制 |
| T-L14 | runtime-server `--config <主配置路径>` 且 fragment 存在 | 只读显式文件，不加载其它层/fragment；signature 覆盖显式文件 + 预设层 |

#### B. 写入路由

| ID | 场景 | 预期 |
|---|---|---|
| T-W1 | 更新 fragment 中的已有 provider | 只改 `providers.yaml`，主配置不变 |
| T-W2 | 更新主配置中的已有 provider（fragment 未定义） | 只改主配置，不静默迁移 |
| T-W3 | 新增 provider，user fragment 存在 | 写入 user `providers.yaml` |
| T-W3b | 新增 provider，fragment 存在但为空（无任何 origin） | 仍写入 fragment（不能因 `ConfigOriginFiles==0` 提前返回） |
| T-W4 | 新增 provider，无 fragment | 写入主配置（旧行为） |
| T-W5 | 新增 project provider，仅 user fragment 存在 | 写入 project 主配置，不跨层写 user 文件 |
| T-W6 | 删除 fragment 中的 provider | 从 fragment 删除；默认值按 origin 处理 |
| T-W6b | 删除 provider，`providers.default_provider` 在主配置而 provider 在 fragment | 跨文件清除/替换默认值，或显式 blocked；不得留下悬空默认值 |
| T-W7 | `SetProvidersEnabledConfig` | 写 provider 所属文件 |
| T-W8 | `SetDefaultProviderConfig` | `providers.default_provider` 写 provider 文件（若 provider 已在 fragment） |
| T-W9 | `SetGlobalProxyConfig` / `RemoveGlobalProxyConfig` | 写 provider 文件 |
| T-W10 | `SetProviderProxyConfig` / `RemoveProviderProxyConfig` | 写 provider 所在文件 |
| T-W11 | `UpdateAICLIChatPreferences`（含 DefaultProvider） | 只写主配置的 `aicli.chat`；provider 文件无 `aicli:` |
| T-W12 | 一次写入同时含 `providers.items.x` + `aicli.chat` | 写主配置，不落 fragment |
| T-W13 | 写入目标 fragment 在加载后被删除 | 创建最小骨架或明确失败；不写全量 starter |
| T-W14 | 并发两个进程更新不同 provider | 文件锁下串行，无丢更新 |
| T-W15 | `DeleteProvidersConfig` + `provider_groups` 同文件 | 级联删除引用；空 group 处理符合现有语义 |
| T-W16 | `DeleteProvidersConfig` + `provider_groups` 在主配置（未迁移） | 实现必须显式返回 blocked 或 warning，不得静默漏删；首版推荐 blocked + 提示先迁移 |

#### C. 配置文档 / Web 保存

| ID | 场景 | 预期 |
|---|---|---|
| T-D1 | `ApplyMergedDocumentChanges` 修改 `providers.items.p.model_capabilities` | 写 fragment，主配置不变 |
| T-D2 | 前端新增 provider（无 origin） | `targetFileFor` → fragment |
| T-D3 | 前端删除 provider | fragment 写显式删除（或重写文档），主配置不变 |
| T-D4 | 前端修改 `providers.proxy` | fragment |
| T-D5 | 前端修改 `provider_groups` | fragment（同文件约束） |
| T-D6 | 前端修改 `aicli.chat` | 主配置 |
| T-D7 | `LoadDocument` 的 `layers` / `origins` | 含 fragment；provider key origin 正确 |
| T-D8 | `PathLayers` 归因 | provider 路径显示 providers kind |
| T-D9 | 一次保存跨 fragment + 主配置 | 两文件都正确；未触碰内容保留 `${VAR}`/注释 |
| T-D10 | fragment 只读/非法 | 保存失败，错误包含目标文件 |

#### D. 热重载与可观测

| ID | 场景 | 预期 |
|---|---|---|
| T-H1 | 编辑 `providers.yaml` 后 runtime-server 外部重载 | 签名变化，配置重载 |
| T-H2 | 编辑 `providers.yaml` 后 CLI 下一 turn | `chatConfigFingerprintForPath` 变化，`refreshChatConfigIfChanged` 重载 |
| T-H3 | `describeConfigLayers` / doctor | 显示 provider 文件与 origin |
| T-H4 | 主配置与 provider 文件重复定义 | doctor warning / `OverriddenKeys` 包含相关 key |

#### E. TUI

| ID | 场景 | 预期 |
|---|---|---|
| T-S1 | TUI 编辑已有 provider | 写 fragment；reload 后显示新值 |
| T-S2 | TUI 新增 provider（fragment 存在） | 写 fragment |
| T-S3 | TUI 全局 proxy | 写 fragment |
| T-S4 | TUI chat 默认偏好 | 写主配置；fragment 不变 |
| T-S5 | TUI 头部显示 | `Config:` 主配置 + `Providers:` fragment（或 inline） |
| T-S6 | TUI 写失败 | 错误包含实际目标文件 |

#### F. micro web client

| ID | 场景 | 预期 |
|---|---|---|
| T-S7 | `POST /web/api/config/providers` 更新 | fragment；快照刷新后值正确 |
| T-S8 | 新增 provider | fragment |
| T-S9 | 删除 provider | fragment；默认值清理正确 |
| T-S10 | 启用/禁用 | fragment |
| T-S11 | `POST /web/api/config/chat` | 主配置 `aicli.chat`；fragment 无 `aicli:` |
| T-S12 | auto-import | provider 在 fragment；key 在 `auth.json` |
| T-S13 | fetch-models / probe-models | 仅 `model_cards.yaml` 变化，provider 文件不变 |
| T-S14 | 快照 `providers_config_path` / `config_layers` | 正确显示来源 |

#### G. 桌面 Frontend

| ID | 场景 | 预期 |
|---|---|---|
| T-F1 | 设置页加载 | layers 含 providers；类型无 TS 错误 |
| T-F2 | provider 新建/编辑/删除草稿 | `draftParsed` 正确；保存后后端落盘正确 |
| T-F3 | proxy / provider groups 草稿 | 保存后落盘正确 |
| T-F4 | source 模式保存 | 合并文档 diff 正确分摊；warning 显示 |
| T-F5 | impact 面板 | provider 变更显示 hot reload / 目标文件（若实现） |

### 7.3 测试命令

后端（从 `backend/` 目录）：

```bash
go test ./internal/agentconfig/... -run 'ProviderFragment|ProviderWrite|ChatPreference|MergedDocument'
go test ./cmd/aicli/commands/... -run 'Config|Provider|Web|Chat'
go test ./internal/runtimeserver/... -run 'ConfigDocument|ConfigSource|RuntimeAgent'
go test ./internal/api/runtimeapi/...
go test ./internal/agentconfig/... ./cmd/aicli/commands/... ./internal/runtimeserver/... ./internal/api/runtimeapi/...
```

前端（从 `frontend/` 目录）：

```bash
pnpm test
pnpm lint
pnpm build
```

> 仓库使用 `pnpm-lock.yaml`；若本机只有 npm，可等价使用 `npm test`、`npm run lint`、`npm run build`（以 CI 实际包管理器为准）。

若只跑相关前端测试：

```bash
pnpm test -- backend-config-settings-page
pnpm test -- runtime-provider-config-utils
```

集成/手工验证脚本建议：

```bash
# 1) 隔离环境，避免污染真实 ~/.aicli
export HOME=/tmp/aicli-split-home
export USERPROFILE=/tmp/aicli-split-home
mkdir -p "$HOME/.aicli"
cd /tmp/aicli-split-project
mkdir -p .aicli

# 2) 写入主配置与 provider 文件
#    主配置只放 aicli.chat；provider 文件放 providers/provider_groups
#    详见附录 A/B 示例

# 3) 只读检查（从仓库根目录进入 backend）
cd /path/to/ai-agent-runtime/backend
go run ./cmd/aicli config --no-tui --json | jq '.'

# 4) TUI 手工编辑 provider / proxy / chat 默认值
go run ./cmd/aicli config

# 5) 检查文件归属
cat "$HOME/.aicli/config.yaml"
cat "$HOME/.aicli/providers.yaml"

# 6) runtime-server 配置文档
curl -s http://127.0.0.1:<port>/api/runtime/config/document | jq '.layers, .origins'

# 7) 外部编辑 provider 文件，观察热重载
```

### 7.4 手工 E2E 清单

1. **启动读取**
   - 只有主配置 → 行为与当前一致；
   - 主配置 + user provider 文件 → `aicli config --json` 能看到 provider；
   - 主配置 + project provider 文件 → 项目 provider 覆盖用户 provider；
   - 只有 provider 文件 → 自动创建主 starter，provider 仍可用。

2. **CLI / chat**
   - `aicli provider list` 显示 fragment provider；
   - `aicli login` 新增 provider → `providers.yaml`；
   - `/provider refresh` 拉取模型 → `supported_models` 在 fragment；
   - `/model` 选择默认模型 → 只写 workspace `chat-prefs.yaml`（不碰全局）。

3. **TUI**
   - 编辑 provider、启用/禁用、设默认、provider proxy、全局 proxy、chat 默认；
   - 每次操作后检查两个文件归属；
   - 退出重进 TUI，值保持。

4. **micro web client**
   - 打开配置页，provider 列表来自 fragment；
   - 新增/编辑/删除/启用/禁用；
   - 保存 chat 默认值；
   - 自动导入；
   - 刷新模型列表（确认只写 model cards）；
   - 页面显示来源文件（若实现）。

5. **runtime-server + 桌面前端**
   - 设置页加载 merged 文档；
   - provider 新建/编辑/删除；
   - proxy / provider groups；
   - 保存后检查 `config.yaml` 与 `providers.yaml`；
   - impact 面板 hot reload 状态；
   - 重启 runtime-server 后配置仍一致。

6. **兼容/回滚**
   - `--config` 显式路径 → provider 文件被忽略；
   - `AICLI_CONFIG_MERGE=off` → 单文件行为；
   - `AICLI_PROVIDERS_FILE=off` → 即使文件存在也忽略；
   - 删除 provider 文件 → 回退到主配置（若 `--keep-main`）。

### 7.5 负向与回归测试

- provider 文件空文件：视为 `{}`，不报错；
- provider 文件只含 `providers: {}`：合法；
- provider 文件含 `providers.items: []`：类型错误，报错清晰；
- provider 文件顶层 `aicli`：报错；
- provider 文件权限不足：读写失败信息包含路径；
- Windows 路径/大小写：`providers.yaml` 大小写不敏感比较、路径规范化；
- 并发写 provider + chat：无丢更新；
- `provider_groups` 引用不存在 provider：合并后校验错误（与现有语义一致）；
- `provider_groups` 引用主配置 provider：合并后校验通过；
- preset 提供 provider、用户 fragment 覆盖：用户值优先；
- `AICLI_CONFIG_MERGE=dry-run`：不启用 fragment 写入（保持 dry-run 只读语义；如决定 dry-run 也读取 fragment，需在测试中明确）。

### 7.6 测试通过标准

- 所有新增测试通过；
- 现有配置层/写路由/provider 管理/Web 配置测试不回归；
- `go test ./...`（至少受影响包）全绿；
- `pnpm lint` / `pnpm build` 通过；
- 手工 E2E 四条入口均验证；
- 测试日志与文件 diff 作为验收附件（PR 描述）。

---

## 8. 迁移、兼容与回滚

### 8.1 兼容矩阵

| 场景 | 行为 |
|---|---|
| 无 provider 文件 | 完全不变 |
| provider 文件存在 + merge on | 合并读取；provider 写入按新路由 |
| provider 文件存在 + merge off | 忽略 provider 文件（文档/help 明示） |
| `--config <path>` | 单文件；忽略 provider 文件 |
| `AICLI_PROVIDERS_FILE=off` | 忽略 provider 文件 |
| provider 文件只读 | 读取可用；写入报错，不回退主配置（避免静默分叉） |
| provider 文件被删除 | 运行时回退主配置；下次写入按当前存在性路由 |
| 主配置与 provider 文件重复定义 | 高层胜出；doctor warning |

### 8.2 迁移流程

推荐两种迁移方式：

**方式 A：复制后切换（安全）**

1. `aicli config split-providers --write --keep-main`
2. 验证 CLI/TUI/micro web/frontend 都从 provider 文件读取；
3. 观察一个版本周期；
4. 执行 `aicli config split-providers --prune-main` 清理主配置残留；
5. 删除备份文件。

**方式 B：直接清理（干净）**

1. `aicli config split-providers --write --prune-main`
2. 重新加载并验证；
3. 保留 `.bak` 直到确认。

迁移命令需要处理：

- 多主配置层（user/project）中同一 provider 的重复定义；
- `${VAR}` 占位符保留（使用 YAML node 写回，不做全量 marshal 重排）；
- `provider_groups` 与 providers 一起迁移；
- `providers.default_provider` 与其他 provider 全局设置一起迁移；
- `auth.json` 不迁移、不删除；
- 失败时输出已写/未写文件，支持手工恢复。

### 8.3 回滚

1. **紧急**：设置 `AICLI_PROVIDERS_FILE=off`，重启进程 → 立即回到主配置；
2. **常规**：保留 `--keep-main` 时，删除 `providers.yaml` 即可回退；
3. **代码回滚**：新增的层栈/写路由是可选路径；旧版本不读 provider 文件，主配置若保留副本则完全兼容；
4. 迁移命令写 `.bak`；回滚时用备份覆盖主配置（文件锁内）。

---

## 9. 风险与缓解

| 风险 | 影响 | 缓解 |
|---|---|---|
| `SourcePath`/`ConfigFilePath` 被 fragment 污染 | starter 写错文件、reload 单文件丢 provider、错误提示错位 | `SourcePath` 只取非 fragment；测试 T-L1/T-L7；代码审查重点 |
| `aicli.chat` 被写进 provider 文件 | 配置分裂、后续读取异常 | 修正 `chatUpdateWriteKeys`；混合 key 禁止 fragment；测试 T-W11/T-W12 |
| 首次写入创建全量 starter | provider 文件出现 `aicli:`/`skills_runtime:` | fragment-aware 最小骨架；测试 T-W13 |
| `provider_groups` 跨文件级联漏删 | 删除 provider 后组引用悬空 | 首版强制同文件 + 迁移；doctor warning；后续跨文件清理 |
| 删除 provider 时默认值跨文件悬空 | 有效默认值指向已删除 provider，启动/路由异常 | `DeleteProvidersConfig` 按 default_provider origin 跨文件清理或 blocked；测试 T-W6b |
| 同一 provider 的 item/default 分散在不同文件 | 单次更新可能把部分字段 pin 到更高层，形成难排查的覆盖 | 迁移保证同文件；doctor warning；测试 T-W8/T-W16 |
| runtime-server `--config` 语义修复改变既有隐式合并行为 | 显式指定 `--config` 的部署可能少加载其它层 | 发布说明 + 诊断提示；若短期内必须保持旧行为，提供兼容开关（默认建议修复，避免入口分叉） |
| 全局 proxy 未路由 | 写入主配置但被 fragment 遮住 | P2-5 补路由 + T-W9 |
| 热重载遗漏 | 外部编辑 provider 文件不生效 | watch/fingerprint 补路径 + T-H1/T-H2 |
| Web/前端 layers 未知 kind | UI 显示异常/类型错误 | API 加 section/fragment；前端类型与 i18n；测试 T-F1 |
| 明文 API key 权限 0644 | 凭据泄露风险 | fragment 0600；推荐 `api_key_ref`；T-S12 |
| 迁移部分失败 | 配置分叉 | 先写 provider、再删主；保留 `.bak`；dry-run 默认 |
| 并发写跨文件 | 丢更新 | 同一文件锁；迁移命令按文件加锁；T-W14 |
| preset 覆盖用户值 | provider 被预设改回 | 保持“用户配置优先”现有语义；T-L 预设用例 |
| fragment 过大导致加载慢 | 启动变慢 | 单文件通常很小；超过阈值可后续做 per-provider 拆分；监控加载耗时 |

---

## 10. 验收标准（DoD）

功能：

- [ ] `~/.aicli/providers.yaml` / `./.aicli/providers.yaml` 被发现并合并；
- [ ] 同层 provider 文件覆盖主配置；项目层高于用户层；
- [ ] provider 文件不存在时行为完全不变；
- [ ] CLI、TUI、micro web、桌面前端、runtime-server 看到同一份 provider 列表；
- [ ] 已有 provider 更新/删除/启用/默认/模型/代理写回正确文件；
- [ ] 新 provider 在 fragment 存在时写 fragment；
- [ ] `aicli.chat` 等通用配置永不写入 provider 文件；
- [ ] `provider_groups` 与 providers 同文件，迁移后级联删除正确；
- [ ] 外部编辑 provider 文件触发热重载/指纹刷新；
- [ ] `--config` / merge off / `AICLI_PROVIDERS_FILE=off` 行为符合兼容矩阵。

质量：

- [ ] 新增测试覆盖 7.2 的 A–G 全部关键用例；
- [ ] 现有 agentconfig / provider / web / runtime config 测试无回归；
- [ ] `go test ./...`（受影响包）通过；
- [ ] 前端 `pnpm lint` / `pnpm test` / `pnpm build` 通过；
- [ ] Windows 路径与大小写测试通过；
- [ ] 文件锁/并发测试通过；
- [ ] 无 `.tmp` 残留、无部分写。

安全：

- [ ] provider 文件创建权限 0600；
- [ ] `api_key_ref` / `auth_ref` 路径不回退明文；
- [ ] 错误信息不泄露密钥；
- [ ] `AICLI_PROVIDERS_FILE=off` 可紧急关闭。

文档：

- [ ] `docs/config/aicli-config-load-save.md` 更新（含 merge 默认值修正）；
- [ ] `docs/aicli/install.md` / `faq.md` 增加 provider 文件示例与兼容说明；
- [ ] `aicli config --help` / doctor 输出更新；
- [ ] 迁移命令帮助与示例。

可观测：

- [ ] `ConfigLayers` / `Origins` / `PathLayers` 在 CLI JSON 与 runtime config document 中可见；
- [ ] doctor 能指出 provider 文件、重复定义、回退原因；
- [ ] 错误信息包含实际目标文件路径。

性能：

- [ ] provider 文件加载不引入显著启动开销（单文件 KB 级）；
- [ ] 热重载签名只 stat 必要文件；
- [ ] 写操作不产生 O(n) 全量重写（除 fragment 本身需要重排）。

---

## 11. 附录

### 11.1 关键文件/函数索引

| 领域 | 文件/函数 |
|---|---|
| 层栈 | `internal/agentconfig/config_layers.go`：`ConfigLayerStack`、`RuntimeConfigLayerStack`、`loadLayeredDocumentFor`、`InitGlobalConfigLayered`、`ReloadGlobalConfig` |
| 合并文档 | `internal/agentconfig/config_merged_document.go`：`LoadMergedConfigDocument`、`ApplyMergedDocumentChanges`、`targetFileFor`、`WriteLayerKindFor` |
| 写路由 | `internal/agentconfig/config_write_route.go`：`WriteTargetForKeys`、`routeConfigWritePath`、`chatUpdateWriteKeys`、`providerUpdateWriteKeys` |
| provider 持久化 | `provider_persistence.go`、`provider_management.go`、`provider_proxy.go` |
| chat 偏好 | `chat_persistence.go`、`chat_persistence_workspace.go` |
| 配置文档 API | `internal/runtimeserver/config_document.go`、`internal/api/runtimeapi/config_document_handlers.go` |
| runtime 加载 | `internal/runtimeserver/config_snapshot.go`、`config_external_reload.go` |
| TUI | `cmd/aicli/commands/config_tui.go` |
| micro web | `cmd/aicli/commands/web_config_handlers.go`、`cmd/aicli/commands/web/js/{config-admin,provider-editor,provider-import}.js` |
| 桌面前端 | `frontend/src/components/workspace/settings/backend-config-settings-page/`、`frontend/src/api/runtime/config.ts` |
| CLI 启动 | `cmd/aicli/main.go`、`cmd/aicli/commands/config_path_write.go` |
| 迁移命令 | 新增 `cmd/aicli/commands/config_split_providers.go`（建议） |

### 11.2 示例：主配置

```yaml
# ~/.aicli/config.yaml
aicli:
  chat:
    stream: true
    default_provider: deepseek
    default_model: deepseek-chat
skills_runtime:
  enabled: true
# 不再包含 providers / provider_groups
```

### 11.3 示例：provider 文件

```yaml
# ~/.aicli/providers.yaml
providers:
  default_provider: deepseek
  headers:
    X-Client: aicli
  proxy:
    enabled: true
    http: http://127.0.0.1:7890
  items:
    deepseek:
      enabled: true
      protocol: openai
      base_url: https://api.deepseek.com
      api_key_ref: deepseek
      default_model: deepseek-chat
      supported_models:
        - deepseek-chat
        - deepseek-reasoner
      model_capabilities:
        deepseek-chat:
          input_modalities: [text]
          max_context_tokens: 65536
provider_groups:
  - name: pool
    strategy: round_robin
    providers:
      - name: deepseek
        weight: 1
        enabled: true
```

### 11.4 开放问题（后续版本）

1. 是否支持 `aicli.providers.config_file` 显式指针（类似 `skills_runtime.config_file`）？
2. 是否支持每 provider 一个文件的 `providers.d/` 布局（便于分享/版本控制）？
3. 是否把 `retry` / `circuit_breaker` 也迁入 provider 文件（需要额外写路由）？
4. `provider_groups` 跨文件级联删除是否值得实现（首版用同文件约束）？
5. provider 文件版本字段（`version:`）用于未来 schema 迁移？
6. 是否在 `aicli doctor` 中提供 `--fix-provider-split` 自动修复重复定义？

### 11.5 评审检查清单

- [ ] 设计是否坚持“fragment 不参与 SourcePath/DefaultWriteLayer”？
- [ ] 是否确认 `chatUpdateWriteKeys` 的风险已修复？
- [ ] 是否确认 `SetGlobalProxyConfig` / `RemoveGlobalProxyConfig` 已路由？
- [ ] 是否确认首次写入不会落全量 starter？
- [ ] 是否确认 `provider_groups` 同文件约束/迁移路径？
- [ ] 是否确认 `--config` / merge off / kill switch 的测试覆盖？
- [ ] 是否确认 micro web / TUI / frontend / runtime-server 四条入口都有端到端测试？
- [ ] 是否确认 provider 文件权限 0600 与密钥不回显？
- [ ] 是否确认文档与 help 文案同步？