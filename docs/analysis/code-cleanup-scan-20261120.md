# 代码清理扫描（2026-11-20）

> 范围：`backend/`。初版为只读扫描；2026-11-20 已执行第一批低风险清理（见 §0.1）。
>
> 目标：找出可移除的过期代码、死代码、低价值测试和兼容层，并解释 `go test` 超时的结构原因。

## 0. 结论摘要

1. **代码本身可编译。** `GOMAXPROCS=2 go build -p 2 ./...` 通过（约 25s），因此当前的 `go test` 超时不是构建失败，而是测试面过大。
2. **测试规模是主要矛盾。** 当前有约 2,932 个 `_test.go`、706k 行测试代码、17,833 个 Test/Benchmark/Fuzz 函数。仅 `cmd/aicli/commands` 就有 641 个测试文件和 4,499 个测试函数。
3. **可以先做低风险清理。**
   - 4 个无生产引用或纯兼容包（约 1.4k 行，含测试）；
   - 3 个文件里的 5 个直接 `t.Skip(...)` 空测试，以及 1 个只有注释的空测试；
   - 281 个“生产代码中零引用”的未导出符号（约 2.5k 行）；
   - 214 个“只被测试引用”的生产符号（约 2.0k 行），需要迁移测试后删除；
   - 259 个 `internal/` 导出符号零引用（约 2.5k 行），需二次确认 API 意图。
4. **不能直接整块删除的兼容层：**
   - Win7：仍有独立发布工作流、`go.win7.mod`、57 个 `win7compat` Go 文件（约 12k 行），属于产品支持决策。
   - TUI legacy：文档记录约 150 个方法、4k+ 行的 fenced-dead 路径；plain/compat/pipe/win7 仍依赖部分实现，必须按模式退役。
   - 工具名/参数别名、`execution_mode=wait|sync`、`roles.*` 等是协议兼容面，需等 deprecation window。
5. **建议把“清理”拆成两条线：**
   - 生产线：删无引用包、死符号、测试专用 seam；
   - 测试线：把 live/E2E/soak/load/进程/PTY 测试移出默认 `go test ./...`，并按包分组。

### 0.1 第一批已执行（2026-11-20）

- 删除无引用/纯兼容包：
  - `internal/config/loader`（136 行）；
  - `internal/types/openai`（590 行）；
  - `internal/types/codex`（343 行，若后续重新接入 Codex 协议类型需从历史恢复）；
  - `cmd/aicli/ui/viewport`（测试已迁移到 `renderengine` 的 `*_compat_test.go`）。
- 删除无效测试：3 个直接 `t.Skip` 测试、1 个空测试。
- 删除/迁移 test-only 生产符号：
  - `renderChatRuntimeEvent`、`newChatSessionManager`、`waitUIActorIdle`、`enableUnifiedRendererWithWriter` 移入 `cmd/aicli/commands/chat_cleanup_test_helpers_test.go`；
  - `readInteractiveLine*` 三个测试入口移入 `cmd/aicli/ui/inputbox_editor_test_helpers_test.go`。
- 删除已确认无行为兼容项：
  - `WSConnector.Dialer`；
  - `RuntimeStoreConfig.DisableBackgroundMaintenance`；
  - `preferredUnionBranch`；
  - `LegacyToolNames`；
  - `MetadataGrantedBy`；
  - 4 个 adapter 的 `AccumulateStreamData`；
  - `SummarizeRenderedLayout` / `RenderedLayoutSummary`；
  - `MockEmbeddingGenerator` / `NewMockEmbeddingGenerator` 旧名称；
  - 三个无引用 startup selection wrapper 与 `hasPromptPreflightContextBudgetOverride`。
- 验证：
  - `GOMAXPROCS=2 go build -p 2 ./...` 通过；
  - `cmd/aicli/commands` 的 DirectWriter inventory 测试通过；
  - `cmd/aicli/ui` 的迁移测试、`cmd/runtime-server` connector 测试、`internal/{prompt,embedding,toolsurface,toolschema,logger,llm,gateway/agent,chat}` 定向测试通过。

### 0.2 第二批已执行（2026-11-20）

- 删除 `internal/api/runtimeapi/agent_context.go` 4 个零引用 helper：
  `summarizeAgentContextString`、`limitAgentContextStrings`、
  `copyAgentContextString`、`agentContextInt`。
