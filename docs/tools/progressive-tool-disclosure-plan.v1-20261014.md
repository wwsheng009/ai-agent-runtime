# 渐进式工具披露（Progressive Tool Disclosure）实施方案

> 状态：设计提案（Draft）
> 日期：2026-10-14
> 参考项目：T-Auto/dsh-ops（Windows DSH 性能插件，其渐进式披露在自测 benchmark 中减少 97% token）
> 适用范围：aicli（CLI 宿主）与 runtime-server（服务宿主）共用的工具装配与 prompt 组装链路
> 关联代码：`backend/internal/toolsurface`、`backend/internal/toolschema`、`backend/internal/prompt`、`backend/internal/toolbroker`、`backend/internal/tools`

---

## 1. 问题陈述

当前每次请求把**全部可用工具**的完整 JSON Schema（含 description）发给模型：

- 工具定义占用大量系统前缀 token，每次对话都重复支付；
- 工具越多，模型决策越分散：该走文件工具时去跑 shell、报错后乱换工具；
- 大量低频工具（mesh、team、schedule、lsp…）在多数会话中从未被调用，却全程占位。

dsh-ops 的核心结论：**token 浪费的主要来源不是执行开销，而是每一次对话重复支付的"认知税"**——上下文里存在"当前不可用或不必要"的工具信息。

## 2. 目标与非目标

### 目标
1. 系统 prompt 中的工具信息从"全量 schema"收敛为"一张静态路由表 + 按需披露"；
2. 工具集在一个会话内**字节级稳定**，保护 prompt 前缀缓存；
3. 能力按组件渐进启用：未启用的组件，其工具**根本不注册**（不是隐藏）；
4. 保持现有权限模型（sandboxPolicy / approval）语义不变。

### 非目标
- 不改变任何工具的执行行为与权限判定；
- 不引入新的 MCP server；
- 第一期不做动态运行时装卸（按会话粒度静态装配即可）。

## 3. 现状盘点（可复用的既有资产）

| 既有资产 | 位置 | 对渐进式方案的价值 |
| --- | --- | --- |
| 统一工具面装配（按 name 排序、幂等去重、surface_source 来源标注） | `internal/toolsurface` | 已解决 schema 顺序稳定性问题的一半：观测面已排序，需把排序推广到发给模型的 tools 数组 |
| Schema 规范化（Canonicalize，修复 MCP 缺省，字节确定输出） | `internal/toolschema` | 可在其上加 description 压缩层（类比 dsh-ops 的 `projectTool`） |
| 分层 prompt 片段（Fragment/Layers，固定 layer + source） | `internal/prompt/layers.go` | 路由表作为新 Fragment 注入，天然有稳定位置；需补固定 `order` 字段 |
| broker 编排面判定（IsBrokerTool） | `internal/toolbroker` | 组件分组判定可直接复用 |
| 请求级账本（SummarizeSources 随 metadata 走，不推高 token） | `internal/toolsurface` | 渐进披露的观测与回放审计可沿用同一机制 |

## 4. 方案设计（四个渐进层）

### 4.1 层一：组件分组与按需注册（capability groups）

把工具按组件分组，组内工具**整组启停**。建议初始分组（对齐现有 toolsurface 来源标签）：

| 组 | 工具族 | 默认 |
| --- | --- | --- |
| `core.file` | view / write / edit / append_write / glob / grep / multiedit | 开 |
| `core.shell` | shell / background job 家族 | 开 |
| `core.search` | web_search / fetch / sourcegraph | 开 |
| `orchestration` | subagent / team / job_* / schedule / plan | 会话相关（多代理会话开） |
| `collab.team` | team_spawn / wait_team 等 | 团队任务开启时才注册 |
| `io` | download / image / artifacts | 按需 |
| `mesh` | mesh 家族 | 仅 mesh 会话 |
| `lsp` | lsp 家族 | 项目检出 LSP 时 |

实施要点：
- 分组表落为静态配置（`configs` 下的能力分组文件或 `internal/capability` 内声明），**不要在运行时按请求动态算组**——组集合在会话建立时确定，之后不变；
- 复用 toolsurface 的 `Classify`：来源标签即是天然分组键，避免新建一套平行体系；
- 未启用组的工具不进入 Surface，而不是注册后过滤——保证观测端点与实际暴露面一致。

### 4.2 层二：schema 瘦身（确定性压缩）

在 `internal/toolschema` 新增 `Compact(schema)` 步骤（类比 dsh-ops `presentation.js`）：

1. **description 压缩**：长 description 改写为固定短语表（按工具名/字段名的静态映射），不使用模型生成内容，保证版本内字节确定；
2. **隐藏高级字段**：确有等价默认行为的字段（如 grep 的非对称 context）可从 inputSchema.properties 删除，服务端仍接受（向后兼容，类比 dsh-ops "asymmetric context remains accepted but is not advertised"）；
3. **Canonicalize → Compact 顺序固定**，任何 warning 记录进 metadata，不改变输出字节。

红线：瘦身必须**确定性**——同一工具在同一构建版本下的 compact 输出必须逐字节相同，否则缓存全灭。用表驱动映射而非运行时截断。

