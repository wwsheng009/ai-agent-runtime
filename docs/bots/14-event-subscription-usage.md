# 14 · 连接器事件订阅：配置与使用手册

> 状态：**使用手册（S1–S4 已落地，2026-10-15）** ｜ 权威设计：[13-event-subscription-routing-design.md](./13-event-subscription-routing-design.md)
> 配置页面交互：[07-config-ux.md](./07-config-ux.md) ｜ 运维与验收：[08-operations-and-acceptance.md](./08-operations-and-acceptance.md)

本文面向**配置者与使用者**（运维 / 开发者），回答两个问题：**「怎么配」**（把 bot 订阅到哪些会话的哪些事件、以什么形态投递）与**「怎么验证」**（审计日志、指标、常见故障）。

适用范围：`aicli` 与 `runtime-server` 两个宿主已接入订阅模型（同一份共享配置）；`gateway` 宿主当前仍走出站投影/notify 链路，订阅面统一在 S3（详见 13 §4.3）。

---

## 1. 五分钟上手

**第 1 步 · 准备连接器与绑定**

连接器配置（token/接入模式等）见 07 号与前端「连接器」页；订阅投递到「绑定 chat」，所以先建立绑定：

- TUI：`/bots list` 查看连接器，`/bots bind <connector-id>`（具体参数以 `/bots` 帮助为准）；
- 或让该 IM 会话先给 bot 发一条消息（默认自动建会话并绑定）；
- 只读观察者（不绑定 chat）见 §6 配方 C。

**第 2 步 · 写订阅**（最小示例：把绑定会话的回合正文也发到 IM）

```yaml
connectors:
  - id: qq-bot1
    provider: qqbot
    enabled: true
    policy:
      events:
        enabled: true
        subscriptions:
          - id: main-chat
            sessions: { mode: bound }          # 绑定到本 bot 的会话
            types: [assistant_message]         # 回合终态正文
            deliver: content                   # 正文副本
```

**第 3 步 · 验证**：让该会话跑一个回合（TUI 直输即可），然后看 bot 运行日志：

- TUI `/bots` → 日志面板，或 REST `GET /api/connectors/{id}/logs`；
- 出站方向应出现：`订阅投递（subscription=main-chat，dedup=turn_…）`；
- 若从 IM 发消息触发的回合**不会**出现副本——这是设计（回复路径已把正文回了发起 chat，见 §5「L3」）。

---

## 2. 配置存放、写入与生效

| 项 | 说明 |
|---|---|
| 配置文件 | 共享 `config.yaml` 的 `connectors:` 节（aicli 默认 `~/.aicli/config.yaml`；runtime-server 同文件；`--config` 可覆盖） |
| 密钥 | 同目录 `connectors.secrets.yaml`（0600），GET 永不回显 |
| 状态账本 | `~/.aicli/data/connectors/state.json`（绑定/去重游标，跨宿主共享路径） |
| 写入方式 | ① 直接改 YAML；② 前端「连接器对话框 → 策略 → 订阅」（图形编辑器，含 deliver 四形态）；③ REST 管理面。TUI `/bots` 只读 |
| 生效 | **保存即生效**：订阅求值在每条事件到达时实时读 Store，无需重启（provider/启用状态的生命周期由 Service 管理） |

---

## 3. 字段参考

```yaml
policy:
  events:
    enabled: true            # 总开关（false 或空订阅 = 回退 legacy，见 §10）
    subscriptions:
      - id: main-chat        # 稳定标识（审计/诊断用；可省略，省略时按序号生成）
        enabled: true        # 单条启停：缺省启用；false 显式停用
        sessions:            # 会话选择器（三选一）
          mode: bound        # bound（默认）| ids | all
          ids: [session_a]   # mode=ids 时的显式会话列表（多会话 → 1 bot）
        types: [assistant_message, "turn.*", approval, question]   # 事件类型或族通配（必填）
        deliver: content     # notice（默认）| content | digest | stream
```