- 删除 `internal/cacheanalytics/correlation.go` 4 个零引用方法及随之失效的
  `turnUser` / `turnOrder` / `turnSeen` 状态，`registerStarted` 去掉未使用的
  `sessionID` 参数。
- 删除 `internal/toolkit/tools/file_encoding.go` 的 3 个零引用包装与 2 个未使用
  常量，保留仍然活跃的 UTF-8 BOM / UTF-16LE 路径。
- 删除 `cmd/aicli/commands/chat_debug.go` 中 5 个已被
  `*WithReadWindow` 取代的旧 wrapper。
- 删除 `cmd/aicli/commands/agent_stdio_question.go` 中两个已被
  `internal/acp` 接口取代的本地 requester interface 类型。
- 验证：`go build -p 2 ./...`、`internal/cacheanalytics` 全包测试、
  `cmd/aicli/commands` 的 DirectWriter/Debug 定向测试通过。

### 0.3 第三批已执行（2026-11-20）

- 清理 `internal/toolkit/tools` 中零引用小包装：
  - `shellCommandTimeoutError`、`searchPathGlobLiteralToken`；
  - `codeExploreBelow`、`newCodeToolBase`、`codeParamBool`；
  - `defaultGlobLimit`、`normalizeGlobPattern`、`splitGlobSegments`、
    `containsDoubleStar`；
  - `boolParamValue`、`intParamValue`；
  - `SourcegraphTool.buildSearchURL`；
  - `toolResultFailure`、`viewCompactHeadLines`。
- 同步清理 `sourcegraph.go` 因此不再使用的 `net/url` import。
- 验证：`go build -p 2 ./...`、`internal/toolkit/tools` 定向测试通过。

### 0.4 第四批已执行（2026-11-20）

- envd 模型可见 schema 移除 deprecated `cwd` 属性（`shell` 与 `pty_start`），
  handler 仍接受 `cwd` 旧入参并保留冲突检测；新增 schema 防回归测试
  `TestEnvdSchemasExposeWorkdirNotCwd`。
- `Makefile` 默认 `test` 改为 `go test -short -p 1 ./...`，降低本地反馈回路的
  测试时长与内存峰值；新增 `test-full` 保留完整后端测试。
- 验证：`internal/remotetools` 定向测试通过。

### 0.5 第五批已执行（2026-11-20）

- 将默认 Windows 测试面中最重的 E2E 移入 `integration` tag：
  - `chat_tty_conpty_live_test.go`（5 个测试，570s 预算）；
  - `chat_residency_trim_conpty_test.go`（2 个测试，480s 预算）；
  - `chat_startup_normal_mode_e2e_test.go`（60s/30s 等待）；
  - `stdio_tree_windows_test.go`（`ping -n 600` 进程树测试）。
- `Makefile` 的 `test-full` 增加 `-tags integration`，完整目标仍可运行这些测试；
  默认 `test` 继续使用 `-short -p 1`，不再编译这批 Windows E2E。
- 验证：默认 commands 测试、`integration` tag 的 commands 编译与
  `integration` tag 的 transport 编译均通过。

## 1. 扫描方法与证据

- `go build -p 2 ./...`：通过。
- Go AST 标识符扫描：统计所有 `.go` 文件（生产 + 测试）中的符号定义和标识符引用，过滤常见接口方法名。
- `go list ./...` 反向导入图：找出无任何包导入的 package。
- `rg`：交叉检查直接跳过测试、长时间等待、外部进程、HTTP/PTY、兼容标记和文档引用。
- 仓库既有清理文档：`docs/aicli/tui-render-architecture.md`、`docs/plan/aicli-legacy-fallback-retirement-plan-20261008.md`、`docs/plan/aicli-tui-unified-render-architecture-refactor-plan.md`。

注意：AST 扫描不做类型检查，零引用结论对未导出符号可信；对导出 API、接口实现和反射/`go:linkname` 场景仍需人工复核。

## 2. P0：可直接删除或合并的候选

### 2.1 无生产引用的独立包