### 4.3 层三：系统 prompt 路由表（替代全量 schema 的"认知地图"）

在 `internal/prompt` 新增一个 Fragment（source: `tooling_route`），内容为一张紧凑路由表：

```
# 工具路由
| 任务 | 工具 |
| 读文件/批量区间 | view (files[] 批量) |
| 搜索内容 | grep (先 patterns 批量) |
| 找路径 | glob |
| 执行命令 | shell |
| ...（仅列出本会话已注册工具） |
- 报错先修参数重试，勿换工具绕行。
```

实施要点：
- **`interpolate: false` 语义**：Fragment Body 在会话装配时渲染一次并缓存，请求间不重算；只依赖"本会话启用了哪些组"（静态）；
- **固定 order**：给 Fragment 增加 order 字段（如 3250），多 Fragment 间位置确定，对齐 dsh-ops 的 `SECTION_ORDERS` 做法；
- 路由表只对"已发布工具"生成行——缺席工具一行都不出现（never advertise absent tools）；
- 表行文案为静态字符串常量，禁止拼入动态值（cwd、时间、会话 ID 等）。

### 4.4 层四：完整 schema 延迟到调用时（第二期，可选）

dsh-ops 的 PTC 路线（ops_batch/ops_result：预检 + 有界结果引用）成本较高；第一期不建议照搬。替代路径：

- **工具级渐进**：模型首次尝试调用某工具时才注入该工具的完整 schema（需要协议层支持"schema 后补"，改动大，列为本期备选）；
- **技能化替代**：低频复杂工具（mesh、e2e、迁移类）移入 skill 文档，模型按需读取——复用现有 `internal/skill` 基建，成本最低，建议优先落地。

## 5. 缓存稳定性守则（硬约束）

1. **工具数组排序**：发给模型的 tools 数组必须 `sort.SliceStable(byName)`——toolsurface 观测面已这样做，推广到请求装配路径；注册顺序不再是暴露顺序；
2. **Prompt 前缀只依赖静态因子**：组集合、路由表、canonical schema 都是会话级常量；禁止把时间戳/配额余量/动态计数拼进 system 或 tools 区；
3. **会话内零重注册**：权限模式变化**不**改变工具集（沿用现有做法：权限在执行期判定，而非注册期），从根上避免 dsh-ops reconcile 式的前缀失效；
4. **变更即新会话**：组件启停、schema 版本变化只影响新会话；进行中的会话冻结首帧装配结果（Surface 一次装配快照，存入 session meta）；
5. **验证手段**：增加单测断言"同配置两次装配的 tools JSON 与 system prompt 逐字节相同"（golden file），防回归。

## 6. 实施计划

### 阶段 1（1~2 天）：排序与确定性
- [ ] 请求装配路径的 tools 数组按 name 稳定排序（对齐 toolsurface）；
- [ ] Fragment 增加 order 字段；tooling_route Fragment 落位；
- [ ] 增加字节级 golden 测试（同配置双装配 diff 为空）。

### 阶段 2（2~3 天）：schema 瘦身
- [ ] `toolschema.Compact` + 静态 description 短语表（先覆盖 token 大户：shell、spawn_subagents、job 家族）；
- [ ] 观测端点同时输出 compact 前后 token 估算（复用 `tokenestimate`），量化收益。

### 阶段 3（3~5 天）：组件分组
- [ ] 分组配置 + 会话建立时静态装配快照；
- [ ] orchestrations/collab/io 等组按会话形态启用；
- [ ] `/runtime/tools` 观测端点暴露 group 与启用原因。

### 阶段 4（可选）：低频工具技能化 + 调用时 schema 后补调研。

### 度量
- 每请求 system+tools 前缀 token（前/后对比，目标：多代理会话 ≥40% 降幅；普通会话 ≥20%）；
- 缓存命中率（如网关侧可观测）；
- 工具误用率（报错后换工具绕行的比例，抽样评估）。

## 7. 风险与对策

| 风险 | 对策 |
| --- | --- |
| 瘦身后模型漏看关键约束导致误调用 | description 短语表保留安全关键句（"绝对路径""只允许会话内 job id"）；golden 测试含关键词断言 |
| 分组过激导致能力缺失投诉 | 分组默认保守（只关 mesh/lsp/e2e 等低频组）；观测端点可解释"为什么没看到该工具" |
| 路由表与实际 schema 漂移 | 路由表行由已注册工具集**程序化生成**，非手写；CI 断言一致性 |
| 缓存仍因 provider 侧因素失效 | 装配层只保证我方前缀确定；配合 usageanalytics 观测 provider 命中情况 |

## 8. 与 dsh-ops 的差异说明

- dsh-ops 是**插件**，只能在注册点之上做披露控制；本项目是**宿主**，可以直接在装配源头控制——所以不需要 dsh-ops 那套 `restrict/deny` 遮罩与 reconcile 补偿逻辑，方案更简单、缓存更稳；
- dsh-ops 的 PTC 批处理（有界结果引用）暂不引入，先落地披露层收益；若 token 大户仍是工具输出，再评估结果引用路线。
