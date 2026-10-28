# aicli-envd-bridge 收编计划：envd 后端并入 `aicli-remote-tools`

> 日期：2026-10-28
> 状态：**待实施**（M0 起逐项执行；完成后回填 §8 结论）
> 关联文档：
> - `docs/architecture/envd-enhance-ai-agent-runtime.md`（设计稿；§4.3/§4.4 实际落地注、§5 路线图）
> - `docs/architecture/envd-architecture-position.md`（定位稿；§8「未采纳放法 E」记录）
> - `docs/architecture/envd-implementation-report.md`（实施报告；§2.1 / §7 / §8.3）
> - `docs/architecture/envd-capabilities.md`（envd 原语面）

## 0. 一句话

把 `cmd/aicli-envd-bridge` 的能力**并入 `aicli-remote-tools`**（新增 `serve --stdio --backend envd` 运行模式），使 envd 路径回到设计形态——「**一套外壳 + 一条新腿**」；同时补齐 `edit/append_write/apply_patch`，消除同名档位的双语义。

## 1. 背景：现状与问题

现状（2026-10-28 代码实测）：

- envd 路径由**独立进程** `cmd/aicli-envd-bridge` 承担：自写 MCP JSON-RPC 循环、自写 profile 映射（`allows()`）、自写 jail（`jail.go`）、自写 audit（opt-in JSONL）与输出上限；**仅共享** `remotetools.CommandPolicy`。
- 工具面 = remotetools 的**子集 + envd 扩展**：view/ls/glob/grep/write/mkdir/remove/move/shell/pty_*/watch_dir/host_metrics，**缺** `edit`/`append_write`/`apply_patch`。
- `remotetargets` 展开 envd 条目时 `Command=aicli-envd-bridge`（`registry.go` envd 分支）。

问题：

1. **同名档位双语义**：`standard` 在 remotetools 是 `write/edit/append_write/apply_patch`，在 bridge 是 `write/mkdir/remove/move`——同一份 YAML、同一套档位词汇，两种含义。
2. **策略/限额双份维护**：jail、audit、limits 各写一份（设计明言「不另起一套并行体系」，实际被记录为「未采纳」偏差）。
3. **编辑三件套缺失**：envd 目标的 coding agent 无法做定点编辑（只能 `write` 整文件覆盖）。
4. **文档口径不一致**：实施报告 §7「按设计实施了」与定位稿 §8「未采纳」并存。

**决策（2026-10-28）**：按设计 §4.3 收敛——bridge 的能力**并入 `aicli-remote-tools`**，以 `serve --stdio --backend envd` 形态存在；`cmd/aicli-envd-bridge` 退役（过渡期可留兼容 shim）。对 aicli 的外部行为不变（同样的 `remote_<target>__*` 前缀工具、同样的 `readonly/standard/ops` 词汇）。

## 2. 目标形态

一个二进制、一套外壳、两条执行腿：

```text
aicli-remote-tools serve --stdio [--backend local|envd]

├─ 外壳（一份，remotetools）
│    MCP 循环（ToolkitMCPServer）/ Profile 档位 / audit（AuditSink）
│    / Limits / CommandPolicy / prefix·registry
│
├─ 执行后端 local（默认，行为不变）
│    共享核 handler（viewcore/lscore/searchcore/mutatecore）直接 os.*
│    —— 服务 ssh/http 目标机
│
└─ 执行后端 envd（本机运行；仅 stdio）
     envd 专属工具体（bridge 搬迁）+ 目标侧 realpath jail
     经 Connect-RPC 在目标机执行（spawn find/rg/sh…）
```

约束与要点：

- `--backend envd` 与 `--http` **互斥**（envd 后端只服务 stdio）。
- envd 模式下 `--workspace` 是**目标机绝对路径**；`NewServer` 在 envd 分支跳过本地 `Workspace` 校验（`NewWorkspace` 的本地 `os.Stat` 仅对 local 后端适用），改用目标侧 jail。
- 档位语义（共享档位 + envd 扩展；`readonly ⊂ standard ⊂ ops`）：

| profile | 共享档位（与 ssh/http 一致） | envd 扩展 |
|---|---|---|
| `readonly` | view, ls, glob, grep | host_metrics, watch_dir |
| `standard` | + write, **edit, append_write, apply_patch** | + mkdir, remove, move |
| `ops` | + shell | + pty_start, pty_write, pty_read, pty_resize, pty_close |