| 候选 | 证据 | 规模 | 风险/建议 |
| --- | --- | ---: | --- |
| `internal/config/loader` | 全仓无 import；`docs/plan/layered-config-merge-design-20260917.md` 已明确称其为死代码 | 136 行 | 低。直接删除。 |
| `internal/types/openai` | 全仓无 import（含测试） | 590 行 | 低。旧的协议结构副本，当前主线使用 `internal/types` 与 adapter。 |
| `internal/types/codex` | 全仓无 import（含测试）；文档只说明“结构已准备但未贯穿” | 343 行 | 中。若短期没有接入计划，删除；需要时再按协议重建。 |
| `cmd/aicli/ui/viewport` | 兼容别名包；生产代码仅 `renderengine` 使用，只有 2 个 UI 测试导入 | 369 行（含测试） | 低。把测试改用 `ui/renderengine`，删除该包并更新 `renderengine/doc.go` 注释。 |

合计约 1.4k 行，属于第一批最干净的删除。

### 2.2 已迁移/已废弃的直接跳过测试

以下测试函数体只有 `t.Skip(...)`，或跳过原因明确写着“已迁移/已被覆盖”：

| 文件 | 行 | 处理 |
| --- | ---: | --- |
| `cmd/runtime-server/connector_command_attention_test.go` | 411, 415 | 删除 2 个已迁移到 `internal/connectors/attention_test.go` 的测试。 |
| `internal/llm/hung_upstream_test.go` | 310 | 删除 `TestProviderRetriesTransientErrorsNormally`；注释说明已由 retry/gateway 测试覆盖。 |
| `internal/pkg/logger/logger_test.go` | 131 | 删除 Windows 上永久跳过的 `TestInitLogger_File_SkipOnWindows`；如需保护文件日志，另建可运行的平台测试。 |
| `internal/toolkit/tools/code_semantic_symbols_test.go` | 265–268 | 测试函数体只有注释，无调用/断言；直接删除。 |

数量不多，但属于确定的无效测试代码。

另外两项不建议直接删，但应处理：

- `internal/webui/handler_basepath_test.go` 的 2 个测试因 `internal/webui/dist` 只有 placeholder 而默认 skip。建议改用 `fstest.MapFS`，或加 `webui_assets` build tag；直接删除会丢失 forwarded-prefix 覆盖。
- 无断言但用于 nil-safe 冒烟的测试（如 `agentstatus/runtime_state_test.go`、`agent/loop_observe_test.go`、`memoryassembly/maintenance_test.go` 等）建议合并为表驱动 “nil inputs do not panic”，不要当空测试删除。

### 2.3 零引用未导出符号

扫描结果：**281 个未导出生产符号在全部 Go 代码中零引用，约 2,475 行**。

集中位置：

| 文件/包 | 数量 |
| --- | ---: |
| `cmd/aicli/commands/chat_debug.go` | 13 |
| `cmd/aicli/commands/chat_runtime_events.go` | 12 |
| `cmd/aicli/commands/chat_interaction.go` | 10 |
| `cmd/aicli/ui/fixed_bottom_surface.go` | 9 |
| `internal/connectors/providers/qqbot/interaction.go` | 7（常量，需确认协议保留意图） |
| `internal/api/runtimeapi/handler.go` | 7 |
| `cmd/aicli/commands/chat_actor_host.go` | 7 |
| `internal/toolkit/tools/file_encoding.go` | 5 |
| `internal/cacheanalytics/correlation.go` | 4 |
| `internal/api/runtimeapi/agent_context.go` | 4 |
| `cmd/aicli/commands/mcp_integration.go` | 4 |

典型且容易确认的例子：

- `cmd/aicli/commands/chat.go`：`selectProvider`、`selectModel`、`selectStreamMode` 三个只做转发的 wrapper；实际生产路径使用 `*WithReader`。
- `internal/agent/loop.go`：`hasPromptPreflightContextBudgetOverride` 全仓只有定义。
- `cmd/aicli/commands/chat_debug.go`：多组 `*Lines` 旧 wrapper，真正的 `*WithReadWindow` 版本仍在使用。
- `cmd/aicli/ui/fixed_bottom_surface.go`：`moveToPromptLocked`、`popupVisibleRowCountLocked`、`maxPopupRowsLocked` 等旧辅助方法。
- `internal/toolkit/tools`：多组 3 行转发函数和旧常量，例如 `newCodeToolBase`、`codeParamBool`、`normalizeGlobPattern`、`boolParamValue`。

