# 工具层威胁模型 → 测试映射（P1 证据）

- 设计：`docs/plan/tool-layer-abstraction-design-20261022.md` §16.3
- 审核文档：`docs/plan/tool-layer-abstraction-design-20261022-review.md` §11.1
- 目的：把 §16.3 的每条威胁映射到仓库中真实存在的测试；缺口显式标注，
  作为独立安全复审的输入。

## 1. 映射表

| # | 威胁 | 主要缓解 | 测试证据 | 状态 |
|---|------|----------|----------|------|
| T1 | 被攻陷的远程 server 伪造 metadata（skip/budget/tail） | §9.3/§9.6 trust clamp、Meta allowlist、hard cap | `internal/tools/mcp_result_trust_test.go::TestSanitizeMCPResultRemoteFoldsMaliciousSkip`、`TestSanitizeMCPResultLocalIsUntouched`、`TestMCPContextOriginUsesConfiguredScope` | ✅ 已覆盖（恶意 skip/budget clamp） |
| T1b | wire 层超大响应绕过 decode 预算 | `MaxMCPResponseBytes` / `REMOTE_RESPONSE_TOO_LARGE` | `internal/mcp/transport/response_limit_test.go::TestResponseLimitRoundTripperRejectsOversizedBody`、`TestResponseLimitAllowsExactLimit` | ✅ 已覆盖（decode 前按字节拒绝，恰好等于上限放行） |
| T2 | 本地/远程/跨 target artifact id 混用 | `ArtifactRef` + target 校验 + `GetOwned` | `internal/remotetools/artifact_scope_test.go::TestArtifactStorePutRefNamespacesTarget`、`TestRemoteArtifactReadRejectsNamespace`、`TestConfigRejectsInvalidTargetName`；`jobs_test.go::TestArtifactReadRejectsTraversal`、`TestArtifactArchiveAndRead` | ✅ 已覆盖（scope/namespace/traversal） |
| T2b | 归档失败/过期错误码 | `ARTIFACT_WRITE_FAILED` / `ARTIFACT_EXPIRED` | `artifact_prune_test.go::TestArtifactStorePrunesExpiredFiles`（72h 回收 + NOT_FOUND expiry 提示）；`artifact_write_failure_test.go::TestShellReportsArtifactWriteFailure`（存储失败 fail-closed） | ✅ 已覆盖 |
| T3 | tool poisoning（描述/schema 注入） | 描述/schema 上限；untrusted 只读；canonical name | `internal/mcp/registry/tool_poisoning_caps_test.go::TestRegisterToolCapsHostileDescription`（4 KiB 描述上限 + 截断标记 + metadata）、`TestRegisterToolQuarantinesOversizedSchema`（64 KiB schema fail-closed）、`TestRegisterToolEnforcesPerServerBudget`/`TestServerToolLimitIsConfigurable`（单 server 256 预算，可配 `maxTools`）、`TestQuarantineTableIsBounded`（隔离表有界 + overflow 计数）、`TestCapToolDescriptionKeepsUTF8Boundaries`、`TestConcurrentRegisterToolCapsAreDeterministic`；untrusted 只读：`policy/tool_policy_test.go::TestToolExecutionPolicy_AllowToolInfo_BlocksRemoteWrite`、`policy/filter_test.go::TestFilterToolInfos_UsesGovernanceChecks`；canonical name：`registry/registry_test.go`（64 字符上限/碰撞隔离） | ✅ 已覆盖（上限 + untrusted 只读 + canonical name） |
| T4 | 路径穿越 / symlink / TOCTOU | PathResolver + sandbox + symlink 复检；特殊文件拒绝 | `toolkit/tools/path_guard_test.go`（FIFO/特殊名）、`device_path_deny_test.go`（device deny）、`remotetools/p4_test.go::TestRootsNarrowOnly`、`roots_transport_test.go`（roots 只能收窄）、`executor/sandbox_symlink_review_test.go` | ✅ 已覆盖 |
| T5 | 命令注入 / 元字符 / 危险命令 | CommandPolicy + shellrisk；deny 优先；空 allowlist 全拒 | `remotetools/write_test.go::TestShellRejectsMetachars`、`TestShellRejectsDestructiveCommand`、`TestShellFailsClosedWithoutAllowlist`；`policy/engine_safety_test.go`（breaker callback/allow rule/plan/dont_ask 全矩阵）；`policy/specifier_test.go`（复合命令尾部变更拒绝） | ✅ 已覆盖 |
| T6 | secrets 进入日志/预览 | env allowlist；token hash；命令 preview 默认关闭；不记录文件内容 | `remotetools/audit_secret_hygiene_test.go::TestAuditLogsCarryNoSecret`（真实 HTTP tools/call 后，断言 audit/access 两个 sink 均不含 bearer token 与命令输出，并**反向确认** `principal=bearer:<hash>`、`command_hash`、`decision=allow`、`bytes_out` 存在，避免“请求根本没记录”导致的假绿）；`audit_fields_test.go`（hash/preview/deny/exit code/request info）；`server_accesslog_test.go`（access log 不含 Authorization）；`p4_test.go`（mTLS） | ✅ 默认姿态已覆盖；残留见下方 T6 说明 |
| T7 | SSRF / 恶意 URL | v1 fetch/download/web_search 仅 local-only，不进远程 | `toolkit/tools/fetch_test.go`、`download_test.go`（本地边界）；远程 profile 不含网络工具 | ⚠️ 设计上 v1 deferred；远程化前必须补 SSRF/DNS/重定向测试 |
| T8 | prompt injection（远程文件/网页） | 外部内容标注不可信；policy 不读内容做授权 | `mcp_result_trust_test.go`（trust 只来自 config）；web_search 外部内容 notice（工具描述） | ⚠️ 部分覆盖：缺“不可信内容不能触发授权升级”的端到端测试 |
| T9 | subagent 权限放大 | read-only boundary、BlockDelegation、工具白名单 | `agent/read_only_tool_surface_test.go`、`policy/tool_policy_test.go::TestToolExecutionPolicy_BlockDelegationOverridesRuntimeEssentialBypass`、`agent/spawn_subagents_writer_policy_test.go` | ✅ 已覆盖 |
| T10 | 并发/大输出 DoS | maxConcurrentCalls + 429、输出硬上限、超时/进程组清理 | server 侧：`remotetools/concurrency_test.go::TestConcurrencyMiddlewareLimitsInflight`（429 + `Retry-After: 1`）、`remotetools/metrics_test.go`、`gateway/ratelimit/ratelimit_test.go::TestMiddlewareReturns429`；client 侧：`mcp/transport/rate_limit_test.go::TestRateLimitRoundTripperWaitsThenReturns`（遵循 Retry-After）、`TestRateLimitRoundTripperPreservesResponse`（429 仍作为响应透传，避免 SDK 拆会话）、`TestParseRetryAfterBoundedAndLenient`（5s 上限/异常值不等待）、`TestRateLimitRoundTripperContextCancel`、`TestRateLimitRoundTripperLeavesOtherStatusesAlone`；输出侧：`toolcore/output` fold 测试、`shellexec/capture_bench_test.go`（capture ≤ limit 断言）、`mcp/registry/tool_poisoning_caps_test.go::TestQuarantineTableIsBounded` | ✅ 已覆盖（429/Retry-After 双向；client 不破坏 SDK transient 语义） |
| T11 | direct CLI 绕过审批/策略 | 同一 `policy.Engine`；`--yes` 只确认 ask；无 bypass 开关 | `internal/toolctl/cli_test.go::TestHardAskIsNotApprovedByYes`、`TestShellRequiresYesForApproval`、`TestPermissionModePlanDeniesWrites`、`TestServeHTTPRequiresTokenForNonLoopback` | ✅ 已覆盖 |
| T12 | token 泄漏/重放 | TLS/mTLS、token 轮换、日志只存 hash、非回环强制 token、常量时间比较 | `remotetools/p4_test.go::TestMutualTLSAuthorizesWithoutToken`、`TestMutualTLSRejectsWithoutClientCert`、`TestMutualTLSRequiresTLSFiles`；`toolctl/cli_test.go::TestServeHTTPRequiresTokenForNonLoopback`；轮换：`remotetools/token_rotation_test.go::TestBearerTokenRotationStopsAcceptingTheOldToken`（重启后旧 token 立即 401，不留双活窗口；未知/缺失 token 也 401）；泄漏：`TestAccessLogRecordsRefusalsWithoutBearerToken`（401/200 都落 access log，但 token 原文不出现）；非可逆标签：`request_info.go::shortHash`（`bearer:<8 hex>`）+ `audit_secret_hygiene_test.go`（audit 侧同样不含原文） | ✅ 已覆盖（轮换语义 + 两个 sink 不泄漏 + mTLS）；残余风险见下方 T12 说明 |
| T13 | 构建/部署链篡改 | commit/version pinning、发布产物校验、最小权限、canary | 部署流程文档；无仓库内自动化测试 | ⚠️ **流程缺口**：需发布签名/校验与 canary 记录 |