- 工具计数：`readonly 6 / standard 13 / ops 19`（现有 bridge 为 6/10/16；合并 + 三件套后为 6/13/19）。

## 3. 资产映射（「搬谁的」结论）

**外壳用 remotetools 的（删 bridge 的重复品），工具体搬 bridge 的（remotetools 现有工具体跑不了 envd）。**

| 资产 | 去向 | 方式 | 约量 |
|---|---|---|---|
| bridge `tools.go` 工具体 | `internal/remotetools`（envd 后端文件） | 搬迁 + 包 `toolkit.Tool` 通用适配器 | ~450 行 |
| bridge `jail.go`（目标侧 realpath 围栏） | 同上 | 原样搬（envd 专属） | 162 行 |
| bridge `watch_tools.go` / `pty_tools.go` | 同上 | 原样搬 + 包装 | 115 / 456 行 |
| bridge `audit.go`（JSONL） | 接入 `remotetools.AuditSink` | 适配 | 90 行 |
| bridge MCP 循环 / `profile.allows` / flags 主体 | 删除 | 用 `toolkit.ToolkitMCPServer` + `serveFlags` | ~350 行 |
| bridge `toolDefs` schema/描述 | 有对照者（view/ls/glob/grep/write）改用共享定义（`viewcore.Schema`、`searchcore.GlobParameters`、`mutatecore.*Parameters`、`Code*` 错误码）；envd 专属工具保留并风格对齐 | 对齐 | ~155 行 |
| `edit`/`append_write`/`apply_patch` | 新写（envd 合成：读→本地计算→回写） | 新实现；schema 复用 `mutatecore.*Parameters()` | ~350–600 行 + 测试 |
| `internal/envdclient` / `remotetools.CommandPolicy` | 不动 | 已共享 | — |
| `remotetargets` envd 展开 | `Command: aicli-remote-tools`，`Args: [serve --stdio --backend envd …]` | 单点改动 + 测试更新 | ~30–60 行 |

**为什么不共用共享核工具体（seam 方案）**：`viewcore`/`lscore`/`searchcore`/`mutatecore` 直接 `os.*`，`toolcore.Runtime`（`contract.go:180-190`）无文件 I/O / 进程执行抽象；让同一批 handler 跑在 envd 上需要穿透共享代码加 I/O seam（粗估 2–5 周，且 envd 原语缺口——无 ReadFile/WriteFile RPC、写限 1 MiB argv——导致部分语义无法对齐）。设计 §4.3 的原始形态即为「**外壳共享 + envd 后端实现**」，本计划按此执行；seam 改造列为**远期可选**，不与本次混合。

## 4. 迁移步骤（估时）

| 步骤 | 内容 | 估时 |
|---|---|---|
| **M0** ✅ | 垂直切片：`view` 单工具走通 `--backend envd`（含 jail + audit + MCP 级 e2e），收敛接入面不确定性——**已完成，结论见 §8** | 0.5 天（实测相符） |
| **M1** ✅ | 骨架：`Config` 增加 backend/envd 字段；`serveFlags` 增加 `--backend / --addr / --user / --token-env / --ca-file / --call-timeout`；`NewServer` 按 backend 分叉（stdio-only、跳过本地 Workspace 校验）——**随 M0 一并完成**（见 §8.1） | 0.5–1.5 天（实测 0） |
| **M2** | 全量搬迁：16 工具体 + jail/watch/pty + `toolkit.Tool` 适配器 + audit/Limits 接线 | 2.5–4 天 |
| **M3** ✅ | `edit/append_write/apply_patch` + schema/描述/错误码对齐 + 测试——**全部完成**（§8.3、§8.4） | 1–2.5 天（实测相符） |
| **M4** | 测试迁移与回归：bridge 现有 4 个测试文件改指新入口；`TestManagerHermeticEnvD` 适配；`remotetools` 全套回归；契约/等价测试扩展 | 1.5–2.5 天 |
| **M5**（可选） | `cmd/aicli-envd-bridge` 保留为兼容 shim（转发到新入口并打印弃用提示） | +0.5 天 |

> **进度（2026-10-28）**：M0/M1 完成（§8.1）；M2 文件面完成 8 个工具 + mutation jail（§8.2）；M3 全部完成（§8.3、§8.4）——**envd 的 standard 档已与原厂一致**。M2 尚余 `shell`（需命令策略 flag）、`pty_*`（会话态）、`watch_dir`。