建议按文件分批删除；每批执行 `gofmt`、`go build ./...` 和目标包测试。

### 2.4 只被测试引用的生产符号

扫描结果：**214 个生产符号仅被测试引用，约 1,997 行**。

优先处理以下“薄 wrapper / 测试 seam”：

| 符号 | 测试引用数 | 建议 |
| --- | ---: | --- |
| `chatInteractionCoordinator.waitUIActorIdle` | 326 | 移入 `_test.go` 或测试 helper。 |
| `newChatSessionManager` | 135 | 测试改调 `newChatSessionManagerWithRuntimeConfig`，删除 wrapper。 |
| `chatInteractionCoordinator.enableUnifiedRendererWithWriter` | 81 | 文档已标注 test-only；67 行实现可移入测试文件。 |
| `renderChatRuntimeEvent` | 73 | 测试改调 `renderChatRuntimeTimelineEvent(...).Line`，删除 3 行 wrapper。 |
| `readInteractiveLine` | 37 | 测试改用底层函数或测试 helper。 |
| `chatMCPCommandTextWithService` | 35 | 3 行 wrapper，测试直接调用底层。 |
| `chatInputQueue.routeLine` | 26 | 同上。 |
| `chatRuntimeEventBridge.replayEventLog` | 24 | 同上。 |
| `resetProcessFolderTrust`、各 `reset*ForTest` | 10–24 | 统一收进 `_test.go` 或测试 helper。 |
| `isSearchToolNoMatch` / `bashCommandFailureNextAction` | 8–14 | 若只用于测试，迁入测试或删除。 |

这一类不应直接“删完再跑测试”，而应先迁移调用点。收益是缩小生产表面，同时减少测试对内部实现的耦合。

### 2.5 已确认无行为的兼容字段/旧方法（跨包）

以下项目由 compat 扫描逐项交叉确认，可作为独立小批次；删除时注意同批更新测试和 freeze/契约白名单。

| 候选 | 位置 | 证据/动作 |
| --- | --- | --- |
| `EventPersistConfig.BatchingEnabled` / `AsyncDispatch` | `internal/config/manager.go:577-590` | 生产只用 `Disable`；两个字段仅测试/历史文档引用。删除字段并更新测试。注意不要动 `chat.EventPersistSettings.AsyncDispatch`。 |
| `DisableBackgroundMaintenance` | `internal/chat/session_runtime_store.go:337-340` | 生产无读取，仅测试设置；删除字段和测试设置。 |
| `WSConnector.Dialer` | `internal/gateway/agent/wsconn.go:36-37` | `Dial` 使用 `websocketDialer(cfg)`，从不读该字段；删除字段并检查 import。 |
| `preferredUnionBranch` | `internal/toolschema/unions.go:187-193` | 零调用，实际使用 `compatibleUnionBranch`；删除函数并修注释。 |
| `MockEmbeddingGenerator` / `NewMockEmbeddingGenerator` | `internal/embedding/index.go:96-110` | 生产已改用 `Local*`，旧名称仅测试使用；测试改名后删 alias。 |
| `SummarizeRenderedLayout` | `internal/prompt/layout_summary.go:118-120` | 仅测试调用；迁移到 `SummarizeInstructionMessages` 后删除。 |
| `LegacyToolNames` | `internal/skill/codex_model.go:274-287` | 全仓零调用；直接删除。 |
| `MetadataGrantedBy` | `internal/toolsurface/observation.go:44-48` | 已停止写入，仅测试断言其不存在；删常量并简化断言。 |
| `AccumulateStreamData` | `internal/llm/adapter/{anthropic,codex,gemini,openai}.go` | 四个 adapter 方法均零调用；整组删除，保留现有 streaming 状态机。 |
| `LegacyReserveState` + `LegacyReserveStateForTest` | `cmd/aicli/ui/renderengine/legacy_reserve.go`、`fixed_bottom_surface.go:159,505,1159,1319-1324` | 生产只清零，读取只来自测试；删类型/字段/accessor，测试改为 state-only 断言。 |
| assistant chrome 旧 helper | `cmd/aicli/ui/boundary/assistant_chrome.go`、`ui/message.go:273-287` | marker/format/strip/indent 多为测试专用；保留生产使用的 `AssistantContentIndent`，其余删除或移测试。 |
| bridge 旧 submit 包装器 | `cmd/aicli/commands/chat_runtime_events.go:3749-3866` | 生产已迁移到 `submitToolRequestedBlock` / `submitToolResultBlock`；删 4 个 bridge 包装器并同步 freeze 白名单。保留 `EventEncoder.SubmitToolResultDisplay`。 |
| `loadFromEnv` | `internal/config/loader/loader.go:99-104` | 空转兼容壳；随整个 `internal/config/loader` 包一起删。 |
| envd schema 的 `cwd` | `internal/remotetools/envd_process.go:55`、`envd_pty.go:208` | 文档要求 schema 只暴露 `workdir`；只从模型可见 schema 移除 `cwd`，保留解析兼容和冲突检测。 |

