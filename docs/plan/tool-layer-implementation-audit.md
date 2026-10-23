# 工具层抽象实施审计（2026-10-23）

**设计文档**: `docs/plan/tool-layer-abstraction-design-20261022.md`  
**审核文档**: `docs/plan/tool-layer-abstraction-design-20261022-review.md`  
**威胁模型映射**: `docs/plan/tool-layer-security-test-map.md`  
**目标**: 核对 §8–§17 各节的实现覆盖率，确认发布门证据是否到位。

---

## 一、执行摘要

| 维度 | 覆盖率 | 关键结论 |
|------|--------|----------|
| **P0/P1 设计冻结** | ✅ 已冻结 | P0-1/2/3/4内容补完；安全门独立评审未做但缺口已显式标记 |
| **契约层验证 (PR0)** | ✅ 完成 | 工具清单双向一致测试、MCP surface 字段、错误码映射 |
| **Output/core (PR1)** | ✅ 完成 | trust clamp, wire cap, artifact scope quarantine, canary + rollback |
| **Read-only core (PR2)** | ⚠️ 部分 | glob matcher 已收敛为单一实现；grep/rigrep vs walker conformance 与 benchmark CI gate 仍缺 |
| **CLI/direct (PR3)** | ✅ 已验证 | WSL 构建 + `mcp tools`/`mcp test`/`--remote` 连接；`cmd/aicli/commands` 全量回归 PASS（197.1s） |
| **威胁模型 T1–T13** | ✅ 大部分关闭 | T1/T2/T3/T4/T5/T9/T10/T11/T12 已覆盖；T6 默认姿态关闭；T7/T8 部分；T13 流程缺口 |
| **Canary/回滚** | ✅ 完成 | 238 target 实测，binary 替换 ±5.5s，100 次 soak 无失败 |
| **CI 回归门槛** | ❌ 缺失 | `tool-layer-bench-gate.ps1` 脚本存在但 baseline 在 Windows laptop，需 CI 机器固化 |
| **文档完善度** | ✅ 良好 | user-guide / security-test-map / review 三者联动更新 |

---

## 二、章节级覆盖检查

### §8 Core Surface & Adapter

#### §8.4 CLI/direct 契约 (PR3)
- **要求**: JSON/exit code/审批同一性；no bypass switch；direct 绕过检测。
- **现状**: `internal/toolctl/cli_test.go::TestHardAskIsNotApprovedByYes` 等验证了 `--yes` 不绕过审批；exit code/JSON schema 见 `toolcore/contract.go` 断言。2026-10-23 在 WSL 用当前源码构建 `aicli`，实测 `aicli mcp tools`、`aicli mcp test`（view/shell）与 `aicli chat --remote` 连接；`cmd/aicli/commands` 全量回归 PASS。
- **缺口**: W-21 模型调用因 WSL provider 回环不可达而 BLOCKED；不影响 CLI/远程工具契约。
- **状态**: ✅ 已验证（全量 CLI 回归 + 客户端调用链）。

#### §8.7 MCP surface (PR0)
- **要求**: auth/roots/session/name/limits/error 字段齐全。
- **现状**: 
  - `internal/mcp/manager` 的 `UpsertRequest` 含所有字段；
  - `internal/mcp/registry` 的 canonical name + 64 字符上限（§8.7.3）已落地；
  - `internal/remotetools/server.go` 的非回环强制 token 验证（§8.7.4）。
- **状态**: ✅ 设计+实现+测试全闭环。

### §9 Output Contract + Trust

#### §9.3/§9.6 Metadata Trust Clamp (PR1)
- **要求**: 恶意 skip/budget/tail 不能突破硬上限；trust 仅来自 server config。
- **现状**: 
  - `internal/toolcore/output` 的 `SanitizeRemoteResult` + `MCPResultContext` 实现；
  - `internal/tools/mcp_result_trust_test.go` 验证了 remote/fold 与 local 的差异；
  - wire cap `MaxMCPResponseBytes` (8 MiB) 在 transport 层拦截。
- **状态**: ✅ 已覆盖。

#### §9.7 Artifact Scope/ID Target (PR1)
- **要求**: local/remote/target namespace严格隔离；跨 scope 拒绝；归档失败 error。
- **现状**: 
  - `internal/remotetools/artifact_scope_test.go` 验证 cross-target 拒绝 (`ARTIFACT_SCOPE_DENIED`)；
  - `remotetools/runtime.go` 与 `toolcore/artifact_handler.go` 对过期/写入失败的错误码处理。
