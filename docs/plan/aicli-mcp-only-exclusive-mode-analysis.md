# 独占模式可行性分析：`--mcp-only <server>`

结论：**可行，而且比 `--remote <url>` 更通用**。已在真实 WSL 环境跑通端到端链路，并
发现一个必须修的独占漏洞（已修）。

---

## 1. 两种抽象的差别

| | `--remote <url\|ssh://...>` | `--mcp-only <server-name>` |
|---|---|---|
| 抽象层次 | 绑定「远程工具服务」这一具体形态 | 只绑定「一个 MCP server」 |
| 依赖 | 必须是 `aicli-remote-tools` | 任意 MCP server（官方 filesystem / github / 自家服务） |
| 凭证 | 固定 `AICLI_REMOTE_TOKEN` | 沿用该 server 自己的 headers / env |
| 配置 | 需要解析 URL/SSH 语法 | 直接复用已有 `mcp.yaml` |
| 适用 | 只想一条命令指向一台机器 | 已有 MCP 配置，想"只用其中那一个" |

**关键洞察**：`aicli-remote-tools` 只是一个 MCP server。只要独占的是
"某个 MCP server 的工具面"，远程工具场景自动被覆盖 —— 抽象的层级更高，覆盖面反而更大。

`--remote` 因此应实现为 `--mcp-only` 之上的**语法糖**：把 spec 展开成一个临时
server 条目，再走同一套独占逻辑。两条路径只有入口不同，出口完全一致。

---

## 2. 为什么独占比 allowlist 收窄更安全

当前 `tools.Manager.ListTools` 的装配顺序是 **MCP 先、本地后**，只有
`ls/glob/grep/view` 这四个读取类工具受 `shouldPreferLocalToolkit` 保护。

由此产生一个静默故障模式：

```
远程 server 的 toolPrefix 忘记配 → 它暴露 write
tools.Manager 先遍历 MCP → write 被远程占用
本地 write 因 seen[] 已被占用 → 被丢弃
模型调用 write 以为在改本地 → 实际改了远程机器
全程无报错
```

已用测试钉死这个行为（`internal/tools/remote_tool_name_collision_test.go`）：

```
TestLocalPriorityToolsCoversOnlyReadFamily        保护集只有 4 个读工具
TestUnprefixedRemoteWriteClaimsTheName            未加前缀的远程 write 抢占成功
TestPrefixedRemoteWriteCoexists                   加前缀后两者共存
TestUnprefixedRemoteReadIsInvisible               未加前缀的远程 view 直接消失
```

**allowlist 收窄救不了这个场景**：allowlist 只决定"能不能调用"，不改变"这个名字指向谁"。
只有**结构上不存在本地工具**，这个故障模式才彻底消失。

---

## 3. 实现路径（已验证）

独占模式 = 三步，全部复用现有组件：

```
① 加载 mcp.yaml（复用 LoadEffectiveFrom）
        ↓
② 过滤到单个 server，得到内存快照
        ↓
③ 用快照启动 manager + tools.NewRemoteOnlyManager(mgr)
   （toolkit == nil ⇒ 本地工具在结构上不存在）
```

### 3.1 关键组件

| 组件 | 作用 | 状态 |
|---|---|---|
| `mcpconfig.LoadEffectiveFrom` | 加载含分层合并的配置 | 已有 |
| `manager.ScopedManager.LoadConfigFromConfig` | 用内存快照替换待加载配置，不写临时文件 | 已有 |
| `tools.NewRemoteOnlyManager` | `toolkit == nil` 的工具管理器 | **本次新增** |

`tools.Manager` 所有 `m.toolkit` 使用点本来就有 nil 保护
（`ListTools` / `executeWithMeta` / `resolveToolSource` / `SetBackgroundSubmitter`），
所以 `toolkit == nil` 不需要改动分发逻辑。

### 3.2 实测输出

```
PASS config loaded, server "wsl" present (type=streamable)
PASS filtered to 1 server(s)
PASS in-memory snapshot accepted
PASS 9 tool(s) connected
PASS remote-only tools.Manager lists 9 tool(s):
   remote_wsl__append_write         mcp="wsl"
   remote_wsl__apply_patch          mcp="wsl"
   ...
   remote_wsl__write                mcp="wsl"
PASS no local tools in the exclusive surface
FEASIBLE
```

注意这里刻意让 `mcp.yaml` 同时配置了**两个** server（`wsl` 指向真实服务、`other`
指向不存在的端口）。过滤后只有 `wsl` 的连接发生，`other` 完全没有被拨号 ——
说明"只连接指定的那一个"，不只是"只显示指定那一个"。

---

## 4. 验证中发现并修复的独占漏洞

**首次跑就失败了**：

```
PASS remote-only tools.Manager lists 10 tool(s):
   list_mcp_resources               mcp=""          ← 不是远程工具
   remote_wsl__append_write         mcp="wsl"
   ...
LOCAL LEAK: list_mcp_resources
FAIL 1 non-MCP tool(s) leaked into the exclusive surface
```

`list_mcp_resources` 是运行时自带的元工具，由 `metaToolDescriptor()` 无条件追加。
它的语义是"列出**所有**已连接 MCP server 的资源" —— 正是独占模式要关掉的那扇门。

**修复**（`internal/tools/manager.go`）：

```go
func (m *Manager) metaToolDescriptor() *ToolDescriptor {
    if m != nil && m.toolkit == nil {
        return nil   // 独占模式：工具面必须只有目标的工具
    }
    ...
}
```

修复后 9 个工具，全部来自 `wsl`，`PASS no local tools in the exclusive surface`。

> 这类"元工具"是最容易漏的一类：它不属于任何 server，也不会出现在 `mcp.yaml` 里，
> 靠读配置永远发现不了。只有真正枚举一次工具面才会暴露。

---

## 5. 建议的 CLI 形态

```bash
# 通用形式：独占某个已配置的 MCP server
aicli chat --mcp-only github

# 便捷形式：直接给地址（内部转成临时 server 条目）
aicli chat --remote http://10.0.0.9:18765#ops
aicli chat --remote ssh://ops@10.0.0.9/srv/app#standard

# 两者互斥；同时给出应报错而不是猜测
```

语义约定：

| 项 | 行为 |
|---|---|
| 工具面 | 只有该 server 的工具；本地工具、`list_mcp_resources`、其他 server 全部不可见 |
| 文件路径 | 全部相对于远端 workspace；本地路径不可达 |
| 策略 | 该 server 仍需通过 L3 per-target 授权（`--mcp-only` 不绕过授权） |
| 缺失 server 名 | 报错并列出可用的 server 名，不做模糊匹配 |
| 与 `--allow-tool` 关系 | 可以叠加，用于进一步收窄；不能用于放宽 |

---

## 6. 待实现清单

已验证可行，剩下的都是接线工作：

1. **CLI flag**：`--mcp-only <name>`、`--remote <spec>`（互斥校验）。
2. **配置过滤**：在 `discoverChatCapabilities` 里，`mcpForTools` 之后、构造
   `runtimetools.Manager` 之前插入过滤逻辑。