## 3. P1：需要小范围迁移或二次确认

### 3.1 `internal/` 导出符号零引用

扫描结果：**259 个 `internal/` 导出符号零引用，约 2,526 行**。集中在：

- `internal/skill` 35
- `internal/observability` 34
- `internal/agent` 15
- `internal/llm` 14
- `internal/pkg/logger` 10
- `internal/pkg/httpclient` 9
- `internal/memorystore` 9
- `internal/supervision` 8
- `internal/executor` 7
- `internal/agentconfig` 7

这些包不能被模块外导入（`internal/` 规则），所以“仓库内零引用”通常意味着可以删；但要先确认：
1. 是否是为即将接线的功能预留的 API；
2. 是否实现外部依赖的接口（如 `http.RoundTripper`、`net.Conn`、`sql.Scanner` 等）；
3. 是否是测试 fixture/生产观测 API。

建议做第二遍，按包删，而不是一次全仓删。

### 3.2 兼容别名/参数归一化

这些是刻意保留的兼容面，不应按“死代码”处理：

- `internal/toolregistry/registry.go`：`ls`、`read`、`bash`、`execute_shell_command`、`background_task`、`spawn_agent` 等现役别名；测试明确保护。
- `internal/toolargs/aliases.go`、`internal/tools/argument_aliases.go`：模型参数旧拼写归一化。
- `internal/remotetools`：`cwd` 是 `workdir` 的 deprecated alias。模型可见 schema 应只暴露 `workdir`；保留 handler 的 `cwd` 解析以兼容旧调用。
- `internal/agentconfig/config.go`：`roles.* -> task_types.*` 注释写明“alias kept for one release”。计划文档也明确“一个 release 后移除”；当前日期很可能已过窗口，但 `cfg.Roles` 仍是自定义 role 的兜底，不能直接删整个字段。
- `internal/subagentbatch/types.go`：`execution_mode=wait|sync` 旧值仍被接受，但会给出 deprecation notice。
- `internal/remotetargets/registry.go`：`deprecatedPolicy` 仅用于识别和警告已失效的 `policy:` 块。

建议：为这些项建立“兼容窗口到期”清单；到期后按版本删除，而不是零散清理。

### 3.3 开发/探针类命令

`cmd/` 下 25 个 main 包中，部分是一次性探针或测量入口，但不建议直接删：

- `cache-probe`：缓存行为测量，文档有实测记录；
- `conpty-probe`：Windows ConPTY 可用性探测，用户手册有说明；
- `goal-conpty-driver`：真实 ConPTY 交互 E2E；
- `aicli-render-fixture`：终端渲染 E2E fixture，被脚本引用；
- `skillsapi-demo`：Skills API 演示客户端，文档有使用说明；
- `supervision-metrics`：离线指标采集，文档明确为一次性入口；
- `tool-schema-measure`：工具面基线测量；
- `toolkit-mcp-server`：当前是 `toolctl` 的兼容薄入口，规划中保留兼容。

如果目标是缩小默认构建/测试面，可以先把纯探针迁到 `scripts/` 或加 build tag，而不是删除，并同步更新文档。

## 4. P2：产品/架构决策后才可删

### 4.1 Win7 兼容层

仍在发布链路中：