| 字段 | 取值 | 语义与边界 |
|---|---|---|
| `sessions.mode` | `bound` / `ids` / `all` | `bound`=仅绑定到本 bot 的会话；`ids`=显式列表（无需绑定）；`all`=宿主内全部会话（广播语义，建议配启用告警） |
| `types` | 见 §4 词表 | 精确规范名、别名、族名（`turn`）、族通配（`"turn.*"`）或 `"*"`；必填，空订阅被忽略 |
| `deliver` | `notice` / `content` / `digest` / `stream` | 投递形态（§5）；缺省 `notice` |
| `enabled`（订阅级） | bool | 缺省启用；`false` 停用单条（不删配置） |

未知 `mode`、空 `types`、`ids` 模式空列表的订阅会被**整条忽略**（不报错）。
---

## 4. 事件词表与可用性（重要）

词表口径（归一名 = 总线上实际事件名；别名可直接写进 `types`）：

| family | 规范名 | 别名 | 可用形态 | 备注 |
|---|---|---|---|---|
| interaction | `approval_requested` | `approval` | `notice` | **绑定会话恒开**（命令回环必须可达，不受开关）；配 `digest` 也按即时 notice 投递（安全优先） |
| interaction | `question_asked` | `question` | `notice` | 同上 |
| turn | `agent.turn.finished` | `completion` | `notice` / `digest` | digest 时纯 completion 批聚合为「✅ N 个会话回合已收尾」 |
| conversation | `assistant_message` | `assistant.message` | `content` | 回合终态正文副本；IM 来源回合整回合跳过（§5 L3） |
| conversation | `assistant_delta` | `assistant.delta` | `stream` | **仅 stream 形态可消费**（其余形态不匹配 delta 事件） |
| work | `tool.requested` | `tool.started` | `digest` | **仅聚合形态**：notice 订阅对工作类不生效（逐条会刷屏） |
| work | `tool.completed` | `tool.finished`、`tool_finished` | `digest` | 同上 |
| work | `tool.denied` | — | `digest` | 同上 |
| work | `subagent.completed` | — | `digest` | 同上 |
| work | `subagent.batch.completed` | — | `digest` | 同上 |
| — | `user_message`、`agent.turn.started`、`turn.suspended/resumed`、`session_start/end/interrupted`、`error` | — | ⚠️ 未接出站映射 | 词表已登记，订阅**暂不投递**（预留：接入出站映射后生效） |

族通配：`conversation` / `turn` / `interaction` / `work` / `system`，或 `"turn.*"`、`"work.*"`、`"*"`（显式全量）。

---

## 5. 投递形态详解

| 形态 | 行为 | 前提 / 降级 | 审计文案（日志面板） |
|---|---|---|---|
| `notice` | 即时模板通知；跨会话订阅（`ids`/`all`）自动在文案中附会话短标识 | 无额外前提 | `通知投递（kind=…，session=…，subscription=…）` |
| `content` | 终态正文全文投递到该会话的绑定 chats；与回复共用 per-chat 串行队列（保序）；同一事件同一目标至多一条 | 需已有绑定 | `订阅投递（subscription=…，dedup=…）` |
| `digest` | 窗口（默认 30s，当前无配置项）内按 (bot, 订阅) 合并为**一条**：纯 completion 批 = 「✅ N 个会话回合已收尾」+ 会话标签；混合/工作类批 = 「🔔 N 个事件已收尾（回合 ×a，工具 ×b，子代理 ×c）」 | 无额外前提 | `通知投递（kind=completion，digest=N，subscription=…）` |
| `stream` | 打字机：首个增量发占位消息 → 节流编辑（≥1s）→ 终态正文做最终编辑；同一 chat 重绑会话时先收尾旧流 | 需绑定；provider 需支持流式，**不支持则退化一次性 content** | `流式投递（subscription=…，dedup=…）`，退化时附 `degraded=content` |

**边界与不变量**（设计 13 §3.6/§3.7）：

