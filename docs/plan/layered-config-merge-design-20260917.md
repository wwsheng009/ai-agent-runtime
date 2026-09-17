# 分层配置合并设计：项目级缺失键回落用户级

- 状态：**P0/P1 已实施**；**P2-7（`runtime.yaml` 分层）已实施**，`mcp.yaml` 与 P2-8/9 未开始
- 日期：2026-09-17
- 范围：bootstrap 配置 `config.yaml`（`runtime.yaml` / `mcp.yaml` 见 §11 P2）
- 关联代码：`backend/internal/aiclipaths/paths.go`（`ResolveConfigFilePath`）、`backend/internal/agentconfig/{bootstrap,config,preset}.go`、`backend/cmd/runtime-server/main.go`、`backend/internal/runtimeserver/config_effective_document.go`

---

## 1. 背景与问题

当前是**单文件选定**模型，不是分层模型：

- `ResolveConfigPath`（`backend/internal/agentconfig/bootstrap.go:172`）返回**首个存在**的文件；`InitGlobalConfig`（`backend/internal/agentconfig/config.go:1022`）只读取**一个** path（`config.go:1029`）。
- 搜索顺序已调整为 `./.aicli/` > `~/.aicli/`（`bootstrap.go:48-64`），因此项目级文件会**整体遮蔽**用户级文件：配置是"首个命中即权威"，不做键级回落。
- 后果：项目级只写 3 个键的片段配置，会让用户级 `~/.aicli/config.yaml` 里的 provider / auth / skills_runtime 等设置**全部消失**。

同时，仓库里**已经存在**一套经过验证的"稀疏覆盖"合并基建，只是当前只用于内置预设层：

- `MergeConfigYAML(baseYAML, overlayYAML)`（`backend/internal/agentconfig/preset.go:371`）+
  `mergeMergeMaps`（`preset.go:440`）、`findFoldKey`（`preset.go:473`）、`yamlToMergeMap`（`preset.go:384`）。
- 语义（`preset.go:366-370` 注释明确）：嵌套 map 递归合并；标量/slice 由 overlay 覆盖；**只有 overlay 显式写出的键参与合并**，未写键不 shadow 低层；大小写不同的同名字段键折叠为同一键。
- 现有用法：`applySystemPresetLayer`（`config.go:1060`）把预设合并到用户文件**下面**（用户优先），且用"解码前的原始 YAML 文本"参与合并以保持稀疏（`config.go:1054-1059` 注释）。
- 另一处合并实现 `mergeConfigDocumentValues`（`backend/internal/runtimeserver/config_effective_document.go:232`）与其快照插槽**当前恒为空**（`config_snapshot.go:19-27` 恒把 `SnapshotPath` 置空）——即"文档层合并插槽"已经预留，但未启用。

**缺的能力只有一个**：用户级与项目级**同时**参与合并（现在互斥，只取首个存在的文件）。

---

## 2. 目标与非目标

目标：

- **G1 键级回落**：项目级只覆盖它显式写出的键；未写键依次回落到 用户级 → 便携默认 → 内置预设。
- **G2 文档级合并**：在 YAML→`map[string]any` 层面合并（解码前），**不允许**"先各自解码成 struct 再合并"——零值会让"未写"与"写了零值"不可区分。
- **G3 可溯源**：每个生效键能回答"我来自哪一层"，供 UI 展示、诊断与写回目标选择使用。
- **G4 零回归**：只有一份 config.yaml 的用户，合并结果必须与现状**逐字节等价**（同一文件、同一展开、同一校验）。
- **G5 可灰度可回滚**：默认不改变行为；有 dry-run 模式先观测差异。

非目标（v1 明确不做）：

- **N1 列表不做元素级合并**：所有 slice 按"整表替换"处理（与现有 `mergeMergeMaps` 一致），不做 append / merge-by-key。理由：顺序与身份语义不明确，易产生"删不掉的值"。
- **N2 不改 `runtime.yaml` / `mcp.yaml` 语义**：v1 只覆盖 `config.yaml`；`runtime.yaml` 已在 P2-7 按同一套层模型接入（只读 portable 层 + 用户/项目可写层），`mcp.yaml` 仍待 P2。
- **N3 不改环境变量契约**：`${VAR:-default}` 仍在**解码前**展开（`config.go:1034` → `expandEnvVars` `config.go:1093-1108`），写回仍保留字面量（`provider_persistence_test.go:226,246,257` 已断言）。
- **N4 不引入新依赖**：仓库当前无 viper / koanf / mapstructure（结构体上的 `mapstructure:"..."` 标签是惰性的，无解码器消费），本设计沿用 `gopkg.in/yaml.v3` + 现有合并函数。