- 57 个 `win7compat` Go 文件，约 12,252 行；
- `go.win7.mod` / `go.win7.sum`；
- 3 个 Win7 PowerShell 构建脚本；
- 2 个含 Win7 构建/发布的 GitHub workflow；
- `configs/runtime.win7.yaml`、3 个 `win7compat` 测试文件。

**不建议现在删。** 只有产品明确 EOL Windows 7 后，才可作为一次性大清理。

### 4.2 TUI legacy/fenced-dead

既有文档已给出更细的账本：

- `docs/aicli/tui-render-architecture.md`：`FixedBottomSurface` 约 150 个 fenced-dead 方法 / 4k+ 行，但 facade、几何、plain/pipe/win7 路径仍必须保留；
- `docs/plan/aicli-legacy-fallback-retirement-plan-20261008.md`：L1–L3 已完成大量物理写路径退役；
- `AICLI_SCENE_PRESENTER` 仍被 CI 双跑验证，默认关闭；
- `terminal_output.go` 仍是测试 seam/legacy sink，生产 `TerminalOutput()` 仍有一个调用点。

可立即做的小件：

- `enableUnifiedRendererWithWriter` 移入测试；
- 清理零引用的 `fixed_bottom_surface` 辅助方法；
- 清理 `LegacyReserveState` 的测试断言与只写字段（需先迁移状态断言）。

不要在未完成 plain/compat/pipe 模式迁移前整体删除 `FixedBottomSurface`。

### 4.3 协议别名与旧配置

- 工具别名、参数别名：保留到新版客户端/模型稳定。
- `execution_mode=wait|sync`：模型可见 schema 已不再暴露，但 SQLite default 仍是 `'wait'`，旧行读取会补该值。顺序应为：停止接受新输入 → 迁移 DB default → 删除常量/解析分支。
- `FinalizationRequired`：旧 host 解码字段，生产不读；但出现在外部 JSON 结果里，等协议版本升级再删。
- `role`、`thinking_effort`、`completionRequirement`：保留一个兼容窗口；先删已知 `verifier→verify`、`writer→implement` 映射，保留自定义 role 兜底。
- `AddMessageWithLimit` 的 `maxHistory` 参数：所有实现都不使用，调用方普遍传 0；可机械迁移为 `AppendMessage(ctx,id,msg)`，但需先确认无外部 `chat.Storage` 实现。
- `apply_patch` 的 `patch` 入参：schema 只暴露 `input`，代码仍接受旧 `patch`；可加一次 warning/telemetry 后删除。`_raw` 可能被 freeform 桥接使用，暂不要动。
- `WarmAsync`：函数体为空但生产仍调用，且 `AICLI_SQLITE_WARMUP` 是排障契约；要么保留，要么调用点/环境变量/测试/文档一起删。
- `deprecatedPolicy`：等 remote target 配置迁移完成、并清理文档示例后删除。
- `AICLI_CHAT_INPUT_ARBITRATION_ENFORCE` 旧路径：默认关闭的灰度开关，等 P2b 单写者落地后删除散点调用。
- `MaxScan`、`PlanIncomplete`、`PlanStalled`、`--vacuum`：仍有前端或 E2E/诊断契约在消费，暂不作为死代码。

## 5. `go test` 超时的结构原因与治理

### 5.1 规模

| 指标 | 数值 |
| --- | ---: |
| Go 文件 | 5,418 |
| Go LOC | 1,607,482 |
| 测试文件 | 2,932 |
| 测试 LOC | 706,288 |
| Test/Benchmark/Fuzz 函数 | 17,833 |
| `cmd/aicli/commands` 测试文件 | 641 |
| `cmd/aicli/commands` 测试函数 | 4,499 |
| `cmd/aicli/commands` 测试 LOC | 191,335 |
| `internal/api/runtimeapi` 测试文件 | 212 |
| `cmd/aicli/ui` 测试文件 | 175 |
| `internal/agent` 测试文件 | 127 |

`cmd/aicli/commands` 一个包就包含 457 个生产文件和 641 个测试文件，测试二进制编译本身就会消耗大量内存。仓库历史记录也出现过该包测试二进制 OOM、以及单包 230–280s 的运行时间。

### 5.2 已确认的超时放大器

这些不是“单个测试慢”，而是失败时会连续消耗预算、或让测试二进制无法在默认 10m 包上限内完成：

