# 远程工具：现状与剩余缺口

更新时间：2026-10-21

---

## 1. 已经完成并验证过的

| 能力 | 状态 | 证据 |
|---|---|---|
| 远程工具服务（stdio / HTTP 双传输） | ✅ | `ssh_transports_e2e.go` 用真实 `ssh-client` 双通道跑通 |
| 路径 jail（含 symlink 二次校验） | ✅ | `/tmp/aicli-perm` 实测：`..`、绝对路径、软链逃逸、设备节点全部 `PATH_DENIED` |
| 命令策略（allowlist / deny / 元字符 / 破坏性分类） | ✅ | `layer_probe.sh` 实测五类拒绝 |
| 四层权限模型 | ✅ | `aicli-remote-tools-permissions.md` |
| mTLS | ✅ | 无证书 `pct=000`，带证书 `200` |
| 审计（NDJSON / 0600 / 不泄露正文） | ✅ | 35 条记录，正文关键字命中 0 |
| 指标 | ✅ | `callsTotal=35 denied=7 tools_tracked=13` |
| 能力开关（jobs / artifacts） | ✅ | 14 → 9 工具 |
| 多目标 + 子代理授权收窄 | ✅ | `researcher` 有目标、`implementer` 为 null |
| MCP Roots 收窄 | ✅ | 交集语义，越界记 violation 且忽略 |
| 真实模型端到端（ConPTY） | ✅ | 修 3 处缺陷 + 建站，2m31s 完成 |
| 真实部署（1.0.0 → 2.0.0 + 回滚） | ✅ | 15 项断言 + 回滚实测 |
| 同名远程工具加载期拒绝 | ✅ | `validateRemoteToolNameCollision` |
| 列表/执行判定合并 | ✅ | 快速通道收窄到读族 |
| **内置 remote profile 模板** | ✅ **本次新增** | `aicli profile create x --template remote` |

---

## 2. 本次补的：内置 remote profile

`internal/profile/templates/remote/`，与其它 6 个模板同源（`//go:embed templates` 自动收录）。

```bash
aicli profile create myremote --template remote --use
```

生成后 `profile validate` → **0 错误 0 警告**；`profile show` 的生效面：

```
[tools]
  denylist：view, ls, glob, grep, read, rg, write, edit, apply_patch,
            append_write, multiedit, shell, bash, execute_shell_command,
            job_start, job_output, job_cancel, job_list, job_search,
            job_monitor, job_control, job_input, aicli_exec, todos
[mcp]
  声明：use=[wsl] exclude=[]
  servers：other(excluded), wsl(enabled)
```

设计取舍：

- **denylist 用显式名单，不用 `*`** —— `*` 会把远程工具也一起禁掉（同一个 deny map）。
- **不声明 `prompts:`** —— 没有 prompt 文件时声明 `mode: replace` 只会产生
  "no prompt files found" 警告，训练运维忽略告警。
- **`use_servers` 默认写 `wsl` 并标 TODO** —— 必须让使用者改成自己的 server 名；
  写一个不存在的占位符会让 `validate` 直接失败，写空则完全不收窄。

三条都写进了 `remote_template_test.go`。

---

## 3. 剩余缺口

### 3.1 高优先级：影响正确性

| # | 缺口 | 影响 | 建议 |
|---|---|---|---|
| **G1** | `--mcp-only` 未接线 | 独占模式只有 `NewRemoteOnlyManager` 和 `remotetargets.ParseSpec`，CLI 与 chat 装配没接 | 复用 `ApplyMCPSelection` + `NewRemoteOnlyManager`，约 120 行 |
| **G2** | `--remote <spec>` 未接线 | `ParseSpec` 已实现且测试通过，但没有 CLI 入口 | 与 G1 一起做，它是 G1 的语法糖 |
| **G3** | 独占模式仍注册 goal functions | `registerGoalFunctions` 不经过 `tools.Manager`，`get_goal`/`update_goal` 仍可见 | 独占时跳过该调用 |
| **G4** | 独占模式仍注入 broker | `job_start` 等本地编排工具会与远端 `job_*` 混淆 | 独占时按"是否触碰本地进程"筛掉 |