- **L3（IM 回合互斥）**：由 IM 消息触发的回合，正文已由回复路径送回发起 chat；`content`/`stream` 订阅对该回合**整回合跳过**（防双发）。本地/TUI 来源回合正常镜像。
- **L2（同目标合并）**：同一 chat 命中多条订阅、形态相同时合并为一次投递；形态不同（如 `content` + `notice`）视为两种信息，**各发一条**——同一目标不要同时配 `content` 和 `stream`（会得到两条：全文 + 打字机）。
- **一条订阅只有一种形态**：`deliver` 是订阅级属性——`assistant_message`（content）、`assistant_delta`（stream）、
  工作类（digest）、completion/审批（notice）分属不同形态时，必须**拆成多条订阅**；形态不匹配的类型会被
  后端**静默忽略**。两端编辑器（React 与 aicli 微前端）均已把这一步自动化：**事件类型按投递形态分组多选、
  面板按当前形态提前过滤**（切换投递形态即切换可见类型；存量跨形态勾选保留可见，可就地取消），**形态由
  所选类型推导**（唯一解锁定；completion 等多形态类型只在 notice/digest 间选；冲突项给出告警并可
  「一键拆分为多条订阅」），无需手工配对（2026-10-15）。
- **默认安静**：`content`/`stream`/`digest` 全默认关，必须显式订阅；绑定会话的 approval/question 恒开。
- `stream` 形态**不受** `policy.reply_mode` 影响（那条开关只描述回复通道）。

---

## 6. 典型配方

```yaml
# A. 单会话绑定 + 正文副本：IM 也能看到 TUI 直输回合的完整输出
{ id: main-chat, sessions: { mode: bound }, types: [assistant_message], deliver: content }

# B. 多会话值班 bot：A/B/C 三个会话的回合与审批/提问都进一个群（通知带会话短标识）
{ id: ops-watch, sessions: { mode: ids, ids: [session_a, session_b, session_c] },
  types: ["turn.*", approval, question], deliver: notice }

# C. 纯观察者：不绑定，仅收指定会话的回合完成事件（"1 会话 → 多 bot"）
{ id: observer, sessions: { mode: ids, ids: [session_x] }, types: [agent.turn.finished], deliver: notice }

# D. 工作看板：工具/子代理只聚合（30s 窗口），不逐条刷屏
{ id: work-board, sessions: { mode: all }, types: ["work.*"], deliver: digest }

# E. 打字机：实时看某会话的输出（Telegram/QQ/飞书支持；微信自动退化一次性投递）
{ id: live, sessions: { mode: bound }, types: [assistant_delta], deliver: stream }

# F. 静音一切、只保留命令回环：订阅为空（或 events.enabled: false）+ 不配 content
#    → 绑定会话仍恒收 approval/question（命令回环不可静音）
```

---

## 7. 验证与排障

### 7.1 审计日志（首选）

查看 bot 运行日志（TUI `/bots` 日志面板；或 REST `GET /api/connectors/{id}/logs`，鉴权按宿主）。出站方向（direction=out）的三种条目：

```text
订阅投递（subscription=main-chat，dedup=turn_xxx）              # content
通知投递（kind=approval，session=session_…，subscription=ops-watch）  # notice
通知投递（kind=completion，digest=3，subscription=quiet-completions） # digest
流式投递（subscription=live，dedup=turn_xxx[，degraded=content]）     # stream
```

成功投递才写审计（失败只记失败日志/指标，不产生成功条目）。

### 7.2 指标

连接器投递面（进程内 observability 注册表，随宿主 metrics 面读数）：

| 指标 | 标签 | 含义 |
|---|---|---|
| `connectors_deliveries_total` | `connector` / `form`(content·notice·digest·stream) / `outcome`(sent·failed) | 每次投递的结果 |
| `connectors_digest_merged_total` | `connector` | 被 digest 窗口吸收的事件数（聚合有效性） |

### 7.3 常见问题