---

## 3. 层模型（优先级由低到高）

| 层 | 来源 | 说明 |
|----|------|------|
| L0 | 内置/系统预设 | 现有 `LoadSystemPresets` / `MergeWithPresets`（`preset.go:170/264`），保持不变 |
| L1 | 便携默认 `configs/config.yaml` | 随二进制/仓库分发 |
| L1b | 遗留散落文件 `./config.yaml`、`./aicli.yaml` | CWD 中的旧式回退位；`aicli.yaml` 高于 `config.yaml`（专用名优先于通用名，避免误读无关的 `config.yaml`） |
| L2 | 用户级 `$HOME/.aicli/config.yaml` | 用户主目录（"主要目录"） |
| L3 | 项目级 `./.aicli/config.yaml` | 受信任门禁约束（§8） |
| L4 | 显式 `--config` / `-c` | **单文件语义：一旦显式指定，禁用分层合并** |
| L5 | 环境变量 / `.env` | 经 `${VAR:-default}` 在**键级**生效；进程环境优先 |

两条硬性规则：

1. **L4 短路**：显式指定路径时只读该文件、不做任何层合并（保持"你指哪个就是哪个"的直觉与可预测性）；需要合并时用显式开关（如 `--merge-config`）解除。
2. **层栈必须唯一**：aicli 与 runtime-server 目前搜索顺序**不一致**（`bootstrap.go:48-64` vs `cmd/runtime-server/main.go:279-295`）。实施前必须先抽出唯一的 `ConfigLayerStack()`，否则同一台机器上 CLI 与 server 会算出不同的层栈——**这是 P0 的前置条件**。

---

## 4. 合并算法

- **顺序**：`merged := {}; for layer in [L0..L3] { merged = merge(merged, normalize(layer)) }`，即高层 overlay 低层。
- **逐层展开**：每层先做 `${VAR}` 展开（复用 `expandEnvVars`），再参与合并。语义与单文件一致；不要在合并后再展开（否则表达式可能跨层泄漏）。
- **复用现有实现**：`yamlToMergeMap` + `mergeMergeMaps`，保留"map 递归 / 标量·slice 覆盖 / 大小写键折叠"三条既有语义。
- **`{}` 不等于清空**：`mergeMergeMaps` 对空 overlay 直接返回 base（`preset.go:444-446`）→ 语义为"本层没写任何键"→ 回落低层。这与 G1 一致，需在文档中显式写出，避免用户误以为 `providers: {}` 能清空。
- **`null` 语义（需决策，见 §10-Q1）**：现状把 nil 当普通值覆盖（`preset.go:461-466`）。建议新增**显式屏蔽**语义：高层写 `key:`（null）= 从合并结果中**删除**该键，使其回到"未配置"状态（这是项目级让用户级某键失效的**唯一**手段）。实现上作为**新选项**加入，不改动预设路径的既有行为（`applySystemPresetLayer` 调用点保持原语义）。
- **产物**：`(mergedYAML []byte, origins map[string]string, layerStack []LayerInfo)`；origins 键路径用点号（与热加载 `AppliedPaths` 风格一致，如 `skills_runtime.config_file`）。
- **注意**：`MergeConfigYAML` 末尾的 `yaml.Marshal`（`preset.go:381`）会**重排键序并丢注释**。它只能用于解码与展示，**绝不能**用于写回（写回必须写用户原始文本，见 §7）。

---

## 5. 环境变量、校验与缺失文件

- **展开时机**：仍为解码前、逐层进行（`config.go:1034`）。低层写的 `${X:-a}` 在高层次未写该键时照常生效，行为与单文件一致。
- **校验次数**：现状为"用户文件校验一次 + 预设合并后再校验一次"（`config.go:1039` / `config.go:1072`）。分层后保持"**每次合并完成后校验一次**"，避免中间态误报。
- **文件缺失**：现状缺失即静默降级为空配置（`config.go:1030-1032`）。分层后语义为"该层不存在 = 空层"，但**必须记录**"期望存在但缺失"的层到 layerStack，供 doctor/UI 显示。
- **`ConfigFilePath`**：现状写回显式路径（`config.go:1049`），缺失文件也会指过去。分层后该字段语义需要升级为"**最高存在层**"，并新增 `LayerFiles []LayerInfo` 承载全栈（避免 UI 只看到一个路径）。

---

## 6. 来源可观测性（provenance）