- **状态**: ✅ 已覆盖。

### §10 Gate & Safety

#### §10.5/§10.6 Gate Order + Direct/CLI同一性 (PR3)
- **要求**: 0–18 gate 顺序不可被 allow rule/callback 覆盖；direct/CLI 走同一 `policy.Engine`。
- **现状**: 
  - `internal/policy/engine_safety_test.go` 验证了 hard safety gates 的优先级；
  - `internal/toolctl/cli_test.go::TestShellRequiresYesForApproval` 验证 direct 绕过检测；
  - `internal/policy/tool_policy_test.go::TestToolExecutionPolicy_BlockDelegationOverridesRuntimeEssentialBypass`。
- **状态**: ✅ 已覆盖（单测齐全）。

#### §11.1 Local-only Final-result Only (PR1)
- **要求**: PTY/jobs/streaming 仅限本地；远程只返回最终结果。
- **现状**: 
  - `remotetools/config.go` profile 配置：readonly 不允许 shell/job_start；
  - `remotetools/shell_conformance_test.go` 验证 streaming 只在本地生效；
  - remote tools 通过 `--legacy-jobs` 明确 opt-in 才能暴露 job_*。
- **状态**: ✅ 已覆盖（profile + 测试）。

### §12 Event Schema + Observability

#### §12.4 Audit Access Rotation
- **要求**: event schema 脱敏；audit/access log 旋转/retention；metrics label 基数限制。
- **现状**: 
  - audit/access log rotation: `remotetools/config.go::AuditConfig::MaxSizeMB/MaxBackups`；
  - `remotetools/server_accesslog_test.go` 证明 Authorization header 不进入日志；
  - metrics label 基数由 `internal/remotetools/metrics.go` 的 `tool/surface/decision` 三标签限定。
- **状态**: ✅ 已覆盖（配置+测试）。

### §13 Migration + Versioning

#### §13.0 Tool Inventory Matrix (PR0)
- **要求**: toolregistry ↔ manager.go 双向一致。
- **现状**: 
  - `internal/toolbroker/toolregistry_contract_test.go` 验证 broker canonical 名与 registry 一致；
  - `internal/toolregistry/tool_naming_contract_test.go` 反向验证 legacy alias 解析。
- **状态**: ✅ 已覆盖（双向对账测试）。

#### §13.7 Rollout Flags
- **要求**: feature flag 可开关；feature flag 可观察；版本轴兼容。
- **现状**: 
  - `internal/remotetools/config.go` 的 `LegacyRemoteJobs` 为 backport gate；
  - `internal/mcp/registry` 的 `maxTools` 可通过 YAML 动态调整（但需重启服务）；
  - 无全局“toolcore output enable”flag，因为 PR1/2/3 是逐步收敛而非开关切换。
- **状态**: ⚠️ 部分覆盖（backport gate 存在，但 rollout flag 按表面分阶段而非统一开关）。

### §14 Read-only Core (PR2)

#### Conformance Corpus
- **要求**: view/grep/glob/ls 行为一致（ignore/binary/long-line/case-sensitivity）。
- **现状**: 
  - `remotetools/search_engine_test.go::TestRemoteRipgrepMatchesWalkerConformance` 验证 grep 一致性；
  - `internal/toolkit/listable_test.go` 的 shouldList 策略映射。
- **2026-10-23 收敛**: `searchcore.GlobMatcher` 已删除 regex 实现，改为与本地 glob 共用
  `MatchGlobPath`/`MatchGlobSegments`；新增 `glob_conformance_test.go` 锁定同一语义。
  `edit`/`apply_patch` 的行比较与空白/缩进 helper 已上移 `mutatecore`；
  `filetransport.LocalService` 的写/追加也改为调用 `mutatecore.Write/Append`。
  `remotetools/glob_conformance_test.go` 直接锁定 remote 侧使用同一 matcher。
- **缺口**: 无跨平台 conformance suite（Linux/Windows 文件命名/路径差异），需扩展；
  `TestGrepToolRealRipgrepStopsAfterPage` 在基线（未含本轮改动）同样失败，属既有问题；
  `remotetools` 偶发 `TestJobListReportsJobs` TempDir 清理竞态（重跑通过）。
