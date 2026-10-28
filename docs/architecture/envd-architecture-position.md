# envd 到底在架构的哪个位置

> 一句话答案：**envd 位于「执行传输层」，和 SSH 平级；它不是新工具，也不是新的一层，它换掉的是「从本机到目标机的那条腿」。**

本文只回答"位置"这一个问题。集成细节见 `envd-enhance-ai-agent-runtime.md`。

---

## 1. 先看现状：没有 envd 时，一次工具调用怎么走

```
┌──────────────────────────────────────────────────────────────┐
│ ① 入口层   aicli CLI  ·  runtime-server  ·  Web 控制台          │
└───────────────────────────┬──────────────────────────────────┘
                            │
┌───────────────────────────▼──────────────────────────────────┐
│ ② 决策层   internal/agent                                     │
│    LLM 循环：决定"调哪个工具、传什么参数"                        │
└───────────────────────────┬──────────────────────────────────┘
                            │  调用方式：按【工具名】+【参数】
                            │  例：shell{command:"npm test"}
┌───────────────────────────▼──────────────────────────────────┐
│ ③ 工具层   internal/toolkit/tools  +  tools.Manager           │
│    ★ 工具面在这里固定：名字 / schema / 语义                      │
│    view · ls · glob · grep · edit · apply_patch · bash · shell │
└──────────┬────────────────────────────────────┬──────────────┘
           │                                    │
           │ 同一个工具面，两个后端               │
           │                                    │
┌──────────▼─────────────┐        ┌─────────────▼──────────────┐
│ ④a 本地执行             │        │ ④b 远端执行                 │
│    executor            │        │    remotetools             │
│    SandboxConfig       │        │    path jail               │
│    · AllowedPaths      │        │    Limits（输出折叠）        │
│    · AllowedCommands   │        │    audit                   │
│    · OSSandbox(bwrap)  │        │    Profile 分级             │
└──────────┬─────────────┘        └─────────────┬──────────────┘
           │                                    │
           │                        ★ 这里才是"传输层"
           │                ┌───────────┬───────┴───────┐
           │          ssh-stdio     ssh-http         http
           │                │           │              │
           ▼                ▼           ▼              ▼
      本机操作系统     目标机上的 aicli-remote-tools（三种形态同一份实现）
```

**关键观察**：`remotetools` 内部已经把「工具面」和「怎么连过去」分开了。
它一个二进制服务三种传输，工具面/jail/Limits/audit 完全共享，只有进程生命周期不同。

这就留下了一个空位 —— **一个可以插新传输的插槽**。

---

## 2. envd 插在哪：④b 的最下面，与 SSH 并列

```
                                    ④b remotetools
                                    （工具面 / jail / Limits / audit 全部不动）
                                          │
              ┌───────────┬───────────────┼──────────────┬────────────────┐
         ssh-stdio     ssh-http          http        ★ envd（新增）
              │           │               │               │
              ▼           ▼               ▼               ▼
        目标机 aicli-remote-tools              envd :49983
        （三种形态，同一份实现）              （Connect-RPC over HTTP/2）
```

换成代码位置就是这样：

```
backend/internal/
├── remotetargets/     ← 改：Spec.Transport 增加 "envd" 分支（与 #ops 同一套解析）
├── remotetools/       ← 改：新增一个 envd 传输适配器，实现与 stdio/http 相同的接口
├── envdclient/        ← ★ 新增：Connect-RPC 客户端（照 sshclient 的结构写）
│
├── executor/          ← 不动（本地执行 + 安全策略核心）
├── toolkit/           ← 不动（工具面）
├── sshclient/         ← 参照物（conn/session/sftp/auth/hostkey，结构可直接借鉴）
└── mesh/              ← 不动（发现与租约层，二期再说）
```

**注意：只新增 1 个包，改 2 个包。不删任何东西。**

> **注（2026-10-28 复核）**：上面是设计意图，**不是实际落地**。`remotetools` 的 envd 传输适配器（§5 放法 E）**未采纳**；实际落地是「`remotetargets` 新传输 + 本地 `aicli-envd-bridge` 进程」，详见 §8 与 `envd-implementation-report.md`。

---

## 3. 换腿之后，同一次调用变成什么样

目标：在 `10.0.0.9` 上跑 `npm test`。