| 现象 | 排查 |
|---|---|
| 订阅完全没反应 | ① `events.enabled: true` 且订阅非空；② `types` 用规范名/别名（注意：其余拼写不会匹配，未登记类型不生效）；③ `sessions` 模式与实际绑定匹配（`bound` 需要绑定）；④ 单条 `enabled` 未被置 false |
| IM 发消息触发的回合没有正文副本 | 设计如此（L3）：正文已由回复路径送回发起 chat，避免双发 |
| 工作类/工具事件收不到 | 工作类**仅 `digest` 形态**：`deliver: notice` 或 `content` 对工作类不生效；改 `deliver: digest` |
| 配了 `user_message`/`session_end` 等没有投递 | 这些类型尚未接出站映射（§4 标注 ⚠️），当前为预留 |
| `digest` 迟迟不来 | 默认 30s 窗口（无配置项）；approval/question 恒即时、不进窗口 |
| `stream` 没有打字机、只收到一条完整文本 | provider 不支持流式（如微信）或占位启动失败 → 自动退化为一次性 content；日志含 `degraded=content`。另：后端该回合无增量事件时也不会有流 |
| 勾选了 `assistant_message` 但 IM 没有收到正文 | 该订阅的 `deliver` 必须是 `content`（`assistant_delta`→`stream`；工作类→`digest`）。**一条订阅只有一种形态**：编辑器现按类型**自动推导**形态，混合勾选点「一键拆分」即可。另注意 L3：IM 触发的回合不镜像 |
| 同一段内容收到两条 | 检查是否同时配了 `content` 与 `stream`（形态不同、各发一条）；或同一 bot 多订阅命中同一 chat 且形态不同属显式行为 |
| 想给「非绑定 chat」发 | 当前 target 固定为「会话的绑定 chats」（多绑定=多 chat 都收）；显式 target 列表为设计预留（13 §3.4），未落地 |

---

## 8. 平台能力矩阵

| Provider | `stream`（占位 + 编辑） | 富媒体附件（`IMAGE:`/`MEDIA:` 指令或结构化 Media） |
|---|---|---|
| Telegram | ✅ `editMessageText`（中间态不泄漏指令、终态补发附件） | ✅ 本地文件上传 / URL / file_id 复用 |
| QQ 官方 Bot | ✅（群/无被动上下文时返回委托会话，安全退化） | ✅（含流式收尾附件） |
| 飞书 | ✅ 卡片 PATCH（中间态剥离指令） | ✅ |
| 微信 | ❌ 自动退化为一次性 content 投递 | ✅（语音降级为文件） |

`notice`/`content`/`digest` 全平台可用。富媒体协议（agent 回复里声明要回发的图片/视频/文件）在回复路径与订阅路径**同口径**解析。

---

## 9. 兼容与默认值

- **默认安静**：`content` / `stream` / `digest` 默认关，显式订阅才开启；绑定会话的 approval/question 恒开（命令回环不可静音）。
- **legacy 回退**（`events` 未配置时逐字生效，保留一个发布周期）：
  - `policy.notify.{enabled,kinds,scope}` ↔ 注意力通知（旧 D5 语义）；
  - `policy.content_mirror: true` ↔ 正文副本开关。
- 迁移建议：新配置一律写 `events`；旧字段在同 bot 上不要与新字段混用（`events` 一旦配置，legacy 判定整体让位）。

---

## 10. 多实例约束（部署前必读）

订阅投递收口在「持有该 bot 的 poller 节点」：当前适用于**单实例部署**，或「事件宿主 = 该 bot 的 poller」。跨实例 fan-out 的定向投递尚未落地（13 §4.4，S3 前置项）。多实例部署下 `scope=all` 类广播仍可经 mesh 转发（fire-and-forget，对端自行过滤）。

---

## 11. 前端入口与编辑路径

订阅模型有三个前端入口，能力与风险不同，按需选择。

### 11.1 React（runtime-server 托管）——推荐，完整编辑面

- 入口：runtime-server 启动且前端构建产物就位（`webui.Available()`）时，打开
  `http://<runtime-server>:<port>/runtime/connectors`（运行管理 → 连接器）。
- 路径：连接器列表 → 打开某连接器 → 对话框中「事件订阅」区：
  - 总开关（`events.enabled`）；勾选任一事件类型会自动打开总开关；
  - 「添加订阅」逐条编辑：`id` / 会话选择器（bound | ids + 列表 | all）/
    **事件类型 = 按投递形态分组的多选面板（按当前形态提前过滤）**（切换投递形态即切换
    可见类型；折叠汇总「已选 N 项」；词表外既有值追加为「自定义(既有)」勾选项，另有
    自定义/别名输入框，往返不丢）/
    **投递形态由所选类型自动推导**（唯一形态锁定；completion 等多形态只在
    notice/digest 间选）/ 单条启停；
  - 存量跨形态勾选保留可见（可就地取消）→ 相容性告警（「X 需要 deliver=…」/
    「X 暂未接出站映射」）+「一键拆分为多条订阅」（按形态分组，续行 id 追加 -2/-3）；
    会话列表在 `mode≠ids` 时禁用并按提示补录（2026-10-15 与微前端同口径）；