- **状态**: ⚠️ 基础覆盖，扩展待 PR2。

#### Benchmark Thresholds (PR2)
- **要求**: 10k/100k 文件基准，p50/p95 阈值，CI 回归门槛。
- **现状**: 
  - `backend/scripts/tool-layer-bench*.ps1` 脚本存在；baseline 记录在 `docs/performance/tool-layer-benchmark-baseline-20261022.md`；
  - 当前 threshold 未设为 CI gate（仅本地 run），需静默机重新标定基线并启用 gate。
- **状态**: ⚠️ 语料/脚本就绪，但 CI gate 未激活。

### §15 Performance

#### §15.2.1/§15.2.2 Benchmarks
- **要求**: 语料规模、阈值、基线报告、CI 方法。
- **现状**: baseline 报告已完成；阈值 provisional 但未固化到 CI。
- **状态**: ⚠️ 同 PR2，需 CI gate。

### §16 Security Threat Model (PR1–3)

| 威胁 | 缓解 | 实现/测试 | 状态 |
|------|------|----------|------|
| T1 | metadata clamp | `mcp_result_trust_test.go` | ✅ |
| T1b | wire cap | `response_limit_test.go` | ✅ |
| T2 | artifact scope | `artifact_scope_test.go` | ✅ |
| T2b | expire/write fail | `artifact_prune_write_failure_test.go` | ✅ |
| T3 | poisoning caps | `tool_poisoning_caps_test.go` | ✅ |
| T4 | path traversal | `sandbox_symlink_review_test.go` | ✅ |
| T5 | command injection | `write_test.go::TestShellRejectsMetachars` | ✅ |
| T6 | secrets in logs | `audit_secret_hygiene_test.go` | ✅ (默认姿态) |
| T7 | SSRF | deferred (v1 no network tools) | ⚠️ |
| T8 | prompt injection | trust notice in `mcp_result_trust_test.go` | ⚠️ |
| T9 | subagent boost | `read_only_tool_surface_test.go` | ✅ |
| T10 | DoS 429/Retry-After | `rate_limit_test.go` | ✅ |
| T11 | CLI bypass | `cli_test.go::TestHardAskIsNotApprovedByYes` | ✅ |
| T12 | token rot/replay | `token_rotation_test.go` + `ConstantTimeCompare` | ✅ |
| T13 | build chain tampering | process gap | ⚠️ |

### §17 Acceptance Gates (PR0–3)

#### §17.4 P0–P1 Docs Gates
- **要求**: 设计 frozen；测试骨架/契约对齐。
- **现状**: 已通过 Review；P0 条目全部 check-boxed in review doc。
- **状态**: ✅。

#### §17.5 Best Practices
- **要求**: DoR/DoD、测试分层、安全复审、canary/no-go。
- **现状**: 
  - canary/rollback 已完成；
  - 独立安全复审未做（外部依赖）；
  - No-go 条件已文档化但未自动化。
- **状态**: ⚠️ 人工 gate。

---

## 三、遗留缺口与行动项

| # | 项目 | 责任 | 紧急度 |
|---|------|------|--------|
| 1 | `cmd/aicli/commands` 全量回归复跑 | CI runner | High（阻塞 PR3 发布） |
| 2 | benchmark baseline 固化 + CI gate | Release owner | Medium（性能回归风险） |
| 3 | conformance corpus 扩展 (跨平台) + 修复 `TestGrepToolRealRipgrepStopsAfterPage` 基线失败 | PR2 owner | Medium（质量与回归可信度） |
| 4 | 独立安全复审签字 | 第三方 reviewer | High（发布门） |
| 5 | release 流程文档化（T13） | Release owner | Medium（合规需求） |
| 6 | PR0/1/2 代码合并策略 | Tech lead | Low（工程优化） |
| 7 | WSL 内模型 provider 回环不可达（`127.0.0.1:18900`） | 环境/CI owner | Low（不阻塞远程工具契约；W-21 已标 BLOCKED） |

---

## 四、结论

**整体评估**: 核心机制已实现且通过测试验证，安全威胁模型基本覆盖；发布门前置条件（独立安全评审、CI 全回归、基准门）尚未完成但已明确缺口和责任。