| 位置 | 问题 | 建议 |
| --- | --- | --- |
| `cmd/aicli/commands/chat_tty_conpty_live_test.go` + `chat_residency_trim_conpty_test.go` | 仅 `windows` build tag；7 个 ConPTY 测试硬预算合计 **1050s**（90×3 + 180 + 120 + 240×2），超过默认 600s 包上限；`chatTTYConPTYAvailable()` 只判断 GOOS | 加 `windows && integration`，nightly 用 30m 超时保留。 |
| `cmd/aicli/commands/chat_startup_normal_mode_e2e_test.go` | 无 build tag；外层等待 60s、内层等待 30s，死锁时 dump 大 goroutine 栈 | 加 `integration`/`e2e`，默认保留快速 buildSession 单测。 |
| `internal/lsp/realserver_test.go` | 仅在 `-short` 时跳过；真实 rust-analyzer 15s 探测 + 60s 启动 + 60s diagnostics | 加 `integration`/`live_lsp` tag。 |
| `internal/memoryruntime/e2e_sqlite_test.go` | 默认运行 3 个 E2E，子进程硬杀/60s + 30s marker 等待 | 保留基础重启恢复；abrupt-exit/hard-kill 加 `integration`。 |
| `internal/mcp/transport/stdio_tree_windows_test.go` | helper 启动 `ping -n 600`（约 10 分钟），并等待 30/10/20s 进程树消失 | 加 `integration`，nightly 保留父死子亡回归。 |
| `internal/tools/mcp_stack_e2e_test.go` | 默认 re-exec 真实 MCP 子进程，30s 总等待 | 加 `integration`。 |
| `internal/chat/runtime_store_wal_concurrency_test.go`、`usageledger/sqlite_store_bench_test.go`、`agentcontrol/concurrency_stress_test.go` | stress/WAL/fsync 测试只在 `-short` 跳过，但默认 Makefile 不带 `-short` | 默认 `go test -short`，stress/perf 另设 tag 或目标。 |
| `cmd/aicli/pprof_test.go`、`internal/gateway/tunnel/proxy_test.go`、`internal/remotetools/metrics_test.go` 等 | `http.Get`/`http.DefaultClient.Do` 无 client timeout，handler 死锁时只能等包级 10m | 补 `http.Client{Timeout: ...}` 或 context。 |
| `internal/sshclient/proxy_test.go`、`internal/toolkit/tools/aicli_exec_test.go` | 每次包运行先 `go build` helper/fake executable；模块缓存冷时放大 wall time | 合并构建或加 integration tag。 |

另：30s/60s/24h helper sleep 本身不是父测试实际等待；风险在于 kill/cleanup 失败后子进程继承 stdout/stderr 管道，`Wait`/`CombinedOutput` 等不到 EOF。不要把这些 helper 当空测试删除；应为每个 re-exec helper 明确 kill cleanup、child `-test.timeout` 和父 context deadline。

### 5.3 优先治理测试类型

按名字和依赖扫描，约有 **153 个 live/e2e/integration/soak/load/baseline/probe/PTY 类测试文件，33.6k 行、525 个测试函数**。其中真正需要默认运行的只是一部分。

建议分组：

- `unit`：默认 `go test -short ./...`，不含外部进程/网络/PTY/长假等待；
- `integration`：`//go:build integration`，覆盖 SQLite 并发、MCP、HTTP、gateway、进程树；
- `live`：`//go:build live`，真实 provider、gopls、envd、SSH、PTY、长跑；
- `soak`：独立脚本，不进入 CI 的常规 go test。

优先迁移的典型文件：

- `cmd/aicli/commands/chat_local_orchestration_integration_test.go`（2,493 行）
- `cmd/aicli/commands/chat_tty_conpty_live_test.go`（1,169 行）
- `cmd/aicli/commands/chat_local_orchestration_live_test.go`（912 行）
- `cmd/aicli/commands/chat_residency_soak_test.go`
- `cmd/aicli/commands/supervision_e2e_*`
- `cmd/aicli/ui/native_scrollback_regression_test.go`
- `cmd/aicli/ui/terminal_session_pty_test.go`
- `internal/lsp/live_*`、`realserver_test.go`
- `internal/knowledge/live_*`、`golden_lsp_live_test.go`
- `internal/envdclient/*_live_test.go`
- `internal/mcp/manager/*_live_test.go`
- `internal/chat/*load_test.go`、`sqlite_storage_snapshot_load_test.go`
- `internal/tools/live_two_managers_share_pool_test.go`