- `Config` 上增加**只读投影**（不参与 YAML 解码，避免污染合并）：`LayerOrigins map[string]string`、`LayerStack []LayerInfo{ID, Path, Present, Bytes, Order}`。
- runtime-server 侧**复用既有插槽**：`loadEffectiveConfigDocument`（`config_effective_document.go:18`）的 `effectiveConfigDocument` 已经含 `Raw/Parsed/SourcePath/SnapshotRecovered`；把合并后的文档与 origins 填进去即可，不新造 API。
- 冲突可诊断：记录"被更高层覆盖的键"清单（键、低层值、高层值、来源层），供 `aicli config doctor` 与 UI 提示使用。
- 值本身不落日志：只记键名与层，避免把 secret 写进日志。

---
## 7. 写回语义（write-back）

这是本设计**最难也最容易出事**的部分：加载侧分层的同时，写回侧必须一起改，否则一次 UI 保存就会把多层"压平"成一个文件。

现状（已核验）：

- CLI：`resolveAICLIConfigWriteTarget`（`backend/cmd/aicli/commands/config_path_write.go:16`）按 `显式 → cfg.ConfigFilePath → 搜索结果首个存在文件 → 兜底 ./.aicli/config.yaml（并建 starter）` 决定写目标。
- 所有落盘函数只接受**单一** configPath：`UpdateProviderConfig`（`internal/agentconfig/provider_persistence.go:122`）、`UpdateAICLIChatPreferences`（`chat_persistence.go:88`）、`UpdateAICLIThemePreferences`（`theme_persistence.go:85`）、`writeProviderConfigDocument`（`provider_management.go:381` → `:392 writeFileAtomic`）等。
- runtime-server：`LocalConfigDocumentService.SaveDocument`（`internal/runtimeserver/config_document.go:151`）把整份内容写回**单一** `documentPath()`（`:235-243`：snapshotPath 或 baseConfigPath）；`PUT /config/document`（`internal/api/skills/handler.go:750`）即此路径。

分层后的规则：

- **R1 读哪层写哪层**：编辑单个键时，写回该键 `origins` 指向的来源层。若来源层是 L0/L1（内置预设/便携默认，不可写），则写入**最高可写层**（项目级；无项目级则用户级），并在 UI 提示"该值将从默认层提升为显式覆盖"。
- **R2 批量保存必须按层分摊**：`PUT /config/document` 不得再用合并结果整体覆盖单文件。做法：先由 origins 计算"请求里被修改的键"，再**只把这些键**写入目标层（默认最高可写层），其余层原文逐字节不动。禁止把生效文档（合并产物）当作写回内容。
- **R3 删除即写 null**：删除一个来自低层的键，等价于向最高可写层写入该键的 null（§4 的屏蔽语义），**不得**把低层值抄写一份到高层来"固定"它。
- **R4 写回保留字面量**：必须保留 `${VAR}` 与注释（既有断言：`internal/agentconfig/provider_persistence_test.go:226,246,257`）。因此**禁止**用 `MergeConfigYAML` 的 marshal 产物（`preset.go:381` 会重排键序、丢注释）作为写回内容；沿用现有节点级/文本级写回实现。
- **R5 目标选择统一**：现有三处目标策略需要收敛为同一条规则——
  - 创建新文件（无任何层存在）→ 用户级 `$HOME/.aicli/config.yaml`（与 `EnsureStarterConfigFile` `bootstrap.go:219-236` 一致；`ResolveWritableConfigPath("")` `bootstrap.go:187` 目前偏向 `./.aicli/config.yaml`，是既有分叉）；
  - 写回既有键 → origins 所属层；
  - 写入新键 → 最高可写层（项目级被信任时优先，否则用户级）。

---

## 8. 信任与安全（foldertrust）

- **L3 项目级层必须过门禁**：untrusted 目录下忽略 `./.aicli/config.yaml`（记 warning），只用 L1/L2。与 `mcp.yaml` 现有门禁同构（候选清单见 `internal/foldertrust/configs.go:76-84`；既有门禁测试模式见 `cmd/aicli/commands/chat_folder_trust_test.go:135`）。
- **合并不放大项目级权限**：L3 本来就是最高优先（除 L4/L5）；分层只是让它不再"抹掉"低层未覆盖键。反向影响才是重点：**项目级文件的"省略"不再具备删除能力**，这正是需要 §4 null 屏蔽语义的原因；且该屏蔽语义同样受门禁约束（untrusted 时 L3 整体不参与，屏蔽不生效）。
- **校验不可绕过**：合并结果仍必须过 `validateLoadedConfig`（`internal/agentconfig/config.go:1039` / `:1072`），分层不得引入"跳过校验"的捷径。
- **日志最小化**：只记录键路径与层名，不记录值（`admin_token`、`api_key_scopes` 等敏感键尤其）。