**WSL 238 实测（2026-10-23）**: 协议级 smoke + 100 次 soak 全绿；`aicli mcp tools`/`mcp test` 通过；`aicli chat --remote` 已连上 238，但模型调用因 WSL 无法访问 Windows 回环 provider 而 BLOCKED。证据见 `docs/evidence/toolcore-238-wsl-20261023.md`。

**下一步**:
1. 等待 CI runner 上 `cmd/aicli/commands` 复跑；
2. 在 CI 机器上运行 `tool-layer-bench-gate.ps1 -UpdateBaseline` 固化新基线；
3. 安排独立安全复审（至少覆盖 P0-1/2 及 T3/T10/T12 新增实现）；
4. 启动 PR2 的 conformance 扩展与基准门激活。

**冻结状态**: P0/P1 设计已冻结（Round 2），后续变化将走 PR backlog 而非重复设计复审。


---

## 五、WSL 实测 238 远程 target 计划（2026-10-23）

> 本节是待批准的执行计划；批准前不修改产品代码、不重启 238 服务、不替换 238 二进制。

### 5.1 背景与已确认事实

| 项 | 现状 | 证据 |
|----|------|------|
| WSL | Ubuntu 24.04.3 LTS，用户 `wangweisheng` | `\\wsl.localhost\Ubuntu\etc\lsb-release` |
| WSL Go | `/usr/local/go/bin/go` 存在 | `\\wsl.localhost\Ubuntu\usr\local\go\bin` |
| WSL PowerShell | **不存在**（`/usr/bin/pwsh` 缺失） | 路径探测 |
| WSL aicli | `~/.local/bin/aicli` 为 2026-10-12 构建，可能落后于当前源码 | 目录列表 |
| WSL 旧 worktree | `~/aar-wt/aar-linux` 在 `d036c3d4`，落后于主仓 `029f4931` | `.git` 指针 + 主仓 `git log` |
| 主仓 | `main` 领先 origin 39 个提交；T12 测试/文档尚在工作区 | `git status` |
| 238 | `http://172.18.3.238:18765/mcp`，workspace `/home/n9e/itsm/repo` | 既有 canary 证据 |
| 238 运维限制 | `n9e` 不能无密码 `systemctl restart`；本轮不重启、不部署 | `docs/evidence/toolcore-238-canary-20261023.md` §5 |
| Token | `E:\projects\itsm\itsm\.env` 中的 `AICLI_REMOTE_TOKEN`（WSL 路径 `/mnt/e/projects/itsm/itsm/.env`） | 既有 canary 脚本参数 |

**结论**：WSL 可作为 Linux 执行环境，但现有 `scripts/canary-remote-tools.ps1` 无法直接在 WSL 运行；需要补一个 Linux 原生的 Go canary。

### 5.2 目标与非目标

**目标**

1. 在 WSL 内构建当前 `backend` 源码，证明 Linux 构建与相关单测可复跑。
2. 从 WSL 对真实 238 target 执行 MCP 协议级 smoke + 100 次 soak，覆盖：
   - `initialize` / `tools/list` / `tools/call`；
   - `view` / `ls` / `glob` / `grep` / `shell` / `artifact_read`；
   - 长输出折叠与 artifact 翻页；
   - artifact 跨 target 越权拒绝；
   - 路径穿越拒绝、命令 allowlist 拒绝。
3. 在 WSL 内用当前源码构建的 `aicli` 验证 `--remote` 的真实客户端接入（工具面可见性；模型调用为可选增强）。
4. 产出可复核的证据文档，并明确区分「协议级实测通过」「aicli 集成通过」「因环境不可用而跳过」。

**非目标**

- 不修改 238 上的任何文件、配置、服务或二进制。
- 不执行 `write` / `edit` / `apply_patch` / `job_start` 等写类远调用。
- 不把 429/重试、token 轮换、审计脱敏等已有单测项目伪装成 live 证据。
- 不在本轮做 PR2 benchmark / CI gate 标定。

### 5.3 测试矩阵