3. **构造分叉**：独占模式下改调 `NewRemoteOnlyManager`，并跳过
   `initializeLocalChatRuntimeHost` 的本地能力面装配。
4. **策略注入**：把该 server 的 `RemoteTargetPolicy` 写进 `ToolExecutionPolicy`，
   复用已有的 `applyRemoteTargetsToPolicy`。
5. **启动自检**：独占模式下 `aicli mcp doctor` 类输出应显示
   "exclusive: <server>，本地工具已禁用"。

### 复用度评估

| 步骤 | 需要新代码 | 说明 |
|---|---|---|
| 加载 + 过滤 | ~30 行 | 复用 `LoadEffectiveFrom` |
| 启动 manager | ~20 行 | 复用 `LoadConfigFromConfig` |
| 独占工具面 | ~15 行 | 已实现 `NewRemoteOnlyManager` |
| spec 解析 | ~200 行 | 已实现 `remotetargets.ParseSpec` |
| 策略接线 | ~10 行 | 复用 `applyRemoteTargetsToPolicy` |
| CLI + 校验 | ~40 行 | flag 与互斥检查 |

**没有一处需要改动传输层、工具实现或权限引擎** —— 这是这个抽象值得做的直接证据。

---

## 7. 风险与边界

| 风险 | 说明 | 处理 |
|---|---|---|
| 用户以为本地工具还在 | 模型说"我写好了文件"，实际写在远端 | 独占模式必须改名/改描述；建议会话标题或状态栏显式标注 `exclusive: wsl` |
| 远端不可达时工具面为空 | 与"工具加载失败"表现相同 | 启动自检要区分"独占且连接失败"与"没有工具" |
| skills 仍会扫描本地目录 | `initializeLocalChatRuntimeHost` 会扫技能目录 | 独占模式应跳过，否则仍然是"混合" |
| `--mcp-only` 指向非远程 server | 如本地 filesystem MCP | 合法且有用，但 L3 授权语义不同，需按 `ExecutionScope` 分流 |

最后一条尤其重要：`--mcp-only` 不该假设目标是远程的。它只负责"独占一个 server"，
远程/本地的差异继续由 `executionScope` 表达 —— 这正是它比 `--remote` 更通用的原因。

---

## 8. 本地豁免工具审计（独占模式的真正难点）

`toolkit == nil` 只关掉了**一个**来源。实际枚举后发现有四个独立来源会进入模型工具列表，
其中三个不受 `tools.Manager` 管辖。

审计脚本：`scripts/local_tool_exemption_audit.go`（可重复运行）。

### 8.1 四个来源的实测清单

| 来源 | 数量 | 受 `tools.Manager` 管辖 | 独占模式是否自动消失 |
|---|---|---|---|
| ① toolkit 本地内置 | 22 | ✅ 是 | ✅ 是（`toolkit == nil`） |
| ② MCP server 工具 | N | ✅ 是 | 保留（这就是独占的目标） |
| ③ goal functions | 2 | ❌ **否** | ❌ **不会** |
| ④ broker 编排面 | 9+ | ❌ **否** | ❌ **不会**（取决于是否注入 Broker） |

### 8.2 ① toolkit：22 个本地内置

```
aicli_exec  append_write  apply_patch  artifact_read  bash  download
edit  execute_shell_command  fetch  glob  grep  list_mcp_resources
ls  multiedit  read  rg  shell  sourcegraph  todos  view  web_search  write
```

注意这里同时出现了**规范名与别名**（`read`/`view`、`rg`/`grep`、`bash`/`shell`/`execute_shell_command`），
所以 22 个条目对应的实际能力少于 22 个。

`toolkit == nil` 时实测：`tools.Manager(toolkit=nil, mcp=nil) => 0 tools`。

### 8.3 ③ goal functions：唯一真正的"旁路"

`registerGoalFunctions(session)` 在 `attachChatCapabilities` 里被**无条件调用**
（只要 `!session.DisableTools`），它直接把能力注册进 `FunctionCatalog`：

```go
// chat_setup.go:524
if !session.DisableTools {
    registerGoalFunctions(session)      // ← 不走 tools.Manager
}

// goal_functions.go:31
catalog.RegisterFunction(aiclitools.FunctionFromCapability(cap, ...))
```

实测注册的是 `get_goal` / `update_goal` 两个。

**影响**：独占模式下模型仍能调用 `get_goal`/`update_goal`。这两个操作的是**本地会话状态**，
不碰远端文件系统，危害有限；但严格来说违反了"工具面只有目标工具"的承诺。

**结论**：`--mcp-only` 必须在 catalog 层显式跳过 `registerGoalFunctions`，
或者在注册时按"独占模式"过滤。这是唯一一个无法靠 `tools.Manager` 解决的豁免。

### 8.4 ④ broker：9 个编排工具

零值 Broker 的定义集：

```
ask_user_question  job_cancel  job_control  job_input  job_list
job_monitor  job_output  job_search  job_start
```

这些由 `agent` 的 tool broker 暴露，**不经过 `tools.Manager`**。是否出现取决于 host 是否注入了
`Broker`（`chat_actor_host.go` 在 `host.ActorRegistry` / `host.Background` / `host.Schedule`
存在时构建）。

**影响需要分开看**：

- `job_start` / `job_output` 等是**本地**后台作业 —— 与远端 `remote_x__job_start` 语义完全不同。
  独占模式下如果两者同时存在，模型极可能混淆"在远端跑"还是"在本地跑"。
- `ask_user_question` 是交互能力，与文件系统无关，保留是合理的。
- `subagent_*` / `team_*` 属于编排面，同样不碰远端文件。

**结论**：需要按"是否触碰本地文件系统/进程"分类，而不是一刀切：

| 类别 | 例子 | 独占模式建议 |
|---|---|---|
| 触碰本地文件/进程 | `job_start`、`job_output`、`job_cancel`、`todos` | **关闭**（易与远端同名工具混淆） |
| 纯编排/交互 | `ask_user_question`、`subagent_*`、`team_*`、`plan_*` | 保留（或按 flag 决定） |
| 会话状态 | `get_goal`、`update_goal` | 保留（无害）或关闭（严格独占） |

### 8.5 ② 中的注册表 Essential 标记已不是豁免

`toolregistry.Entries()` 里有 41 个 `Essential: true`。历史上这个标记意味着
"allowlist 豁免，一定进入工具列表"；自 2026-10-16 起**已不再是豁免**
（`Essential` 只影响观测面与目录 API，不再影响可用性）。

所以"哪些本地工具一定进入 LLM 列表"的答案不是这 41 个，而是：

- **一定进入**：`toolkit` 注册的 22 个（在 `toolkit != nil` 且未被 allowlist 裁剪时）
- **一定进入**：`get_goal` / `update_goal`（只要 `!DisableTools`）
- **条件进入**：broker 的 9 个（取决于 host 是否注入 Broker）
- **不再特殊**：`Essential: true` 的 41 个，与普通工具同样受 allowlist 约束