| 步骤 | 走 SSH（现状） | 走 envd（新增） | 是否变化 |
|---|---|---|---|
| 1. agent 决定调工具 | 工具名 `shell` | 工具名 `shell` | ❌ 不变 |
| 2. tools.Manager 分发 | → remotetools | → remotetools | ❌ 不变 |
| 3. Profile 授权检查 | `ops` 才放行 shell | `ops` 才放行 shell | ❌ 不变 |
| 4. path jail 校验 | 校验工作目录 | 校验工作目录 | ❌ 不变 |
| 5. **选传输** | `transport: ssh-http` | `transport: envd` | ✅ **在这里分叉** |
| 6. **建连接** | sshd 拉起 `aicli-remote-tools --stdio` | Connect-RPC → `:49983/process.Process/Start` | ✅ **换掉** |
| 7. **跑命令** | MCP over stdio | envd 内部 fork + pty | ✅ **换掉** |
| 8. **取输出** | 一次性返回（现状） | Server-Stream 逐块回传 | ✅ **新增能力** |
| 9. 写审计 | `audit.go` | `audit.go` | ❌ 不变 |
| 10. 输出折叠 | `Limits.ModelVisibleCap` | 同左 | ❌ 不变 |
| 11. 回给 agent | 同一份 ToolResult | 同一份 ToolResult | ❌ 不变 |

**11 步里只有 5–8 步换了。这就是"换腿"的准确含义。**
第 1–4 步是"神经"（决定做什么、允不允许做），第 9–11 步是"记忆"（记账、裁剪、回报）—— 都留在原地。

---

## 4. 一个能立刻对上号的类比

envd 在两边扮演的是**同一个角色：被人远程调用的执行代理**。区别只是谁在调它。

```
【在 tabbit 沙箱里】（之前分析的）
  浏览器里的 agent
        │  Connect-RPC over HTTP/2 :49983
        ▼
      envd（沙箱内唯一的执行入口）


【在 ai-agent-runtime 里】（本次要做的）
  本机 agent
        │
        ▼
    remotetools（本机的工具面外壳：jail + audit + Limits）
        │  Connect-RPC over HTTP/2 :49983
        ▼
      envd（目标机上的执行入口）
```

看出来了吗 —— **envd 的位置没变，都是"目标机上的执行入口"。**
变的只是：在 tabbit 里，agent 直接连它；在 ai-agent-runtime 里，中间隔了一层 remotetools 做安全外壳。

这也顺便回答了一个常见误解：**envd 是装在「目标机」上的，不是装在本地。** 本地只需要一个 `envdclient` 包。

---

## 5. 为什么不能换个位置放（错误放法对照）

这是理解"位置"最有效的方式 —— 看放错了会出什么事。

### 放法 A：当成新工具，加到 toolkit 里（❌）

```
toolkit/tools/
├── shell.go
├── envd_shell.go   ← 新工具
```

- **症状**：模型会同时看到 `shell` 和 `envd_shell` 两个语义几乎一样的工具，选择困难；
- **后果**：`envd_shell` 走自己的路径，**完全不经过 remotetools 的 jail / audit / Limits**；
- **本质**：把安全外壳拆了。

### 放法 B：当成 executor 的本地后端（❌）

```
executor/ → envd（本机 127.0.0.1:49983）
```

- **症状**：本地跑命令要多一跳 HTTP；
- **后果**：丢掉 `OSSandbox=bwrap` 的全部隔离，envd 自己不做路径围栏；
- **本质**：本地场景没有任何收益，只有成本。

### 放法 C：当成 mesh 节点（❌）

```
mesh → envd
```

- **症状**：mesh 需要的是「心跳 + capability 声明 + 租约」，envd 一个都不提供；
- **后果**：要在中间硬造一层映射，两边语义都不对齐；
- **本质**：把发现层和执行层混为一谈。
- **正确做法**：mesh 是 envd 的**上游消费者**，不是它的宿主。

### 放法 D：直接把 envd 挂成 MCP server 给模型（❌ 最危险）

```
agent → MCP → envd
```

- **症状**：看起来最省事，一个 config 就接上了；
- **后果**：模型拿到一个**无路径围栏、无命令白名单、无审计**的远程 shell —— 等于把整台目标机交出去；
- **本质**：跳过了本项目最有价值的三个安全设施。

### 放法 E：作为 remotetools 的传输（✅ 唯一推荐）

```
remotetools → { ssh-stdio | ssh-http | http | envd }
```

- 工具面复用，安全外壳复用，只换底层通道；
- 新增能力（流式/PTY/watch/指标）自然获得；
- 改动最小，风险最低。

