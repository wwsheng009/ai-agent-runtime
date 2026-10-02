# 跨会话共享一个 LSP 服务：优化分析

> 2026-10-01 · 收口轮（十二）的输入分析 · 配套实验见 `backend/internal/lsp/live_second_instance_cost_test.go`
> 与 `backend/internal/lsp/live_shared_endpoint_test.go`（`-tags=live_semantic`）

## 结论先行

要优化，但**先做进程内共享，再做跨进程共享**，而且跨进程那一档先别急着上。

两句话的完整依据在下面两节：跨会话共享在本机是**真痛**（边际 500MB + 重付冷启）；
跨进程共享在协议上**已验证可行**（两个 LSP 会话共用一个 gopls，429MB），但它把
gopls 的生命周期从"本进程的孩子"变成"机器上的公共设施"，**这是新的风险面，不是
省内存的顺手延伸**。

---

## 一、今天到底浪费在哪：先量，别猜

### 实验 1：第二个实例的边际成本

`TestLiveSecondInstanceCostExperiment`（12 个真实文件 + 强制 `textDocument/diagnostic`，
两边等 RSS 稳定）：

| 指标 | 实测 |
|---|---|
| 实例 A 稳态 RSS | 541 MB |
| 实例 B 稳态 RSS | 525 MB |
| 两实例合计 | 1073 MB |
| **边际成本占比** | **97%**（第二个实例几乎付了整整一份） |
| 首答延迟 | A 6.5s / B 5.6s（**各自付一遍冷启**） |

结论：**没有任何工作被复用**。gopls 的索引、类型信息、SSA 全在进程堆里，第二个
实例从零再来一遍。共享省下的就是"第二个实例的全部成本"——约 500MB + 一次冷启。

补充实测：长期使用中的 gopls 稳态 RSS 会爬到 1.3GB（观察到一个运行中的实例
1307MB）。所以真实节省是 **≥500MB/额外会话，且随使用深度增长**，不是恒定 500MB。

### 实验 2：跨进程共享的技术前提

`TestLiveSharedEndpointServesTwoClients`：`gopls -listen=127.0.0.1:PORT` + 两个独立
客户端各自 `initialize`：

```
gopls -listen=127.0.0.1:53210 pid=34600
client_A definition = file:///E:/.../code_common.go
client_B definition = file:///E:/.../code_common.go
单实例达成：两个客户端共用 pid=34600，RSS=429MB
```

两个前提都成立：

1. **gopls 支持多客户端连接**（`-listen`，官方 flag；另有 `-listen.timeout` 可让
   服务端在无连接后自行退出）。
2. **我们的客户端能借连接而不拥有进程**：`DialResult.Conn` 是
   `io.ReadWriteCloser`，`Kill`/`Wait` 注释明确写着 "May be nil"（`client.go:20-40`）。
   实测两个客户端在共享连接上各自拿到一致且正确的 definition。

也就是说：**协议侧不需要改一行**，成本全在进程发现、租约与生命周期治理。

---

## 二、为什么"先进程内"

进程内共享和跨进程共享的收益差一个数量级，代价也差两个数量级。

| | 进程内共享（同 root 多会话） | 跨进程共享 |
|---|---|---|
| 覆盖场景 | 一个 aicli 开多个会话窗口 / ACP host 多会话 | 两个 aicli 进程指向同一项目 |
| 收益 | 500MB + 一次冷启，**零协议风险** | 同上 + 跨进程收益 |
| 机制 | 进程内共享实例表（带引用计数） | 命名端点/注册表 + 租约 + 心跳 |
| 主要风险 | 引用计数错误导致进程被提前关停 | gopls 崩溃影响**所有人**；所有权不清导致误杀；孤儿进程 |
| 可回退性 | 好（一个 map 的事） | 差（涉及"谁有权杀这个进程"） |

而**进程内的重复现在确实存在**：`NewDefaultManagerWithRuntimeConfig`
（`internal/tools/manager.go:73`）每个 ChatSession 一次（`chat_setup.go:478`），
每次都 `newLSPBridgeWith` → `lsp.NewBridgeWithOptions` 自建一个 `*Registry`，
没有任何进程内全局缓存。`Registry` 里也没有单例（`NewRegistry` 每次 `new(Registry)`）。