### 8.6 对实现的直接要求

`--mcp-only` 不能只做"`toolkit = nil`"。完整清单：

1. `tools.NewRemoteOnlyManager(mgr)` —— 关掉 ① 与 `list_mcp_resources` ✅ 已实现
2. **跳过 `registerGoalFunctions`** —— 关掉 ③ ⬜ 待实现
3. **不注入本地 Broker**（或只保留 `ask_user_question`）—— 关掉 ④ ⬜ 待实现
4. **跳过 `initializeLocalChatRuntimeHost` 的技能目录扫描** —— 否则 skills 仍是混合面

第 4 条容易被忽略：`initializeLocalChatRuntimeHost` 会扫描本地 `.agents/skills` 并注册
skill 函数（命名形如 `skill__<name>`），那也是本地工具面的组成部分。

### 8.7 验收断言建议

独占模式的集成测试不该只检查"本地工具不在列表里"，而应断言**工具总数等于远端上报数**：

```
len(最终 catalog 工具) == len(远端 tools/list)
```

任何一个额外工具都会让等式失败，从而同时覆盖 ①②③④ 四个来源。

---

## 9. 方案对比：profile 裁剪 vs `--mcp-only`

有一个不新增任何运行时机制的方案：**用 profile 同时做两件事** ——
`mcp.use_servers` 把 MCP 收窄到单个 server，`tools.denylist` 把冲突的本地工具裁掉。

实测下来这个方案**基本可行，但有两处必须知道的天花板**。

### 9.1 实测：profile 做到了什么

真实的 `profile.yaml`：

```yaml
mcp:
  use_servers: [wsl]

tools:
  denylist:
    - view, ls, glob, grep, read, rg            # 本地读
    - write, edit, apply_patch, append_write, multiedit   # 本地写（危险）
    - shell, bash, execute_shell_command        # 本地执行（危险）
    - job_start, job_output, job_cancel, job_list, job_search,
      job_monitor, job_control, job_input       # 本地进程（危险）
    - download, aicli_exec, todos
```

`aicli profile show` 的真实输出：

```
[tools]
  denylist：view, ls, glob, grep, read, rg, write, edit, apply_patch, ...
[mcp]
  声明：use=[wsl] exclude=[]
  servers：other(excluded), wsl(enabled)      ← 只剩一个
```

`aicli profile validate` → **错误 0 / 警告 0，校验通过**。

审计脚本（`scripts/profile_surface_audit.go`）的四项结论：

| 验证项 | 结果 |
|---|---|
| 本地 toolkit 22 个工具是否全部被 deny | **22/22 全部拦住** |
| 带前缀的远程工具是否被误伤 | **7/7 全部 ALLOW**（`remote_wsl__write` 等） |
| `mcp.use_servers` 是否裁到单 server | **kept=[wsl] dropped=[other]** |
| `ApplyMCPSelection` 是否污染输入配置 | 否（返回 clone，原 cfg 仍是 2 个） |
| broker 的 `job_*` 是否被拦住 | **8/9 拦住**（仅 `ask_user_question` 保留） |

**关键点**：deny 是**别名感知的精确匹配**（`deniedToolsReject`），
所以 `deny: write` 不会连带 `remote_wsl__write` —— 这正是这个方案能成立的技术前提。

### 9.2 天花板一：denylist 是"黑名单"，不做保证

审计第一轮就漏了 5 个：`artifact_read`、`fetch`、`web_search`、`sourcegraph`、
`list_mcp_resources`。它们不在我手写的列表里，于是就漏过来了。

这 5 个本身无害（不碰本地文件、不起本地进程），但**说明机制性质**：

| | profile denylist | `toolkit == nil` |
|---|---|---|
| 性质 | 黑名单：不写就不拦 | 白名单：结构上不存在 |
| 新增本地工具 | **自动泄漏** | 自动不可达 |
| 需要维护 | 手动跟进工具清单 | 零维护 |

这个差别在长期维护中会放大：任何新增的本地工具（比如以后加了 `local_git`）
都会默认出现在 remote profile 里，直到有人记得加进 denylist。

**缓解手段**：用通配而非逐个列举。`tools.denylist` 支持 `*` 与前缀通配
（`toolregistry.MatchPattern`），但注意 `*` 会把远程工具也一起禁掉，
所以真正安全的写法只有两种：

```yaml
# ✗ 不行：连 remote_wsl__* 一起禁
tools: {denylist: ["*"]}

# ✓ 可行：显式列出本地名，并靠 toolPrefix 保证远程名不会被命中
```

也就是说 **profile 方案无法用一条通配规则表达"除远程之外的都禁"**。

### 9.3 天花板二：goal / broker 走 catalog，denylist 的效力取决于调用点

第 8 节已经说明 `get_goal` / `update_goal` 由 `registerGoalFunctions` 直接注册进
catalog。审计确认：只要把它们**列进 denylist**，`DeniesTool` 就能命中
（`job_*` 8/9 被拦住就是证据）。但这是**运行时拦截**，工具仍然出现在目录里，
模型会先看到它、调用、然后被拒。

对比 `--mcp-only` 的做法是**根本不注册**，模型看不到。

对模型行为的影响是实质性的：可见但不可用会诱导重试与困惑。

### 9.4 结论：两者不是替代关系

| 维度 | profile 裁剪 | `--mcp-only` |
|---|---|---|
| 需要新代码 | **几乎不需要**（配置即可） | 需接线（~120 行） |
| 工具面 | 黑名单式，可见但不可调 | 白名单式，结构性不可见 |
| 新增本地工具 | 需手动补 denylist | 自动不可达 |
| MCP 收窄 | ✅ `use_servers` 已实现 | ✅ 同一机制 |
| skills | ✅ `skills.denylist` 可裁 | 需跳过 host 装配 |
| 复用现有 profile | ✅ 天然融合 | 需额外定义关系 |

**推荐组合，而不是二选一**：

```
① profile 负责"声明意图"：use_servers + tools.denylist + skills.denylist
   —— 零代码即可上线，解决 90% 的冲突
② --mcp-only 负责"结构性保证"：toolkit=nil + 不注册 goal + 不注入 broker
   —— 需要绝对保证时使用
```

而且 `--mcp-only` 应该**复用 profile 的 MCP 选择机制**（`ApplyMCPSelection` /
`LoadMCPConfigSnapshot` 已经存在），而不是另写一套过滤 —— 这样两条路径的语义
天然一致，不会出现"profile 说用 wsl、--mcp-only 又连了 other"的矛盾。

### 9.5 如果只做 profile 方案，必须做的三件事

1. **denylist 使用精确名 + 注释说明理由**，不要写 `*`（会连远程一起禁）。
2. **加一条 CI 断言**：`profile show` 的生效工具数与预期一致。否则新增本地工具时
   无人察觉。可直接复用 `scripts/profile_surface_audit.go` 的判定。
