# 审查报告：Codex 文本类 Skill 方案完整性（2026-10-02）

> 审查对象：`docs/plan/codex-text-skill-mention-injection-plan-20261002.md`（v1.0 → 审查后修订为 v1.1）
> 审查方法：逐条对照 Codex 源码（`E:\projects\ai\codex`）与本仓库源码**重新核实行号**；区分"已核实事实 / 推理 / 待验证"。
> 结论：方案方向正确、结构基本完整；修订前存在 **5 处高优先级缺口**（preflight 契约、持久化语义错误、
> catalog 注入位置、观测口径、安全边界）与 6 处中优先级缺口，**已在 v1.1 全部修订**；剩余待决项见 §8。

---

## 1. 完整性评级（对 v1.0 审查）

| 维度 | 评级 | 说明 |
|---|---|---|
| 目标 / 非目标 / 范围 | ✅ 完整 | 文本类判定明确，与函数路径/桥执行的边界清楚 |
| Codex 参考对齐 | ✅ 基本完整 | 主要机制均有行号；缺"子代理禁转述"条款与依赖联动差异说明（v1.1 补） |
| 架构落点与代码索引 | ✅ 完整 | 注入点/通道/解析点均有文件行号；新增"解析不得依赖曝光面"约束（v1.1 补） |
| 兼容矩阵 | ✅ 完整 | `/skill`、`/call`、handler/workflow、headless 全覆盖 |
| 失败模式与降级 | ⚠️ 修订前缺失关键一项 | 未定义与 preflight/auto-compact 的顺序契约（H1） |
| 生命周期/持久化语义 | ❌ 修订前有事实错误 | 把 turn 级注入写成"随会话持久化"，实际 `Durable=false` 不落盘（H1） |
| 预算与缓存 | ⚠️ 修订前部分缺失 | 有注入预算，但未定义常驻 catalog 注入位置对前缀缓存的影响（H2） |
| 安全与信任边界 | ⚠️ 修订前缺失 | folder-trust 未定义、缺子代理禁转述条款（H4） |
| 观测口径 | ⚠️ 修订前不完整 | 缺 mentioned vs injected 两级与 status=ok/error（H3） |
| 测试策略 | ✅ 基本完整 | v1.1 补 fuzz/golden/preflight 回归/负例（M5） |
| 灰度与回滚 | ⚠️ 修订前缺量化门槛 | v1.1 补 Rollout Gates（M4） |
| 文档更新清单 | ⚠️ 部分 | 缺 `/skills` 帮助文案与纪律说明落点（M6，v1.1 记入 P3） |

---

## 2. 高优先级发现（v1.1 已修订）

### H1（阻断级）preflight/auto-compact 顺序契约缺失 + 持久化语义写错

- **事实（新核实）**：turn 级 system 注入在 `internal/agent/loop.go:688-695` 被并入请求历史，且标记
  `Durable=false`；落盘时经 `DurableMessagesForPersist` 剥除（`internal/agent/system_reminder.go:389-410`）。
  随后请求在 `loop.go:5784 enforcePromptPreflightWithTools` 做预算 preflight（调用点 `loop.go:2166`）。
- **影响**：① 大段注入可能把请求推过阈值，触发 active-turn **历史压缩**甚至 preflight 失败；
  ② v1.0 §4.9 写成"随会话持久化，resume 后旧注入保留"与实现相反，会误导实现与测试。
- **修订（v1.1）**：§4.4 幂等段落改为"prompt-only、不落盘、无需跨回合去重"；§4.9 持久化/resume 修正；
  新增 §4.11 顺序契约（本地裁决 → 降级 → preflight 余量 ≥20% → 注入不得成为压缩触发器）；§6.1 增回归；
  §7 增 R12；P0 验收增第 5/6 条。

### H2（阻断级）常驻 catalog 注入位置未定义，存在前缀缓存与 preflight 双重风险