这里有个**尚未验证但很可能的泄漏**：多会话场景下，每个会话各自 attach 一个池；
`Manager.Close()`（`lsp_bridge.go:297`）会 `bridge.Stop` → 关掉池。若某个会话被
丢弃而没走 `Close`，它的 gopls 就成了孤儿——而孤儿 gopls 正是历史上"整机内存耗尽"
的直接形态。这一条需要按会话创建/销毁路径逐个核对（本次分析未完成取证，
**列为待验证项**，不在结论里）。

---

## 三、跨进程共享要解决的四件事（按难度排序）

### 1. 端点发现：gopls 不回填实际地址

`-listen=127.0.0.1:0` 让 gopls 自己选端口，但地址**不写到 stdout**，客户端无从得知
（实验里只能自己先占一个空闲端口再交出去，实验代码里已经标注了 TOCTOU 风险）。

三条路：
- **自己选端口再传**（实验用的就是这个）：简单，但有 TOCTOU 窗口。
- **落一个端点文件**：`<root>/.aicli/lsp/gopls-<hash>.endpoint`，写 `{addr, pid, started_at}`，
  客户端读它连接。落盘与 listen 之间仍有竞态，需要"写文件 → listen → 重写实际地址"
  或让监听方先建 socket 再传 fd（Windows 上不可行）。
- **固定端口 + 命名空间**：不可取，端口是全局资源，多 workspace 会撞。

推荐**端点文件 + 原子写（临时文件 + rename）**，并让服务端在 listen 成功后才发布地址。

### 2. 所有权与生命周期（最难，也是最危险的一档）

这是把 gopls 从"本进程的孩子"变成"机器上的公共设施"。必须先回答：

- **谁 spawn？** 第一个需要它的会话 spawn，但那时它的"进程组守卫"（ADR-0005 的
  Windows Job Object / Unix 进程组）绑定的是**那个会话的进程**。第一个会话退出 →
  Job Object KILL_ON_JOB_CLOSE → **共享 gopls 被杀，所有人的查询一起挂**。
  这是个结构性陷阱：`executor.ProcessGuard` 的语义与"跨会话存活"直接冲突。
  解法只能是共享模式下**不挂 Job Object**，改为显式的引用计数 + 空闲超时（对应
  gopls 自带的 `-listen.timeout`）。代价是失去"进程死了连带回收子进程"的保证，
  孤儿风险从进程内转移到机器级——**需要更强的证据才能接受**。
- **引用计数存哪？** 内存里的计数跨不了进程。需要每个客户端在端点目录留下
  带心跳的租约文件，服务端（或客户端）据此判断"还有人用吗"。
- **能不能杀？** `DialResult.Kill` 为 nil 时客户端**物理上无法**杀掉共享进程——这是
  好事（借方不该有杀权），但意味着"最后一个客户端退出时谁来收尸"必须有人负责。

### 3. 诊断归属：共享后诊断会串台

两个客户端共享一条 gopls 连接时，`textDocument/publishDiagnostics` 是**广播**的。
客户端 A 会收到只有 B 打开的文件产生的诊断。当前的诊断内联渲染
（`Bridge.Diagnose` → 编辑结果尾部追加）如果不做归属过滤，会把**别的会话的编辑
文件**的诊断追加到 A 的编辑结果尾部——这是用户可见的错误输出。

必须引入"这份诊断是本客户端打开的文件产生的吗"的判定。归属信息 gopls 不提供，
只能由客户端自己记账（谁 `didOpen` 了哪些 URI）。

### 4. 配置归属：一个进程改了 `lsp.enabled` 影响所有人

`config.workspace.write_scope` 已有 `session`/`project` 两级（`internal/config`），
但 LSP 池当前是**构造期**决定的（`manager.go:88`）。共享池之后，"会话 A 关闭了
LSP" 与 "项目级 `lsp.enabled: false`" 会变成两回事：前者不能影响共享池，后者也不该
被单个会话推翻。这需要明确一条规则：**共享池只受项目级配置控制，会话级只能停止使用、
不能停止进程**。

---

## 四、建议的推进顺序

**阶段 0（零风险，立刻可做）**：把"第二个实例的边际成本"与"单实例稳态"纳入观测。
`/lsp status` 显示内存，`lsp_servers` 加 `rss_mb`。没有这层，任何共享改动都无法验收。

**阶段 1（进程内共享，推荐本轮做）**：`internal/lsp` 增加进程内共享实例表，
key = `(server 名, root)`，value = 引用计数的 pool。`Registry` 构造先查表。
收益：同进程多会话立刻省 500MB/额外会话；机制是纯内存的，出问题只影响本进程。