---

## 6. 一句话记住

> **envd 不是第 ⑤ 层，它是第 ④b 层底下的一条新通道。**
>
> 它和 SSH 是同一个位置上的两个选项 —— 选哪个，取决于你要不要「流式输出 / 交互式 stdin / 远程文件监听 / 主机指标」。
> 要，就选 envd；不要，SSH 已经够用。

---

## 7. 位置速查表

| 问题 | 答案 |
|---|---|
| envd 是新的一层吗？ | 不是。是 remotetools 下的一条传输通道 |
| 它和谁平级？ | `ssh-stdio` / `ssh-http` / `http` |
| 它替代了谁？ | 替代「目标机上的 aicli-remote-tools」这个角色 |
| 它不替代谁？ | `executor`、`SandboxConfig`、`audit.go`、`mesh` |
| 装在哪个机器？ | **目标机**（远端），本地只放 `envdclient` |
| 代码改哪？ | 新增 `internal/envdclient`；改 `remotetargets`（加 transport 分支）、`remotetools`（加适配器） |
| 该不该用？ | 需要流式/交互/监听/指标 且 目标是 Linux 远端 → 用；否则不用 |

---

## 8. 实施修正（2026-10-28）

本章记录**实际落地**与本文设计稿的偏差；细节与实测数据见 `envd-implementation-report.md`。

**实际形态：`remotetargets` 新传输 + 本地 bridge 进程**

- `remotetargets` 新增 `envd` 传输：`Spec` 增加 `EnvDTokenEnv` 字段，`spec.go` / `registry.go` 增加 `envd` 分支；`expandOne` 把 `envd://` 展开为一条 **stdio MCP 条目** —— `Command` 是本地 `aicli-envd-bridge`，`Args` 携带 envd 地址、user、workspace、profile；
- `cmd/aicli-envd-bridge` 是**本机进程**：一侧说 MCP（stdin/stdout），另一侧走 Connect-RPC 连目标机 `envd :49983`；
- `internal/envdclient` 按本文设计新增（Connect-RPC 客户端）。

**未采纳：原「放法 E」**

- 本文 §2 的「`remotetools` 新增一个 envd 传输适配器」（§5 放法 E，标为「唯一推荐」）**没有落地**。envd 不是 `remotetools` 的第四种传输，而是独立于它的一条新通道。

**后果**

- path jail、Profile 分级、输出上限（`Limits` 的等价物）在 **bridge 内实现**；文件路径在目标机经 `realpath`/`readlink -f` 规范化后再比对 jail，堵住了文本级围栏可被 symlink 绕过的问题；**命令授权已复用** `remotetools.CommandPolicy`，不再是两套 allowlist；
- `remotetools` 的 `audit` / `Limits` 仍未直接复用：bridge 有自己的 opt-in JSONL 元数据审计和 1 MiB 写入/输出上限；命令白名单、metachar 拒绝与 destructive 分类已覆盖 `shell` / `pty_start`；
- `registry` 展开 envd 条目时会**输出 warning**，声明 envd 不做这些检查（见 implementation report §3.3 `TestExpandOneEnvDBridge`）。

**受影响章节**：§2 代码结构图、§3 第 2–4 与 9–10 步、§5「放法 E」、§7 速查表中涉及 `remotetools` 的行，均以本节为准。

---

## 9. 收敛决策（2026-10-28）

§8 记录的偏差已完成收敛：`aicli-envd-bridge` 的能力已并入 `aicli-remote-tools`（新增 `serve --stdio --backend envd`，本机运行、以 Connect-RPC 为腿；仅 stdio），独立 bridge 进程**已删除**（不留 shim），`remotetargets` 的 envd 展开已指向新入口。

收敛后「envd 是 remotetools 的第四条腿」在**外壳层面**成立——MCP 循环 / Profile 档位 / 审计 / Limits / CommandPolicy / prefix 一份，执行后端两条（local-os / envd）；envd 专属的执行实现（目标侧 realpath jail、spawn `find`/`rg` 的合成、PTY 会话）保留在 envd 后端内。

§8「后果」段描述的是**收编完成前**的现状；收编完成后以本节形态为准。

计划与验收门见 `docs/plan/aicli-envd-bridge-merge-plan-20261028.md`。

---

*代码结构引用自本地仓库 `ai-agent-runtime` v0.5.0 实测读取。*
