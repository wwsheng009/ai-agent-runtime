# 10 · 在场信号收敛设计（D1：presence → notify 策略）

> 上游：[06-target-architecture.md](./06-target-architecture.md) §1 D1（在场信号唯一权威 = notify 中台）、
> §2.4 策略链（静默/合并/在场/偏好）；gateway 现状见 [../gateway/08-notifications.md](../gateway/08-notifications.md) §5.4。
> 状态：**2026-10-14 S1（网关侧）已实施**（wire 字段 + 多源登记表 + tunnel 接线 + 门禁 flag）；S2/S3 生产端待续；实施切片见 §5。

## 1. 背景与现状（代码核实）

- gateway `presenceRegistry`（`cmd/gateway/presence.go`）：键 `nodeID|sessionID`，TTL 条目 +
  `tail` 位；**唯一信号源 = console 客户端** POST（`presence.go` handler → `Touch`）。
- 消费点：`pushNotifier.observe` 在构建通知前检查 `presence.Active(node, session)` →
  **跳过且不记入 seen 快照**——"先看后推"：在看着就不推；切走后下一次 observe 视作
  新事件**补推一次**。
- `wire.SessionSummary` 已逐会话携带丰富状态（state/attention/status_tail/…），
  **无在场字段**。

缺口：用户在桌面（aicli TUI 前台 / runtime-server web 会话页 / aicli 微前端）时，
手机与浏览器仍收到推送——"人机同屏"重复打扰。D1 要求三宿主在场信号收敛。

## 2. 目标与非目标

目标：

1. 三源在场信号（gateway console / runtime web（SSE 订阅）/ aicli TUI 前台）到达
   gateway 同一登记表，与 console 源**并列合并**（多源并存，任一生效即在场）；
2. 抑制语义与现网一致（在场 → 跳过且不记 seen → 切走补推一次）；`tail` 观察位保持
   console 专属（watch/unwatch 控制面不扩散）；
3. 兼容：旧 agent 不发新字段 = 行为不变；新 agent 对旧 gateway 发新字段 = 被忽略。

非目标（本期不做）：

- 优先级差异化（如 high 穿透在场）——维持"人机同屏不打扰"；如需区分另立策略切片；
- 跨机在场合流（同一用户在 A 机 TUI、B 机不推）——会话粒度已天然覆盖（会话归属单机）；
- notify 内核 `Presence` 接口下沉——随 D4"迁到三宿主"一并做（见 §5 S4）。

## 3. 设计

### 3.1 信号形态：附着会话摘要（可选布尔）

`wire.SessionSummary` 增字段：

```go
// Viewing 表示该宿主机上存在“本地前台查看者”（aicli TUI 当前挂载会话 /
// runtime web 该会话 SSE 订阅者 >0）。宿主机视角、尽力而为；缺省 false。
Viewing bool `json:"viewing,omitempty"`
```

理由：心跳/会话增量已经逐会话上报（增量帧 + digest），复用同通道**零新增协议面**；
"该宿主正在看它"是宿主可自知的事实，无需指出"谁在看"。

### 3.2 网关合并：多源登记表

`presenceRegistry` 升级为**源位**模型（键不变）：

```go
type presenceEntry struct {
    sources map[string]time.Time // source -> 到期时间（console/host/…）
    tail    bool                 // console 专属
}
```

- `TouchSource(source, nodeID, sessionID, tail)`；保留 `Touch` = `TouchSource("console", …)`
  （既有调用与测试行为不变）；
- `Active` = **任一源**未过期；`TailWatched` 只看 console 源的 tail 位（不变）；
- TTL：console 源沿用 `--presence-ttl`（默认 90s）；host 源 TTL = `2 × heartbeat 周期`
  （心跳停止即自动失效；每帧到达续期）；
- host 源 ingest 点：tunnel 收到 REGISTER/SESSION_UPDATE/心跳摘要后，对 `Viewing=true`
  的会话回调 `Config.OnSessionPresence(nodeID, sessionID)`（可选字段，nil 安全）；
  `main.go` 接线到 `presence.TouchSource("host", …)`。

### 3.3 抑制语义（冻结现状）

- observe 前置检查不变：在场（任意源）→ 跳过、不记 seen → 切走**补推一次**；
- 语义边界：同回合中注意力种类变化（approval→error）按现有 seen 差分规则处理；
- **不做 high 穿透**（评审点：审批/提问在用户同屏时控制台已可见；若仪式化要求手机
  确认另议）。

## 4. 生产端（各自“本地前台”的定义）

| 宿主 | 判定 | 说明 |
|------|------|------|
| aicli TUI | 当前挂载会话 && 终端聚焦（聚焦不可得时退化为仅"挂载且激活面"） | 周期心跳上报；切换会话即变化 |
| runtime-server web | 会话 SSE 订阅者 >0（含 30s 静默尾随计数） | runtimeapi 已有订阅面，计数即可 |
| aicli 微前端 | 同 SSE 计数（复用 runtime-server 通道） | 与上同源 |
| gateway console | 现状：浏览器可见性驱动 POST（不改） | `tail` 语义仅此源 |
| aicli `--headless`/后台 | 不上报（无前台） | `Viewing=false` |

默认开启 host 源；逃生门 `--presence-sources=console,host`（gateway flag，缺省两源，
配置为 `console` 即回退现状，一秒回退）。

## 5. 实施切片

- **S1（网关侧）**：wire 字段 + registry 源位模型 + tunnel `OnSessionPresence` 接线 +
  单测（多源合并/TTL 独立/tail 隔离/旧字段兼容）。本切片行为无变化（暂无生产端）。
  ✅ 2026-10-14 落地：`--presence-sources=console,host` 缺省两源（置 `console` 即
  回退现状）；host 源以心跳 TTL（2×周期，下限 90s）续期。
- **S2（aicli TUI）**：前台会话检测 + 心跳携带 `viewing` + 单测（切换/聚焦边界）。
- **S3（runtime-server web）**：SSE 订阅计数 → `Summary.Viewing` + 单测。
- **S4（收敛）**：notify 内核 `Presence` 接口 + Dispatcher gate（此刻才从 gateway 私有
  登记表抽象），三宿主迁移随 D4 余下切片。
- **验收（S1–S3 后，真机）**：TUI 前台看 A 会话时其 attention 不推送手机；切走补推
  一次；console 行为不回归；旧 agent/旧 gateway 双向兼容。

## 6. 风险与回退

- **误报吞通知**：host 源判定过宽（"挂载"≠"在看"）→ 保守上报（TUI 仅前台激活面）；
  出问题 `--presence-sources=console` 立即回退；
- **心跳频率/占用**：布尔字段折算几字节，复用既有帧；host TTL 绑定心跳周期防残留；
- **多源语义歧义**（console 在看、TUI 不看了）→ **任一生效即在场**（保守：宁可不打扰）。