**阶段 1 实施记录（2026-10-01，已落地并实测）**——见下方"§七 阶段 1 落地"。

**阶段 2（跨进程共享，建议独立一轮，先只读）**：先把"发现 + 连接"做出来，且
**默认不接管生命周期**——共享进程由创建者会话持有，其他会话只读连接；创建者退出时
其他会话降级回索引（而不是让它们一起死）。这是**渐进式**的跨进程共享：拿到内存
收益，但不让别人承担 Job Object 那个结构性陷阱。

**阶段 3（真正的机器级共享）**：需要先解决 Job Object 语义冲突 + 孤儿回收 +
端点竞态，并且要有独立 ADR。这一档我建议**在阶段 2 的运行数据出来后再决策**。

---

## 五、必须先验证的两件事（本次分析未完成）

1. **进程内多会话的实际重复度**：ACP host 与 web session 路径下，一个进程是否真的
   持有多个 `*Registry`。若实际都是单会话，阶段 1 的收益只在特定入口成立。
   （已确认构造路径无全局缓存，但未逐条核对所有会话创建入口。）
2. **孤儿 gopls 的产生路径**：会话被丢弃而未走 `Manager.Close()` 时是否留下活着的
   gopls。这既决定阶段 1 的紧迫性，也可能是一个**独立的、更小的 bug**。

---

## 六、本轮实测数据（可复现）

```
$ go test -tags=live_semantic ./internal/lsp/ -run TestLiveSecondInstanceCostExperiment -v
  instance_A 稳态 RSS = 541MB
  instance_B 稳态 RSS = 525MB
  边际成本：第二个实例相对已有实例 (4%)
  两实例合计 = 1073MB；共享方案可省 = 525MB

$ go test -tags=live_semantic ./internal/lsp/ -run TestLiveSharedEndpointServesTwoClients -v
  gopls -listen=127.0.0.1:53210 pid=34600
  client_A/B definition 一致且正确
  单实例达成：两个客户端共用 pid=34600，RSS=429MB

$ gopls version
  golang.org/x/tools/gopls v0.23.0（-listen / -listen.timeout / -remote 均可用）

机器：13.9GB 总内存 / 空闲 3.8GB；观察到一个长期运行的 gopls RSS=1307MB
```
---

## 七、阶段 1 落地（2026-10-01）

进程内共享已实现并经真机验证。下面记录**做了什么**与**顺带证伪/证实了什么**，
因为本节最有价值的部分不是"能共享"，而是共享必须避开的那几个坑。

### 7.1 共享键：比"（server 名, root）"更严

原方案写的是 `key = (server 名, root)`。实现时用了**整个规范化后的声明**
（`sharedRegistryKey`），因为 server 名不足以区分进程：两个会话都声明了
`gopls`、但一个带 `-listen`、一个不带，或 `max_tracked_docs` 不同，
按名字合并会让**先注册的声明静默胜出**。这类"谁生效取决于注册顺序"的 bug
最难查，宁可多一个进程。

`Env` / `InitializationOptions` 是 map，必须排序后再进键，否则 Go 的 map
迭代顺序会让本该共享的池分裂（`TestSharedRegistryEnvMapOrderDoesNotSplitKey`）。

**键里最容易漏的是"共享状态所依赖的配置"**，而不是"进程启动参数"。审查时
漏掉过 `RestartLimit` / `RestartWindow`：重启计数 `registryEntry.restarts` 与
窗口 `lastRestartAt` 存在**共享的 entry** 上，而预算是从 `entry.parent.cfg`
读的——也就是第一个注册者的声明。不进键的后果是双向静默：声明
`restartLimit: 0`（禁用恢复）的会话会在崩溃后被悄悄拉起；反过来声明要恢复的
会话可能撞上别人的 `0` 而永久停在 degraded。
判据是：**凡是"从池里读、且按会话语义本该各自独立"的字段，都要进键。**

**不参与共享的三类**（各有理由，不是遗漏）：
- `Enabled=false`：没有进程可共享，不该占共享表的键。
- 注入了 `Dial`：测试与自定义宿主的假 server 必须私有，否则两个互不相干的
  fake 会被并成一个，测试隔离当场失效。
- 成员列表为空：`Normalize` 会把 `nil` 填成预设目录，所以"没有成员"必须写
  空切片而不是 `nil`（这是实现期踩到的：`Config.Normalize` `spec.go:417`）。