3. **在 profile 的 title/description 里写明"仅远程"**，让 `/status` 与启动横幅可见，
   避免使用者误以为本地工具还在。

### 9.6 一个容易踩的坑

`tools.denylist` 里写 `job_start` 会同时禁掉**远程**的 `remote_wsl__job_start` 吗？

**不会**。deny 走 `deniedToolsReject` → `listNameMatches` / `MatchPattern`，
都是对**完整工具名**匹配；`job_start` 与 `remote_wsl__job_start` 是两个不同的名字。

实测已验证（第 2 组断言：`remote_wsl__job_start` 仍然 ALLOW）。

反过来说，这也意味着**想让远程工具与本地工具同名是不可能的** ——
`toolPrefix` 必须配，否则远程 `write` 会抢占本地 `write` 的名字（见第 2 节）。

---

## 10. 已落地的防护：同名远程工具在加载期拒绝

第 2 节描述的静默覆盖问题，已经用一条加载期校验堵住。

### 10.1 校验位置与理由

位置：`internal/tools/manager.go` 的 `ListTools`，在 `shouldPreferLocalToolkit`
判断之后、加入工具面之前。

    if err := m.validateRemoteToolNameCollision(info); err != nil {
        m.recordRemoteNameCollision(info, err)
        continue
    }

为什么必须放在这里：判定需要三个同时可得的信息 ——
MCP 工具名（来自 `info.Tool.Name`）、本地 toolkit 是否同名（`m.toolkit.Get`）、
以及该 server 的 `ExecutionScope`（`m.mcp.GetMCPStatus`）。只有 `ListTools`
的循环里三者齐备。

### 10.2 判定规则

| 条件 | 结果 |
|---|---|
| `m.toolkit == nil`（独占模式） | 放行 |
| 工具名为空 | 放行 |
| server 的 scope 不是 `remote` | 放行 |
| 本地 toolkit 没有同名工具 | 放行 |
| 其余 | **拒绝 + 记录** |

四条放行条件都是刻意的：

- **独占模式放行**：`toolkit == nil` 时不存在覆盖问题，此时远程工具就该用自己的名字。
- **本地 MCP 放行**：`filesystem` 这类本地 server 镜像内置名是其正常工作方式，
  拒掉会破坏既有部署。危险只存在于"远程"这一侧。
- **status 读不到时按非远程处理**：一个无关的状态查询失败不应该让原本可用的
  本地环境起不来。宁可漏报，不可误杀。
- **只查本地 toolkit**：`list_mcp_resources` 是 manager 追加的元工具而非 toolkit
  条目，与它同名不构成"遮蔽本地能力"。

### 10.3 错误信息

```
remote MCP server "wsl" publishes tool "write", which collides with the local
builtin of the same name; give the remote tools a prefix (remote.toolPrefix)
so the two surfaces stay distinguishable
```

要点是**给出修复动作**（配 `toolPrefix`），而不是只说"冲突了"。

### 10.4 冲突记录为什么必须留

被拒绝的工具不会出现在工具列表里。如果只 `continue` 不记录，运维看到的是
"工具少了一个"，没有任何线索。

因此拒绝的同时写入 `remoteNameCollisions`，并提供
`func (m *Manager) RemoteNameCollisions() []RemoteNameCollision`，
按 server、tool 排序，供 `mcp list` / `doctor` 等诊断面读取。

### 10.5 测试覆盖

`internal/tools/remote_name_collision_guard_test.go`：

| 用例 | 断言 |
|---|---|
| `TestRemoteToolNameCollisionIsRefused` | 远程 `write` 被拒；列表里只剩本地那条（无 `mcp_name`）；记录含修复提示 |
| `TestLocalMCPMayShadowLocalTool` | `executionScope: local` 的 server 仍可同名 |
| `TestRemoteToolWithPrefixIsAccepted` | 带前缀的远程工具正常出现，本地工具也在 |
| `TestRemoteToolNameWithoutLocalPeerIsAccepted` | 远程独有名字（无本地同名）照常放行 |
| `TestCollisionCoversEveryLocalName` | 遍历本地 toolkit 的每个名字，逐一验证 |
| `TestRemoteOnlyManagerSkipsCollisionGuard` | 独占模式下校验是 no-op |

`TestCollisionCoversEveryLocalName` 特意区分了两种正确结果：

- **读族**（`ls`/`glob`/`grep`/`view` 及别名）在校验之前就被
  `shouldPreferLocalToolkit` 排除了 —— 它们不会记录冲突，因为远程工具
  从未变成"可达但危险"，而是直接不可达。
- **其余工具**必须产生冲突记录。

把这两种情况混为一谈会掩盖真实缺口，所以测试里显式分开断言。

### 10.6 这条校验没有解决的问题

它阻止的是"**新增**的冲突配置"。以下仍然是开放项：

1. **已有的 `executionScope: remote` 且未配前缀的部署**，升级后会直接少一批工具。
   这是预期行为（宁可拒绝也不静默写错机器），但需要在 release note 里写明。
2. **`executeWithMeta` 与 `ListTools` 的判定不一致本身没改**
   （`shouldPreferLocalToolkitTool` 仍把 mcpName 硬编码为 `"toolkit"`）。
   校验让这个不一致无法被触发，但代码层面的陷阱还在。
3. **本地 MCP 与内置同名的分裂行为**在校验豁免的本地 server 上仍然存在。

第 2、3 条建议后续单独修：让两条路径共用同一份"名字归属"判定。

---

## 11. 根治：两条判定路径合并（2026-10-21 完成）

第 10.6 节列出的开放项 2 和 3 已修复。

### 11.1 原来的分裂

`executeWithMeta` 里有一段"本地快速通道"：

```go
if m.toolkit != nil {
    if tool, ok := m.toolkit.Get(lookupName); ok {
        if m.shouldPreferLocalToolkitTool(lookupName) {   // ← mcpName 硬编码 "toolkit"
            return executeLocalToolkitTool(...)
        }
    }
}
```

而 `shouldPreferLocalToolkitTool(name)` 的实现是：

```go
return m.shouldPreferLocalToolkit(toolkitMCPName, name)   // mcpName 恒等于 "toolkit"
```

`shouldPreferLocalToolkit` 里的判断是"本地有同名工具 && (强制优先 || mcpName == 'toolkit')"。
把 mcpName 传成 `"toolkit"`，第二个条件**恒真**，于是：

- `ListTools` 用**真实**的 mcpName 判断 → 非读工具返回 false → MCP 抢占名字
- `executeWithMeta` 用**假的** mcpName 判断 → 恒返回 true → 执行本地

同一个名字，两个结论。

### 11.2 修复方式

快速通道只保留**读族**，其余全部走 MCP 查找：

```go
// 只有读族可以短路，其余交给 MCP 查找，使执行与列表一致
if m.toolkit != nil && m.shouldForceLocalToolkitTool(lookupName) {
    if tool, ok := m.toolkit.Get(lookupName); ok {
        return executeLocalToolkitTool(ctx, tool, lookupName, args)
    }
}
```