| ID | 层 | 用例 | 调用/命令 | 期望 | 证据形式 |
|----|----|------|-----------|------|----------|
| W-01 | 环境 | WSL/Go/网络工具 | `uname -a`、`go version`、`command -v curl jq git` | 全部可用 | 文本 |
| W-02 | 网络 | 238 TCP 可达 | `timeout 5 bash -c '</dev/tcp/172.18.3.238/18765'` | exit 0 | 文本 |
| W-03 | 鉴权 | 无 token 请求 | `curl -sS -o /dev/null -w '%{http_code}' .../mcp` | `401`（或文档规定的拒绝码） | 状态码 |
| W-04 | 协议 | `initialize` + `tools/list` | canary `-list-tools` | 成功；工具名全部带 `remote_238__`；六个必需工具存在；数量 ≤256；描述 ≤4 KiB、schema ≤64 KiB | JSON 摘要 |
| W-05 | 读面 | `view README.md` | `remote_238__view` | `isError=false`，非空 | 字节数/预览 |
| W-06 | 读面 | `ls .` | `remote_238__ls` | 成功，含预期目录项 | 字节数/预览 |
| W-07 | 读面 | `glob **/*.md` | `remote_238__glob` | 成功，非空 | 字节数/预览 |
| W-08 | 读面 | `grep README` | `remote_238__grep` | 成功，命中 `README.md` | 字节数/预览 |
| W-09 | 执行面 | `shell "git status"` | `remote_238__shell` | 成功，输出含仓库状态 | 字节数/预览 |
| W-10 | 折叠 | `shell "git log"` | `remote_238__shell` | 输出被折叠并返回 `artifact_id` | artifact id |
| W-11 | 归档 | `artifact_read` 翻页 | 以上 artifact，offset=0/limit=4096 | 成功，内容与 git log 一致 | 字节数/预览 |
| W-12 | 越权 | 跨 target artifact | `artifact_ref=remote:999:<art>` | `isError=true`，文本含 `ARTIFACT_SCOPE_DENIED` | 错误码 |
| W-13 | 沙箱 | 路径穿越 | `view ../../etc/passwd` | `isError=true`，含 `PATH_DENIED` | 错误码 |
| W-14 | 命令策略 | 非 allowlist 命令 | `shell "whoami"` | `isError=true`，含 `COMMAND_DENIED` | 错误码 |
| W-15 | 命令策略 | 破坏性命令 | `shell "rm -rf /"` | `isError=true`，含 `COMMAND_DENIED` | 错误码 |
| W-16 | 稳定性 | 100 次混合调用 | canary `-scenario soak -count 100` | `failed=0`、`error_rate=0`；记录 p50/p95；无意外 429 | JSON |
| W-17 | 单测 | Linux 相关包 | `go test ./internal/remotetools/... ./internal/toolcore/... ./internal/tools/...` | 全部 PASS | 测试日志 |
| W-18 | 竞态 | remote 包 | `go test -race ./internal/remotetools/...` | PASS | 测试日志 |
| W-19 | 构建 | 当前源码构建 | `go build ./cmd/aicli ./cmd/aicli-remote-tools` | 成功 | 二进制/exit 0 |
| W-20 | 集成 | `aicli --remote` 工具面 | 空工作区 + `aicli tools` 或等价清单命令 | 出现 `remote_238__*`，无前缀冲突 | 命令输出 |
| W-21 | 模型调用 | 真实模型调用远程工具（可选） | `aicli chat --no-interactive --remote ... --permission-mode plan --prompt ...` | transcript 中出现真实 `remote_238__view` 调用及结果 | transcript 摘要 |

**强制门槛**：W-01～W-19 必须全绿。W-20/W-21 若因 WSL 侧 provider 不可达而失败，记录 `BLOCKED` 与原始错误，不视为产品失败，但不得写成通过。

### 5.4 执行步骤

#### 步骤 0：预检（只读）

```bash
wsl.exe -d Ubuntu -- bash -lc '
  set -e
  uname -a
  /usr/local/go/bin/go version
  command -v curl jq git python3
  timeout 5 bash -c "</dev/tcp/172.18.3.238/18765"
'
```

鉴权探测（不打印 body/token）：

```bash
wsl.exe -d Ubuntu -- bash -lc '
  code=$(curl -sS -o /dev/null -w "%{http_code}" http://172.18.3.238:18765/mcp || true)
  echo "mcp_http_status=$code"
'
```

若 TCP 不通，先在 Windows 侧做同一探测以区分「WSL 网络问题」与「238 不可达」；不得用本地 mock 服务替代 238。