---

## 9. 热加载与生效文档

现状（已核验）：

- `RuntimeConfigHotReloader` 对"合并后的 parsed 值"做 diff 得到 `AppliedPaths` / `RestartRequiredPaths`，**没有来源层维度**（分类函数 `ClassifyConfigDocumentPath`，测试见 `internal/runtimeserver/config_document_runtime_test.go:77/154/161`）。
- `SaveDocument`（`config_document.go:151`）在写盘后重新 `LoadDocument` 并回填 impact，写回目标是单文件（`:152/:185`）。

分层后：

- **H1 保存必须先定层**：见 §7 R2；否则热加载看到的是"被压平"的文档，之后所有 origins 都会错误地变成"项目级"。
- **H2 diff 带层来源**：`AppliedPaths` 之外增加 `path → layer` 归因，用于审计与"该键生效值来自哪层"的提示；键路径沿用点号风格（如 `skills_runtime.config_file`）。
- **H3 重算走完整层栈**：热加载重算必须"重读所有层 → 重新合并 → 重新校验"，不得只重读被改动的单文件（否则低层变更不会触发高层的回落变化）。
- **H4 生效文档接口向后兼容**：`GET /config/document`（`internal/api/skills/handler.go:747`）新增 `layers` / `origins` 字段；`Path`、`Content` 等既有字段语义不变，旧前端不受影响。

---
## 10. 迁移、灰度与回滚

- **开关**：`AICLI_CONFIG_MERGE=off | dry-run | on`（环境变量优先；另提供 `--merge-config` 显式开启），**默认 `off`**。
  - `off`：现状语义（首个命中文件，单文件）。
  - `dry-run`：**行为不变**（仍单文件生效），但计算并输出"若开启合并，哪些键会被低层补回、哪些高层值会覆盖低层"的差异报告（键名 + 层名，不含值）。
  - `on`：启用分层合并。
- **灰度节奏**：`dry-run` 至少一个发布周期（观察差异规模与误伤面）→ 改默认或由用户显式开启 `on`。
- **零回归判据（必须进 golden 测试）**：只有一份 config.yaml 时，`on` 与 `off` 的**合并结果逐字节等价**。
- **回滚**：置 `off` 即时恢复；分层实现**只读**既有文件（除 §7 的写回改动），回滚无残留。写回改动（按层分摊）需独立开关（如 `AICLI_CONFIG_LAYERED_WRITE=off` 时退回"写最高存在层"），使加载与写回可以分开回滚。
- **观测**：dry-run/on 均记录层栈摘要（层数、各层路径存在性、origins 计数）到启动日志与生效文档接口。

---

## 11. 落地阶段

> 实施状态（2026-09-17）：**P0 已完成**；**P1 已完成**（写回按层分摊 CLI+服务端、runtime-server 分层读取与 `layers`/`origins` 暴露、热加载 diff 层归因、D4 落点统一 + `aicli init --project`），仅余若干**已知限制**（见 P1-5 条目与 §14 门槛说明）。P2 未开始。

**P0（前置，必须先做）— 已完成**

1. ✅ 统一 aicli 与 runtime-server 的层栈解析为唯一 `ConfigLayerStack()` / `ConfigLayerSearchPaths()`（`config_layers.go`）：`DefaultConfigSearchPaths()`（`bootstrap.go:47`）与 `defaultRuntimeServerConfigSearchPaths()`（`cmd/runtime-server/main.go:279`）现在都从它派生。**不统一就不能开启合并**（同一目录树两条链路会算出不同结果）。
2. ✅ 实现分层加载器 `loadLayeredConfigDocument()`：逐层读文件 → 逐层 `${VAR}` 展开 → 屏蔽式深合并 → 一次校验 → 产出 `(mergedYAML, origins, layers, fallback/overridden keys)`；复用 `preset.go:371/440` 的既有语义，未新造合并算法。
3. ✅ 接入 aicli 链路：新增 `InitGlobalConfigLayered(resolvedPath, explicitPath)`（`config_layers.go`），`cmd/aicli/main.go:133` 已切换到该入口；`off/dry-run/on` 由 `AICLI_CONFIG_MERGE` 控制，默认 `off`。
4. ✅ 回归网：`config_layers_test.go`（7 个用例：模式解析、层栈顺序与搜索序互为倒序、深合并/切片替换/null 屏蔽/空 map 回落、用户级回落 + origins、off/dry-run 行为不变、显式路径短路）；并同步更新 `bootstrap_test.go` 与 `cmd/runtime-server/config_path_test.go` 的期望值。