合计：**5.5–10.5 人日**（单人、含测试；±50%，M0 完成后收敛区间）。

## 5. 验收门（全部通过才宣告完成）

1. **档位一致**：profile 测试更新为 `6 / 13 / 19`；`standard` 语义与 ssh/http 一致（edit 三件套在场，mkdir/remove/move 为声明的 envd 扩展）。
2. **契约测试**：`core_surface_contract_test.go` 扩展断言 envd 后端核心工具面 = 共享面 + 声明扩展。
3. **manager 级 e2e**：`TestManagerHermeticEnvD` 指向 `aicli-remote-tools serve --stdio --backend envd` 并通过（配置加载 → 子进程 → MCP 握手 → 工具调用）。
4. **registry 展开**：`remotetargets` 测试断言 `Command=aicli-remote-tools` 且参数含 `--backend envd`。
5. **构建与回归**：`go build ./... && go vet` 通过；`remotetools` 包全套回归全绿。
6. **（可选）真机冒烟**：`ENVD_TEST_ADDR` 门控的 live 测试通过。
7. **文档回填**：本文件 §8 结论 + 三份架构文档口径统一（见 §7 的文档修正记录）。

## 6. 保留的既有约束（不因收编而变）

- 写文件经 process + 位置参数（无引号拼接），**1 MiB** argv 上限；
- `glob` 依赖目标机 `/usr/bin/find`（无回落）；`grep` 优先 `rg`、回落 `grep -rnI`，并回报实际使用的工具；
- envd 不做路径/命令检查 → jail/策略必须前置（收编后由 envd 后端在调用 envdclient 前执行，位置与语义不变）；
- 目标机 symlink 经 `realpath -m`（缺失时回落 `readlink -f`）规范化后再比对 jail；`remove`/`move` 只解析父目录；
- live 测试由 `ENVD_TEST_ADDR` 门控；
- audit 为 opt-in、只记元数据（不记参数与内容）。

## 7. 风险与未决

- **最难点**：`NewServer`/`buildRegistry` 的本地 `Workspace` 依赖分叉——envd 分支不建本地 Workspace、改用目标侧 jail；边界明确但需小心。
- **profile 扩展挂点**的结构设计（避免再造第二套档位表）——M1 内定稿。
- **`toolkit.Tool` 适配细节**（`Registry.Register` 的校验强度）——M0 验证。
- **过渡期策略**：shim（M5）或硬切 + 迁移说明（`remote.binary` 语义从 bridge 名改为 aicli-remote-tools 路径）。

**文档修正记录（已完成）**：

- `envd-enhance-ai-agent-runtime.md`：§4.3 实际落地注增加收敛指引；新增 §10「收敛计划」。
- `envd-architecture-position.md`：新增 §9「收敛决策」。
- `envd-implementation-report.md`：§7 口径修正（接入位置按设计 / 外壳复用未按设计）。

## 8. 变更记录 / 结论回填

- 2026-10-28 初稿：决策、目标形态、资产映射、M0–M5、验收门（依据设计稿 §4.3/§4.4 实际落地注、定位稿 §8「未采纳」记录，以及当日代码实测）。

### 8.1 M0 已完成（2026-10-28）

**落地内容**：`aicli-remote-tools serve --stdio --backend envd` 可用，`view` 经目标侧 jail + Connect-RPC 读远端文件。

- 新增：`internal/remotetools/envd_backend.go`（backend 装配 + 目标侧 jail + `toolkit.Tool` 适配器 + `view`）、`envd_backend_test.go`（5 个测试）。
- 修改：`config.go`（`Backend`/`EnvDConfig`/`normalizeEnvD`）、`server.go`（`NewServer` 按 backend 分叉 + `Server.envd`）、`registry.go`（`buildResult.envd`）、`cmd/aicli-remote-tools/main.go`（`--backend/--addr/--user/--token-env/--ca-file/--call-timeout` + usage）。

**验证**：M0 新测试 5 PASS；`remotetools` 全量回归通过（37.97s，与改前一致）；`remotetargets` / `cmd/aicli-envd-bridge` 通过；`go build` / `go vet` / `gofmt`（本次文件）干净。CLI 实测：`tools --backend envd` → `["remote_envd__view"]` 且 workspace 保留 `/srv/app`；相对 workspace fail-closed；`serve --http --backend envd` 被拒。

**与计划的偏差**（均记录于此，供 M2/M3 沿用口径）：