### 7.2 生命周期：谁关停进程

引用计数在 **Bridge** 上，不在 Registry 上：

```
Bridge.Stop → (私有池) registry.Stop
            → (共享池) releasePool()   // 只减引用；最后一个减到 0 才 Stop
```

`Bridge.Stop` 用 `sync.Once` 包住整段：宿主在 `Close` 与迟到清理两条路径都可能
调 `Stop`，多减一次引用会把**别人还在用的池**减到 0 并提前关停——症状是
"第二个会话的编辑突然没有诊断"。`chat_setup.go:630` 的 `toolManager.Close()`
就是这条归还路径。

因此有一条**越权禁令**：共享池上禁止直接调 `registry.Stop`（那会把所有人一起
关停）。测试里曾短暂写成这样，随即改为从 Bridge 层断言。

### 7.3 观测扇出：hub 属于桶，不属于创建者

`client` 只在构造时拿到一个 `Observer` 函数。共享之后事件要送到每一个借用方，
所以共享表里的每个桶自带一个 `observerHub`，各 Bridge 订阅自己的那一份，
`Stop` 时退订。

**这里有一个我在实现中自己造出来又抓到的 bug**：最初在 Bridge 侧建 hub 并把
`hub.emit` 塞进 `RegistryOptions`，于是第二个借用方的事件被发到一个**没人读的
hub** 里——症状是"第二个会话收不到任何事件"，而第一个会话一切正常。
必须让 hub 随桶一起创建、随桶一起存在。

### 7.4 状态面：同一个 pid 必须能自证

共享后第二个会话的 `/lsp status` 会显示与第一个会话**相同的 pid**。不做说明，
这看起来就是"重复实例"——正是我们想消除的现象。

新增 `ServerStatus.ProcessSharedInProc`（共享会话数），在 `lsp_servers` 与
`/lsp status` 里渲染为 `in_proc_sessions=N（同一进程，未额外起 gopls）`，
沿用既有 `shared=diagnostics-pool` 的口径。

### 7.5 真机验证（可复现）

```
$ go test -tags=live_semantic ./internal/lsp/ -run TestLiveTwoBridgesShareOneServerProcess -v
  session_A 启动 gopls pid=43184 RSS=55MB
  session_B 复用同一进程 pid=43184（未新增 gopls）
  两个会话在同一进程上完成查询（A/B 各一次）
  关闭 session_A 后 pid=43184 仍存活，session_B 仍可借用
  session_B 关闭后 pid=43184 已回收（无孤儿）
```

判据是 **pid**，不是指针。指针相同也可能是各自 spawn（那样 pid 仍不同）——
本轮实测的 RSS=55MB 也说明第二会话未付出边际成本。

### 7.6 顺带修掉的一个既有竞态

`TestObserverSeesLifecycleAndDiagnostics` 在三包并行时偶发失败：诊断拿到了，
但 `diagnostics` 事件没到。根因是 `client.go` 在 `c.mu` 下写入快照、**之后**才
`emit`，而 `WaitDiagnostics` 看到快照就返回，于是 `AppendToResult` 可能早于
emit 落地。已改为 `waitFor` 等事件。这与共享改动无关（该用例注入 `Dial`，走私有
路径，观测链与改前逐字节一致），但既然撞见了就不留。

### 7.7 `-race` 抓到的既有竞争：同一个 `exec.Cmd` 被 Wait 两次

`internal/lsp` 整包跑 `-race` 会报 3 处 DATA RACE，两处写同一个地址：

```text
Write by goroutine 329: os/exec.(*Cmd).Wait()  client.go:1057  (Shutdown)
Previous write        : os/exec.(*Cmd).Wait()  client.go:302   (watchExit)
```

`watchExit`（监控崩溃）与 `Shutdown`（等进程收尾）各调了一次
`c.dialRes.Wait()`，而 `DialResult.Wait` 就是 `(*exec.Cmd).Wait`。并发调用
`Cmd.Wait` 是未定义行为：它在 `Cmd.ProcessState` 与 `awaitGoroutines` 上竞争，
两处还可能拿到互相矛盾的退出状态。

**归属已取证**：`git worktree add HEAD` 建一个完全不含本轮共享代码的干净副本，
跑同样的 `-race`，复现出**同样的 3 处**、同样的行号——既有缺陷，不是共享引入的。
但共享给 `Shutdown` 多加了一条触发路径（另一个会话归还引用时才会停池），
撞到这条竞争的概率更高，所以顺手修掉：新增 `Client.waitProcess()`，用
`sync.Once` 保证底层 `Wait` 只发生一次，结果经 `waitDone` 广播给所有等待者。
修后 `go test -race ./internal/lsp/` → `races: 0`。