**P1（进行中）**

4. ✅ runtime-server 接入：`LocalConfigDocumentService.loadEffectiveDocument`（`config_document.go:255`）在 `on` 模式且**存在多层**时改走 `agentconfig.LoadMergedConfigDocument()`（`config_effective_document.go` 的 `effectiveConfigDocument.Layered`），单层时仍走原来的单文件/快照路径（字节不变）。`GET /config/document` 与预览响应新增 `layers`（候选栈 + present 标记）与 `origins`（键 → 层类别）字段，后者向后兼容（`omitempty`）。
5. ✅ 写回按层分摊（§7 R1–R5 / D3）——**CLI 与服务端均已完成**：
   - `ConfigOriginFiles`（键路径 → 层文件）随分层加载产出（`config_layers.go`）；
   - `(*Config).WriteTargetForKeys(keys...)`：来源层优先；同深度取优先级更高的层；未知键返回空（= 保持调用方目标，即"新键写最高可写层"）；
   - `routeConfigWritePath()`：**仅当** ① 当前配置为 `on`、② 传入路径属于当前层栈、③ 至少一个键有来源 时才改道 → `off`/`dry-run`/`--config` 自定义路径/临时文件/测试路径 **零影响**；
   - 已接入：`UpdateProviderConfig`（含 proxy/balance/model picker 等经由它的路径）、`UpdateAICLIChatPreferences`、`UpdateAICLIThemePreferences`、`DeleteProvidersConfig`、`SetProvidersEnabledConfig`、`SetDefaultProviderConfig`；
   - 测试：`config_write_route_test.go` 4 例，含端到端用例——`ConfigFilePath` 指向项目层时，对用户层 provider 的编辑仍落在**用户层文件**，项目文件零改动；
   - ⏳ **已知限制（待补）**：批量操作（删除/启用多个 provider）若条目分散在**多个层**，只能路由到"这些条目中优先级最高的一层"，其余层的条目会报 `NotFound` 而非被正确删除/修改（安全但功能不完整）——需按层分组后逐文件改（重构 `yaml.Node` 流程）。同理 `SetDefaultProviderConfig` 在"provider 在用户层、`providers.default_provider` 只在项目层"的极端组合下会把 default 写入项目层（语义上可接受，但需知悉）。
   - ✅ 服务端：`SaveDocument`（`config_document.go:151`）在分层模式下改走 `agentconfig.ApplyMergedDocumentChanges()`（`config_merged_document.go`）：与当前**合并文档**做叶子级 diff → 每个改动键按 `OriginFiles` 写回来源层 → 新键写 `SourcePath`（最高存在层，**不写快照**）→ 删除写 `null`（屏蔽而非删除低层）；未改动文件零写入，目标文件按**不展开 `${VAR}`** 解析后改写，故未触碰的占位符与注释保留。**读取与写回同批上线**，不存在"能读会压平"的中间态。
   - ✅ 测试：`internal/runtimeserver/config_document_layered_test.go`（服务端合并视图 + origins/layers 字段 + 分摊保存落到用户层 + `off` 时用户层不可见）、`internal/agentconfig/config_merged_document_test.go`（按层写回 / null 屏蔽 / 新键落最高层 / `${VAR}` 保留 / 无改动零写入）。
   - ⏳ 已知限制：目标层文件以 map 级改写（非 `yaml.Node` 级），该文件内的注释与键序不保证保留；快照（snapshot）路径在分层模式下不参与读取与写回（设计取舍，待 P2 与热加载归因一并说明）。
6. ✅ 热加载与生效文档带层来源（§9 H2–H4）：
   - ✅ `GET /config/document`（含预览）暴露 `layers`（候选栈 + `present`）与 `origins`（键 → 层类别）；
   - ✅ 保存后重算走完整层栈（`SaveDocument` 末尾直接 `LoadDocument()`）；
   - ✅ diff 带层归因：`ConfigDocumentRuntimeImpact.PathLayers`（改动路径 → **这次写入会落到哪一层**），由 `merged.WriteLayerKindFor()` 计算（来源层；新键回落到最高存在层，与写回规则严格一致）；预览与保存两条路径都会填充（`attachConfigDocumentPathLayers`），`off` 时为空。
   - 测试：`TestLayeredConfigDocumentPreviewAttributesChangedPaths`（用户层改动→`user`、项目层改动→`project`）。
