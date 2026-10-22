# 远程工具权限模型

目标：一个人问「为什么这个调用被拒了」时，能立刻说出是哪一层拦的、怎么改。

---

## 一句话

**三层决定"能不能用"，一层决定"合不合法"，每层都 fail-closed，且内层不能让外层放宽。**

```
L1 端点鉴权      谁能连上这个服务        →  HTTP 401
L2 能力面        这个 target 暴露哪些工具  →  工具根本不在 tools/list
L3 会话权限      这次会话允不允许做       →  mode 判定：allow / ask / deny（本地，请求不出网）
L4 目标策略      路径/命令合不合法        →  PATH_DENIED / COMMAND_DENIED（服务端拒绝）
```

L1、L2 在**服务端**执行；L3 在**本地 agent** 执行（请求根本不发出去）；L4 回到**服务端**执行。
这个顺序是有意的：本地先拒能省一次网络往返，也保证"本地没授权的东西，远端都不知道被请求过"。

> **L3 的形态在 2026-10-22 变过，读旧文档时注意**：它原来是一张 per-target 授权表
> （`remote-targets.yaml` 里的 `policy.allowRead/allowWrite/allowShell` +
> `subagentTargets`）。现在 L3 = **会话的 permission mode**（`--permission-mode`）
> 加上**工具面名单**（`--allow-tool` / `--deny-tool`），远程工具与本地工具走**同一条**
> 判定链。删掉那张表不是因为不重要，而是因为它对"这次能不能写"给了**第二个答案**，
> 而且会压制 mode：`accept_edits` 本该放行远程写，却被默认的 `BlockRemoteWrites`
> 提前硬拒——同一件事两处判断，必然出现一处说了不算。
> 现在"能不能写"只由 mode 决定；"要不要给这个目标留写工具"由 L2（目标侧 profile）
> 或 `--deny-tool "remote_prod_*"` 决定。

---

## L1 端点鉴权

由 `aicli-remote-tools` 的 HTTP handler 做，两种方式**任一通过即可**：

| 方式 | 配置 | 说明 |
|---|---|---|
| Bearer token | `http.tokenEnv: AICLI_REMOTE_TOKEN` | 常量时间比较（`subtle.ConstantTimeCompare`），避免时序侧信道 |
| 客户端证书 | `http.clientCAFile` + `requireClientCert` | 由 `crypto/tls` 在 handler 之前完成校验 |

规则：

- 配了 token → 必须带；带错就是 401。
- 配了 mTLS → 证书验过就放行（代理会剥 `Authorization` 的场景用这个）。
- **两者都没配** → 只有在 loopback 上才允许启动；`validateHTTPAuth` 会拒绝非 loopback 的无鉴权监听。

实测：

```
no token      -> HTTP 401
wrong token   -> HTTP 401
correct token -> HTTP 400   （400 是"没带 initialize"，说明已过鉴权）
```

---

## L2 能力面（profile）

目标侧的 `profile` 决定**哪些工具存在**。不存在的工具，模型连看都看不到，更不可能调用。

| profile | 暴露的工具 |
|---|---|
| `readonly`（默认） | `view` `ls` `glob` `grep` |
| `standard` | 上面 + `write` `edit` `append_write` `apply_patch` |
| `ops` | 上面 + `shell` |
| `custom` | 就是 `tools:` 里列的那些 |

两个额外的收窄开关：

- `tools: [...]` —— 在 profile 之上再做一层过滤（任何 profile 都生效）。
- `readOnly: true` —— **硬覆盖**：即使 profile 写错成 `ops`，也只暴露读工具。

还有两个**能力型**工具组，不是 profile 档位，而是"有没有后端存储"决定：

- `job_*` —— 只在 `jobs.root` 配了、且 profile 是 `ops`/`custom` 时出现。
- `artifact_read` —— 只在 `artifacts.root` 配了时出现。

实测（同一个 `remotetools` 实现，只有 profile 不同）：

```
profile: ops         → 14 tools（含 job_* / artifact_read）
profile: readonly    →  4 tools
无 jobs.root / artifacts.root → 14 → 9（job_* 与 artifact_read 全部消失）
```

---

## L3 会话权限（mode 就是这一层，不再另有一套）

这一层在**本地 agent 的 policy**里，但它**不区分远程与本地工具**：

1. 工具先按**自己的属性**归类成 capability（`write_fs` / `exec_shell` /
   `background_task` / `read_only`…）——真源是 `internal/toolregistry` 的登记事实，
   未登记的名字退回子串启发式；
2. 再由**会话的 permission mode** 给出唯一结论：`allow` / `ask` / `deny`。

判定表在 `internal/policy/modes.go` 的 `modeDecision`（对外导出为 `ModeDecision`）。