## 2. 结论

- 已覆盖：T1、T1b、T2、T2b、T3、T4、T5、T6（默认姿态）、T9、T10、T11；
- 部分覆盖：T7、T8、T12；
- 明确缺口：T13（构建链，属发布流程而非仓库内测试）。

T10 补充说明：client 侧原本**没有任何** 429 处理，`Retry-After` 被丢弃；
`fix(mcp): honor Retry-After on remote 429` 之后才有双向覆盖。

`remotetools` 的 legacy `job_*` 路径曾在 `-race` 下暴露真实数据竞争
（`JobManager.Start` 把 supervisor 正在原地改写的同一个 `*Job` 指针交给调用方，
`job_start` 读 `job.State` 与 `JobManager.supervise` 写冲突，
表现为 `TestJobsSurviveServerRestart` 失败）。已由
`fix(remote): return an immutable job record from Start` 修复：`Start`
改为返回 supervisor 启动前的快照（其余读取路径 `Status/ReadOutput/Cancel/List`
一直走磁盘记录，本就安全）。回归测试
`TestJobStartReturnsImmutableRecord` 在修复前失败、修复后通过，
`./internal/remotetools -race -count=2` 现已干净。

T6 残留（有意的 opt-in，不是 bug）：把 `audit.commandPreviewBytes` 调成非 0 时，
preview 记录的是**命令原文前 N 字节**，因此作为命令行参数出现的凭据会原样落盘
（负控制实测：`command_preview: "echo sk-live-0123456789abcdef"`）。这一语义在代码里
是显式声明的：`remotetools/audit.go` 的 `CommandPreview` 字段注释
（“opt-in … may contain credentials, so it is empty by default”）与
`remotetools/config.go` 的 `CommandPreviewBytes` 注释（“Zero keeps the default
privacy posture”）。默认值 0 是安全的；威胁模型写的缓解是“preview **上限**”，
而长度上限≠脱敏，所以开启后仍需运维自行判断。
若要让 preview 可用于排障又不泄漏，需要单独的**参数级脱敏**设计（哪些形态算密钥、
如何避免误删合法审计信息），交给独立安全复审定稿，不在本轮擅自加启发式规则
（现有测试以字节精确断言 preview 语义，启发式改写会掩盖真实回归）。