7. ✅ D4 收尾（`aicli init --project` 与落点统一）：
   - `ResolveWritableConfigPath("")`（`bootstrap.go:168`）现在返回**用户级** `$HOME/.aicli/<config>`（home 不可用时才回落项目级），与 `EnsureStarterConfigFile` 的建点一致；新增 `ResolveProjectConfigPath()` 供显式项目级创建使用。
   - `aicli init` 默认写入用户级；新增 `--project` 写项目级；`--global` 保留为兼容别名（与默认等价）；`--config` 与 `--project/--global` 互斥时报错。
   - 测试：`cmd/aicli/commands/init_command_test.go` 4 例（默认落用户级且项目目录零创建、`--project` 落项目级且用户目录零创建、`--global` 兼容、冲突标志报错）。

**P2**

7. ✅ `runtime.yaml` 同构接入（2026-09-17 完成）：
   - 层栈 `agentconfig.RuntimeConfigLayerStack()`：`configs/runtime.yaml` / `backend/configs/runtime.yaml`（**只读** portable，仅开发仓库存在）< `$HOME/.aicli/runtime.yaml`（user）< `./.aicli/runtime.yaml`（project）。
   - 合并读取：`LoadMergedRuntimeConfigDocument()`（复用 `loadLayeredDocumentFor`，与 config.yaml 同一套 origins/layers 元数据）；runtime-server 启动经 `loadRuntimeServerManager()` 注入合并文档（`RuntimeManager.LoadDocument`），无任何层存在时回落内置默认并把路径指向用户级写入目标。
   - 按层写回：`ApplyDocumentPathChange`；只读 portable 层永不写入（新键/只读来源改道可写层），首次写入自动创建 `$HOME/.aicli`。`agent.maxSteps` 的读写已切换到分层版 persister/reader。
   - 接口：`ConfigDocumentLayer` 增加 `read_only`；`GET/PUT /api/runtime/config/agent/max-steps` 返回 `layers`（候选栈快照）；前端在该卡片渲染层栈（`layerSummary/layerReadOnly/layerWritable/layerCandidate`，zh/en）。
   - 测试：`agentconfig/config_runtime_layers_test.go`（只读层归因、写用户层且 portable 零改动、全新安装落用户级）、`runtimeserver/runtime_config_layers_test.go`（provider 快照、persister 端到端、幂等不重写）、`api/skills/agent_max_steps_test.go`（layers 字段）、`cmd/runtime-server/runtime_manager_layered_test.go`（启动接线）、前端卡片测试（层栈文案）。
   - 仍待 P2：`mcp.yaml` 同构（清单类字段需单独迁移说明，见 Q4/D5）。
8. `aicli config doctor`：打印层栈、每个键来源、被覆盖值清单。
9. 前端：来源徽标 + 冲突提示 + "提升到项目级"显式操作。

**P3（非目标，另行设计）**

10. 列表 merge-by-key（如按 provider 名合并 `providers.items`）——`providers.items` 是 map 而非 slice，天然按 key 合并；真正的 slice（如 `skill_dirs`）在 v1 一律整表替换。

---

## 12. 测试矩阵（最小集）

1. **优先级矩阵**：L1 < L2 < L3；L4 显式路径短路（不合并）；L5 环境变量键级覆盖。
2. **深合并**：嵌套 map 部分覆盖；大小写键折叠（`findFoldKey` `preset.go:473`）；slice 整表替换；`{}` 不构成清空（`preset.go:444-446`）；`null` 屏蔽（若采纳 §4 建议）。
3. **回归**：单文件 `off` vs `on` golden 等价；`${VAR:-default}` 展开位置不变；写回保留字面量（`provider_persistence_test.go:226/246/257`）。
4. **写回分摊**：编辑用户级来源的键 → 写用户级；编辑默认可写层 → 提升到项目级；删除 → 写 null（不抄值）；`PUT /config/document` 不再压平多层。
5. **信任**：untrusted 目录 L3 被忽略（沿用 `chat_folder_trust_test.go:135` 模式）。
6. **热加载**：跨层修改后 `AppliedPaths` 带层归因；低层变更能触发高层回落变化（H3）。
7. **双链路一致性**：同一目录树，aicli 与 runtime-server 解析出的层栈与 origins 完全相同。

---

## 13. 风险与开放问题

风险：