实测（`go run ./scripts/gatecheck.go <目录>`，取 `remote-demo__*` 与本地同名逐一比对）：

```
tool          default   accept_edits   plan    bypass_permissions   dont_ask
ls/view/glob  allow     allow          allow   allow                allow
write/edit    ask       allow          deny    allow                deny
shell         ask       ask            deny    allow                deny
job_start     ask       ask            deny    allow                deny
```

**远程与本地逐格相同**——这条不变式由 `gatecheck.go` 与
`chat_remote_targets_test.go:TestRemoteToolsAreGovernedLikeLocalOnes` 双向钉住。
出现分歧就说明有人又给远程单独加了一层控制，而那正是这次要消除的东西。

### 想按目标收窄，用工具面名单，不要再加权限层

`--deny-tool` / `--allow-tool` 走 `toolregistry.MatchPattern`，支持前缀通配，
而远程工具名天生带目标前缀，所以"这个目标别想写"是一句名单：

```bash
aicli chat --remote 'ssh://ops@prod#ops' --deny-tool "remote_prod__shell" --deny-tool "remote_prod__job_*"
aicli chat --remote 'http://10.0.0.9:18765' --allow-tool "remote_srv__view" --allow-tool "remote_srv__grep"
```

### 子代理：继承父会话的判定，写/执行仍会被剥

子代理跑在**本地**，用的是本地 LLM loop 与工具面——不存在"远程子代理"。
它能不能用某个远程工具，取决于父会话的工具面与 mode，
`DeriveChildForTask` 会把子策略收成只读（委派不得提权）。

原先的 `policy.subagentTargets`（按角色细分远程权限）已移除：
它是 mode 之外的第四个答案，且角色名单无法覆盖调用方传来的任意 role 拼写。
真要"这个目标谁都不能委派着用"，用 `--deny-tool "remote_prod_*"` 在父会话收窄即可。

---

## L4 目标侧路径与命令策略

请求通过前三层后，服务端还要校验**具体做什么**。

### 4a 路径 jail

每次路径参数都走三步：`clean → 拼到 workspace → 确认在 allowedRoots 内`，
然后**再做一次 symlink 解析后的复核**。

实测（目标上预先造好各种逃逸）：

```
../../etc/passwd            → PATH_DENIED  path "/tmp/etc/passwd" is outside the allowed roots
/etc/passwd                 → PATH_DENIED
/dev/zero                   → PATH_DENIED
leak.txt -> /tmp/secret.txt → PATH_DENIED  path "/tmp/aicli-perm/secret.txt" is outside ...
```

最后一条最关键：**软链接是在解析后才被拦的**，不是只做了字符串前缀判断。

另外还会拒绝 FIFO / socket / 设备节点（读 FIFO 会永久阻塞）。

### 4b 命令策略

按顺序判，前面命中就直接拒：

