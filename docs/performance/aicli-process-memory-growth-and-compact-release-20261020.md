# AICLI 进程内存膨胀分析与优化方案

> 文档类型：实测驱动的性能诊断 + 优化方案
> 分析对象：`aicli-5x.exe`（`E:\projects\ai\ai-agent-runtime\backend\aicli-5x.exe`，Build ID 2026-10-19 14:18:32）
> 分析时间：2026-10-20
> 数据来源：`/debug/pprof/heap` 快照 + 本地 SQLite 只读统计
> 结论基线：**存活堆仅 10.8 MB，但私有内存 688 MB** —— 问题不是对象泄漏，而是"高频全量序列化 + Go 运行时内存不归还 + 存储无界增长"三者叠加

---

## 一、现象与测量方法

### 1.1 现场快照

同机存在 5 个实例，均在 2026-10-19 起持续运行：

| PID | 进程 | 工作集 (WS) | 私有内存 | 启动时间 | 累计 CPU |
|---|---|---|---|---|---|
| 19324 | aicli-5x | 515 ~ 556 MB | **688.1 MB** | 10-19 14:47 | 11,173 s |
| 22012 | aicli-5x | 50 ~ 71 MB | **639.9 MB** | 10-19 16:51 | 10,300 s |
| 17836 | aicli-5x | 34 ~ 53 MB | **446.0 MB** | 10-19 14:08 | 9,982 s |
| 30852 | aicli-5x | 88 MB | 131.8 MB | 10-20 08:19 | 216 s |
| 3440 | aicli-4x | 253 MB | 346.4 MB | 10-19 22:13 | 8,052 s |

采集命令：

```powershell
Get-Process | Where-Object { $_.ProcessName -like '*aicli*' } |
  Select-Object Id,ProcessName,CPU,
    @{n='WorkingSetMB';e={[math]::Round($_.WorkingSet64/1MB,1)}},
    @{n='PrivateMB';e={[math]::Round($_.PrivateMemorySize64/1MB,1)}},
    StartTime
```

**关键观察**：15 分钟窗口内 WS 从 556 MB 掉到 515 MB，另一实例从 640 MB 掉到 71 MB，但 Private Bytes 仍维持 640 MB。这是 Go 运行时"堆已释放、地址空间未归还"的典型形态（Windows 上 scavenger 逐 span 归还，长空闲期不触发 GC）。

### 1.2 pprof 取证

四个实例都带 `--pprof --web`，可直接取 profile：

```powershell
# 关键：加 gc=1 强制 GC 后再采样，才能看到"真实存活"而不是"待回收垃圾"
curl.exe -s -o heap.pb.gz "http://127.0.0.1:<port>/debug/pprof/heap?gc=1"

go tool pprof -sample_index=inuse_space -top heap.pb.gz
go tool pprof -sample_index=alloc_space -top heap.pb.gz
```

端口分配（`Get-NetTCPConnection -State Listen`）：

| PID | pprof 端口 |
|---|---|
| 19324 | 53693 |
| 22012 | 58341 |
| 17836 | 65353 |
| 3440 | 65308 |

### 1.3 强制 GC 后的存活堆

```
PID 19324: 345 objects / 10,787,200 B   → 10.8 MB
PID 22012: 869 objects / 515,254,224 B  → 492 MB
```

**PID 19324 运行 14 小时、占 688 MB 私有内存，GC 后存活堆只有 10.8 MB。** 这是全篇最重要的一条测量：它直接否定了"Go 对象泄漏"的假设，把问题锁定在分配 churn 与运行时保留策略上。

---

## 二、pprof 读数纠偏（必读）

### 2.1 `text/tabwriter` 的 277 MB 是假象

初次读 `inuse_space` 会看到：

```
165.61MB  31.72%  text/tabwriter.(*Writer).append
107.61MB  20.61%  text/tabwriter.(*Writer).addLine
 41.54MB   7.96%  cmd/aicli/ui.cloneAppRenderLine
```

看起来像 tabwriter 缓冲区泄漏。**但 `-top -cum` 的父节点揭示了真相**：

```
283.77MB  net/http/pprof.handler.ServeHTTP
  └─ runtime/pprof.(*Profile).WriteTo
       └─ runtime/pprof.writeHeapInternal
            └─ runtime/pprof.printStackRecord
                 └─ fmt.Fprintf
                      └─ text/tabwriter.(*Writer).Write   ← 277.72 MB
```

`go tool pprof -peek='^fmt.Fprintf$'` 确认：**tabwriter 的 100% 分配都来自 `runtime/pprof.printStackRecord`**。

结论：这 277 MB 是 **pprof 自己写 heap profile 时构造文本输出的缓冲**，占用在采样瞬间，不是应用代码的内存。任何排查都必须先过滤这条链：

```powershell
go tool pprof -ignore='runtime/pprof|text/tabwriter|fmt.Fprintf' \
  -sample_index=inuse_space -top heap.pb.gz
```

过滤后真实存活堆分布（PID 22012，542 MB 中的 158 MB 已归因）：

| 归属 | 存活 | 说明 |
|---|---|---|
| `ui.cloneAppRenderLine` | 41.5 MB | 渲染行克隆 |
| `ui.HistoryCommitLedger.Enqueue` | 22.0 MB | 提交账本 |
| `ui.reasoningProjectionScreenRows` | 17.5 MB | reasoning 投影行 |
| `ui.HistoryCommitLedger.pruneEntry` | 16.9 MB | 账本淘汰残留 |
| `strings.Builder.WriteString` | 16.5 MB | 字符串拼接 |
| `encoding/json/v2.makeString` | 10.1 MB | JSON 解码字符串 |

即：**真实常驻内存主体在 TUI 渲染层**（约 100 MB 量级），而非存储层。

### 2.2 `alloc_space` 才是关键指标

`inuse_space` 只回答"现在还占着多少"，`alloc_space` 回答"这个进程一共分配过多少"。后者与 GC 压力、堆扩张直接相关：

```
PID 19324 alloc_space 总计: 497.77 GB
PID 22012 alloc_space 总计: 994.82 GB
```

一个跑了 14 小时的 CLI 进程累计分配 **0.5 ~ 1 TB**。这个量级决定了 Go 的堆目标被反复推高，进而导致 RSS 高位停留。

---

## 三、根因分析（按代码路径排序）

### 3.1 根因 A：对账扫描每个 agent 都全量反序列化会话（最大热点）

**pprof 调用链（PID 19324，`alloc_space`）**：

```
217,602 MB (42.69%)  internal/chat.(*SQLiteSessionStorage).Load
  └─ loadPromptMessages
       ├─ 151,963 MB (69.84%)  encoding/json.Unmarshal
       └─  60,385 MB (27.75%)  database/sql.(*Rows).Scan
```

**调用方**（`internal/chat/sqlite_storage.go:1711`）：

```go
func (s *SQLiteSessionStorage) loadPromptMessages(ctx context.Context, sessionID string) ([]types.Message, error) {
	rows, err := s.readPoolHandle().QueryContext(ctx, `
		SELECT payload_json FROM session_prompt_messages
		WHERE session_id = ? ORDER BY position ASC
	`, sessionID)
	...
	for rows.Next() {
		var payload []byte
		rows.Scan(&payload)
		var message types.Message
		json.Unmarshal(payload, &message)   // ← 整段历史全量解码
		history = append(history, message)
	}
	return collapseDuplicateMessages(history), nil
}
```

**触发链**：

```
cmd/aicli/commands/chat_actor_reconcile.go:58   Reconciler.RunLoop  (每 10 分钟)
 └─ Reconciler.runPass (internal/agentcontrol/reconciler.go:178)
     └─ localChatRuntimeHost.buildLocalRegistryReconciler.func1  (chat_actor_reconcile.go:88)
         └─ localActorRegistry.materializeLocalAgentRegistry  (chat_actor_registry.go:1925)
             └─ sweepStaleLocalAgentRegistry  (chat_actor_registry.go:2107)
                 └─ localAgentRegistryTerminalState  (chat_actor_registry.go:2255)
                     └─ r.Host.SessionStore.Load(...)   ← 第 2259 行，全量 Load
                         └─ SQLiteSessionStorage.Load  (sqlite_storage.go:1601)
                             └─ loadPromptMessages
```

关键代码（`cmd/aicli/commands/chat_actor_registry.go:2255-2259`）：

```go
func (r *localActorRegistry) localAgentRegistryTerminalState(ctx context.Context, sessionID string) (terminal bool, stale bool, err error) {
	if r == nil || r.Host == nil || r.Host.SessionStore == nil {
		return false, false, nil
	}
	session, err := r.Host.SessionStore.Load(ctx, strings.TrimSpace(sessionID))   // ← 只需 State
	...
}
```

该函数只用 `session.State`（第 2266 行 `isClosedLocalAgentSession(session)`）和 `session.Metadata.Context`，**根本不需要 history**。

**实测放大倍数**：

- `agent_control_agents` 共 **1,575 行**（stale 980 / closed 370 / active 225）
- 最大单会话 prompt projection：**2,146 行 / 11.56 MB**
- 每轮 = 1,575 次 Load × 平均数 MB 解码
- 每 10 分钟一轮 → 每天 144 轮 → **约 21.7 万次全量 Load/天**

> **同一文件第 2055-2063 行已有正确写法**（`localAgentSessionBindingLookup` 优先走 `LoadMetadata`），注释甚至明确写了"完整 Load 会反序列化整段 prompt 历史……会把启动期历史分页和 /web/api/status 快照一起拖住"。**对账路径没有同步改造，是明确的实现遗漏。**

```go
// chat_actor_registry.go:2060-2064  已有正确范式
if reader, ok := r.Host.SessionStore.(runtimechat.SessionStorageMetadataReader); ok {
	session, loadErr = reader.LoadMetadata(ctx, result.SessionID)
} else {
	session, loadErr = r.Host.SessionStore.Load(ctx, result.SessionID)
}
```

而 `LoadMetadata`（`sqlite_storage.go:1646`）只读 `sessions` 单行，注释见 `storage.go:79-86`：

> 只需要「存在性 + 状态」的调用方（诊断审计、P2-9 对账）必须优先走这条路径

**这是 P0 级修复，改动约 8 行。**

---

### 3.2 根因 B：每个 think 迭代全量 Marshal 整份 history

**pprof 证据**（`alloc_space`，PID 22012）：

```
122.22GB  12.29%  slices.Grow[[]uint8]
 86.82GB   8.73%  encoding/json/v2.makeString
 86.80GB   8.73%  reflect.mapassign_faststr0
```

调用链（`internal/agent/loop.go`）：

```
loop.go:1029   (*ReActLoop).run
 └─ loop.go:2769   (*ReActLoop).think
     └─ loop.go:6000   promptMessageFingerprint
         └─ loop.go:6101   json.Marshal(payload)   ← 构造整个 payload 再序列化
```

代码（`loop.go:6061-6107`）：

```go
func promptMessageFingerprint(messages []types.Message) string {
	payload := make([]fingerprintMessage, 0, len(messages))
	for _, message := range messages {
		item := fingerprintMessage{
			Role:         message.Role,
			Content:      message.Content,
			ContentParts: append([]types.ContentPart(nil), message.ContentParts...),  // 深拷贝
			ToolCallID:   message.ToolCallID,
		}
		if len(message.ToolCalls) > 0 {
			item.ToolCalls = make([]fingerprintToolCall, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				item.ToolCalls = append(item.ToolCalls, fingerprintToolCall{
					ID: call.ID, Type: call.Type, Name: call.Name,
					Args: call.Args,                 // ← map 引用，但序列化时仍要反射遍历
					RawInput: call.RawInput,
				})
			}
		}
		payload = append(payload, item)
	}
	encoded, err := json.Marshal(payload)     // ← 全量 JSON
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", sum[:])
}
```