#### 步骤 1：新增 Linux 原生 canary（唯一代码新增）

新增 `backend/scripts/remotetools_canary.go`（`//go:build ignore`，不进入生产二进制），与现有 PowerShell canary 对齐：

- flags：`-url`、`-env-file`、`-token-env`、`-prefix`、`-scenario smoke|soak`、`-count`、`-list-tools`、`-timeout`；
- 从 `.env` 读取 token，兼容 CRLF 与引号；**绝不打印 token**；
- 使用现有 MCP SDK `StreamableClientTransport` + bearer RoundTripper；
- smoke 覆盖 W-04～W-15；
- soak 预热生成 artifact 后执行 100 次混合调用，输出 JSON（count/ok/failed/error_rate/p50_ms/p95_ms/按工具分组）；
- 任一断言失败 exit 1，并输出失败用例名与脱敏后的错误。

不修改 `internal/` 或 `cmd/` 下任何生产代码。

#### 步骤 2：WSL 构建与单测

```bash
wsl.exe -d Ubuntu -- bash -lc '
  set -e
  cd /mnt/e/projects/ai/ai-agent-runtime/backend
  export PATH=/usr/local/go/bin:$PATH
  export GOPROXY=https://goproxy.cn,direct GOSUMDB=off
  export GOMAXPROCS=4
  mkdir -p /tmp/aar-wsl/bin /tmp/aar-wsl/logs
  go build -p 1 -o /tmp/aar-wsl/bin/aicli ./cmd/aicli
  go build -p 1 -o /tmp/aar-wsl/bin/aicli-remote-tools ./cmd/aicli-remote-tools
  go test -p 1 ./internal/remotetools/... ./internal/toolcore/... ./internal/tools/... 2>&1 | tee /tmp/aar-wsl/logs/unit.log
'
```

说明：

- 使用 `/mnt/e` 当前源码，避免 WSL 旧 worktree `d036c3d4` 的陈旧代码；
- `-p 1` 限制并行包数，避免重演 Windows 侧内存压力；
- Go 构建缓存位于 WSL ext4（`~/.cache/go-build`），不会写 Windows 磁盘缓存。

竞态测试单独执行：

```bash
wsl.exe -d Ubuntu -- bash -lc '
  cd /mnt/e/projects/ai/ai-agent-runtime/backend
  export PATH=/usr/local/go/bin:$PATH
  export GOPROXY=https://goproxy.cn,direct GOSUMDB=off
  go test -race -p 1 ./internal/remotetools/... 2>&1 | tee /tmp/aar-wsl/logs/race.log
'
```

#### 步骤 3：真实 238 smoke

```bash
wsl.exe -d Ubuntu -- bash -lc '
  cd /mnt/e/projects/ai/ai-agent-runtime/backend
  export PATH=/usr/local/go/bin:$PATH
  export GOPROXY=https://goproxy.cn,direct GOSUMDB=off
  go run ./scripts/remotetools_canary.go \
    -url http://172.18.3.238:18765/mcp \
    -env-file /mnt/e/projects/itsm/itsm/.env \
    -scenario smoke 2>&1 | tee /tmp/aar-wsl/logs/238-smoke.log
'
```

smoke 输出必须逐项列出 W-04～W-15 的 PASS/FAIL；失败立即停止，不继续 soak。

#### 步骤 4：100 次 soak

```bash
wsl.exe -d Ubuntu -- bash -lc '
  cd /mnt/e/projects/ai/ai-agent-runtime/backend
  export PATH=/usr/local/go/bin:$PATH
  export GOPROXY=https://goproxy.cn,direct GOSUMDB=off
  go run ./scripts/remotetools_canary.go \
    -url http://172.18.3.238:18765/mcp \
    -env-file /mnt/e/projects/itsm/itsm/.env \
    -scenario soak -count 100 2>&1 | tee /tmp/aar-wsl/logs/238-soak.log
'
```

判定：

- `failed=0` 且 `error_rate=0` 为通过；
- p50/p95 记录并与 Windows canary 基线（p50=228ms、p95=690ms）对比；若 p95 > 1500ms 或出现 429，标记 `INVESTIGATE`，不直接判失败，但必须在证据中说明；
- 不并发压测，避免把限流行为误判为产品缺陷。