1. **deny 列表**（永远优先，包括通过 wrapper 到达的）
2. **allow 列表为空 → 全拒**（fail closed，注意本地沙箱是"空=全放"，远端刻意反过来）
3. **不在 allow 列表** → 拒
4. **元字符**（`;` `&&` `||` `|` `` ` `` `$(` `>` `<` 换行）→ 拒。
   引号内的不算，所以 `python3 -c "...;..."` 是合法的
5. **破坏性命令分类**（共享 `shellrisk`）→ 拒

允许列表按 **argv 前缀**匹配，所以 `echo` 能匹配 `echo hi`，也能写 `echo hi` 只允许这一种。

实测：

```
whoami            → COMMAND_DENIED  not in the allowlist
rm /tmp/x         → COMMAND_DENIED  denied by policy
echo hi; whoami   → COMMAND_DENIED  contains a command separator (;)
echo hi | cat     → COMMAND_DENIED  contains a pipe (|)
echo hi           → 成功，输出 "hi\n"
```

---

## MCP Roots：客户端只能收窄

如果客户端在 handshake 里声明 roots，服务端会做**交集**：

- 声明的 root 不在 allowedRoots 内 → 记为 violation 并忽略，**不会扩权**。
- 声明为空 → 不改变（用完整 workspace）。
- 效果只在**这次会话**内，通过 `workspaceForCall(ctx, fallback)` 生效，不重建工具注册表。

一个已知坑（已修）：stateless HTTP 每个请求的 session ID 都是空串，
早期按 `""` 缓存会把一个客户端的收窄结果泄漏给另一个客户端。现在只在 session ID 非空时缓存。

---

## 四层怎么对应到"我要做什么"

| 你想做的事 | 改哪一层 |
|---|---|
| 让某台机器能连上 | L1：配 token 或 mTLS |
| 让目标能执行 shell | L2：`profile: ops`；L4：`shell.allowedCommands` 列具体命令 |
| 让目标能写文件 | L2：至少 `profile: standard`；L3：用 `accept_edits` 或 `default`（写会 ask） |
| 只给某个会话读权限 | L3：`--permission-mode plan`（与本地写同样处理） |
| 只让某个工具可见 | L2：`tools: [view, grep]` 或 `readOnly: true`；或 L3 名单 `--deny-tool "remote_x_*"` |
| 让子代理能用某个 target | 不单独配置：子代理继承父会话的工具面与 mode，写/执行由 `DeriveChildForTask` 收成只读 |
| 限制能访问的目录 | L4：`workspace` + `allowedRoots` |
| 紧急全禁 | L2：`readOnly: true`（比改 profile 更难写错） |

---

## 排错顺序

遇到"被拒绝"，按这个顺序查，不要猜：

```
1. HTTP 401?                 → L1，检查 token / 证书
2. 工具不在 tools/list?      → L2，检查 profile / tools / readOnly
3. 被 mode 拒/要审批?         → L3，检查 --permission-mode 与 --deny-tool
                                （deny 的报错点名 "denied by policy"；mode 的拒绝记 decision=deny）
4. PATH_DENIED?              → L4a，检查 workspace / allowedRoots / 软链接
5. COMMAND_DENIED?           → L4b，检查 allowedCommands / deniedCommands / 元字符
```

第 3 层的错误信息是**唯一在本地产生的**，所以它不会出现在目标机审计日志里 ——
如果你在审计里找不到某次调用，大概率它根本没出去。

---

## 一个快速自检脚本

```bash
go run ./scripts/permission_layers.go <含 .aicli/remote-targets.yaml 的目录>
```

它会读真实 YAML、展开，按真实 capability 解析与真实 mode 表逐工具打印
allow/ask/deny，并**逐格比对远程与本地同名工具**（不一致即报错）。
用来回答"这套配置到底允许了什么"，比读文档快。

---

## 10. 控制远程工具权限：三个入口（`--remote` 场景）

### 10.1 `#depth` = 向服务端请求 profile，不是本地授权

```bash
aicli chat --remote 'ssh://ops@10.0.0.9#readonly'   # 服务端只起 readonly profile
aicli chat --remote 'ssh://ops@10.0.0.9#standard'   # 服务端起 standard
aicli chat --remote 'ssh://ops@10.0.0.9#ops'        # 服务端起 ops
```

| depth | 谁执行 | 效果 |
|---|---|---|
| `readonly`（默认） | 目标侧 | `tools/list` 只返回读工具——工具**不存在**，本地无从调用 |
| `standard` | 目标侧 | 额外暴露文件写工具 |
| `ops` | 目标侧 | 再额外暴露 `shell` / `job_*` |

**这不再是本地的一票否决。** 它通过 `--config-b64` 传给 ssh-stdio 服务端，服务端
按 profile 注册工具；本地随后只面对"实际存在的工具"，再交给 mode 决定放行/询问/拒绝。

> `http://` 目标的服务已经在跑，本地改不了它的 profile。此时会明确提示
> `profile "standard" is not applied to an http target`，而不是假装限制生效。
> 想收窄就靠下面两个入口。

### 10.2 收窄工具面：`--allow-tool` / `--deny-tool`

这是**唯一**按目标收窄的本地手段，走的是通用工具名单（支持前缀通配），没有远程专用分支：

```bash
# 只允许读工具
aicli chat --remote '...' --allow-tool "remote_wsl__view" --allow-tool "remote_wsl__grep"

# 排除某个目标的执行类工具
aicli chat --remote '...' --deny-tool "remote_wsl__shell" --deny-tool "remote_wsl__job_*"
```

**关键安全性质**：名单只在"工具面"这一维生效，**不能突破 capability 与 mode**。
即使 `--allow-tool 'remote_wsl__*'` 把写工具放进允许名单，`plan` mode 仍会 deny、
`default` mode 仍会 ask——因为模式判定发生在名单之后，且看的是工具自己的能力分类。

实测（`go run ./scripts/gatecheck.go`）：

```
write   default=ask   plan=deny   accept_edits=allow   dont_ask=deny   bypass=allow
shell   default=ask   plan=deny   accept_edits=ask     dont_ask=deny   bypass=allow
```

这两行对 `remote_wsl__write` 与本地 `write` 完全一致；一旦不一致，gatecheck 会直接失败。

### 10.3 会话模式：`--permission-mode`

```bash
aicli chat --remote '...' --permission-mode plan          # 只读：写/执行一律 deny
aicli chat --remote '...' --permission-mode default       # 写/执行 ask（有审批）
aicli chat --remote '...' --permission-mode accept_edits  # 写 allow，执行 ask
aicli chat --remote '...' --permission-mode dont_ask      # 写/执行一律 deny（无人工可问）
aicli chat --remote '...' --permission-mode bypass_permissions  # 全放（慎用）
```

远程与本地共用这张表，没有"远程更严格/更宽松"的隐藏差异。

### 10.4 服务端：目标自己的命令策略

即使本地 mode 是 `bypass_permissions`，**服务端的 allowlist 仍然是第二道闸门**（L4）：

```
$ probe remote_wsl__shell '{"command":"whoami"}'
COMMAND_DENIED: command "whoami" is not in the allowlist
→ audit: remote_wsl__shell deny
```

目标侧配置（`remote.yaml`）：

```yaml
profile: ops
shell:
  allowedCommands: [echo, ls]     # 空 = 全拒（fail-closed）
  deniedCommands:  [rm]           # 永远优先
  denyShellMetachars: true        # 拒 ; && || | ` $( > <
  envAllowlist: [HOME, PATH]      # 空 = 不继承任何环境变量
```

### 10.5 三层叠加的实际效果

| 层 | 在哪 | 控制什么 | 谁能改 |
|---|---|---|---|
| L1 端点鉴权 | 服务端 | 谁能连 | 目标侧 `http.tokenEnv` / mTLS |
| L2 能力面 | 服务端 | 暴露哪些工具 | 目标侧 `profile` / `tools` / `readOnly` |
| **L3 会话权限** | **客户端** | **允许 / 询问 / 拒绝** | **`--permission-mode`（判定）+ `--allow-tool`/`--deny-tool`（工具面）** |
| L4 目标策略 | 服务端 | 具体路径与命令 | 目标侧 `workspace` / `allowedCommands` |

判断"哪一层拦的"最快的方法：

| 现象 | 层 |
|---|---|
| 工具不在 `tools/list` / 模型看不到 | L2（目标侧 profile/tools） |
| 报错点名 `denied by policy`，且目标审计无记录 | L3（本地名单） |
| 返回 `ask` 并等审批；或 plan/dont_ask 下拒绝 | L3（mode，本地，请求不出网） |
| `PATH_DENIED` / `COMMAND_DENIED`，目标审计有 `decision=deny` | L4 |

### 10.6 常见组合

```bash
# 生产只读排查（本地一票不让写；与目标 profile 无关）
aicli chat --remote 'https://prod:18765' --permission-mode plan

# 开发：允许写文件，但仍要审批命令
aicli chat --remote 'http://dev:18765' --permission-mode accept_edits

# 只给读 + 单个写工具（其余写工具从工具面移除）
aicli chat --remote '...' \
  --allow-tool "remote_wsl__view" --allow-tool "remote_wsl__grep" \
  --allow-tool "remote_wsl__write"

# 目标暴露了 shell，但这个会话排除它
aicli chat --remote '...' --deny-tool "remote_wsl__shell" --deny-tool "remote_wsl__job_*"

# 用 profile 固化（团队共享）
aicli chat --remote '...' --profile remote
```

> `http://` 目标上的 `#standard` / `#ops` 不再影响本地，也不会改变服务端
> （它已经在跑）。这两个后缀只在 `ssh://` 下有意义。

### 10.7 权限不能放宽的地方

以下都无法通过命令行放宽，必须改**目标侧**配置：

| 想做的 | 必须改哪 |
|---|---|
| 让 `ssh://...#ops` 能跑某个新命令 | 目标侧 `shell.allowedCommands` |
| 让写操作能到 jail 外的目录 | 目标侧 `workspace` / `allowedRoots` |
| 增加 shell 工具本身 | 目标侧 `profile: ops` |
| 让目标接受新 token | 目标侧重启并换 `http.tokenEnv` |

这是刻意的：**客户端不能单方面扩大服务端授予的能力**。

### 10.8 测试固化

| 用例 | 断言 |
|---|---|
| `policy.TestRemoteToolsClassifyByDeclaredName` | 远程工具按服务端声明的裸名分类（`job_start` → `background_task`） |
| `policy.TestRemoteAndLocalClassifyIdentically` | 远程与本地同名工具逐项 capability 相同 |
| `policy.TestRemoteBackgroundTaskIsNotPlanAllowed` | plan mode 不再放行远程 `job_start` |
| `commands.TestRemoteToolsAreGovernedLikeLocalOnes` | 远程/本地在策略层的判定一致 |
| `commands.TestDenyToolShutDownsOneRemoteTarget` | `--deny-tool "remote_prod_*"` 按目标收窄 |
| `remotetargets.TestPolicyBlockStillLoadsButIsReported` | 旧 `policy:` 块仍可加载，并明确告警"不再生效" |

**最重要的性质**：远程工具没有独立的权限判定。任何"远程比本地多一层"的实现
都会让 `TestRemoteAndLocalClassifyIdentically` / `TestRemoteToolsAreGovernedLikeLocalOnes`
失败——这两条就是防止控制层重新长回来的钉子。