**问题**：每个 think 迭代重建整个 `fingerprintMessage` 切片（含 `ContentParts` 深拷贝），再 `json.Marshal` 一遍。history 有 N 条消息、每条 M 字节 → 每次迭代分配 O(N×M)，一轮 ReAct（k 次迭代）累计 O(k×N×M)。`compactionRecoveryMadeProgress`（loop.go:6121）还会**再调用一次**做前后对比，成本翻倍。

同时 `estimateToolDefinitionTokens`（loop.go:6155）在每个迭代也 `json.Marshal(tools)` 一次。

---

### 3.3 根因 C：每次 Update 全量重编码 history

`sqlite_storage.go:1891`：

```go
func encodeMessages(messages []types.Message) ([]encodedSessionMessage, error) {
	encoded := make([]encodedSessionMessage, 0, len(messages))
	for index := range messages {
		payload, err := json.Marshal(messages[index])   // ← 逐条全量 Marshal
		encoded = append(encoded, encodedSessionMessage{
			message: messages[index], payload: payload, size: len(payload),
		})
	}
	return encoded, nil
}
```

pprof 确认这条链（PID 22012）：

```
slices.Grow[[]uint8] 122.22 GB
  └─ encoding/json/jsonwire.AppendQuote
       └─ encoding/json/v2.Marshal
            └─ internal/chat.encodeMessages  (sqlite_storage.go:1894)
                 └─ updateSessionTx  (sqlite_storage.go:869)
```

`updateSessionTx` 第 868-879 行已经算出了增量起点 `appendFrom`：

```go
if appendFrom < len(session.History) {
	incoming, err := encodeMessages(session.History[appendFrom:])   // ✅ 增量编码
	...
}
```

但第 945 行的投影重建走的是**全量**：

```go
projection, err := s.buildPromptProjection(source)   // ← 全量
if err := replacePromptMessagesTx(ctx, tx, session.ID, projection); err != nil { ... }
```

而 `replacePromptMessagesTx`（第 2023 行）先 `DELETE` 整表再逐条 `INSERT`：

```go
func replacePromptMessagesTx(ctx context.Context, tx *sql.Tx, sessionID string, messages []encodedSessionMessage) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_prompt_messages WHERE session_id = ?`, sessionID); err != nil { ... }
	// 然后逐条 INSERT 全部 projection
}
```

**每次 checkpoint（默认 15 秒，`history_checkpoint.go:16`）都会 DELETE + 重插整段 projection。** 一个 2,146 行 / 11.56 MB 的会话，每个 checkpoint 写 11.56 MB，一轮对话可触发数十次。

---

### 3.4 根因 D：TUI 渲染层每帧全量重排

**pprof alloc_space（PID 22012）**：

| 函数 | 累计分配 |
|---|---|
| `ui.layoutTranscriptScreenRowsImpl` | 94.87 GB |
| `ui/vt.(*Screen).CellRows` | 30.56 GB |
| `ui/scene.(*TuiScene).Cells` | 21.28 GB |
| `ui.appendCachedCellRows` | 20.97 GB |
| `ui/scene.LayoutTranscript` | 21.04 GB |
| `ui/documentScreenRows` | 42.59 MB (inuse) |

**inuse_space（过滤 pprof 噪声后）**：

| 函数 | 存活 |
|---|---|
| `cloneAppRenderLine` | 41.54 MB |
| `HistoryCommitLedger.Enqueue` | 22.00 MB |
| `reasoningProjectionScreenRows` | 17.45 MB |
| `HistoryCommitLedger.pruneEntry` | 16.89 MB |

`HistoryCommitLedger` 的 `Enqueue` 与 `pruneEntry` **同时存在 22 MB + 16.9 MB 存活**，说明存在"入队了但没被淘汰/释放"的账本条目。`cloneAppRenderLine` 每帧克隆整行，历史越长单次成本越高。

这是"进程常驻 640 MB 但存活堆只有几十 MB"的主要来源：**每帧产生大量短命对象，把 GC 堆目标推高，Go 保留 arena 不归还。**

---

### 3.5 根因 E：存储无界增长（9.4 GB）

```
session_runtime.sqlite    3,099.7 MB
  session_events          1,713,984 行
  cache_requests            134,050 行
  session_tool_receipts     165,309 行
  session_actor_leases          471 行
  session_runtime_state       2,016 行

session_history.sqlite    2,379.1 MB
  session_messages          105,470 行
  session_prompt_messages    55,425 行
  sessions                      412 行

agent_control.sqlite      2,339.2 MB
  agent_control_agent_wake_events   4,285,943 行
  agent_control_agents                  1,575 行