#### 步骤 5：aicli 客户端集成

```bash
wsl.exe -d Ubuntu -- bash -lc '
  set -e
  mkdir -p /tmp/aar-wsl/ws
  cd /tmp/aar-wsl/ws
  export AICLI_REMOTE_TOKEN="$(grep -m1 "^AICLI_REMOTE_TOKEN=" /mnt/e/projects/itsm/itsm/.env | cut -d= -f2- | tr -d "\r\"")"
  test -n "$AICLI_REMOTE_TOKEN"
  /tmp/aar-wsl/bin/aicli chat --no-interactive \
    --remote "http://172.18.3.238:18765" \
    --permission-mode plan \
    --prompt "List the available remote_238 tools, then call remote_238__view on README.md with limit 5 and quote the first line."
'
```

先决条件：

- WSL 侧 provider 可访问（`~/.aicli/config.yaml` 中 `free_models` 指向 `127.0.0.1:18900`，可能只在 Windows 侧监听）；
- 若 provider 不可达，先运行 `aicli chat --no-interactive --prompt "Reply OK"` 记录原始错误，再标记 W-21 `BLOCKED`；
- 不为了“跑通”而切换到未知 provider 或伪造 transcript。

若 `aicli` 没有独立的 `tools` 子命令，W-20 以 `chat` 启动日志中的 `remote: added server ...` 和工具面告警为准；命令名称以实际 `--help` 为准，不猜测。

#### 步骤 6：证据落盘

新建 `docs/evidence/toolcore-238-wsl-20261023.md`，固定结构：

1. **环境**：WSL 发行版/内核、Go 版本、源码 commit、执行时间、238 endpoint/workspace；
2. **预检**：TCP、HTTP 状态码、token 来源（只写变量名和文件路径，不写值）；
3. **构建/单测**：命令、exit code、包级 PASS/FAIL 摘要、race 结果；
4. **smoke 表**：W-04～W-15 逐行 `case | expected | actual | duration_ms | evidence`；
5. **soak JSON**：原样粘贴 canary 输出（脱敏后）；
6. **aicli 集成**：命令、工具面输出摘要、transcript 中真实工具调用片段；失败则写 `BLOCKED` + 原始错误；
7. **跳过项**：明确列出未覆盖的 live 场景（429/Retry-After、token 轮换、写类工具、job_*、审计日志），并指向已有单测；
8. **秘密卫生**：执行 `rg -n "Bearer |AICLI_REMOTE_TOKEN=" docs/evidence/toolcore-238-wsl-20261023.md`，确认无 token 值；
9. **结论**：分别给出「协议级」「构建/单测」「aicli 集成」三行结论，禁止合并成模糊的“全部通过”。

同步更新：

- `docs/plan/tool-layer-implementation-audit.md`：仅按实测结果修改 PR3/§17 状态，并链接新证据；
- `docs/plan/tool-layer-security-test-map.md`：只给实际复跑的项加 `WSL re-run` 注记，不把 live canary 冒充 T10/T12 证据；
- 若 W-21 失败，在审计报告“遗留缺口”中新增一条 provider 可达性阻塞，而不是勾选通过。

### 5.5 失败处理与回滚

| 现象 | 处理 |
|------|------|
| WSL 无法访问 238 | 记录 TCP/HTTP 错误；Windows 侧复测区分网络层；停止，不替换目标 |
| 401/403 | 检查 `.env` 路径、CRLF、变量名；不打印 token；仍失败则报告 BLOCKED |
| 工具缺失/旧 schema | 记录 `tools/list` 实际名称与 schema；**不部署/不重启 238**，在证据中标注目标二进制落后 |
| smoke 失败 | 保留完整脱敏日志；不执行 soak；不修改产品代码“救测试” |
| 单测失败 | 区分 Linux 环境问题与代码回归；保留失败包/用例；不勾选 PR3 |
| provider 不可达 | W-21 标记 BLOCKED；W-01～W-20 结果仍有效 |
| 证据含 token | 立即删除该行并重写；提交前用 `rg` 复核 |

**回滚**：本轮只读调用，无需回滚 238；WSL 侧只新增 `/tmp/aar-wsl` 临时目录和 `backend/scripts/remotetools_canary.go`。若 canary 被否决，删除该脚本与临时目录即可，不触碰 238。