- **R-1 写回压平层栈**（最高风险）：`SaveDocument` 当前把整份内容写回单文件（`config_document.go:152/185`）。若 P0 只做加载侧，UI 一次保存即可把用户级设置"烧进"项目级文件，且不可自动回退。→ 加载与写回必须同批上线。
- **R-2 `{}`/`null` 语义反直觉**：`{}` 回落、`null` 屏蔽 需要 UI 明确提示，否则会产生"删不掉/清不空"的支持成本。
- **R-3 `ConfigFilePath` 语义变更面大**：所有以它为写目标的命令（provider / chat preferences / theme / balance / proxy / TUI，见 §7 清单）都依赖它指向"唯一可写文件"。
- **R-4 合并产物不可用于写回**：`MergeConfigYAML` 的 marshal 往返丢注释与键序（`preset.go:381`）。

**已定稿决策（2026-09-17，按最佳实践收敛；原 Q1–Q4 已全部关闭）**

- **Q1 → D2：`null` = 屏蔽低层键**（采纳）。依据：Kubernetes strategic merge、Helm、Docker Compose 均采用 "null 表示删除/重置该键"的通行语义；而"把低层值抄到高层再改"会把值在层间固化，后续低层更新不再生效。
- **Q2 → D4：无任何层存在时，新建配置落用户级** `~/.aicli/config.yaml`（与 `EnsureStarterConfigFile` 现有行为收敛，避免工具在任意目录就地生成配置）；项目级由显式操作创建（`aicli init --project`，随 P1 补）。
- **Q3 → D3：UI 编辑低层来源的键默认就地写回该层**，另提供显式"提升到项目级"操作；不做静默迁移（迁移会让低层后续更新失效且难以回退）。
- **Q4 → D5：`runtime.yaml` / `mcp.yaml` 不在本轮范围**，留 P2；且 P2 必须附迁移说明——`mcp.yaml` 一旦分层，只写 `enabled: false` 的项目级片段将不再"整体遮蔽"用户级服务器列表（清单类字段按 §4 整表替换，但其它键会回落）。

---

## 14. 决策清单（已定稿）

| # | 决策点 | 结论 | 依据（最佳实践） | 状态 |
|---|--------|------|------------------|------|
| D1 | 合并开启方式与默认值 | 默认 `off`；`dry-run` 观测 ≥1 个发布周期；`AICLI_CONFIG_MERGE=on` 显式开启 | 新优先级语义必须可预览、可回滚（黄金等价判据见 §12.3） | ✅ P0 已实现 |
| D2 | `null` 语义 | 高层 `null` = 屏蔽低层该键；`{}` = 未写键（回落） | K8s/Helm/Compose 的 null-deletes 惯例；避免值在层间固化 | ✅ P0 已实现（`mergeMergeMapsMasked`，预设路径语义不变） |
| D3 | 写回目标策略 | 读哪层写哪层；新键写最高可写层；删除写 `null` | 防止一次保存把多层压平（§7 R-2/R-3）；保证低层仍可更新 | 已定稿，P1 实施（**开启 `on` 的前置**） |
| D4 | 新建配置落点 | 用户级 `~/.aicli/config.yaml`；项目级由 `aicli init --project` 显式创建 | 与 `EnsureStarterConfigFile` 收敛；避免在任意目录就地生成配置 | 已定稿，P1 实施 |
| D5 | 是否扩展到 `runtime.yaml` / `mcp.yaml` | 本轮不做，留 P2 | 清单类字段语义与 MCP 遮蔽直觉不同，需单独迁移说明 | 已定稿（不在本轮范围） |

> **开启 `on` 的门槛（2026-09-17 更新）**：CLI 与 runtime-server 的读取与写回均已按层分摊，`on` 在两条链路上语义一致，可按 §10 的灰度范围开启。开启前请确认以下已知缺口可接受：① 批量 provider 删除/启用只覆盖优先级最高的一层（其余层条目报 `NotFound`，安全但不完整）；② 目标层文件按 map 级改写，注释/键序不保证保留（未改动文件不受影响）；③ 分层模式下快照不参与读写；④ 热加载 diff 现已带层归因（`ConfigDocumentRuntimeImpact.PathLayers`）；⑤ `runtime.yaml` 的 portable 层只读——开发态编辑 `agent.maxSteps` 会写入用户级文件而非仓库文件（预期行为）；`on` 下建议先跑 `dry-run` 观测一个周期再全量。

---

## 附录 A：可直接复用的既有回归网

以下用例已覆盖本设计要复用的合并语义，实施时**先跑通它们再改代码**（行号取自当前工作区，实施前请复核）：