`shouldForceLocalToolkitTool` 正是 `shouldPreferLocalToolkit` 内部用来判断
"这个工具是否属于强制本地优先集"的函数，两者现在共用同一个集合
（`localToolkitPriorityTools` = `ls`/`glob`/`grep`/`view` 及别名）。

同时删除已无调用方的 `shouldPreferLocalToolkitTool` —— 留着它就是留着一个
"用错就分裂"的陷阱。

### 11.3 顺带修掉的第三个问题：审计标签

`resolveToolSource` 有同样的硬编码：

```go
if _, ok := m.toolkit.Get(lookupName); ok && m.shouldPreferLocalToolkitTool(lookupName) {
    return toolresult.SourceToolkit
}
```

这会让"列表说远程、执行走远程"的调用在审计里被记成 `SourceToolkit`。
已改成与 `executeWithMeta` 相同的判定。

### 11.4 结果

| 工具 | 列表归属 | 执行归属 | 审计标签 |
|---|---|---|---|
| `view`/`ls`/`glob`/`grep` | 本地 | 本地 | toolkit |
| 其余同名工具 | MCP | **MCP**（原为本地） | **mcp**（原为 toolkit） |

### 11.5 测试

新增 `internal/tools/dispatch_consistency_test.go`：

| 用例 | 断言 |
|---|---|
| `TestReadFamilyStillShortCircuitsLocally` | 读族仍然本地优先，**MCP 从未被调用** |
| `TestNonReadMcpOwnedNameDispatchesToMCP` | write/edit/apply_patch/append_write/shell 全部落到 MCP |
| `TestResolveToolSourceMatchesDispatch` | 审计标签与实际路由一致 |

前两条互为镜像：一条防止把读族也推给 MCP，一条防止非读工具留在本地。
只测其中一个方向，都可能放过另一半回归。

`TestSameNameSurfaceAndExecutionAgree`（原 `...Disagree`）改为断言新行为：
同名非读工具被列出后，**必须**在该 MCP server 上执行。

### 11.6 修复过程中暴露的一个崩溃

第一次跑上层回归时，`cmd/aicli/commands` 直接 panic：

```
panic: runtime error: invalid memory address or nil pointer dereference
  stubSessionScopedMCPManager.GetMCPStatus(<autogenerated>)
  tools.(*Manager).isRemoteScopedServer
  tools.(*Manager).validateRemoteToolNameCollision
```

原因：`stubSessionScopedMCPManager` 内嵌了一个值为 nil 的 `manager.Manager`
接口来满足接口定义，没有实现 `GetMCPStatus`。调用时不是返回错误，而是
**解引用 nil 接口**直接 panic。

这不是测试替身独有的问题：任何"只转发部分方法的包装器"都是这个形状。
而且 `ListTools` 是热路径，一次 panic 会直接终止会话。

**修复**：在 `isRemoteScopedServer` 里加 `recover()`，异常时按"非远程"处理。

```go
defer func() {
    if recovered := recover(); recovered != nil {
        remote = false
    }
}()
```

这与该函数的既定策略一致：**一个诊断用的守卫绝不能因为读不到状态就把会话搞挂**。
宁可漏报（当作非远程、放行），不可误杀。

测试：`TestCollisionGuardSurvivesManagerPanic` —— 用一个内嵌 nil 接口的 manager
验证 `ListTools` 不 panic，且该条目按普通（非远程）工具列出。

### 11.7 仍然存在的边界

1. **本地优先集是硬编码的四个名字**。要扩大（比如加入 `todos`）必须改
   `localToolkitPriorityTools`，并同步更新本节的测试。
2. **`executionScope: local` 的 MCP 与内置同名时，列表归 MCP、执行也归 MCP** ——
   现在一致了，但意味着本地内置在被遮蔽时确实不可用。这是既有设计的预期结果，
   不是回归。
3. **远程同名仍在校验层拒绝**（第 10 节），所以生产路径里根本到不了这个分派逻辑。
   本节的修复是让"如果真到了"也不会出错。

---

## 12. 方向调整（2026-10-21 晚）：用 `--remote` 取代 `--mcp-only` / 独占

### 12.1 变更理由

上一轮设计过 `--mcp-only`（独占一个 MCP server）。复核后改为不采用，原因有两条：

1. **它会隔离掉其它 MCP 工具**。用户的实际需求是"再加一个远程目标"，不是"把其它 MCP 全关掉"。
2. **配置要简单**。`remoteTargets` + `mcp.yaml` 双重声明对一个目标来说太重。

新方向：`--remote <spec>` 在**连接时**直接构造远程 MCP 配置并加载，**不写本地 mcp.yaml**，
且**与已有 MCP 服务器共存**。

### 12.2 已实现

```bash
# 空目录，没有任何 .aicli 配置
aicli chat --remote 'http://10.0.0.9:18765#standard'
aicli chat --remote 'ssh://ops@10.0.0.9/srv/app#ops'
```

一条命令即可。实测（空目录）：

```
$ aicli chat --no-interactive --remote 'http://127.0.0.1:18778#standard' \
    --prompt 'Use the remote tool to read a.txt'
remote: added server "127-0-0-1" (transport=streamable, depth=standard); ...
a.txt 的内容是：
hello-from-remote
```

模型确实通过远程工具读到了 WSL 上的真实文件，本地目录是空的。

### 12.3 实现要点

| 项 | 做法 |
|---|---|
| 配置来源 | 合成一个内存 `MCPConfig` 条目，**不落盘** |
| 与文件共存 | 先按常规分层加载（user→project→upward），再把 flag 条目**合并**进去 |
| 无文件也能用 | 加载不到文件时用空 `Config{}` 起步，flag 成为唯一的 server |
| 同名处理 | flag 覆盖同名的文件条目（命令行更具体） |
| 缓存隔离 | 每次重建 manager，不写 `mcpManagerConfigPath`，避免污染后续会话 |
| 授权 | 合成条目同时产出 `RemoteTargetPolicy`，注入 `ToolExecutionPolicy.RemoteTargets` |
| prompt 友好 | 解析失败在启动期报错，而不是起一个没有远程工具、也没有解释的会话 |

关键复用：`remotetargets.ParseSpec` + `Expand`（上一轮已实现并有 7 组测试）
直接产出 `MCPConfig` 与 `RemoteTargetPolicy`，`--remote` 只是把它接进 chat 的装配路径。

### 12.4 不独占的实证

同一次会话里，工具面是**本地 + 远程并存**（60 个工具）：

```
本地：aicli_exec, append_write, apply_patch, ..., view, write, shell
远程：remote_wsl__append_write, remote_wsl__apply_patch, remote_wsl__edit,
      remote_wsl__glob, remote_wsl__grep, remote_wsl__ls, remote_wsl__view,
      remote_wsl__write
```

没有 `--mcp-only` 那种"只留一个 server"的行为。

### 12.5 能力档位