- **事实**：Codex 把 catalog 渲染为 developer 片段并在 extension state 缓存
  （`ext/skills/src/extension.rs:92`、`ext/skills/src/fragments.rs:39-58`），属会话级稳定前缀；
  本仓库回合级消息一律追加在历史尾部（`loop.go:691`）。
- **影响**：若按 v1.0"每回合注入一条消息"，每回合改写请求前缀 → provider 前缀缓存命中率下降；
  同时把 catalog 推入 preflight 压缩面。
- **修订（v1.1）**：§4.5 明确常驻 catalog 必须落在**会话级稳定前缀**（系统提示/首部 developer 消息），
  仅 fingerprint 或配置变化时重建；§4.7 增缓存命中率下降 ≤2% 的灰度门槛；§8 增 Q10。

### H3（高）观测缺 mentioned vs injected 两级口径

- **事实**：Codex 分离 `mentioned_skills` 与 `injected_skills`（读取失败 → `status=error`），
  每次注入上报 `codex.skill.injected`（`core/src/skills.rs:38-119`、`ext/skills/src/host_prompt.rs:96-104`）。
- **影响**：只看单一"注入计数"无法区分"提及未命中/被禁用/被上限截断/读取失败"，排障与灰度指标失真。
- **修订（v1.1）**：§4.10 改为四元组：`skill_mentions` / `skill_injected{name,chars,truncated}` /
  `skill_inject_skipped{reason}` / `skills.invoked{invoke_type=mention}`（与 pin/函数路径去重）。

### H4（高）安全边界两处缺失：folder-trust 与"禁止子代理转述技能指令"

- **事实**：Codex 纪律原文包含 "Do not delegate reading, summarizing, or interpreting skill instructions
  to a subagent"（`ext/skills/src/catalog_prompt.rs:13`）；本仓库有 folder-trust 体系
  （`docs/product/folder-trust.md`），v1.0 未定义未信任项目下的注入行为。
- **影响**：多代理运行时可能把技能指令转给子代理执行（越权/失控）；未信任项目可能借 mention 获得
  额外内容通道。
- **修订（v1.1）**：§4.8 增第 7（信任边界）与第 8（子代理边界）；§4.5 纪律块补两行；Q12 已拍板（P1 与 folder-trust 联动）。

### H5（高）解析链路与函数曝光面的耦合风险

- **事实**：现有曝光入口 `SelectRequestFunctions`（`function_catalog.go:330-357`）与
  `AnalyzeSkillExposure`（`skills_integration.go:128-207`）服务于**函数面**路由；P3 计划隐藏文本类函数后，
  若 mention 解析复用它，会出现"函数被隐藏 → 提及解析也失效"的连锁问题。
- **修订（v1.1）**：P0 表新增第 9 行——解析/注入直接读 `binding.skillFunctions`，与曝光装饰解耦；
  §6.1 增"函数面隐藏后的前向兼容"测试。

---

## 3. 中优先级发现（M1-M6）