- 合并语义核心 —— `backend/internal/agentconfig/preset_test.go`：
  - `TestMergeConfigYAML_UserOverridesPreset`（`:57`）
  - `TestMergeConfigYAML_ZeroValueFieldsDoNotShadow`（`:96`）← 正是"缺键回落"的语义锚点
  - `TestMergeConfigYAML_SliceReplacedNotAppended`（`:118`）← 对应非目标 N1
  - `TestMergeConfigYAML_EmptyInputs`（`:135`）、`_FieldCaseVariantsMerge`（`:147`）、`_DataKeysKeepCase`（`:168`）
- 加载层等价性 —— `preset_test.go`：`TestInitGlobalConfig_NoSystemDirIsUnchanged`（`:333`）、`TestInitGlobalConfig_MergesEnabledPresetUnderUserConfig`（`:356`）← 单文件 golden 等价的现成模板。
- 路径优先级 —— `backend/internal/agentconfig/bootstrap_test.go`（`:311/:330/:346` dotenv 相关）、`backend/cmd/runtime-server/config_path_test.go`（`:12/:46/:84/:118/:134`）、`backend/internal/aiclipaths/paths_test.go`（`:52/:68/:147/:197`）。
- 写回保留字面量 —— `backend/internal/agentconfig/provider_persistence_test.go:226/246/257`。
- 生效文档/热加载 —— `backend/internal/runtimeserver/config_document_runtime_test.go`（`:77/:154/:161/:166/:186/:225/:260`）、`backend/internal/api/skills/config_document_handlers_test.go`（`:51/:75/:105/:152`）。
- 信任门禁 —— `backend/internal/foldertrust/foldertrust_test.go`、`backend/cmd/aicli/commands/chat_folder_trust_test.go:135`。

**测试缺口（需要在 P0/P1 补）**

- `resolveAICLIConfigWriteTarget` / `ensureWritableAICLIConfigPath`（`backend/cmd/aicli/commands/config_path_write.go:16/54`）**没有任何直接单测**——而它正是"写回哪个文件"的决策点，分层后必须先补测再改。
- 没有 `${VAR:-default}` 展开的独立单测（最接近的是 `internal/agentconfig/auth_store_test.go:118`）。
- 没有"多文件层栈"的用例（因为能力本身不存在）。
- `internal/runtimeserver/config_snapshot_test.go:12 TestLoadRuntimeAgentConfigUsesBaseConfigOnly`、`:32 IgnoresSnapshotFile` 把"快照层当前不生效"钉住了——P1 启用层栈时要一并更新，否则会出现"测试与设计意图相反"的僵局。

## 附录 B：相邻配置链路的差异（P2 前必须知道）

| 文件 | 加载实现 | 环境变量展开 | 备注 |
|------|----------|--------------|------|
| `config.yaml` | `InitGlobalConfig`（`config.go:1022`）+ `yaml.v3` | 解码前，自研 `expandEnvVars`（`config.go:1093`） | 本设计 P0 目标 |
| `config.yaml`（runtime-server） | `LoadRuntimeAgentConfig`（`internal/runtimeserver/config_snapshot.go:30`）→ `loadEffectiveConfigDocument`（`config_effective_document.go:18`） | 解码前，`expandConfigDocumentEnvVars`（`config_document.go:438-451`） | 同一文件、第二条实现路径，层栈必须与 CLI 对齐 |
| `runtime.yaml`（skills_runtime） | `agentconfig.LoadMergedRuntimeConfigDocument`（分层，P2-7 已实施）→ `RuntimeManager.LoadDocument` | 解码前（层内展开，复用 bootstrap 的 `expandEnvVars`） | 只读 portable 层 + 用户/项目可写层；写回走 `ApplyDocumentPathChange`（只读层改道） |
| `mcp.yaml` | `internal/mcp/config/loader.go:63,121-134`：先解码、后**字段级** `os.ExpandEnv` | 字段级 | 与 bootstrap 语义不同，P2 需单独说明（见 Q4） |
| `presets.yaml` | `preset.go:170/195/209` | 解码前 | 已在分层内（L0），无需改动 |

**死代码提醒**：`backend/internal/config/loader/loader.go`（`:53/:71-97`，含另一份 `${VAR:-default}` 展开实现与空壳 `applyDefaults`）全仓无 import，属死代码。实施时**不要**误改它，建议另开清理提交删除，避免出现第二套"真相"。

---

> 实施提示：本文所有行号取自 2026-09-17 的工作区版本；动手前请以 `grep`/`view` 复核，尤其是 `preset.go`、`config.go`、`config_document.go` 三处。