**更新（2026-10-21 晚）**：方向已调整 —— 不做 `--mcp-only` 独占，改为 `--remote` 直连。

- **G2 已完成**：`aicli chat --remote <spec>` 可用，无需本地 mcp.yaml，且与已有 MCP 共存。
- **G1 取消**：独占会隔离其它 MCP 工具，不是用户要的。
- **G3 / G4 随之取消**：既然不独占，goal/broker 的本地工具保留是合理的。

详见 `aicli-mcp-only-exclusive-mode-analysis.md` §12。

### 3.2 中优先级：可用性

| # | 缺口 | 影响 | 建议 |
|---|---|---|---|
| **G5** | profile 的 denylist 是黑名单 | 新增本地工具不会自动被排除 | 已写入 README 与模板注释；根治靠 G1 |
| **G6** | skills 未纳入远程隔离 | 远程 profile 仍加载全部本地 skills（`skill__*` 工具） | profile 里加 `skills.denylist`，或在独占模式跳过 host 扫描 |
| ~~**G7**~~ | ~~`remoteTargets` 与 `mcp.yaml` 双份声明~~ | **已修复（2026-10-22）**：声明现在会自己连接 `Servers`，无需再写 mcp.yaml；同名冲突显式报错 | 见 `aicli-mcp-only-exclusive-mode-analysis.md` §14 |
| **G8** | 无 `aicli remote doctor` 聚合命令 | 排错要在三个命令间切换 | 把 `permission_layers` + `profile show` + 目标侧 `doctor` 合成一个 |

### 3.3 低优先级：完整性

| # | 缺口 | 说明 |
|---|---|---|
| G9 | Windows 目标端未实测 | 代码支持（`platform.go`），只在 POSIX 侧跑过 |
| G10 | artifact 72h 过期后无提示补全 | 目前只返回 `NOT_FOUND` + "re-run the command" |
| G11 | 多目标并发上限 | `maxParallelCalls` 单目标有效，跨目标无全局闸 |
| G12 | 远程 job 的 stdin | `job_input` 本地有、远端没有 |

### 3.4 非本特性、但阻塞验证

`cmd/aicli/commands` 当前**编译不过**：

```
chat_interaction.go:7939: assignment mismatch: 2 variables but maybeRefreshStreamGeometryLocked returns 1 value
chat_geometry_watcher_test.go: coord.geometryWatchProbe undefined
```

这是工作区里另一条未完成的重构（`M` 状态的 `chat_interaction.go` + 一批
`chat_geometry_*_test.go`），与本特性无关。我用 `git stash` 临时隔离验证过
本特性可以编译，随后已恢复原状。**上层包的任何端到端验证都要等这批 WIP 完成。**

---

## 4. 建议的推进顺序

```
第 1 步（半天）：G1 + G2 —— 把独占总线接通
    复用已有组件：
      ApplyMCPSelection / LoadMCPConfigSnapshot   （MCP 收窄）
      NewRemoteOnlyManager                        （本地工具归零）
      ParseSpec                                   （紧凑语法）
    产出：aicli chat --mcp-only <server> 可用

第 2 步（2 小时）：G3 + G4 —— 堵住独占的两个旁路
    跳过 registerGoalFunctions、按类别筛 broker 工具

第 3 步（2 小时）：G6 —— skills 纳入隔离

第 4 步（可选）：G8 —— 一个 aicli remote doctor 聚合诊断
```

前三步做完，"本地与远程完全不混合"才算真正成立。

---

## 5. 一句话总结

**部署、执行、权限、审计、双传输、真实模型端到端都已验证可用；
`remote` profile 模板已内置。剩下的是独占模式（`--mcp-only`）从"零件齐备"
走到"接线完成"，以及 skills 一层的隔离。**