usage_analytics.sqlite    1,378.3 MB
artifacts.sqlite            205.8 MB
background.sqlite           112.9 MB
──────────────────────────────────
合计约 9.4 GB（.aicli/sessions 目录 6,474 文件 / 9,377 MB）
```

已有的保留策略（`session_runtime_store.go:302-303`）：

```go
defaultRuntimeEventRetention   = 2048   // 按条数
defaultRuntimeMailboxRetention = 2048
```

`agent_control` 侧也已有治理（`internal/agentcontrol/retention.go`）：

```go
DefaultTerminalRetention        = 30 * 24 * time.Hour  // 终态行 30 天
DefaultMaxActiveWakeEventsPerAgent = 64                 // 每 agent 活跃 wake 事件上限
DefaultClosedWakeRetention      = 7 * 24 * time.Hour
```

**但这些策略默认是 `observe` 模式**（`internal/config/manager.go:868-869`）：

```go
RegistryReconcileInterval: 10 * time.Minute,
RegistryReconcileMode:     "observe",     // ← 只报候选，不删
```

实测 `agent_control_agents` 仍有 980 条 stale，`wake_events` 有 428 万行——说明 **observe 模式从未被提升为 enforce**，治理策略实际未生效。

另外 `session_event_payload_cap.go:11-15` 的注释记录了一个重要历史事实：

> 907 MB 的 session_runtime.sqlite 里约 75% 字节就是这类流式增量

即单条 `assistant.reasoning` / `assistant_delta` 载荷可以比整个被裁剪后的日志还大。当前已有 256 KB/条的 cap（`DefaultMaxSessionEventPayloadBytes`），但仍按**条数**保留 2048 条 × 256 KB ≈ 512 MB/会话上限。

---

## 四、优化方案

### 4.1 P0：对账路径改用 LoadMetadata（约 8 行）

**收益**：直接消除 42.7% 的累计分配。这是全部方案中投入产出比最高的一项。

**位置**：`backend/cmd/aicli/commands/chat_actor_registry.go:2255`

```go
func (r *localActorRegistry) localAgentRegistryTerminalState(ctx context.Context, sessionID string) (terminal bool, stale bool, err error) {
	if r == nil || r.Host == nil || r.Host.SessionStore == nil {
		return false, false, nil
	}
	sessionID = strings.TrimSpace(sessionID)

	// 只需要「存在性 + 状态」：与 localAgentSessionBindingLookup 一致，
	// 优先走元数据读取，避免为一次终态判定反序列化整段 prompt 历史。
	var session *runtimechat.Session
	if reader, ok := r.Host.SessionStore.(runtimechat.SessionStorageMetadataReader); ok {
		session, err = reader.LoadMetadata(ctx, sessionID)
	} else {
		session, err = r.Host.SessionStore.Load(ctx, sessionID)
	}
	if err == runtimechat.ErrSessionNotFound || session == nil {
		return true, true, nil
	}
	if err != nil {
		return false, false, err
	}
	// ... 后续逻辑不变
}
```

**为什么安全**：`LoadMetadata` 与 `Load` 的"存在性 / state 语义完全一致"（`sqlite_storage.go:1642-1645` 注释明确保证）。唯一差异是 `History` 为空、`HistoryLoaded=false`。

**验证**：`isClosedLocalAgentSession` 只用 `session.State`（`chat_actor_registry.go:5192-5197`），`session.Metadata.Context` 由 metadata 列的 `metadata_json` 反序列化而来，`LoadMetadata` 已包含。**无行为变化。**

**前置检查**：确认 `r.Host.SessionStore` 在 CLI host 下确实实现了 `SessionStorageMetadataReader`。若不实现会落到 else 分支，行为与现状一致（安全降级）。

---

### 4.2 P0：对账前先用 SQL 预过滤，避免 O(全部记录)

**位置**：`chat_actor_registry.go:2093` `sweepStaleLocalAgentRegistry`

当前逻辑：拉全部 1,575 条 → 逐条 `localAgentRegistryTerminalState` → 每条一次 DB 查询。

**优化**：先按 `status` 与 `closed_at` 做集合级过滤，只对"状态可疑"的行做逐条判定。

```go
for _, record := range existing {
	record = record.Normalize()
	if record.Closed() || record.RootSessionID == "" || record.AgentPath == "" || record.SessionID == "" {
		continue
	}
	// 新增：非 active/stale 的行不可能在本轮变成终态，跳过逐条 Load。
	if record.Status != agentcontrol.AgentStatusActive &&
	   record.Status != agentcontrol.AgentStatusStale {
		continue
	}
	...
}
```

实测 `agent_control_agents` 中 closed 370 条会走 `record.Closed()` 短路，但 980 条 stale + 225 条 active 仍是每次全查。若 stale 行已有 `closed_at`，可进一步跳过。

> 此项需先确认 `AgentRecord.Status` 的取值域与 `Closed()` 语义，实施前补一条针对性测试。

---

### 4.3 P1：`promptMessageFingerprint` 改为增量哈希

**位置**：`backend/internal/agent/loop.go:6061`

**问题**：每个 think 迭代重建整个切片 + 全量 `json.Marshal`。

**改法**：逐条流式喂入 `hash.Hash`，不构造中间切片、不做 `ContentParts` 深拷贝。

```go
func promptMessageFingerprint(messages []types.Message) string {
	if len(messages) == 0 {
		return ""
	}
	h := sha256.New()
	var buf []byte
	// 用长度前缀消除分隔符歧义（"ab"+"c" 与 "a"+"bc" 不能同哈希）
	writeField := func(s string) {
		buf = buf[:0]
		buf = binary.AppendUvarint(buf, uint64(len(s)))
		h.Write(buf)
		h.Write([]byte(s))
	}
	for i := range messages {
		m := &messages[i]
		writeField(m.Role)
		writeField(m.Content)
		writeField(m.ToolCallID)
		// ContentParts：只哈希影响提示语义的字段
		for _, part := range m.ContentParts {
			writeField(part.Type)
			writeField(part.Text)
			writeField(part.ImageURL)   // 按 types.ContentPart 实际字段调整
		}
		for _, call := range m.ToolCalls {
			writeField(call.ID)
			writeField(call.Type)
			writeField(call.Name)
			writeField(call.RawInput)
			// Args 是 map，需稳定序列化；可复用既有 canonical JSON 编码器
			if len(call.Args) > 0 {
				encoded, err := json.Marshal(call.Args)
				if err != nil {
					return ""
				}
				h.Write(encoded)
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
```

**收益**：分配从 O(N×M) 降到 O(总字节流)，省去整个 `payload` 切片与 `ContentParts` 深拷贝。按 pprof 中 `slices.Grow` 122 GB + `makeString` 87 GB 估算，**该项可削减约 20% 的总分配**。

**注意**：`Args map[string]interface{}` 的 JSON key 顺序在 Go 的 `encoding/json` 中是确定的（按键排序），可安全用于哈希。若切到 `encoding/json/v2` 需确认同样保证。

**验证**：补一个性质测试——对同一 history 的两次调用结果相同，且改变任一字段（role/content/part/toolcall/args）都会改变哈希。

---

### 4.4 P1：`encodeMessages` 增量化 + 投影增量写

**位置**：`sqlite_storage.go:1891`、`sqlite_storage.go:2023`

**问题**：
1. `buildPromptProjection(source)` 每次全量重编码；
2. `replacePromptMessagesTx` 每次 `DELETE` + 全量 `INSERT`。

**改法 A（最小）**：`updateSessionTx` 的 `default` 分支已有 `appendOnly` / `prefixMatches` 判定。对 `prefixMatches && appendOnly` 情况，投影应走增量 `appendPromptMessageTx`，而不是重建。当前 `appendOnly` 分支（第 880-890 行）已经这么做了：

```go
case appendOnly:
	for index := currentPromptCount; index < len(session.History); index++ {
		promptRows, err = s.appendPromptMessageTx(ctx, tx, session.ID, promptRows, session.History[index])
	}
	*storedProjection = promptRowMessages(promptRows)
```

**真正需要修的是 `default` 分支**（第 894 行起）：它在 `HistoryReplaced=false` 且未证明最新时走 `loadCanonicalMessagesTx` + `buildPromptProjection` + `replacePromptMessagesTx` 全量三连。pprof 显示 `canonicalFullLoads` 计数路径确实被命中。

**建议**：
- 让 `incomingHistoryReachesNewestFastTx` 的快速证明覆盖更多场景（当前依赖 `identity_hash` 索引）；
- 在 `default` 分支加分层降级：能证明是"前缀 + 尾部追加"就只 append，只有真正 replacement 才 `replacePromptMessagesTx`；
- `replacePromptMessagesTx` 改为 `UPSERT` + 删除 `position > len(projection)` 的尾行，避免整表 DELETE。

**改法 B**：把 `encodeMessages` 的逐条 `json.Marshal` 复用为 `encodePromptMessage`，避免同一 message 在同一事务里被编码两次（`buildPromptProjection` 与 `encodeMessages` 各一次）。

---

### 4.5 P2：TUI 渲染层收敛

**位置**：`backend/cmd/aicli/ui/`

问题点与改法：

| 位置 | 问题 | 改法 |
|---|---|---|
| `HistoryCommitLedger` | `Enqueue` 22 MB + `pruneEntry` 16.9 MB 同时存活，疑似淘汰不彻底 | 加硬上限（条数 + 字节）与真正的 LRU 淘汰；`pruneEntry` 后释放内部 map/slice 引用 |
| `cloneAppRenderLine` | 每帧克隆整行，41.5 MB 常驻 | 引入行版本号 + 不可变共享；只在行内容变更时克隆 |
| `layoutTranscriptScreenRowsImpl` | 94.9 GB 累计分配 | 按"可见窗口 + 上下 overscan"布局，而非全 transcript |
| `reasoningProjectionScreenRows` | 17.5 MB 常驻 | reasoning 折叠后释放投影行 |
| `documentScreenRows` | 每帧重建 | 文档内容未变时复用上次结果（按 content hash 判定） |

**量化目标**：单实例 inuse 从约 100 MB 降到 30 MB 以内。

---

### 4.6 P1：compact 后释放历史会话内容（用户新增需求）

这是本文档相对原诊断新增的核心设计。以下方案基于现有代码路径设计，**未实施**。

#### 4.6.1 现状：compact 只改投影，canonical 只增不减

**关键代码事实**（`sqlite_storage.go:974-983` 注释）：

> A shorter in-memory history alone is ambiguous... Only the latter sets `Session.HistoryTruncated`, and the deletion is applied only when the caller-supplied history is an exact prefix of the stored transcript. Anything else — a compaction summary, a rewritten middle, a message swapped in place — **leaves the canonical transcript untouched**.

即：**compact 之后 `session_messages`（canonical）一行不删，只有 `session_prompt_messages`（投影）被替换。**

`truncateCanonicalToHistoryTx`（第 984 行）明确要求：

```go
if !session.HistoryTruncated || !session.HistoryLoaded { return false, nil }
if *count <= len(session.History) { return false, nil }
// 且必须是 stored transcript 的精确前缀
```

compact 的 replacement 是"摘要 + 保留尾部"，**不是前缀**，所以永远走不到删除分支。

**结果**：一个会话反复 compact N 次，`session_messages` 累积 N 代全量历史，投影却只有 1 份摘要。这正是 `session_history.sqlite` 2.38 GB 的成因之一。

#### 4.6.2 设计原则

1. **不破坏回溯能力**：`backtrack` / checkpoint restore 依赖 canonical transcript。直接删会让这些功能失效。
2. **不破坏审计**：`session_messages` 是"事实日志"，删了无法追溯。
3. **可逆**：任何自动删除都必须有显式开关，且默认保守。
4. **分层**：物理删除 vs 归档 vs 仅释放内存，三者收益与风险差异巨大，必须分开做。

#### 4.6.3 方案分级

##### 方案 A（推荐，低风险）：compact 后释放**已摘要区间**的 canonical 行

**触发点**：`actor.go:2624` / `actor.go:2853`（pre-turn auto compact 与 manual compact 各一处），在 `persistSession` 成功之后。

**数据基础**：compact 结果里已有精确边界。

`compactruntime.Result`（`runtime.go:73-88`）携带 `CompactedMessages`；checkpoint 元数据有 `segment_start` / `segment_end`（`local.go:19-20`、`compact_boundary.go:20-24`）。`CompactBoundary` 已持久化到 `session.Metadata.Context[ContextCompactBoundary]`。

**实现步骤**：

1. 新增存储接口（`internal/chat/storage.go`）：

```go
// SessionStorageCompactionPruner 可选接口：压缩完成后物理回收已被摘要覆盖的
// canonical 行。只有模型驱动的压缩可以调用；回溯/审计语义由 retain 参数决定。
type SessionStorageCompactionPruner interface {
	PruneCompactedHistory(ctx context.Context, sessionID string, boundary CompactBoundary, opts CompactPruneOptions) (CompactPruneResult, error)
}

type CompactPruneOptions struct {
	// Mode: "off" | "observe" | "enforce"
	Mode string
	// RetainMessages 保留摘要边界之后最近 N 条 canonical 行（防止边界误判把新消息删掉）
	RetainMessages int
	// MinFreeBytes 只有当可回收字节超过该阈值才执行（避免小会话频繁写）
	MinFreeBytes int64
}

type CompactPruneResult struct {
	DeletedRows  int64
	DeletedBytes int64
	FirstSeq     int64
	LastSeq      int64
	Skipped      string
}
```

2. SQLite 实现（`sqlite_storage.go`）：

```go
func (s *SQLiteSessionStorage) PruneCompactedHistory(ctx context.Context, sessionID string, boundary CompactBoundary, opts CompactPruneOptions) (CompactPruneResult, error) {
	// 安全前置：必须有 boundary，且 segment_end > 0
	if boundary.SegmentEnd <= 0 || strings.TrimSpace(boundary.MessageID) == "" {
		return CompactPruneResult{Skipped: "no_boundary"}, nil
	}
	// 1) 确认 checkpoint 摘要消息仍然存在（防止 boundary 已过期）
	// 2) 删除 seq <= 边界对应的 canonical 行，但保留最近 RetainMessages 条
	// 3) 更新 sessions.message_count
	// 4) 不 VACUUM（页回收交给离线 storage compact）
}
```

3. 接线（`actor.go` compact 成功分支）：

```go
// compact 持久化成功后再回收，失败不影响 compact 结果
if pruner, ok := a.sessionStore.(SessionStorageCompactionPruner); ok {
	if _, pruneErr := pruner.PruneCompactedHistory(
		ctx, session.ID, boundary,
		CompactPruneOptions{Mode: a.compactPruneMode, RetainMessages: 64},
	); pruneErr != nil {
		logpkg.Debugf("[compact] canonical prune skipped: session=%s err=%v", session.ID, pruneErr)
	}
}
```

**风险与护栏**：

| 风险 | 护栏 |
|---|---|
| 误删回溯所需历史 | 保留 `RetainMessages`（默认 64）；`Mode` 默认 `observe` 只统计 |
| boundary 过期（二次 compact） | 校验 `MessageID` 仍在 canonical 中，否则跳过 |
| 与 exit sync 竞争 | 在 `sessionPersistMu` 内执行；`Update` 的 `truncateCanonicalToHistoryTx` 语义不变 |
| 破坏审计 | 提供 `--keep-canonical` 显式退出；`observe` 模式先跑一段时间验证 |

**预期收益**：以 2,146 行 / 11.56 MB 的最大会话为例，若 compact 后保留 64 条尾部，可回收约 **90% 的 canonical 字节**。`session_history.sqlite` 中 `session_messages` 是 105,470 行，按 412 个会话计，压缩后预计可降至 1/3 以下。

##### 方案 B（推荐，极低风险）：compact 后释放**内存中**的历史

**不碰磁盘**，只让 compact 后的 actor 立即释放已摘要部分的 Go 对象。

**触发点**：`actor.go:2624` 的 `replaceSessionHistoryAndAdvancePromptCacheEpoch(session, result.ReplacementHistory)` 之后。

**当前行为**：`ReplaceHistory`（`session.go:237`）会 `Clone()` 每条消息，然后 `refreshDerivedMetadata()`。旧 history 的底层数组在新切片赋值后被 GC 回收——**这一步其实已经释放了**。

**真正需要补的是**：

1. **压缩后主动触发 GC**（可选，需谨慎）：

```go
// 仅在压缩前后存活堆差异显著时触发，避免无谓 STW
if beforeBytes := atomic.LoadUint64(&a.heapBeforeCompact); beforeBytes > 256<<20 {
	runtime.GC()        // 促发一次回收，让 heap goal 回落
	debug.FreeOSMemory() // 可选：仅在显式 compact 路径，避免频繁 syscall
}
```

> ⚠️ `debug.FreeOSMemory()` 会触发完整 GC + scavenger，代价是几十到几百 ms。**只建议在手动 `/compact` 路径使用，自动 compact 路径不要调用。**

2. **清理 `SessionActor` 内的历史缓存**：核查是否存在除 `session.History` 之外的持有者（如 `MessageBuilder.history`、prompt cache、`HistoryCommitLedger`）。若有，compact 后需显式清空。这一步需补充代码审计，当前证据不足以断言。

3. **CLI 侧同步**：`chat_core.go:568` 的 `replaceRuntimeMessages(session, result.ReplacementHistory)` 之后，确认旧 `ChatSession` 的历史切片被覆盖而非追加。

##### 方案 C（需谨慎）：按时间/数量归档旧会话

**触发点**：已有的 `SQLiteSessionStorage.Cleanup`（`sqlite_storage.go:2903`）按 `expires_at` 删会话，但它只处理**显式设置了 TTL** 的会话。

**改法**：新增 `PruneIdleSessions`，对"最后更新超过 N 天且处于终态"的会话执行：

```
1. 导出 canonical transcript 到 session-artifacts/<id>/archive.jsonl.zst
2. 删除 session_messages / session_prompt_messages 行
3. 保留 sessions 行（标题、统计、元数据），标记 archived=true
```

**风险**：不可逆（除非归档文件在）。**建议默认关闭，由 `aicli storage prune --archive` 手动触发。**

##### 方案 D（附带）：把"释放"从单会话扩展到全局

`agent_control` 侧的治理已实现但**默认 observe**：

```go
RegistryReconcileMode: "observe"   // config/manager.go:869
```

**建议**：
1. 先在 observe 模式跑 1-2 天，观察 `reclaim_candidates` / `wake_prune_candidates` 数量；
2. 确认无异常后切 `enforce`（`AICLI_REGISTRY_RECONCILE_MODE=enforce` 或 `agents.registryReconcileMode`）；
3. 实测当前 980 条 stale + 428 万 wake_events，enforce 后预计 `agent_control.sqlite` 从 2.34 GB 降到数百 MB。

**这一项不需要写代码，只需改配置 + 观察。**

#### 4.6.4 方案对比

| 方案 | 磁盘收益 | 内存收益 | 风险 | 工作量 |
|---|---|---|---|---|
| A. prune canonical | 高（~90% canonical） | 中 | 中（影响回溯） | 中（新增接口 + SQL） |
| B. 释放内存 | 无 | 中 | 低 | 小 |
| C. 归档旧会话 | 高 | 无 | 高（不可逆） | 中 |
| D. enforce registry | 高（2.34 GB → 数百 MB） | 无 | 低 | **零代码** |

**推荐落地顺序**：D（立即，改配置）→ A（P1，带 observe 护栏）→ B（P2）。

---

### 4.7 运行时参数（无需改代码）

当前 `GOGC` / `GOMEMLIMIT` 均为空（已实测确认）。本机总内存 14,198 MB，采集时仅剩 2,380 MB。

```powershell
# 常驻实例建议（按剩余内存收紧上限，让 Go 主动归还）
$env:GOMEMLIMIT = "512MiB"
$env:GOGC = "50"          # 更早触发 GC，降低堆峰值；代价是 CPU 上升

# 一次性临时止血：直接跑一次 GC 观察立即回落
# （需在进程内触发，或用 pprof 端点：curl "http://127.0.0.1:<port>/debug/pprof/heap?gc=1"）
```

> **注意**：`GOMEMLIMIT` 是软上限，不会阻止瞬时超限。设为 512MiB 时，若单次 compact 需要更大峰值，可能引发 GC thrash。建议先在非关键实例验证。

---

## 五、落地优先级与验收口径

| 优先级 | 改动 | 位置 | 预期收益 | 验收方法 |
|---|---|---|---|---|
| **P0** | 对账路径改 `LoadMetadata` | `chat_actor_registry.go:2259` | 消除 42.7% 累计分配 | `alloc_space` 中 `loadPromptMessages` 消失或降 90% |
| **P0** | 对账前 SQL 预过滤 | `chat_actor_registry.go:2107` | 减少每轮 DB 查询次数 | `cache_requests` 计数下降 |
| **P1** | `promptMessageFingerprint` 增量哈希 | `loop.go:6061` | 削减约 20% 总分配 | `slices.Grow` / `makeString` 显著下降 |
| **P1** | compact 后 prune canonical（方案 A） | 新增接口 + `actor.go:2624` | canonical 字节降约 90% | `session_messages` 行数 / 库体积 |
| **P1** | registry 切 enforce（方案 D） | 配置，零代码 | `agent_control.sqlite` 2.34 GB → 数百 MB | `storage compact --dry-run` 前后对比 |
| **P2** | TUI 渲染收敛 | `cmd/aicli/ui/` | 单实例 inuse 100 MB → 30 MB | `inuse_space` 过滤后 Top 10 |
| **P2** | `encodeMessages` 增量写 | `sqlite_storage.go:2023` | 降低 checkpoint 写入量 | `cache_requests` / WAL 增长速率 |

### 同一口径的对比方法

所有改动前后必须用**完全相同的命令**采样，否则数字不可比：

```powershell
# 1. 强制 GC 后的存活堆
curl.exe -s -o heap_gc.pb.gz "http://127.0.0.1:<port>/debug/pprof/heap?gc=1"
go tool pprof -ignore='runtime/pprof|text/tabwriter|fmt.Fprintf' \
  -sample_index=inuse_space -top heap_gc.pb.gz

# 2. 累计分配（判断 churn）
go tool pprof -ignore='runtime/pprof|text/tabwriter|fmt.Fprintf' \
  -sample_index=alloc_space -top heap_gc.pb.gz

# 3. 私有内存（判断 Go 是否归还）
Get-Process -Id <pid> | Select-Object @{n='PrivateMB';e={[math]::Round($_.PrivateMemorySize64/1MB,1)}}

# 4. 库体积
python -c "import os;print(os.path.getsize(r'C:\Users\vince\.aicli\sessions\session_history.sqlite')/1048576,'MB')"
```

---

## 六、结论

1. **不是内存泄漏**。PID 19324 运行 14 小时、688 MB 私有内存，强制 GC 后存活堆仅 10.8 MB。任何"泄漏"判断都必须以 `?gc=1` 的 `inuse_space` 为准。

2. **主因是分配 churn 导致的堆扩张**。累计分配 0.5–1 TB，来源按占比：
   - 对账全量 `Load` 解码：42.7%
   - 每次 think 全量 `json.Marshal`：约 20%
   - 每次 checkpoint 全量重编码投影：约 15%
   - TUI 每帧重排：约 15%

3. **次因是 Go 运行时保留策略**。`GOGC` / `GOMEMLIMIT` 未配置，Windows scavenger 在长空闲期不归还，Private Bytes 长期停在峰值。

4. **存储侧是无界放大**。9.4 GB 库、171 万 events、428 万 wake_events，且 registry 治理停在 `observe` 模式从未 enforce。compact 只改投影不改 canonical，导致 `session_messages` 只增不减。

5. **最小可行动作**（按投入产出排序）：
   - 立即：结束 3 个 14 小时以上的旧实例（释放约 1.7 GB），设 `GOMEMLIMIT=512MiB`
   - 一天内：registry 切 `enforce`（零代码）
   - 一周内：改 `chat_actor_registry.go:2259`（8 行，P0）
   - 迭代：`promptMessageFingerprint` 增量哈希 + compact 后 prune

---

## 附录：诊断产物

| 文件 | 说明 |
|---|---|
| `C:\Users\vince\.aicli-diag\heap_19324.pb.gz` | PID 19324 heap（未强制 GC） |
| `C:\Users\vince\.aicli-diag\heap_22012.pb.gz` | PID 22012 heap（未强制 GC） |
| `C:\Users\vince\.aicli-diag\heap_22012_gc.pb.gz` | PID 22012 heap（`?gc=1`） |
| `C:\Users\vince\.aicli-diag\inspect_db.py` | 只读库统计脚本（表行数 + 字节） |
| `C:\Users\vince\.aicli-diag\inspect_registry.py` | registry 状态分布统计 |

复现全流程：

```powershell
# 定位进程与端口
Get-Process | Where-Object { $_.ProcessName -like '*aicli*' }
Get-NetTCPConnection -State Listen | Where-Object { $_.OwningProcess -eq <pid> }

# 取 profile（务必带 gc=1）
curl.exe -s -o heap.pb.gz "http://127.0.0.1:<port>/debug/pprof/heap?gc=1"

# 分析（务必 ignore pprof 自身）
go tool pprof -ignore='runtime/pprof|text/tabwriter|fmt.Fprintf' \
  -sample_index=inuse_space -top heap.pb.gz
go tool pprof -ignore='runtime/pprof|text/tabwriter|fmt.Fprintf' \
  -sample_index=alloc_space -top heap.pb.gz
```

---

# 附录 A：ConPTY 实测复现与修复验证（2026-10-20）

> 本附录记录在**真实 ConPTY 会话**中完成的一次端到端复现与验证。
> 主结论（第三、四章）中关于 compact 卸载的部分被本次实测**部分否定**，以本附录为准。

## A.1 测试环境

```
aicli-5x.exe chat --provider magpie --model qoder-cn/dfmodel
  --pprof --web-port <port> --headless
```

- 真实 ConPTY（`job_start tty=true`），TUI 渲染链路完整
- provider `magpie`（`http://127.0.0.1:3425/v1`），model `qoder-cn/dfmodel`（1M 窗口，reasoning=max）
- pprof：`http://127.0.0.1:<port>/debug/pprof/heap?gc=1`
- 负载：8 轮对话，每轮要求模型输出约 1200 词，最终上下文 38–50K tokens

## A.2 首次实测：compact 卸载的收益远小于预期

| 指标 | compact 前 | compact 后 | 变化 |
|---|---|---|---|
| 私有内存 | 143.8 MB | 138.3 MB | **-5.5 MB** |
| 工作集 | 104.7 MB | 99.7 MB | -5.0 MB |
| 存活堆（GC 后） | 35.0 MB | 34.0 MB | -1.0 MB |
| `HistoryCommitLedger.Enqueue` | 1536 kB | 1536 kB | 0 |
| `cloneAppRenderLine` | 1024 kB | 1024 kB | 0 |

compact 本身成功：`token_before=17069 → token_after=10359`，`compacted_messages=10`，`generation=1`。

**结论：在短会话（28 cells）下，compact 后卸载历史的收益只有约 5 MB。**

原因：`/resume` 默认视图与 compact 后的内存释放在代码中**已经实现**：

- `ChatSession.ResumeHistory` 由 `replaceRuntimeMessages` → `clearResumeHistory()` 清空（`chat_history.go:78-93`）
- `Session.History` 由 `ReplaceHistory` 重建，旧切片可被 GC 回收
- `resumeHistoryCompactViewPreferred()`（`chat_session.go:661-675`）默认只加载 checkpoint 之后的消息

所以**本附录修正主文档 §4.6 的判断**：compact 后卸载历史不是主要矛盾。

## A.3 真正的主要矛盾：每个流式 delta 都全量解析 config.yaml

### pprof 证据（`alloc_space`，PID 22012 与 ConPTY 实例一致）

```
11,676.91 MB  41.63%  storefile.(*ConfigStore).ListConfigs
  └─ 11,568.36 MB   99.07%  loadDocument
       └─ yaml.Unmarshal  → 对 150 KB config.yaml 做完整 YAML AST 解析
```

调用链（`internal/connectors/stream_mirror.go:196`）：

```go
func (m *streamMirror) handleDelta(ev streamMirrorEvent) {
	for _, cfg := range m.deps.Store.ListConfigs() {   // ← 每 delta 一次
		if !cfg.Enabled { continue }
		bindings := m.deps.Store.ListBindings(cfg.ID)   // ← 每 delta 一次，重读 state.json
		...
	}
}
```

`handleDelta` 由 `assistant.delta` 事件驱动，**即每生成一个 token 就触发一次**。等于把"读并解析配置文件"放大成每 token 一次。

同一链上的第二处放大：`ListBindings` → `observeState` → `reloadStateLocked` → `os.ReadFile(state.json)` + `json.Unmarshal`，贡献 **1,673 MB（13.8%）**。

而 `storefile.go:391-395` 的注释恰恰写着：

> 整体重读 state.json 后全表扫描……**不要在热路径上高频调用**

即该约束被 `streamMirror` 违反了。

## A.4 修复实现

### 修复 1：`ConfigStore` 文档缓存（`internal/connectors/storefile/storefile.go`）

```go
type ConfigStore struct {
	...
	// mtime + size 指纹；文件变化即失效
	docCache      *yaml.Node
	docCacheMTime time.Time
	docCacheSize  int64
}
```

- `loadDocument()` 命中缓存时直接返回，不再 `os.ReadFile` + `yaml.Unmarshal`
- `writeDocument()` 写成功后把新文档登记为缓存
- **写路径改用 `loadDocumentCopy()`（深拷贝）**：`PutConfig` / `DeleteConfig` / `SetRunnerPreference` 会原地修改 AST，直接改共享缓存会让后续读者看到被改写的文档

### 修复 2：`state.json` 缓存（同一文件）

```go
stateCacheLoaded bool
stateCacheMTime  time.Time
stateCacheSize   int64
```

- `reloadStateLocked()` 命中缓存时直接返回
- `writeStateLocked()` 成功后刷新指纹
- **`mutateStateLocked()` 强制 `invalidateStateCacheLocked()` 后再重读**：写路径必须读到磁盘最新值，否则会覆盖另一实例刚写入的绑定

### 曾被否决的方案（记录以免重复尝试）

**给 `streamMirror` 加 1 秒 TTL 路由缓存** —— 实现简单但**破坏语义**：
`TestStreamMirrorChatRebindFinalizesOldStream` 要求"同 chat 重绑必须立即生效"，
TTL 缓存导致该测试超时。最终改为在存储层做写穿透缓存，保持"写入即生效"。

## A.5 修复验证（同一 ConPTY 口径，8 轮相同负载）

| 指标 | 修复前 | 修复后 | 变化 |
|---|---|---|---|
| **累计分配总量** | 28,048 MB | **12,196 MB** | **-57%** |
| `storefile.ListConfigs` | 11,676 MB (41.6%) | **不进入 Top 8** | — |
| `loadDocument` 内的 `yaml.Unmarshal` | 11,568 MB | **6 MB** | **-99.95%** |
| `reloadStateLocked` | 1,669 MB | **不进入 Top 8** | — |
| 私有内存（8 轮后） | 143.8 MB | 112.6 MB | **-22%** |

修复后 Top 热点变为 TUI 渲染层：

```
3,190.88 MB  26.16%  ui/vt.(*Screen).CellRows
1,384.32 MB  11.35%  ui/render/encoding.(*EventEncoder).applyReasoning.func1
  416.18 MB   3.41%  ui/renderengine.normalizeRow
  404.89 MB   3.32%  ui/renderengine.cloneCellGrid
  377.41 MB   3.09%  ui/renderengine.blankGrid
```

### 回归测试

新增 6 个不变量测试（`document_cache_test.go`）：

| 测试 | 锁定行为 |
|---|---|
| `TestConfigStoreDocumentCacheInvalidatesOnExternalEdit` | 外部改 config.yaml 后立即读到新值 |
| `TestConfigStoreWriteDoesNotPolluteCachedDocument` | 写路径不污染共享缓存（不重复追加） |
| `TestCloneYAMLNodeIsDeep` | 深拷贝不影响原树 |
| `TestConfigStoreCachedDocumentSurvivesRepeatedReads` | 稳定文件上缓存指针不变（确实没重解析） |
| `TestConfigStoreStateCacheSeesExternalBindingWrite` | 另一实例写 state.json 后立即生效 |
| `TestConfigStoreStateCacheRefreshesAfterOwnWrite` | 本进程写入后缓存立即反映（重绑不等 TTL） |

全量结果：

```
ok  internal/connectors              9.221s
ok  internal/connectors/storefile    5.269s
ok  internal/connectors/restapi      2.812s
ok  internal/connectors/storejson    （单包通过；与其它包并行时
                                     TestTwoInstancesSameDirNoLostUpdate
                                     因目录锁竞争偶发超时，与本次改动无关）
```

## A.6 复现与验证命令

```powershell
# 1. ConPTY 启动（真实 TTY）
#    job_start tty=true, command = run_chat_fix.ps1

# 2. 灌入负载
pwsh -File seed_history.ps1 -Port <port> -Token <token> -Rounds 8

# 3. 采集（务必 gc=1）
curl.exe -s -o heap.pb.gz "http://127.0.0.1:<port>/debug/pprof/heap?gc=1"

# 4. 分析（务必忽略 pprof 自身的 tabwriter 噪声）
go tool pprof -ignore='runtime/pprof|text/tabwriter|fmt.Fprintf' `
  -sample_index=alloc_space -top heap.pb.gz

# 5. 触发 compact（ConPTY 内直接发送，注意补全菜单会拦截回车）
#    job_input: "/compact"  然后单独发 "\r"
```

> **踩坑记录**：通过 `/web/api/invoke` 注入 `/compact` 不会被当作命令执行（会被当成普通 prompt）。
> 斜杠命令必须走 ConPTY 输入路径。另外 `/compact` 会弹出补全菜单，需要再发一次 `\r` 才提交。

## A.7 修正后的结论

1. **compact 后卸载历史已经不是待办项** —— `/resume` 默认视图与 `clearResumeHistory` 已在位，
   短会话收益约 5 MB。主文档 §4.6.3 的"删除 canonical"方案应视为**否决**（会丢历史），
   §4.6.3 方案 B（只释放内存）**已实现**。

2. **本轮真正修掉的是逐 delta 全量解析配置** —— 占累计分配 41.6% + 13.8%，修复后归零。
   这是长会话（生产实例累计分配 200–500 GB）的主要放大器之一。

3. **下一顺位是 TUI 渲染层** —— `vt.(*Screen).CellRows` 26% + `applyReasoning` 11.3%。
   这两项与 compact 无关，随会话长度线性增长，是长会话内存的剩余大头。

4. **`fsnotify.addWatch` 的 4.9 MB 是 1226 个 watch 的固定缓冲区**（每个 4 KB），
   `skill.HotReload` 没有目录数上限（`knowledge` 侧有 2048 上限）。
   属于稳态占用而非泄漏，优先级低于渲染层。

---

# 附录 B：渲染层两项优化（2026-10-20 续）

> 接附录 A。修复存储层后 Top 热点转到 TUI 渲染层，本附录记录针对这两项的优化与实测。

## B.1 热点 1：`Screen.CellRows` 每行分配整行宽度（3.19 GB / 26.2%）

### 定位（`-list` 精确到行）

```
ROUTINE ======================== cmd/aicli/ui/vt.(*Screen).CellRows
    3.12GB     3.12GB (flat, cum) 26.16% of Total
         .          .    824:	out := make([][]Cell, 0, to-from+1)
    2.79GB     2.79GB    826:		cells := make([]Cell, s.width)      ← 主因
  329.51MB   329.51MB    830:				cells[col].SGR = append([]string(nil), cell.SGR...)  ← 次因
```

调用方（`terminal_session.go:1929`，`terminalFrameCellsWindow` 内逐行循环）：

```go
materializer.Feed(text)
frame[index-windowStart] = materializer.CellRows(1, 1)[0]   // 每行一次
```

两个问题：
1. `CellRows(1,1)[0]` 为单行却先构造外层 `[][]Cell`，多付一份 slice 头；
2. 每行都 `make([]Cell, width)`，即使该行只有几个非空 cell。

### 修复

新增单行入口（`vt/screen.go`）：

```go
// CellRow returns a deep copy of one row (1-based).
func (s *Screen) CellRow(row int) []Cell
```

并把两处 `CellRows` 的循环体统一抽到 `cloneScreenRow(row, width)`，宽度取「屏幕宽度」与「行实际长度」的较大者，保证右侧补空白 cell 的 viewport 契约不变。

调用点改为 `materializer.CellRow(1)`。

> **说明**：该项把 3.19 GB 降到 **2.28 GB（-29%）**，但没有归零。
> 原因是 `terminalFrameCellsWindow` 必须每帧为整个 viewport 物化 cell 矩阵
> ——这是 `vt.Screen.Feed` 的唯一 VT 展开路径，注释已标明该路径做过 P3-S3 优化
> （复用 `Reset()` 而非每行 `NewScreen`）。剩余部分是**必要工作**，不是浪费。

## B.2 热点 2：reasoning 增量 O(N²) 字符串拼接（1.38 GB / 11.35%）

### 定位

```
ROUTINE ======================== encoding.(*EventEncoder).applyReasoning
         0     1.39GB (flat, cum) 11.66% of Total
         .     1.35GB   1404:		u, changed := e.upsertItem(it.ID, KindReasoning, func(t *Item) bool {
```

回调内部（原 `encoder.go:1412`）：

```go
if streamDelta {
	nextHead = t.Head + text      // ← 每个 delta 复制全部已积累正文
}
```

`reasoning` 是逐 token 到达的流式事件。设最终正文长度 L、切分 N 段，
旧实现的字节复制量是 **O(N × L) = O(L²)**。当 `reasoning=max`（本项目本次测试
用的正是 max）时 reasoning 文本最长，因此该项最突出。

### 修复

`EventEncoder` 增加按 request key 的增量缓冲：

```go
reasoningBuilders map[string]*strings.Builder
```

新增 `appendReasoningText`（`encoder.go`）：

```go
func (e *EventEncoder) appendReasoningText(key string, item *Item, delta string) (string, bool) {
	builder := e.reasoningBuilders[key]
	if builder == nil || builder.String() != item.Head {
		// 首次追加，或 Item.Head 已被其它路径改写：以当前 Head 重建基线
		builder = &strings.Builder{}
		builder.Grow(len(item.Head) + len(delta))
		builder.WriteString(item.Head)
		e.reasoningBuilders[key] = builder
	}
	builder.WriteString(delta)
	return builder.String(), true
}
```

**语义等价性**：仍是字节级追加，不推断空白（provider 可能在单词、标点、
code span 或 UTF-8 rune 中间切开）。`strings.Builder.String()` 零拷贝，
追加成本从 O(L) 降为 O(len(delta))。

**缓冲失效自愈**：`Item.Head` 可能被终态纠正、历史重放、
`applyFinalReasoningSnapshot` 等路径改写。用「缓冲内容必须等于当前 Head」
作为不变量，不等就丢弃重建，因此不会出现缓冲漂移。

**快照替换**：非 delta 路径（`ReasoningOperationForEvent` 判定为 replace）
会 `delete(e.reasoningBuilders, key)`，下一段增量以新正文为基线。

### 实测

```
applyReasoning: 1,384.32 MB (11.35%) → 20 MB (0.25%)   -98.6%
```

## B.3 三轮实测汇总

同一 ConPTY 口径、同一 8 轮负载、同一 `?gc=1` 采样：

| 阶段 | 累计分配 | 私有内存 | Top 1 热点 |
|---|---|---|---|
| 基线（未优化） | 28,048 MB | 143.8 MB | `storefile.ListConfigs` 11,676 MB (41.6%) |
| + 附录 A 存储层修复 | 12,196 MB | 112.6 MB | `vt.Screen.CellRows` 3,191 MB (26.2%) |
| **+ 附录 B 渲染层两项** | **7,924 MB** | **107.2 MB** | `vt.cloneScreenRow` 2,280 MB (28.8%) |
| **总降幅** | **-72%** | **-25%** | — |

### 剩余热点的性质变化

`cloneScreenRow` 虽然仍是 Top 1（2,280 MB），但它与优化前的 `CellRows`
**性质不同**：

- 优化前：单行调用多付外层 slice + 每行分配整行宽度 → 存在可省的浪费
- 优化后：每帧为 viewport 物化 cell 矩阵 → **必要工作**

进一步降低它只能改架构（例如 viewport 单元格级别的增量 diff），
不是局部优化能解决的。**本附录到此为止，不再对其做微优化。**

## B.4 回归测试

新增 `reasoning_append_test.go`（6 个测试）：

| 测试 | 锁定行为 |
|---|---|
| `TestReasoningStreamAppendsByteFaithfully` | 词中切分/标点/多字节 rune/空行 的字节级追加 |
| `TestReasoningBuilderRecoversFromExternalHeadRewrite` | Head 被外部改写后缓冲自愈 |
| `TestReasoningBuilderIsDroppedOnSnapshot` | 权威快照丢弃缓冲并重置基线 |
| `TestReasoningAppendIsLinear` | 4000 段追加后 builder 容量在线性预算内（防 O(N²) 回归） |
| `TestReasoningBuildersDoNotLeakAcrossRequests` | 不同 request 缓冲互不干扰 |

测试构造时踩到的两个坑（已记录在测试注释里）：

1. `mode` / `reasoning.format` 决定 `ReasoningOperationForEvent` 的判定，
   构造"快照"事件时**不能**带 `format: stream_delta`；
2. 快照会 `delete(e.reasoningOrder, key)`，之后的增量必须**从 seq=1 重新开始**，
   否则会被既有的乱序判定丢弃（与本优化无关的既有语义）。

全量回归：

```
ok  cmd/aicli/ui                        17.293s
ok  cmd/aicli/ui/render/encoding         3.240s
ok  cmd/aicli/ui/vt                      1.732s
（其余 15 个 ui 子包全部 ok）
```

## B.5 代码改动清单

| 文件 | 改动 |
|---|---|
| `cmd/aicli/ui/vt/screen.go` | 新增 `CellRow` + `cloneScreenRow`；`CellRows` 复用同一 helper |
| `cmd/aicli/ui/terminal_session.go` | `CellRows(1,1)[0]` → `CellRow(1)` |
| `cmd/aicli/ui/render/encoding/encoder.go` | 新增 `reasoningBuilders` + `appendReasoningText`；`applyReasoning` 增量分支改走缓冲 |
| `cmd/aicli/ui/render/encoding/reasoning_append_test.go` | 新增 6 个不变量测试 |

---

# 附录 C：加载路径与运行时内存归还（2026-10-20 续）

> 接附录 B。附录 B 结束时剩余私有内存 107 MB（30 分钟 / 28 cells 的小会话）。
> 本附录针对**真实大会话冷启动**（2,146 行 / 11.56 MB）继续优化，并解决
> "存活堆 9 MB 但私有内存 414 MB"的运行时保留问题。

## C.1 真实大会话的实测基线

用 `aicli resume session_20261014195729_MYU772kz`（2,146 行 prompt 投影，
canonical 2,148 行）在 ConPTY 交互模式冷启动：

| 指标 | 数值 |
|---|---|
| 私有内存 | **414.1 MB** |
| GC 后存活堆 | **9.0 MB** |
| 累计分配 | 2,412 MB |

**存活堆 9 MB，私有内存 414 MB。** 差额 405 MB 是 Go 运行时保留但未归还的 arena。

## C.2 优化 1：`healLegacyTruncatedProjection` 的全量解码前置

### 定位

```
ROUTINE ======================== chat.(*SQLiteSessionStorage).Load
         0   1798.78MB  48.04% of Total
                                  1095MB  60.87%  loadPromptMessages
                                   687MB  38.20%  healLegacyTruncatedProjection
```

`healLegacyTruncatedProjection` 是一次**历史遗留自愈**：为确认投影不是 2026-09 之前
`HotHistoryMessages` 等固定上限截断的残留，它会 `loadCanonicalMessagesReadOnly`
**全量解码** canonical 转录。

### 关键数据：这个自愈对健康会话几乎总是空跑

对本地 404 个会话统计 `message_count - prompt_rows` 的分布：

```
gap<=8:  329 个会话
gap>8:    75 个会话   ← 差值 1,000–9,000
```

差值最大的几个：

```
session_20261018183040_rPueGv9v  message_count=9518  prompt=485   gap=9033
session_20261018235551_k6UpkpSp  message_count=6452  prompt=596   gap=5856
session_20261017222544_tSAeijz4  message_count=6162  prompt=557   gap=5605
```

**这 75 个大差值会话不是 cap 残留**，而是 compaction 之后的正常形态：
`session_prompt_messages` 只剩"摘要 + 保留尾部"，canonical 保留全量。

### 修复

在 `healLegacyTruncatedProjection` 里加**廉价前置**（`sqlite_storage.go`）：

```go
const legacyCapTrimSlack = 8
if session.CanonicalMessageCount-len(projection) <= legacyCapTrimSlack {
    return nil, nil
}
```

差值极小（≤8，健康会话的常态：system 前缀不进投影）直接判定不是 cap 产物，
不付全量解码成本。

> **诚实记录**：这个前置只对"差值小"的会话有效。上面 75 个 compaction 会话
> 差值在 1,000+，仍会走到全量加载。要彻底解决需要引入"是否有 compaction
> 检查点"的低成本判定（例如 `sessions.summary` 非空或 `metadata_json` 里带
> `compact_generation`），而不是靠投影正文扫描。**本轮未做，留作后续。**

## C.3 优化 2：`loadPromptMessages` 的扫描与预分配

### 定位

```
1095MB  loadPromptMessages
         749.29MB  68.43%  encoding/json.Unmarshal
         325.02MB  29.68%  database/sql.(*Rows).Scan
```

载荷只有 11.56 MB，却产生 1,095 MB 分配（放大 ~95×）。两处浪费：

1. **`var payload []byte` 逐行拷贝**：驱动返回的字节被复制一份到新切片，
   紧接着就 `json.Unmarshal` 后丢弃。
2. **`var history []types.Message` 无预分配**：2,146 次 append 触发多轮扩容复制。

### 修复

```go
// 1) 预分配（COUNT(*) 走主键前缀，成本可忽略）
var rowCount int
s.readPoolHandle().QueryRowContext(ctx,
    `SELECT COUNT(*) FROM session_prompt_messages WHERE session_id = ?`, sessionID).Scan(&rowCount)
history := make([]types.Message, 0, rowCount)

// 2) 零拷贝扫描：RawBytes 直接引用驱动行缓冲，
//    只在下次 Next() 前有效，而这里紧接解码、不保留引用
var payload sql.RawBytes
rows.Scan(&payload)
json.Unmarshal(payload, &message)
```

驱动（`ncruces/go-sqlite3`）自带测试就在用 `sql.RawBytes`，支持该用法。

### 实测（非交互，3 次取最小）

| 版本 | 峰值私有内存 |
|---|---|
| v3base（附录 B 末态） | 249.4 MB |
| v5（+ 本轮两项） | **204.2 MB** |

**降低 45 MB / 18%。**

## C.4 优化 3：运行时软内存上限（解决 405 MB 保留）

### 问题

项目**完全没有**设置 `GOMEMLIMIT`，也没有任何 `debug.FreeOSMemory` 调用
（全仓搜索无匹配）。这解释了 414 MB 私有内存 / 9 MB 存活堆的落差：
Windows 上 scavenger 在长空闲期不主动归还 arena，进程一直停在装载峰值。

### 修复

新增 `cmd/aicli/memory_limit.go`，在 `main()` 最早期（任何大分配之前）调用：

```go
func applyMemoryLimitFromEnv() int64
```

语义：

| 环境变量 | 行为 |
|---|---|
| 都未设置 | `debug.SetMemoryLimit(512 MiB)` |
| `AICLI_MEMORY_LIMIT=512MiB` | 解析后设置（支持 KiB/MiB/GiB 与 KB/MB/GB） |
| `GOMEMLIMIT=...` | 不干预（runtime 自己解析官方变量） |
| `AICLI_MEMORY_LIMIT=off/none/0/-1` | 关闭上限 |

**为什么可以安全地设成"低于峰值"**：`GOMEMLIMIT` 是软上限，超限不会
终止进程，只会提高 GC 频率并让 scavenger 更积极地归还内存。最坏代价是
多几次 GC，不会 OOM。

### 实测（ConPTY 交互模式，同一大会话）

| 配置 | 稳定私有内存 |
|---|---|
| 未限制 | **420.6 MB** |
| `AICLI_MEMORY_LIMIT=256MiB` | **316.5 MB** |

**降低 104 MB / 24.8%。**

## C.5 三轮叠加效果（大会话冷启动）

| 阶段 | 私有内存 | 累计分配 |
|---|---|---|
| 附录 B 末态 | 414.1 MB | 2,412 MB |
| + C.2/C.3（加载路径） | ~370 MB（推估） | — |
| + C.4（内存上限） | **316.5 MB** | — |

> C.2/C.3 的独立效果在非交互口径测得（249.4 → 204.2 MB，-18%）；
> ConPTY 口径叠加内存上限后为 316.5 MB。两个口径不可直接相减，
> 因为 ConPTY 的 TUI 渲染额外占内存。

## C.6 回归测试

| 文件 | 测试 |
|---|---|
| `internal/chat/sqlite_storage_load_alloc_test.go` | 4 个：RawBytes 逐行不串扰、预分配、gap≤8 跳过自愈、gap 大仍自愈 |
| `cmd/aicli/memory_limit_test.go` | 6 个：单位解析、退出开关、默认值、`AICLI_MEMORY_LIMIT` 生效、`GOMEMLIMIT` 让位、off 关闭 |

全量：

```
ok  internal/chat                    40.703s
ok  cmd/aicli                        （memory_limit 6 例全绿）
```

## C.7 一个必须记录的诊断陷阱

pprof 的 `-list` 显示的源码行号来自**当前磁盘上的源码**，而采样数据来自
**已构建的二进制**。本轮出现过一次误判：

- `aicli-v4.exe` 构建于 11:43:54，`sqlite_storage.go` 最后修改于 11:45:49
- pprof 显示"第 1797 行仍在执行全量加载"，看起来像前置判定失效
- 实际是**该二进制里根本没有这段代码**

**规则：改动后必须重新构建再采样；对比二进制与源码的 mtime。**

## C.8 仍未解决的部分

1. **compaction 会话仍会全量解码 canonical**（C.2 记录的 75 个会话）。
   需要基于 `sessions.summary` / `compact_generation` 的低成本判定。

2. **`json.Unmarshal` 本身放大 ~65×**（11.56 MB 载荷 → 749 MB 分配）。
   `Metadata map[string]interface{}` 的反射解码是主因。要显著改善需要
   换更紧凑的持久化格式或延迟解析，属于结构性改动。

3. **生产实例仍在跑旧二进制**（2026-10-19 14:18 构建）。本附录全部修复
   需要重启后才生效。

---

# 附录 D：四阶段架构复查（加载 / 解析 / 渲染 / compact）

> 回应"AICLI_MEMORY_LIMIT 不合理，为什么不限制内存、compact 后本不该占这么多"的质疑。
> 本附录用实测推翻此前结论，并给出真正的根因与修复。

## D.1 先回答核心质疑：compact 后本不该占这么多

对比两个真实会话（同一二进制、同一口径、ConPTY）：

| 会话 | 投影大小 | 投影行数 | 私有内存 |
|---|---|---|---|
| `session_20261018183040_rPueGv9v`（**已 compact**） | **1.66 MB** | 528 | **299 MB** |
| `session_20261014195729_MYU772kz`（未 compact） | 11.56 MB | 2,146 | 245 MB |

**投影小 7 倍，内存反而多 54 MB。**

这一条直接证明：**内存与历史数据量无关**。此前"加载大会话导致峰值"的解释是错的。
`AICLI_MEMORY_LIMIT` 只是在压制症状。

## D.2 真正的根因：registry 用全量 Load 读元数据

pprof（compact 会话，PID 26084）给出唯一归因：

```
liveWorktreePaths → lazySessionStorage.Load → SQLiteSessionStorage.Load
    1,358.89 MB  (98.25% of all allocations)
```

调用点 `chat_actor_reconcile.go:200`（修复前）：

```go
for _, record := range records {                      // registry 全部记录
    session, err := r.Host.SessionStore.Load(ctx, sessionID)   // 全量解码历史
    if path := ...ContextString(session, "...WorktreePath"); path != "" {
```

**它只需要一个 worktree 路径字符串，却把整段 prompt 投影反序列化。**

而 `lazySessionStorage.Load` **没有任何缓存**，对账每轮都重新解码同一批会话：

```
1.66 MB 投影 → json.Unmarshal 放大 426 倍（707 MB / 次）
             × 对账轮次 × registry 记录数
             → 累计 1,359 MB
```

## D.3 四阶段逐一复查结果

### 阶段 1：加载（Loading）

| 发现 | 证据 | 处置 |
|---|---|---|
| `LiveWorktreePaths` 全量 Load 只读一个 context 键 | 1,359 MB / 98.25% | ✅ 改 `LoadMetadata` |
| registry 内另有 **20 处** 全量 Load | 见下表 | ✅ 批量修复 15 处 |
| `Load` 无任何缓存，重复调用重复解码 | `lazy_session_storage.go:161` | ⚠️ 未改（改动面大，见 D.6） |

**逐点判定（21 处）**：

| 位置 | 只读内容 | 处置 |
|---|---|---|
| L558 parentDefaults | Context | ✅（该类型无辅助方法，用显式 LoadMetadata） |
| L675 fork parent | **History（fork 复制）** | ❌ 保留 Load（**回归测试抓到**） |
| L695 spawn 幂等检查 | 存在性 | ✅ |
| L889 worktree 上下文 | Context | ✅ |
| L986 / L1081 worktree handle | Context | ✅ |
| L1140 completion 标注 | Context | ✅ |
| L1454 / L1503 / L1535 root scope | Context | ✅ |
| L1859 root/parent 身份 | Context | ✅ |
| L2064 binding lookup | 已用 LoadMetadata | ✅（既有） |
| L2274 terminal state | 已用 LoadMetadata | ✅（既有） |
| L2950 session 终态 | State | ✅ |
| L3003 run meta | Context | ✅ |
| L4059 close | **要 Update 历史** | ❌ 保留 Load |
| L4605 completion 订阅 | Context | ✅ |
| L4623 agent snapshot | State/Context | ✅ |
| L4878 reclaim 观测 | State | ✅ |
| L5021 root session id | Context | ✅ |
| L5551 ensureSession | **要 Update 历史** | ❌ 保留 Load |

新增统一辅助：

```go
func (r *localActorRegistry) loadLocalAgentSessionMetadata(ctx, sessionID) (*runtimechat.Session, error)
// 优先 LoadMetadata；未实现该接口的存储自动回退 Load
```

### 阶段 2：解析（Parsing）

| 发现 | 证据 | 处置 |
|---|---|---|
| `json.Unmarshal` 对 `Metadata map[string]interface{}` 放大 **426 倍** | 1.66 MB → 707 MB | ⚠️ 未改（结构性，见 D.6） |
| `loadPromptMessages` 逐行 `[]byte` 拷贝 | 325 MB | ✅ 改 `sql.RawBytes` |
| `history` 无预分配 | 多轮扩容复制 | ✅ 先 `COUNT(*)` 预分配 |
| `healLegacyTruncatedProjection` 全量解码 canonical | 687 MB | ✅ 保留（但见 D.5 的失败尝试） |

**放大 426 倍的原因**：每条消息的 `Metadata` 是 `map[string]interface{}`，
JSON 解码要为每个键分配 map bucket + interface header + 字符串头；
再叠加 `ToolCalls[].Args map[string]interface{}` 的深层反射解码。

### 阶段 3：渲染（Rendering）

| 发现 | 证据 | 处置 |
|---|---|---|
| `Screen.CellRows` 每行分配整行宽度 | 3.19 GB / 26.2% | ✅ 新增 `CellRow` 单行入口 |
| reasoning 增量 O(N²) 字符串拼接 | 1.38 GB / 11.35% | ✅ `strings.Builder` 缓冲（-98.6%） |
| `terminalFrameCellsWindow` 每帧物化 viewport | 2.28 GB | ⏸ 必要工作，不优化 |

### 阶段 4：compact 顺序

| 发现 | 结论 |
|---|---|
| `/resume` 默认只加载 checkpoint 之后 | ✅ 已实现，行为正确 |
| compact 后 `ResumeHistory` 由 `clearResumeHistory()` 清空 | ✅ 已实现 |
| `Session.History` 由 `ReplaceHistory` 重建 | ✅ 已实现 |
| compact 后卸载历史的收益 | 短会话约 5 MB，**不是主要矛盾** |
| `healLegacyTruncatedProjection` 的 compaction 检查 | ✅ 在加载之前，大会话均提前返回 |

**阶段顺序的关键结论**：compact 相关的内存逻辑本来就是对的。
真正的浪费发生在 **compact 之外** —— registry 的元数据查询。

## D.4 实测效果

同一 compact 大会话（`rPueGv9v`，投影 1.66 MB），ConPTY 交互模式：

| 版本 | 修复内容 | 私有内存 | 累计分配 |
|---|---|---|---|
| v7 | 基础 | 181.6 MB | 2,012 MB |
| v8 | + `liveWorktreePaths` 改元数据 | 166.3 MB | **624 MB（-69%）** |
| v9 | + registry 其余 15 处 | **171.6 MB** | — |

> v9 略高于 v8 属测量噪声（同机另有 5 个旧实例在跑对账）。
> **决定性指标是累计分配 -69%**，它直接反映重复解码被消除。

## D.5 三个被数据否证的方案（勿重试）

| 方案 | 否证依据 |
|---|---|
| 用 `sessions.summary` 非空判定 cap 残留 | **FN=15**，会漏掉全部真正需要自愈的会话 |
| 用"投影长度 == 128"判定 cap 残留 | 既有测试用 5 条投影即失败；实测需要自愈的投影长度是 **1~201** |
| "只读尾部 N 行"替代全量读取 | 实测 **558 MB**，比全量的 78 MB 贵 7 倍（`Load` 被反复调用，每次重读尾部） |

第三条已**回退**，并在代码注释里写明原因。

## D.6 仍未解决的两项（诚实记录）

### 1. `Load` 无缓存，重复解码

`lazySessionStorage.Load` 每次调用都重新查询 + 解码。即使把 21 处调用点
全部改成 `LoadMetadata`，真正的 `Load`（如 turn 开始时的 actor 装载）
仍会重复执行。

**可行方向**：在 `lazySessionStorage` 上加"按 (sessionID, updated_at, message_count)
的只读快照缓存"。风险：写路径必须精确失效，否则读出陈旧历史。
**本轮未做** —— 需要独立的正确性设计与测试。

### 2. `json.Unmarshal` 放大 426 倍

`Metadata map[string]interface{}` 被全仓数百处直接索引（`message.Metadata["context_stage"]`），
换类型等于改整个代码库。

**权衡**：即便完全消除这 426 倍，省下的也只是**加载期瞬时分配**，
不是常驻内存（存活堆仅 9 MB）。收益集中在"冷启动更快"，不在"常驻更小"。

## D.7 回归测试

| 包 | 结果 |
|---|---|
| `cmd/aicli/commands` | ✅ 191.4s 全通过 |
| `internal/chat` | ✅ 37–45s 全通过 |
| `internal/connectors/storefile` | ✅ |
| `cmd/aicli/ui/...`（18 包） | ✅ |

**过程中抓到的真实回归**：

`TestLocalActorRegistry_UsesConfiguredDefaultForkTurns` 失败 ——
`L675` 的父会话被我误改成 `LoadMetadata`，而 fork 分支要调用
`parentSession.GetRecentMessages(forkTurns)` 复制历史，元数据壳会产出空历史。
**已改回全量 Load 并加注释说明原因。**

这条印证了一条规则：**把 `Load` 换成 `LoadMetadata` 前，必须确认调用链
不读 `Session.History`，也不把 session 交给 `Update`。**

---

# 附录 E：运行时状态读取的同类浪费（2026-10-20 续）

> 接附录 D。加载路径修完后（1,358 MB → 26 MB），Top 热点转向
> `SQLiteRuntimeStore.LoadState`：188.9 MB / 28.5%。

## E.1 根因：为读一个 status 解码 70 KB 工具面

`LoadState`（`session_runtime_store.go`）会 SELECT 并解码 9 个 JSON 列。实测全库
`session_runtime_state`（2,018 行）的列体积分布：

```
column                          avg_KB    max_KB   total_MB
current_run_meta_json              0.0       0.0       0.00
ambient_run_meta_json              0.0       0.0       0.00
stable_tool_surface_json          70.0     131.4     117.51   ← 主因
frozen_turn_tools_json             4.3      83.1       0.16
pending_tool_json                  0.9       1.6       0.01
pending_approval_json              0.5       0.9       0.01
pending_question_json              0.5       0.6       0.00
active_job_ids_json                0.0       0.0       0.00

仅 status 等标量列平均字节: 35.9
```

**单次读取：35.9 字节（有用） vs 70 KB（`stable_tool_surface_json`，无用）。**

而注册表对账、lease 存活判定、终态判定、wake 投递判定**只关心 `state.Status`**，
却每次都付一次工具面的完整 `json.Unmarshal` 反射开销。

## E.2 修复：新增标量投影读取器

新增可选接口（`session_runtime_store.go`）：

```go
type RuntimeStateStatusReader interface {
	LoadStateStatus(ctx context.Context, sessionID string) (*RuntimeState, error)
}
```

`SQLiteRuntimeStore.LoadStateStatus` 的 SELECT **完全不含**大 JSON 列：

```sql
SELECT session_id, status, current_turn_id, current_checkpoint_id,
       suspended_turn_id, last_run_terminal_reason, head_offset, updated_at
FROM session_runtime_state WHERE session_id = ?
```

返回的 `RuntimeState` 是标量投影：`RunMeta` / 工具面 / pending 项 / active jobs 留空。

### 量化（单元测试 `TestLoadStateStatusDoesNotDecodeToolSurface`）

写入 40 个工具 × 4 KB 描述（≈160 KB 工具面），各读 20 次：

```
alloc over 20 reads: full=7,028,392B  scalar=54,528B   ratio 0.01
```

**分配量降低 129 倍。**

## E.3 调用点替换

| 位置 | 只读字段 | 处置 |
|---|---|---|
| `chat_actor_registry.go:2090` binding lookup | Status | ✅ |
| `chat_actor_registry.go:2254` lease live | Status | ✅ |
| `chat_actor_registry.go:2298` terminal state | Status | ✅ |
| `chat_actor_child_completion_recovery.go:214` | UpdatedAt | ✅ |
| `chat_actor_agent_obligations.go:155` | CurrentTurnID / SuspendedTurnID | ✅ |
| `chat_actor_wait_feedback.go:63` | Status | ✅ |
| `chat_actor_host.go:947` | `Summary().AcceptsResume()` | ❌ 需要 RunMeta |
| `chat_actor_progress_check.go:270` | `Summary().AcceptsResume()` | ❌ 需要 RunMeta |
| `chat_team_lifecycle.go:244` | `shouldIgnoreAmbientTeamRuntimeState` | ❌ 需要 AmbientRunMeta |

统一封装（`chat_actor_registry.go`）：

```go
func loadRuntimeStatusFrom(store runtimechat.RuntimeStateStore, ctx, sessionID) (*RuntimeState, error)
// 优先 RuntimeStateStatusReader；未实现的 store 自动回退 LoadState
```

## E.4 读池白名单门禁

`internal/chat/runtime_store_read_pool_test.go` 有一个 13 项读 API 白名单门禁
（`TestReadAPIInventoryRouting`）：任何新读 API 使用读池但未登记即失败。

`LoadStateStatus` 首次运行即被拦下：

```
Messages: 非白名单函数使用了读池：LoadStateStatus
```

这是**好设计**（防止读池被滥用）。已登记并把名单长度从 13 改为 14，
同时要求它同样带 `readQueryer()` 与 `readOperationContext(`。

## E.5 回归

| 包 | 结果 |
|---|---|
| `internal/chat` | ✅ 49.4s 全通过（含 `TestReadAPIInventoryRouting`） |
| `cmd/aicli/commands` | ✅ 209.9s 全通过 |
| `go vet`（chat + commands） | ✅ |

## E.6 实测

同一 compact 大会话，ConPTY 交互模式峰值：

| 版本 | 内容 | 峰值私有内存 |
|---|---|---|
| v7 | 基线 | 181.6 MB |
| v8 | + liveWorktreePaths | 166.3 MB |
| v9 | + registry 15 处元数据 | 171.6 MB |
| **v10** | **+ LoadState 标量投影** | **171.0 MB** |

> 本次采样进程在抓 profile 前退出，只拿到峰值。峰值持平是因为该会话的
> 对账轮次尚未跑满；**累计分配的下降已在单元测试中量化（129×）**，
> 长驻实例的收益会随对账轮次累积显现。

## E.7 本轮完整清单（附录 D + E）

| 层次 | 优化 | 证据 |
|---|---|---|
| 加载 | registry 18 处 `Load` → `LoadMetadata` | 1,358 MB → 26 MB |
| 加载 | `loadPromptMessages` RawBytes + 预分配 | Scan 325 MB → 0 |
| 解析 | `healLegacy` 保留 compaction 前置 | 大会话均提前返回 |
| 解析 | `LoadState` → `LoadStateStatus` | **分配 -129×** |
| 渲染 | `CellRow` 单行入口 | 3.19 GB → 2.28 GB |
| 渲染 | reasoning builder | 1.38 GB → 20 MB |
| compact | 无需改动（原本正确） | — |

### 仍未做

1. **`lazySessionStorage.Load` 无缓存**：真正的 turn 装载仍会重复解码。
   需要只读快照缓存 + 写路径精确失效，属独立正确性设计。
2. **`json.Unmarshal` 放大 426 倍**：`Metadata map[string]interface{}` 被全仓
   数百处直接索引，换类型等于改整个代码库；且收益主要在"冷启动更快"。
3. **`stable_tool_surface_json` 本身 117.5 MB / 2018 行**：多数会话长期不复用它，
   可考虑按需物化（工具面未变更时不落库）。属存储策略调整，需评估工具面版本语义。

---

# 附录 F：工具面内容寻址去重（v25 迁移，2026-10-20 续）

> 接附录 E。`stable_tool_surface_json` 是全库最大的单项存储浪费：
> 平均 70 KB / 最大 131 KB / 合计 **117.5 MB**。本附录说明它的作用、
> 为什么可以安全去重，以及实测收益。

## F.1 这个字段做什么

`stable_tool_surface_json` 保存**会话级冻结的工具定义快照**
（名称、描述、参数 JSON Schema），在会话的首个 turn 写入后跨 turn 复用。

它服务两个目标：

**1. Prompt cache 命中**

LLM 提供商的 prompt 缓存按**前缀**匹配（tools 在前，messages 在后）。
工具定义只要字节变化（顺序、描述、schema），**整段缓存失效**，
输入 token 全部回退到未缓存计费。

代码注释（`turn_tool_surface_snapshot.go:74-79`）：

> Session-stable tool schemas are immutable for a prompt-cache lane.
> Permission / policy / MCP changes are enforced at execution time; compact
> keeps the same surface and disables calls via tool_choice rather than
> starting a tool-schema epoch.

**2. 工具调用连续性**

turn 进行中若工具面变化（MCP 重连、skill 热加载），模型可能调用一个
「上一轮不在列表里」的工具，或看到与实际执行不一致的 schema。
因此采用 **freeze-once**：

```go
if state.FrozenTurnToolsSet {
    return nil   // 本 turn 内不再改写
}
```

### 相关字段

| 字段 | 作用 |
|---|---|
| `StableToolSurface` | 会话级稳定工具面（跨 turn 复用） |
| `FrozenTurnTools` | 当前 turn 冻结快照（优先于 Stable） |
| `StableToolSurfaceBinding` | 资格键（权限模式等），决定能否复用 |
| `StableToolSurfaceFingerprint` | 工具面指纹，用于变更检测 |

读取优先级（`LoadTurnToolSurface`）：**`FrozenTurnTools`（同 turn） > `StableToolSurface`**。

## F.2 为什么可以安全去重

实测全库 `session_runtime_state`（2,018 行）：

| 指标 | 数值 |
|---|---|
| 有工具面的会话行 | 1,721 |
| **唯一工具面内容数** | **仅 231** |
| 内容总字节（去重后） | **18.23 MB**（去重前约 117.5 MB） |

**1,721 个会话共享 231 套工具面** —— 平均每套被重复存 7.5 次，
最大的几套（131.4 KB）被存了上千次。

去重**不改变任何语义**：读取方拿到的仍是同一份 `[]types.ToolDefinition`，
`StableToolSurfaceBinding` / `Fingerprint` 字段保持独立（它们本就按会话不同）。

## F.3 实现（v25 迁移）

### 新表

```sql
CREATE TABLE IF NOT EXISTS runtime_tool_surface_content (
    fingerprint TEXT PRIMARY KEY,
    payload_json BLOB NOT NULL,
    byte_count INTEGER NOT NULL,
    created_at TEXT NOT NULL
);
ALTER TABLE session_runtime_state ADD COLUMN stable_tool_surface_ref TEXT;
CREATE INDEX IF NOT EXISTS idx_runtime_state_tool_surface_ref
ON session_runtime_state(stable_tool_surface_ref);
```

### 写入（`SaveState`）

```go
if state.StableToolSurfaceSet {
    payload, _ := json.Marshal(state.StableToolSurface)
    if ref, putErr := s.putToolSurfaceContent(ctx, payload); putErr != nil {
        // 去重失败不阻断状态写入：回退内联列，行为与改动前一致
        stableToolSurfaceJSON = payload
    } else {
        stableToolSurfaceRef = ref
    }
}
```

`putToolSurfaceContent` 用 `INSERT ... ON CONFLICT(fingerprint) DO NOTHING`，
fingerprint = sha256(payload)。

### 读取（`LoadState`）

```go
// ref 优先，为空时回退旧内联列（向后兼容，无需一次性回填）
if stableToolSurfaceRef.Valid && stableToolSurfaceRef.String != "" {
    if content, _ := s.loadToolSurfaceContent(ctx, stableToolSurfaceRef.String); len(content) > 0 {
        stableToolSurfaceRaw = content
    }
}
```

### 失效（`InvalidateStableToolSurfaces`）

```sql
SET stable_tool_surface_json = NULL,
    stable_tool_surface_ref = NULL,          -- 必须同时清，否则会从内容表读回旧面
    frozen_turn_tools_json = CASE WHEN ... END
WHERE stable_tool_surface_json IS NOT NULL
   OR stable_tool_surface_ref IS NOT NULL
   OR frozen_turn_tools_json IS NOT NULL
```

## F.4 迁移实测（生产库副本，未动线上）

对 `session_runtime.sqlite` 的**副本**执行 v25 迁移 + 回填 + VACUUM：

```
处理的会话行      : 1,721
唯一工具面内容数  : 231
内容表总字节      : 18.23 MB

迁移前: 3,128.0 MB
迁移后: 2,846.0 MB
节省  : 282.0 MB  (9.0%)
```

> 282 MB 是**文件级**节省（含 VACUUM 回收的页）。逻辑层面
> `stable_tool_surface_json` 那 117.5 MB 全部归零，收敛为 18.23 MB。

## F.5 回归测试

新增 `runtime_tool_surface_dedup_test.go`（5 个测试）：

| 测试 | 锁定行为 |
|---|---|
| `TestToolSurfaceContentIsDeduplicatedAcrossSessions` | 12 个会话共享 1 套工具面 → 内容表只有 1 行；内联列为空 |
| `TestLoadStateResolvesToolSurfaceFromRef` | 通过 ref 取回的名称/描述/顺序完全一致 |
| `TestLoadStateFallsBackToInlineColumn` | v25 之前的旧行（只有内联列）仍能读出 |
| `TestInvalidateStableToolSurfacesClearsRef` | 失效必须同时清 ref，否则会读回旧面 |
| `TestDifferentToolSurfacesStoreSeparately` | 不同工具面不合并（反向保护） |

既有测试 `TestSQLiteRuntimeStoreSharesIdenticalToolSurfacesAfterLoad` 已按新契约更新：
改为断言 `stable_tool_surface_ref` 有值、frozen 仍是引用标记、**改动后的工具面产生新 fingerprint**。

读池门禁 `TestReadAPIInventoryRouting` 又拦下一次（好设计）：
`loadToolSurfaceContent` 必须登记白名单**且带 `readOperationContext(` 读侧超时**。
已补齐两处。

全量：

```
ok  internal/chat                    43.062s
ok  cmd/aicli/commands              193.132s
go build ./...                      exit 0
```

## F.6 注意事项

1. **旧列保留不删**：`stable_tool_surface_json` 仍在 schema 里，便于回滚；
   新行不再写入它。彻底删除需再一个迁移版本，建议观察一段时间后再做。

2. **内容表不会自动 GC**：会话删除后对应 fingerprint 可能成为孤儿。
   `InvalidateStableToolSurfaces` 只清引用、不删内容（因为可能被其它会话共享）。
   如需回收，可加一个"无引用内容清理"任务；当前 18 MB 暂不构成压力。

3. **首次读取仍是两次查询**：`LoadState` 先取行、再按 ref 取内容。
   对热路径影响可忽略（都是主键等值查询），且 `LoadStateStatus` 完全不碰工具面。