判据提醒：**新增并发共享状态后要重跑 `-race`，但必须先把既有噪声与新噪声分开**
（用干净 worktree 做基线对比），否则很容易把旧缺陷当成新引入的回归。

### 7.8 进程生命周期：加固其实早就存在，是我一开始查错了地方

审查退出行为时我断言过一句"全仓库无 `JobObject` / `Pdeathsig` / `SysProcAttr`，
LSP 子进程没有任何 OS 级生命周期绑定"。**这句话是错的**：那次 grep 的范围只给了
`internal/lsp`、`internal/tools`、`cmd/aicli/commands`，而实现在
`internal/executor`，正好被排除在外。

实际情况（ADR-0005 §4.1 早已落地）：

| 位置 | 内容 |
|---|---|
| `executor/process_guard_windows.go:33-46` | `CreateJobObject` + `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`（含 BREAKAWAY_OK 逃生口） |
| 同上 `:80` | `AssignProcessToJobObject`，失败降级到 `taskkill /T /F` |
| `executor/process_guard_other.go` | Unix 侧 `SysProcAttr.Setpgid` |
| `internal/lsp/spec.go:536-548` | **`SpawnProcess` 已经接入** `NewProcessGuard()` + `Bind` + `Attach`，并注明"禁止在别处自建 Job Object" |

所以**没有新写 `child_lifetime_*.go`**：那会重复实现，且正是 ADR-0005 选项 A
（"在别处新写一套 Job Object 收树"）被显式否决的内容。

真正的缺口是**验收**，而不是实现——ADR-0005 的 D2 写着"aicli 崩溃/强杀后不得
残留 LSP 孤儿 … 复用 `acp_e2e_mcp_parent_kill.go` 的验证手法，**扩展断言 LSP
pid**"，而那条验收此前只覆盖 MCP。已补齐（§7.9）。

### 7.9 验收：必须用"故意不合规"的 server，否则等于没验

第一版硬杀实验用 gopls，**结论是假的**。LSP over stdio 的现实退出机制是
"客户端关闭 stdin → 服务端读到 EOF → 自行退出"，而 gopls 遵守这个约定——于是
"强杀宿主后无孤儿"在**根本没绑定 Job Object 的情况下照样通过**。它证明的只是
gopls 的礼貌。

改用 `internal/lsp/testdata/stubborn_lsp`：完成 `initialize` 握手后**再也不读
stdin**，睡死。宿主死亡对它毫无影响。于是判据变成：

```
宿主被强杀 → stub 消失	⇒ 只可能来自 OS 级绑定（不是服务端读 EOF）
宿主被强杀 → stub 存活	⇒ 绑定没生效，真孤儿
```

**负向对照（这一步不能省）**：临时把 `SpawnProcess` 的 `guard.Bind` / `guard.Attach`
短路掉，两个用例立刻双双 FAIL、stub 作为真孤儿存活；恢复后双双 PASS。这证明
用例不是空转的，也顺带证实了第一版 gopls 实验确实是空转的。

写这个 stub 时踩的几个坑，都是"看起来在测、其实没测"的典型：

1. **`taskkill /T` 会连子进程树一起杀** —— stub 被 taskkill 直接干掉，实验自证。
    必须不带 `/T`：要验的正是"只死父进程"。
2. **`select {}` 会触发 Go 死锁检测** —— 没有其它 goroutine 时运行时直接 panic
    杀掉 stub 自己，它变成一个主动退出的进程。必须用 sleep。
3. **往 stdout 打调试信息会污染 LSP 流** —— stdout 就是协议通道。
4. 探针不能用英文 `"No tasks"` 判存活（本地化 Windows 上那句话不存在，会恒判
    存活），要匹配带引号的 pid 字段；且必须有"杀之前它还活着"的对照组。

### 7.10 本节未做的事

- **跨进程共享（阶段 2）未动**。本节的所有结论只在"同一进程内"成立。
- 未做空闲驱逐（idle eviction）：池按引用计数存活，会话开着就一直占内存。
  这与孤儿 gopls 是两个问题——后者已由 `chat_setup.go:630` 的 Close 路径覆盖，
  但"会话还在、用户三天没编辑"的场景本轮没处理。