### 5.4 Makefile/CI 建议

当前 `make test` 是 `go test ./...`，但 release workflow 已经按包拆分。建议让本地默认目标更接近 CI：

```make
test:            # 快速默认面
	cd backend && go test -short -p 1 -timeout 10m ./...

test-integration:
	cd backend && go test -tags integration -p 1 -timeout 20m ./...

test-live:
	cd backend && go test -tags live -p 1 -timeout 30m ./...

test-full:
	cd backend && go test -p 1 -timeout 30m ./...
```

同时：
- 在低内存 Windows 上使用 `GOMAXPROCS=2`、`-p 1`、必要时 `-vet=off`；
- 保留 `go test ./...` 作为显式 full 目标，不要作为默认本地反馈回路；
- 把 `cmd/aicli/commands`、`cmd/aicli/ui`、`internal/api/runtimeapi` 分成独立 CI job；
- 对测试二进制 OOM，优先减少同时编译的包数，而不是简单调大 timeout。

## 6. 建议执行顺序

1. **第一批（无行为风险）**
   - 删除 `internal/config/loader`、`internal/types/openai`；
   - 迁移并删除 `cmd/aicli/ui/viewport`；
   - 删除 5 个直接跳过/已迁移测试和 1 个空测试；
   - 删除 `chat_debug.go` 等文件里的零引用 wrapper；
   - 每批 `go build ./...` + 相关包测试。

2. **第二批（0 行为变化）**
   - 按包删除 281 个零引用未导出符号；
   - 优先 `cmd/aicli/commands`、`cmd/aicli/ui`、`internal/toolkit/tools`；
   - 对零引用常量（如 QQ Bot interaction 常量）单独确认协议保留意图。

3. **第三批（测试迁移）**
   - 把 214 个 test-only 生产符号迁入 `_test.go` 或测试 helper；
   - 优先 `waitUIActorIdle`、`newChatSessionManager`、`enableUnifiedRendererWithWriter`、`renderChatRuntimeEvent`；
   - 再处理 `ForTest` reset hooks。

4. **第四批（测试分层）**
   - 先给 ConPTY 两个文件、startup E2E、realserver、memoryruntime 硬杀 E2E、stdio_tree、mcp_stack 加 `integration` tag；
   - 再给其余 live/e2e/soak/load 测试加 build tag；
   - 调整 Makefile 与 CI；
   - 重新测量 `go test -short ./...` 和 full suite 的时间/内存。

5. **第五批（需决策）**
   - Win7 EOL；
   - TUI legacy/fenced-dead 整块退役；
   - 协议别名和旧配置兼容窗口到期清理。

## 7. 不建议现在删除的项目

- `win7compat` 相关文件和 `go.win7.mod`：仍在发布链路。
- `AICLI_SCENE_PRESENTER`：CI 仍在双跑验证，默认关闭但有明确收敛计划。
- `configwriteguard`：它是独立静态守卫测试包，不是死代码。
- `driftguard`：被 `scripts/drift_measure.go` 和测试使用，是工具/守卫。
- `toolregistry` 的 `ls/read/bash/...` 别名：是协议兼容面。
- `fixed_bottom_surface` 的 facade/几何/lease 面：删除会破坏交互式 TUI。

## 8. 可直接采用的验证清单

```bash
cd backend

# 基础
gofmt -w <changed-files>
GOMAXPROCS=2 go build -p 2 ./...

# 按包验证（示例）
go test ./internal/toolkit/tools -count=1
go test ./internal/api/runtimeapi -count=1
go test ./cmd/aicli/ui -count=1
go test ./cmd/aicli/commands -count=1 -timeout 900s

# 门禁
go test ./cmd/aicli/ui ./cmd/aicli/commands \
  -run 'DirectWriterInventory|WriterInventory' -count=1
```

在低内存 Windows 上，`go test ./cmd/aicli/commands` 仍可能 OOM；应先用 `-run` 子集验证，再按包分批全量。