- 保存 = `PATCH /api/connectors/{id}`：前端把「路由 + 事件订阅」合并进该连接器**现有 policy**
  （保留未展示的键）；关闭订阅时显式写 `enabled:false`。
- 鉴权：runtime-server 管理面 token（与 skills 管理面同口径）。

### 11.2 aicli 微前端（micro web，`/web/`）——连接器与订阅均可编辑

- 入口：`aicli --web` 后打开 `http://127.0.0.1:<port>/web/`，切到「连接器」页签。
- 能力：连接器列表 / 新建 / 编辑 / 测试 / 日志 / 绑定管理；策略表单含
  `allowed_users`、`command_prefix`、`session_mode`、`reply_mode`、`runtime_owner`、`route`，
  以及**事件订阅编辑器**（总开关 + 逐条 id / 会话选择器（bound|ids|all + 列表）/
  事件类型（**按投递形态分组的多选面板，按当前形态提前过滤**，切换形态整表重渲染）/ 投递形态（**由所选类型
  自动推导**，冲突项一键拆分）/ 单条启停；2026-10-15 补齐）。
- 保存语义（与 React 对话框对齐）：保存走 `PATCH /api/connectors/{id}`，前端先**原样带回
  既有 policy** 再覆盖表单字段与 `route`/`events`——未展示键（`notify`、`content_mirror` 等）
  不再丢失（2026-10-15 修复此前「保存策略会清空订阅/开关」的问题）。
- TUI `/bots`：只读（状态 / 日志 / 绑定查看）。

### 11.3 REST 直连（脚本 / 自动化）

管理面契约（三宿主一致，鉴权按宿主：runtime-server=Bearer、aicli=本机会话守卫、gateway=owner cookie）：

```text
GET|POST      /api/connectors               列表 / 新建
GET|PATCH|PUT|DELETE /api/connectors/{id}   查看 / 更新 / 删除（policy 整体替换）
GET           /api/connectors/{id}/logs     运行日志（entries + stats）
POST          /api/connectors/{id}/reload|takeover|test
PUT|DELETE    /api/connectors/{id}/bindings/{chat}   绑定 / 解绑
```

⚠️ **`policy` 是整体替换**（`settings`/`secrets` 则以现有值为底合并）：PATCH 必须携带要保留的
全部 policy 键（含 `notify`/`content_mirror`/`route`），缺省的键会被清空。示例（保留既有键、仅更新订阅）：

```bash
curl -X PATCH http://127.0.0.1:PORT/api/connectors/qq-bot1 \
  -H "Authorization: Bearer <admin-token>" -H "Content-Type: application/json" \
  -d '{
    "policy": {
      "command_prefix": "/",
      "allowed_users": [],
      "session_mode": "chat",
      "reply_mode": "stream",
      "runtime_owner": "",
      "content_mirror": true,
      "notify": { "enabled": false },
      "events": {
        "enabled": true,
        "subscriptions": [
          { "id": "main-chat", "sessions": { "mode": "bound" },
            "types": ["assistant_message"], "deliver": "content" },
          { "id": "ops-watch", "sessions": { "mode": "ids", "ids": ["session_a", "session_b"] },
            "types": ["turn.*", "approval", "question"], "deliver": "notice" }
        ]
      }
    }
  }'
```

## 12. 相关文档

| 主题 | 文档 |
|---|---|
| 订阅模型与全部决策（权威） | [13-event-subscription-routing-design.md](./13-event-subscription-routing-design.md) |
| 配置页交互（四终端/扫码/密钥语义） | [07-config-ux.md](./07-config-ux.md) |
| 运维手册与验收矩阵 | [08-operations-and-acceptance.md](./08-operations-and-acceptance.md) |
| 多实例绑定与实例间通讯边界 | [09-multi-instance-binding.md](./09-multi-instance-binding.md)、[gateway 15](../gateway/15-multi-instance-architecture.md) |
| notify 中台（静默/在场/合并内核） | [12-notify-consolidation-plan.md](./12-notify-consolidation-plan.md) |