`#` 后缀决定授权阶梯，实现在 `remotetargets.SpecDepth`：

| spec | allowWrite | allowShell |
|---|---|---|
| 无后缀 / `#readonly` | ✗ | ✗ |
| `#standard` | ✓ | ✗ |
| `#ops` | ✓ | ✓ |

实测 `#readonly` 下写操作被正确拒绝：

```
error: policy:remote target "127-0-0-1" does not allow write-like tool: remote_wsl__write
```

### 12.6 一个实测暴露的提示错误

第一次运行时提示打印的是本地推导的前缀：

```
remote: added server "127-0-0-1" (... tool prefix remote_127-0-0-1__)
```

但实际工具名是 `remote_wsl__*` —— 因为**前缀由目标服务端自己的 `toolPrefix` 决定**，
本地在握手前无从得知。已改为不打印猜测值，只说明"工具名来自目标侧的 toolPrefix"。

> 这类"提示信息说谎"比不提示更糟：运维会按提示去找 `remote_127-0-0-1__*`，然后找不到。

### 12.7 `remoteTargets` 的定位调整

仍然是**可选的高级形式**，用于：

- 多目标声明（一个会话连多台）
- 子代理目标授权（`subagentTargets`）
- ssh-stdio / ssh-http 的细粒度参数（identityFile、knownHosts、localForward）

但**不再是使用远程工具的前置条件**。单目标场景用 `--remote` 一条命令即可，
不需要 `remoteTargets`，也不需要 `mcp.yaml`。

精简空间：`remoteTargets` 中与 `--remote` 重复的字段（`transport: http` 单目标、
`workspace`、`profile`）在只有单目标时可省略 —— 这属于后续清理项，当前保持兼容。

### 12.8 测试

`cmd/aicli/commands/chat_remote_flag_test.go`，9 组：

| 用例 | 断言 |
|---|---|
| `TestBuildRemoteFlagTargetIsOptional` | 空值 → nil，调用方无需特判 |
| `TestBuildRemoteFlagTargetDerivesEverything` | 一个 URL 推导出 server 名/前缀/transport/scope/trust/grant |
| `TestBuildRemoteFlagTargetDepthLadder` | 四档能力映射（防误加 shell） |
| `TestBuildRemoteFlagTargetAcceptsSSH` | ssh:// 生成 stdio 命令 |
| `TestBuildRemoteFlagTargetRejectsBadSpec` | 5 种非法输入在启动期失败 |
| `TestApplyRemoteFlagTargetIsAdditive` | **已有 server 必须存活**（不独占） |
| `TestApplyRemoteFlagTargetOverridesSameName` | 同名时 flag 覆盖文件条目 |
| `TestApplyRemoteFlagTargetHandlesNil` | 无 flag 是真空操作 |
| `TestRemoteFlagNeedsNoConfigFile` | 空配置 + flag 即可成面 |

---

## 13. `--remote` 连不上时必须报错（2026-10-22 修复）

### 13.1 现象

用户报告：`aicli chat --remote 'http://127.0.0.1:18765#standard'` 没有生效。

排查结果：**`--remote` 本身工作正常，是 18765 端口上没有服务**（上一轮清理时已删除）。

但真正的缺陷在这里 —— 命令**没有任何错误提示**：

```
$ aicli chat --no-interactive --remote 'http://127.0.0.1:18765#standard' --prompt 'say OK'
remote: added server "127-0-0-1" (transport=streamable, depth=standard); ...
OK
$ echo $?   → 0
```

模型正常回答、退出码 0，用户完全无法察觉远程工具面是空的。

### 13.2 根因链

三层都"合理地"放过了一个明确的失败：

| 层 | 行为 | 为什么不报错 |
|---|---|---|
| MCP manager | `StartAsync` 只触发后台建连，失败写入该 server 的 `LastError` | 本意是不阻塞启动 |
| `initMCPManagerWithRemoteFlag` | 只检查 `StartAsync` 的返回值 | 后台建连的失败不在返回值里 |
| `discoverChatCapabilities` | 把 `prepareChatMCPManager` 的错误降级为 warning | 合理的默认：坏的 mcp.yaml 不该让本地会话起不来 |

三层单独看都对，叠加起来就成了"用户显式指定了一个目标，连不上却静默继续"。

### 13.3 修复

**两处改动**，都在"用户明确指定"这个前提下收紧了默认值：

**(1) 增加连接自检**（`chat_remote_flag.go`）：

```go
// 8 秒预算内轮询 GetMCPStatus
// 连上 → 返回 nil（保持静默）
// 报错 → 返回 error，带原始 network error
// 都超时 → 返回 error，提示检查地址/token/监听
if err := verifyRemoteFlagConnected(managerInstance, target); err != nil {
    return err
}
```

**(2) 让错误 fatal**（`chat_setup.go`）：

```go
if err := prepareChatMCPManager(cfg, session); err != nil {
    if session.RemoteFlagTarget != nil {
        return nil, err        // ← 有 --remote 时不再降级
    }
    fmt.Fprintf(os.Stderr, "Warning: 初始化 MCP 失败: %v\n", err)
    logpkg.Warnf("AICLI MCP init failed: %v", err)
}
```

**关键区分**：`--remote` 是"我就要用这个目标"，而 mcp.yaml 里的 server 是"配了但可能用不上"。前者失败必须中断，后者保持宽容。

### 13.4 修复后的行为

```
$ aicli chat --remote 'http://127.0.0.1:18765#standard' --prompt 'say OK'
remote: added server "127-0-0-1" (transport=streamable, depth=standard); ...
Error: 操作错误: 能力面初始化失败: --remote 127-0-0-1: cannot connect to the remote tool server:
  连接 MCP Server 失败: calling "initialize": sending "initialize": rejected by transport:
  Post "http://127.0.0.1:18765/mcp": dial tcp 127.0.0.1:18765: connectex:
  No connection could be made because the target machine actively refused it.
$ echo $?   → 1
```

正常路径不变（连接成功时全程静默）：

```
$ aicli chat --remote 'http://127.0.0.1:18765#standard' --prompt 'Use remote_wsl__view to read ok.txt'
remote: added server "127-0-0-1" (...)
`ok.txt` contains exactly:
ok-file
$ echo $?   → 0
```

### 13.5 测试

`cmd/aicli/commands/chat_remote_connect_test.go`：

| 用例 | 断言 |
|---|---|
| `TestVerifyRemoteFlagConnectedFailsFast` | 明确报错，且带上原始 network error |
| `TestVerifyRemoteFlagConnectedAcceptsHealthyServer` | 健康连接零噪音 |
| `TestVerifyRemoteFlagConnectedWithoutErrorStillFails` | 有无错误信息都超时 → 仍报错（不假装成功） |
| `TestVerifyRemoteFlagConnectedDefaultsToNil` | 没有 flag 的会话完全不受影响 |

最后一条很重要：这条自检只对 `--remote` 生效，普通 mcp.yaml 会话的宽容语义原样保留。