1. **audit 接线实际成本为 0**（计划估 ~90 行适配）：`wrapRegistryWithAudit`（`server.go`）在 registry 层包装，工具一注册即被审计。测试已锁：审计含 `remote_envd__view` 记录、**不含文件内容**。
2. **计划 §7 的"最难点"（本地 `Workspace` 依赖）实际很轻**：`Workspace` 全部方法 nil-safe，envd 分支只需构造"目标侧描述符"（`root=cfg.Workspace`，不做本地 `os.Stat`），describe/审计标签/roots 装饰器只读其文本 → 分叉 8 行完成。
3. **限额改为共享口径**（行为变化，需对外说明）：`view` 上限由 `Limits.CaptureLimit`（默认 2 MiB）决定，bridge 原为硬编码 **1 MiB**；输出折叠改用共享 `toolcoreoutput.ShapeText` + `ModelVisibleCap`（bridge 原为私有 400 KiB 截断）。M2 搬迁其余工具时统一按共享 Limits。
4. **`view` 参数同时接受 `file_path`（本地/ssh 拼法）与 `path`（bridge 拼法）**：兼容期策略，M2 再决定是否收敛单一拼法并对齐 schema。
5. **`remotetargets` 展开未切换**（按计划"先保留 bridge 兼容"）：envd 条目仍指向 `aicli-envd-bridge`；切换点连同 `--allow-command*` 等 flag 对齐一并在 M4 完成。

**接入面不确定性已消除**：M2（全量搬迁）、M3（编辑三件套）可直接按 §3/§4 推进，无需再做探测。

### 8.2 M2 文件面已完成（2026-10-28）

**落地内容**：`envd_tools.go` 迁入 8 个工具体（`ls/glob/grep/write/mkdir/remove/move/host_metrics`）；`envd_backend.go` 补 mutation jail（`resolveMutationPath`/`protectedRoot`）与档位门禁；测试桩扩展为覆盖 filesystem unary 全家 + 进程流式。

**档位挂点定稿**（§7 风险项的实现答案）：新增 `envdExtensionTiers`（name→Profile 映射）+ `envdToolEnabled` 统一门禁。扩展工具沿用共享的 `readonly ⊂ standard ⊂ ops` 词汇，`ReadOnly` 硬覆盖与显式 `--tools` 收窄对两条后端一致生效——**没有**出现第二套档位系统。实测档位面：readonly 5 个、standard 9 个（edit 三件套尚未迁入，见 M3）。

**验证**：envd 后端累计 14 个用例全绿（M0 4 + 本轮 10）；`remotetools` 全量 52.1s 通过；`remotetargets` / `cmd/aicli-envd-bridge` 通过；vet / gofmt 干净。新增断言类型：越权在**联系 envd 之前**拒绝（glob 搜索根、超限 write、remove 保护根、move 目的端）与 write→view 真回读闭环。

**口径补充**：

- `write` 仍保留 **1 MiB** argv 上限（§6 约束不变）；`view` 上限走共享 `Limits.CaptureLimit`（默认 2 MiB）。
- `ls` 用 `ListDir(depth)` 原生递归参数，排序/格式化仍在本端（与 bridge 语义一致）。
- `glob`/`grep` 依旧依赖目标机 `find` / `rg`→`grep` 回落（grep 回报所用引擎）。

**剩余**：`shell`（含 `--allow-command*` flag 接入，命令授权已共享 `CommandPolicy`）、`pty_*`、`watch_dir` → M2 收尾；`edit/append_write/apply_patch` → M3；`remotetargets` 展开切换 → M4。

### 8.3 M3 编辑两件套已完成（2026-10-28）

**关键发现（降低后续成本）**：共享核的**编辑语义是纯函数层**——`MatchEditStrings`、`EditOccurrenceLineNumbers`、`BuildEditOldStringNotFoundError`、`FindClosestEditSnippetWithLine`、`Decode/Encode`、`LooksBinary`、`UnifiedDiff`、`FormatEditSuccessContent`，以及 `ParsePatch` + `ApplyHunks`；只有 `Inspect/Write/Append/Edit` 这几个入口把 I/O 与语义绑在一起。因此 envd 侧**只重写 I/O、语义直接调用纯函数层**，不存在第二套编辑规则。`apply_patch` 同理可复用 `ParsePatch`+`ApplyHunks`，成本远低于 §4 的估时。