T12 说明：`Server` 只在构造时读一次 `http.tokenEnv`（`server.go:89`），比较走
`subtle.ConstantTimeCompare`（`server.go:498`）。因此**轮换等于重启**，且重启后
旧 token 立刻失效——运维次序已写进 `docs/user-guide/aicli-remote-tools.md` §9.1。
这消除了“重启后仍有双活窗口”的重放面，但 §16.3.3 列出的残余风险仍在：
token 一旦在被窃取后、轮换前仍然可用；进程存活期内没有在线换 token 的通道。
真正的短 TTL/多凭据轮换需要协议级设计（同时接受新旧两个凭据一个窗口），
属独立安全复审与后续 PR 的范围，本行只声明**现有机制的语义已被测试固定**。

T3 的“不执行描述中的指令”属行为约束（policy 不以描述内容做授权、外部内容按数据处理），
由 T8 的端到端项继续跟踪；本行只声明**上限/只读/命名**三条机制已有测试。

独立安全复审（review §11.1 P0-1/P0-2/P0-3）应优先针对上表 ⚠️/缺口项；
新增测试后再更新本映射。

## 3. WSL 238 实测复跑（2026-10-23）

- `go test -p 1 -count=1 ./internal/remotetools/... ./internal/toolcore/... ./internal/tools/...`：PASS；
  `CGO_ENABLED=1 CC="zig cc" go test -race -p 1 ./internal/remotetools/...`：PASS。
- Live 238 smoke 覆盖：T2（跨 target `ARTIFACT_SCOPE_DENIED`）、T4（`PATH_DENIED`）、
  T5（`whoami`/`rm -rf /` 均 `COMMAND_DENIED`）、T3 的 surface 上限（六个工具描述
  150–185B、schema 275–521B，均远低于 4 KiB/64 KiB/256 上限）。
- Live 238 soak：100/100，failed=0，无 429。**不**构成 T10 的 `Retry-After` 证据。
- T6/T12 仍以单测为准；本次未检查 238 侧审计日志、未轮换 token、未做 live 429。
- 证据：`docs/evidence/toolcore-238-wsl-20261023.md`。