### 13.6 排错时常见误判

这次排查最初的方向是"`--remote` 没实现"，但实际逐项排除：

| 检查 | 结果 |
|---|---|
| `aicli-4x.exe chat --help` 里有 `--remote` | ✅ 有 |
| 二进制构建时间 | ✅ 0:03，比参考实现还新 |
| `127.0.0.1:18765` 是否在监听 | ❌ **000，不通** |
| WSL 里配置/二进制是否存在 | ❌ `CONFIG_GONE` / `BINARY_GONE` |

**根因是环境被清理，不是代码缺陷。** 但正是这次误判让静默失败暴露出来 —— 如果当时报了错，用户一秒就能看出是端口没起。

### 13.7 一点经验

"配置了但连不上"和"你指定让我用却连不上"是两种不同的语义：

- 前者应该是警告 + 继续（降级可用比完全不可用更有价值）
- 后者必须是错误 + 退出（继续执行会给出误导性的成功结果）

同一条代码路径同时承担这两种场景时，需要按"是否由用户显式指定"分流，
而不是统一取一个更宽松的默认值。

---

## 14. `remoteTargets` 三个"声明了但没接线"的缺陷（2026-10-22 修复）

### 14.1 起因

`--remote` 落地后，`remoteTargets` 是否还值得保留被重新审视。结论是**保留**——它承担 `--remote` 结构上无法承担的三件事：

| 能力 | `--remote` | `remoteTargets` |
|---|---|---|
| 多目标并存 | ✗（`String` 不是 `StringSlice`） | ✓ |
| 完整连接参数（knownHosts / localForward / allowedRoots / TLS） | ✗（只有 workspace/binary/identity） | ✓ |
| 子代理授权 `subagentTargets` | ✗ | ✓ |

但审查时发现：这个文件**只有"读授权"一个消费者**。`Expanded` 里另外三个数据成员全部没有接到生产路径。所以它看起来像"设计冗余"，实际上是**`--remote` 落地后没人回头补齐的接线**。

### 14.2 三个缺陷

#### 缺陷 1：声明的目标不会连接（G7 的真实成因）

`ApplyToConfig`、`Expanded.Servers` 在生产代码里**零调用者**，只出现在 `registry_test.go` 和 `scripts/`。

`remoteTargetsForSession` 只取两个字段：

```go
return expanded.Policies, expanded.Warnings
//     ↑ 只贡献授权        ↑ 而且没人用
```

**后果**：`.aicli/remote-targets.yaml` 不连接任何服务器，只提供一份"按 server 名字匹配的授权表"。连接仍必须另写 mcp.yaml——这就是 G7「同一目标写两处」的真正原因，不是设计取舍，而是**漏了一根线**。

**修复**：`loadMCPConfigWithRemoteFlag` 现在同时接收声明，把 `Servers` 并入同一份内存快照（`applyRemoteTargetsServers`）。

冲突语义按来源区分：

| 场景 | 处理 |
|---|---|
| `--remote` vs mcp.yaml 同名 | flag 胜（用户在命令行上打的字） |
| `remoteTargets` vs mcp.yaml 同名 | **报错**（两份都是文件，没有谁更权威） |
| `remoteTargets` vs `--remote` 同名 | flag 胜（会话级覆盖文件） |

#### 缺陷 2：`subagentTargets` 是 no-op

`SetSubagentRemoteTargets` 只在测试里被调用，生产路径从未调用。`agent.subagentRemoteTargets` 恒为 `nil`，于是 `narrowChildRemoteTargets` 把每个子代理都收窄到"零远程目标"。

**方向是对的**（默认拒绝，不越权），但**配置了的功能不存在**。

修复时发现第二个隐藏缺陷：**`subagentTargets: any` 这个标量形式完全不生效**。

```go
// 修复前
g.Deny = strings.EqualFold(strings.TrimSpace(scalar), "deny")
// "any" → Deny=false, Roles=nil
// → resolveSubagentAccess 走"无角色"分支 → 拒绝所有人
```

`[any]`（列表形式）是好的，所以这个 bug 只在文档推荐的写法上出现。现在标量 `any` / `*` 归一化成 `Roles: ["any"]`，未知标量**报错**而不是被当成角色名——授权是唯一"猜错就提权"的地方。

**修复**：新增 `SetSubagentRemoteTargetsByRole`，因为平铺 map 无法表达"researcher 有、implementer 没有"，合并会让每个子代理拿到所有角色的并集，正是 MT-R1 要防的提权。

```go
// 角色特有条目优先，baseline 兜底
if roleTargets, ok := a.subagentRemoteTargetsByRole[normalizeDelegationRole(role)]; ok {
    return cloneRemoteTargetPolicyMap(roleTargets)
}
```

#### 缺陷 3：加载警告被丢弃

`remoteTargetWarnings` 零调用者，`targets, _ := remoteTargetsForSession(...)` 直接把 warnings 扔掉。

这与 §13 的 `--remote` 静默失败是**同一类缺陷**：用户写的文件没有任何效果，但看不到任何提示。

**修复**：`emitRemoteTargetWarnings` 输出到 stderr，已验证可见：

```
警告: remoteTargets: target "sshbox": the remote profile "readonly" must be
      started with the same workspace in the server config
```

### 14.3 附带修复：声明路径也必须 fail-fast

端到端验证时发现语义不一致：

```
# 声明里写了一个连不上的目标
$ aicli chat --prompt "say OK"
Warning: 初始化 MCP 失败: remoteTargets 目标 "dead" 无法连接: ...refused
OK
$ echo $?   → 0          ← 又是一个"静默继续"
```

`chat_setup.go` 只对 `session.RemoteFlagTarget != nil` 做 fail-closed，声明路径走的是"warn and continue"。

**判断标准**（与 §13.7 同一条）：

| 场景 | 语义 | 处理 |
|---|---|---|
| mcp.yaml 里的 server | 共享文件，列了但可能用不上 | 警告 + 继续 |
| `--remote` | 你指定让我用 | 错误 + 退出 |
| `remoteTargets` 声明 | 这个工作区声明要对哪些机器工作 | **错误 + 退出** |

修复后：

```
$ aicli chat --prompt "say OK"
Error: 操作错误: 能力面初始化失败: remoteTargets 目标 "dead" 无法连接: ...
$ echo $?   → 1
```

### 14.4 端到端验证（WSL 真实环境）

| 场景 | 工作区内容 | 结果 |
|---|---|---|
| **仅声明，无 mcp.yaml** | 只有 `.aicli/remote-targets.yaml` | `exit=0`，模型读到远端 `ok-file` |
| 声明目标不可达 | 指向 18799（无服务） | `exit=1` + 完整 network error |
| 声明与 mcp.yaml 同名 | 两份都声明 `e2e` | `exit=1` + 冲突提示 |
| 声明产生 warning | ssh-stdio 目标 | 警告可见 |
| `--remote` 正常路径 | 无任何文件 | `exit=0`，回归无损 |