**落地内容**：`envd_edit.go`（`load`/`store` 两个 I/O 原语 + `envdEdit`/`envdAppend` + `envdEditToolSpecs`）。schema 直接发布 `mutatecore.EditParameters()` / `AppendParameters()`，**不另写一份**。

**语义对齐明细**：歧义拒绝（含命中行数与 `replace_all`/`replacement_count` 逃生阀）、stale-context 诊断附当前最近片段与 `next_action`、二进制拒绝、文件必须存在、`create_if_missing` **默认 true**（与共享契约一致，envd 不更严）、`expected_offset` 回放检测与前置条件失败不写入、diff 由共享 `UnifiedDiff` 产出。测试逐项锁定，且断言**拒绝后目标字节不变**。

**新增限制（明说，不沉默）**：argv 不能携带 NUL 字节，因此 **BOM/UTF-16 文件在 envd 侧拒绝编辑**（本地核可经 `WriteAtomic` 往返），提示改用 `write`（UTF-8）或 `shell`。这是传输约束，非策略选择。

**验证**：envd 后端累计 23 个用例全绿（M0 5 + M2 9 + M3 9）；`remotetools` 全量 45.2s 通过；`remotetargets` / `cmd/aicli-envd-bridge` 通过；vet / gofmt 干净（`go vet` 当场抓到一处 `%d` 参数顺序错误，已修）。实测档位面：readonly 5 / **standard 11**（edit、append_write 已就位）。

**剩余**：见 §8.4 末尾（M2 收尾与 M4）。

### 8.4 M3 已完成 apply_patch（2026-10-28）

**结论：envd 的 `standard` 档已与原厂对齐**——共享核心面 `view/ls/glob/grep + write/edit/append_write/apply_patch` 全部到位，另有 envd 扩展 `host_metrics/mkdir/remove/move`（standard 计 12 个工具）。

**实现方式**：`envd_patch.go` 只做**编排**，补丁语言全数复用纯函数（`ParsePatch`、`ApplyHunks`、`UnifiedDiff`、`PatchSummaryMessage`、`FormatEditSuccessContent`、`PatchNotFoundMessage`、`DocumentExtensionRefusal`、`SanitizePatchPath`）。`mutatecore.PatchResult` 字段全导出，因此渲染层直接复用、结果文本与本地/ssh 面同形。`ApplyPatch` 本身**不可用**：`PatchBindings` 有 `Write/Remove` seam 却没有 Read seam（读走 `Inspect` 的 `os.ReadFile`），在 envd 上会读本地写远端——正是本次收编要消除的混合作用域错误。

**编排守住三条规则**：

1. **预授权**：所有路径先过 jail（delete 与 move 源用 mutation jail，其余用 canonical jail），任一越界则整个补丁不执行；
2. **事务回滚**：已落地的写/建/删记入日志，后续失败时逆序撤销（含删掉本补丁刚创建的文件）；
3. **移动与新增的拒绝规则**：改名不得覆盖已存在目标；add 遇不同内容拒绝、遇相同内容视为回放。

**过程中修正的两个真实缺陷**（均由测试抓出）：

- 纯改名（`*** Move to:` 无 hunk）被送去调 `ApplyHunks`，而它对空 hunk 列表是故意报错的 → 改为仅在 `len(Hunks) > 0` 时应用。
- 写入逻辑若按原状会在 edit 与补丁编排里各一份 → 抽出 `storeBytes`（NUL 检查、上限、写后 stat 确认为单一实现），`store` 退化为其包装。

**验证**：envd 后端累计 **31 个用例全绿**（M0 5 + M2 9 + M3 17）；`remotetools` 全量 35.4s 通过；`remotetargets` / bridge 通过；vet / gofmt 干净。排序类断言含：stale hunk 后本补丁新建的文件已消失且目标字节未变；越界路径使同一补丁的**内侧文件也未被改**。

**限制**：与 edit 同源——内容经 argv，含 NUL（BOM/UTF-16）一律拒绝并说明替代路径。

**剩余**：M2 收尾（`shell` + `--allow-command*`、`pty_*`、`watch_dir`）；M4（`remotetargets` 展开切换 + bridge 测试迁移）。

- 实施结论：（M1–M5 完成后继续回填：实际耗时 / 偏差 / 遗留项）

---

*本计划所有代码引用（`tools.go`/`jail.go`/`main.go`/`registry.go`/`contract.go` 等）均来自本地仓库 2026-10-28 实读；估时为静态推算，M0 完成后收敛。*