### 5.6 验收标准

- [x] W-01～W-19 全部有命令、exit code、结果三段证据；
- [x] smoke 六个工具全部真实调用成功，artifact 折叠/翻页/跨 target 拒绝均有原始错误码；
- [x] soak `failed=0`，p50/p95 已记录，429 情况已说明；
- [x] WSL `go test` 与 `-race` 结果已落盘；
- [x] 证据文档不含 token、Authorization 值或完整 `.env` 内容；
- [x] 审计报告只按真实结果更新，未运行项保持原状态；
- [x] W-21 若未跑通，明确写 `BLOCKED` 及原因，不写“通过”。

### 5.7 风险与边界

- **共享目标风险**：238 是共享环境，所有调用限制为只读命令；不执行 `git fetch`、不写文件、不启动 job。
- **WSL I/O 风险**：`/mnt/e` 跨文件系统编译较慢；使用 `-p 1` 与 ext4 构建缓存，若超时改为 `rsync` 到 `/tmp/aar-wsl/src` 后构建，并在证据中注明副本来源 commit。
- **前缀不确定性**：238 服务端已输出 `remote_238__*`；`--remote` 客户端不会重写 HTTP 工具名。若实际前缀不同，以 `tools/list` 为准并记录，不硬编码猜测。
- **模型不确定性**：W-21 依赖 provider 与模型选择；协议级 canary 才是发布门，模型调用只作为集成佐证。

### 5.8 执行结果（2026-10-23）

**总评**：协议级 PASS；构建/单测/race PASS；`aicli` 工具面与直接调用 PASS；模型调用 BLOCKED（provider 回环），不是远程工具契约失败。

| 项 | 结果 | 关键证据 |
|----|------|----------|
| 预检 | PASS | WSL Ubuntu 24.04.3 / Go 1.26.0 / 238 TCP ok / 无 token HTTP 401 |
| 构建 | PASS | WSL 构建 `aicli`（78,523,534B）与 `aicli-remote-tools`（13,857,889B） |
| 单测 | PASS | `remotetools` 3.395s；`toolcore/*` 与 `tools` 全 ok |
| Race | PASS | `CGO_ENABLED=1 CC="zig cc" go test -race`，`remotetools` 6.503s |
| 238 smoke | PASS | 六个工具、`git log` 折叠为 `art_f28dfc5be74e0aa1672adf28137b4e8c`、artifact 翻页 4096B |
| 安全探针 | PASS | `ARTIFACT_SCOPE_DENIED` / `PATH_DENIED` / `COMMAND_DENIED` ×2 |
| 238 soak | PASS | 100/100，failed=0，p50=113ms，p95=400ms，无 429 |
| `aicli mcp tools` | PASS | 六个 canonical `remote_238__*` 工具，`mcp_name=remote_238` |
| `aicli mcp test` | PASS | `remote_238__view`（README 1–5 行）与 `remote_238__shell`（git status，3087B） |
| `aicli chat --remote` | PASS（仅连接） | 日志 `remote: added server "172-18-3-238"`；随后模型 provider 连接失败 |
| W-21 模型调用 | **BLOCKED** | `dial tcp 127.0.0.1:18900: connect: connection refused`（WSL 无法访问 Windows 回环 provider） |
| 238 变更 | 无 | 全程只读；未写文件、未重启服务、未替换二进制 |

**证据文件**：`docs/evidence/toolcore-238-wsl-20261023.md`

**新增观察**：

1. `tools/list` 未返回 `readOnlyHint` 注解（全部为 false）。本次策略与 trust clamp 不依赖该提示，故不计失败；若后续 UI/策略消费 annotations，需单独立项。
2. `aicli mcp test` 要求 canonical 工具名（`remote_238__view`）；裸名 `view` 返回 `unknown tool "view"`。这是 CLI 易用性问题，不是远程 target 失败。
3. WSL 的 `free_models` provider 绑定 Windows `127.0.0.1:18900`，WSL NAT 下不可达；模型调用因此 BLOCKED。
4. WSL `/tmp` 在实例重启后会清理；本轮二进制/日志改放 `/home/wangweisheng/aar-wsl`。

**未覆盖（与 §7 一致）**：live 429/Retry-After、live token 轮换、写类工具、`job_*`、238 审计日志、独立安全复审。