| # | 发现 | 处置 |
|---|---|---|
| M1 | 性能预算未量化：回合装配读取 N 个 SKILL.md 的 warm/cold 成本、主循环阻塞风险 | v1.1 R13：复用 `resolvedTurnSkill()` 缓存、N≤4、warm ≤20ms、超限降级；测试补耗时断言 |
| M2 | 大小写策略未决且与 Codex 存在偏差（Codex 文本精确匹配，`selection.rs:164-196`） | v1.1 §4.2 定为"不敏感（贴合现状）"，标注有意偏差；Q9 保留 |
| M3 | 路径未归一化：Windows `\` 会破坏 fingerprint 与 golden 稳定性（Codex `mentions.rs:75-77` 做归一化） | v1.1 §4.2/§4.5 增 `\`→`/`；测试补 golden |
| M4 | 灰度无量化门槛 | v1.1 §4.7 增 Rollout Gates（5 条 + catalog 2 条） |
| M5 | 测试缺 fuzz/golden/preflight 回归/系统输入负例/P3 前向兼容 | v1.1 §6.1 增 6 行用例 |
| M6 | 文档清单不完整（`/skills` 帮助文案、纪律说明落点） | v1.1 P3 表已含 `/skills` 帮助；建议实现时同步 `aicli_skills_usage.md` 纪律段落 |

---

## 4. 低优先级发现（L1-L3）

| # | 发现 | 处置 |
|---|---|---|
| L1 | `/skills` 全屏选择器当前回填 `/skill <name> ` 草稿；mention 时代是否改为 `$name` 未说明 | 建议保留 `/skill` 回填（显式 pin 语义），`$` 补全独立存在；文档说明两条入口差异 |
| L2 | `$` 高亮与代码块内不解析属可选体验项，未分级 | 保留在 P2 可选，不作为验收门槛 |
| L3 | 术语"文本类"与 Codex "host/instruction skill" 混用 | 实现文档中统一为"文本类（instruction-only）host skill" |

---

## 5. 与 Codex 的覆盖/偏差矩阵

| 机制 | Codex | 本方案 v1.1 | 判定 |
|---|---|---|---|
| 常驻 catalog | developer 片段，会话级，预算 8000/2%，缓存于 extension state | `catalog_resident` 开关 + 稳定前缀 + fingerprint 重建 | ✅ 对齐（注入位置已修正） |
| `$` 提及语法 | `$name` + 链接式，env 名单过滤 | `$name`（P0），链接式 P2，env 名单 + PowerShell 形态 | ✅ 对齐（链接式延后） |
| 多提及选择 | 结构化优先、目录顺序、唯一性/禁用/歧义规则、path 去重 | 目录顺序、去重、歧义/禁用、上限 4；绑定 path 优先（P2） | ✅ 对齐（新增上限） |
| 正文注入 | user 角色 `<skill>` 片段，逐个注入 | **user 角色片段**（Q1 重审后与 Codex 对齐），边界标记，非持久通道 | ✅ 对齐（改判依据见方案 §4.12 协议矩阵） |
| 多技能纪律 | 多提及全用、最小集合+顺序、不跨回合、禁子代理转述 | 全量采纳（v1.1 补齐后两条） | ✅ 对齐 |
| 正文截断 | 仅 agent-plugin 技能截断 + warning | 所有文本类 32KB 截断 + warning | ⚠️ 有意偏差（防上下文爆炸，更严） |
| 失败降级 | 单技能失败 warning，不阻断 | 同 + 计入 skipped 诊断 | ✅ 对齐 |
| 依赖联动 | 提及 skill 的 MCP 依赖检查/安装（`turn.rs:988-1006`） | P0 不做（工具面常驻）；Q11 | ⚠️ 待决 |
| 观测 | mentioned/injected + status ok/error | 四元组（v1.1 修订） | ✅ 对齐 |
| 启停 | `skills_runtime.disabled_skills` 等 | 复用 | ✅ 对齐 |
| 热更新 | watcher + 快照 | 复用既有 hot reload；fingerprint 触发重建 | ✅ 对齐 |
| 信任边界 | 无 folder-trust 概念（scope + policy） | 沿用本仓库 folder-trust；Q12 决定是否禁用 | ⚠️ 本仓库特有，待决 |

---

## 6. 最佳实践检查表（设计文档要素）

| 要素 | 状态 | 备注 |
|---|---|---|
| 背景/目标/非目标 | ✅ | §1 |
| 参考实现（带行号） | ✅ | §2 |
| 现状盘点（带行号） | ✅ | §3 |
| 决策记录 | ✅ | D1-D3 + Q1-Q12 |
| 兼容矩阵 | ✅ | §4.6 |
| 失败模式与降级 | ✅ | §4.4 + §4.11（v1.1） |
| 性能/缓存契约 | ✅ | §4.5/§4.11 + R13（v1.1） |
| 安全与信任边界 | ✅ | §4.8（v1.1） |
| 观测与指标 | ✅ | §4.10（v1.1） |
| 测试策略（含 fuzz/golden） | ✅ | §6（v1.1） |
| 灰度门槛与回滚 | ✅ | §4.7（v1.1） |
| 分阶段实施与验收 | ✅ | §5 |
| 风险登记 | ✅ | §7（R1-R13） |
| 开放问题 | ✅ | §8（Q1-Q12） |
| 文档更新清单 | ⚠️ | 建议实现时补 `/skills` 帮助与 `aicli_skills_usage.md` 纪律段 |

---

## 7. v1.1 变更日志（本轮已应用到方案）

1. §4.4：注入语义修正为 prompt-only/不落盘（H1）；
2. §4.9：持久化与 resume 行为修正（H1）；
3. §4.11：新增 preflight/auto-compact 顺序契约（H1）；
4. §4.5：常驻 catalog 稳定前缀 + 路径/排序稳定性（H2）；
5. §4.10：mentioned/injected 四元组观测（H3）；
6. §4.8 + 纪律块：folder-trust 边界 + 禁止子代理转述（H4）；
7. §4.2：大小写策略、绑定优先、PowerShell env 形态、路径归一化（M2/M3）；
8. P0 表 +3 行（解析解耦/契约落地/测试补充）（H5/M5）；
9. P0 验收 +2 条、P1 验收 +前缀稳定（H1/H2）；
10. §6.1 +6 行测试（fuzz/golden/preflight/前缀/负例/前向兼容）；
11. §7 +R11-R13；§8 +Q9-Q12；
12. 顶部状态升级 v1.1 并链接本审查报告。
13. **Q1 重审（2026-10-02 追加）**：协议适配层复核（Anthropic 非 leading system→user、Gemini system→model、
    system-role 回退合并 user）后，注入角色由 system 改为 **user**；方案新增 §4.12 协议矩阵、R14 与协议
    转换测试行，版本升级 v1.3。

---

## 8. 待决项 → 已拍板（2026-10-02）

全部按最佳实践建议采纳（Q11/Q12 小幅加严）；最终决策见方案 §8 决策记录。摘要：

- Q1 **user 注入**（重审修订，依据方案 §4.12 协议矩阵）；Q2 忽略非文本 + 提示；Q9 大小写不敏感；Q10 稳定前缀；
- Q4/Q5/Q6/Q7/Q8 同建议；M6 文档落点进入 P0/P2 清单；
- Q11：P0 不检查/不安装，P1 增"依赖不可用"提示；Q12：P1 与 folder-trust 联动
  （未信任默认禁用，显式 `on` 覆盖）。

---

## 9. 附录：本次审查**新核实**的证据行号

| 事实 | 证据 |
|---|---|
| turn system 注入 prompt-only（Durable=false） | `backend/internal/agent/loop.go:688-695`；`backend/internal/agent/system_reminder.go:389-410` |
| preflight 在注入之后执行 | `backend/internal/agent/loop.go:691, 2166, 5784` |
| 能力面 await 入口 | `backend/cmd/aicli/commands/chat_capabilities_async.go:249` |
| 斜杠命令解析入口 | `backend/cmd/aicli/commands/command_invoke.go:198` |
| 函数选择与曝光耦合 | `backend/cmd/aicli/commands/function_catalog.go:330-357`；`skills_integration.go:128-207` |
| Codex catalog 片段缓存 | `ext/skills/src/extension.rs:92`；`ext/skills/src/fragments.rs:39-58` |
| Codex mentioned/injected 与 status | `core/src/skills.rs:38-119`；`ext/skills/src/host_prompt.rs:96-104` |
| Codex 纪律（多提及/子代理） | `ext/skills/src/catalog_prompt.rs:8,13,17` |
| Codex 选择规则（绑定优先/唯一性） | `skills/src/selection.rs:62,164-196` |
| Codex 路径归一化 | `skills/src/mentions.rs:75-77` |
| 本仓库技能配置字段 | `backend/internal/agentconfig/config.go:880-937` |