**第一条是关键**：修复前这是不可能做到的——工作区没有 mcp.yaml 时 `resolveChatMCPStartupConfigPath` 返回 `shouldInit=false`，MCP 初始化被整个跳过。

### 14.5 测试

| 文件 | 覆盖 |
|---|---|
| `chat_remote_declaration_test.go` (7) | Servers 暴露、合并、冲突拒绝、角色授权解析、无文件 fail-closed、副本隔离 |
| `chat_remote_delegation_test.go` (4) | baseline + 角色授权落位、无声明 no-op、子策略收窄（正反两向） |
| `remotetargets/subagent_grant_test.go` (3) | 标量 `any` / `deny` 解析、未知标量拒绝 |

其中 `TestDelegationNarrowsChildPolicy` 跑的是**真实生产路径**（`agentChildPolicy` → `narrowChildRemoteTargets`），而不是只断言声明内容——后者在授权没送达 agent 时同样会通过，正好漏掉本次缺陷。

### 14.6 一个判断错误值得记录

最初把缺陷 2 判为"fail-closed，安全，只是功能不存在"，优先级排在最后。

但核对 `--remote` 的教训后修正了：**静默失效的配置比不存在的配置更危险**。用户写了 `subagentTargets: [researcher]`，会以为子代理有远程只读能力而去设计工作流；实际跑起来子代理没有工具，报错信息是"工具不存在"，排查方向完全错。

"安全但没说"仍然是"没说"。

---

## 15. §14 的缺陷 2 被推翻：撤掉 per-target / per-role 授权层（2026-10-22）

### 15.1 触发

§14.6 把"子代理拿不到远程工具"定性为"静默失效的配置"，并据此把
`subagentTargets` 接上了线。随后被问到一个更根本的问题：

> 工具只提供工具属性，权限应该由不同的运行 mode 来控制，而不是额外增加过多的控制。

这个问题是对的，§14.2 的修复方向也因此被推翻。

### 15.2 实测证据：mode 本来就覆盖了远程工具

用真实 resolver + 真实 mode 表跑探针（`internal/policy/remote_mode_probe_test.go`）：

```
remote_prod__view        -> [read_only]
remote_prod__write       -> [write_fs]
remote_prod__edit        -> [write_fs]
remote_prod__shell       -> [exec_shell]
remote_prod__job_start   -> [background_task]     ← 修复后
```

对照 mode 判定：

```
write   default=ask  plan=deny   accept_edits=allow  dont_ask=deny  bypass=allow
shell   default=ask  plan=deny   accept_edits=ask    dont_ask=deny  bypass=allow
```

**逐格与本地同名工具相同。** 也就是说"远程工具能不能写"根本不需要第二个开关。

### 15.3 三层问题，一层比一层严重

| # | 问题 | 后果 |
|---|---|---|
| 1 | `RemoteTargets` 授权表与 mode 对同一问题给两个答案 | 语义重复；用户不知道哪个说了算 |
| 2 | `BlockRemoteWrites/BlockRemoteShell` 默认 `true`，且在 mode 之前**硬拒** | `accept_edits` 本该放行远程写，却被提前否决——**mode 被压制** |
| 3 | `subagentTargets` 按角色细分远程权限 | 第四层控制；且角色名靠调用方传，无法枚举穷尽 |

第 2 条是决定性的：不是"两个开关都生效"，而是**一个静默否决另一个**。
这正是"额外增加的控制"最坏的样子。

### 15.4 同时发现的两个真实缺陷

**(a) `job_start` 分类错误（而不是"需要补丁表"）**

`remote_prod__job_start` 的名字里没有 shell/bash/exec 标记，前缀又让注册表查不到
`job_start`，于是回退到子串启发式 → 被判成 `read_only` → **plan mode 会放行**一个
能在目标机起进程的工具。

原实现用一张补丁表 `remoteCommandToolSuffixes = {job_start, job_input}` 兜住它。
正确做法在**属性层**：MCP adapter 已经把服务端声明的裸名写进
`metadata["mcp_raw_tool_name"]`，分类器改用它查表即可：

```go
// internal/policy/capability.go
classifyName := rawToolNameForClassification(toolName, meta)
```

补丁表与 `toolWantsRemoteShell` 一并删除。

**(b) 旧 `policy:` 块会静默失效**

一旦移除授权层，老配置文件里的 `allowWrite: false` 就不再拦截任何东西。
若只是静默忽略，读者会以为它还在生效。

处理：**仍然能加载**（不因死键把会话搞崩），但 `Expanded.Warnings` 明确点名被忽略的键：

```
警告: remoteTargets: target "demo": the `policy:` block (allowRead, allowWrite,
      subagentTargets) no longer gates anything; remote tools are governed by the
      session permission mode like local ones
```

### 15.5 新模型

| 层 | 位置 | 控制什么 | 配置入口 |
|---|---|---|---|
| L1 端点鉴权 | 目标侧 | 谁能连 | `http.tokenEnv` / mTLS |
| L2 能力面 | 目标侧 | 暴露哪些工具 | `profile` / `tools` / `readOnly` |
| **L3 会话权限** | **客户端** | **allow / ask / deny** | **`--permission-mode`（判定）+ `--allow-tool` / `--deny-tool`（工具面）** |
| L4 目标策略 | 目标侧 | 路径与命令 | `workspace` / `allowedRoots` / `allowedCommands` |

`remote-targets.yaml` 退化为**纯连接声明**；`--remote` 的 `#depth` 退化为
**服务端 profile 请求**（ssh-stdio 真生效，http 明确提示未生效）。

### 15.6 不变式与钉子

新增两条互为镜像的测试，专门防止"远程又多一层"重新长回来：

| 测试 | 断言 |
|---|---|
| `policy.TestRemoteAndLocalClassifyIdentically` | 远程与本地同名工具 capability 逐项相同 |
| `commands.TestRemoteToolsAreGovernedLikeLocalOnes` | 策略层判定逐格相同 |

配套诊断：`scripts/gatecheck.go` 逐格比对远程/本地，任一格不同即 `FAIL`；
`scripts/permission_layers.go` 打印各 mode 下的真实结论。

### 15.7 教训

§14.6 写的是"安全但没说仍然是没说"，这条仍然成立。
但它漏了另一半：**说了两遍，而且两遍互相矛盾，比只说一遍更糟。**

当初加 per-target 授权的理由是 MT-R1（"子代理会继承全部远程目标"）。
回头核对：MT-R1 真正的危险是子代理继承**写和 shell**，而
`policy.narrowRemoteTargetsForChild` 已经把这两项剥掉——修复在属性/派生层，
不在新增授权层。**用更小的改动解决了同一个问题，就不该再引入第二套控制。**

新的判断标准：

1. 这个能力能不能用**工具属性**表达？（write-like / shell-like / background）
2. 能不能用既有 **mode** 表达？
3. 能不能用既有**名单**表达（支持通配）？
4. 三问都否，才考虑新层——并且要能指出它压制了上面哪个既有机制。
