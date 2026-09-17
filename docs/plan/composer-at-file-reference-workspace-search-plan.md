# Composer `@` 文件引用优化方案（工作区文件小批量列表 + 模糊搜索）

> 状态：**已实施（2026-09-17）**——P0 与 P1（P1-1..P1-11）落地，实施位置与验证证据见「§10 实施记录」；P2 未立项。§1 为现状取证，§3 起为设计与排期（保留原始方案口径）。
> v2 变更：新增 §4.7「右侧文件浏览器搜索扩展」（`/fs/search` 的第二消费方）与 §9「完整性审查与 v2 修订记录」；并补齐 §4.4 的契约缺口。
> v2.1 变更（2026-09-17）：Q7/Q8 已决策并回写——面板搜索结果采用"替换树视图 + 显式返回"（§4.7.2-A，不做分屏/独立结果区）；P1 不做搜索结果与树的定位联动（§4.7.4，reveal 列入 §5-P2）。
> 日期：2026-09-17（本地 +08:00）
> 适用版本：当前仓库（前端 `frontend`，dev 端口 5193，`/api` 代理到 runtime-server；后端 Go module `backend`）
> 关联文档：
> - `docs/plan/workspace-right-panel-file-browser-and-git-diff-plan.md`（`/api/runtime/fs/*` 权威契约：scope/path 语义、限额、错误模型；本方案**复用**不修改其既有契约）
> - `docs/development-guidelines.md`（§10 文档规范、§11 前端工程门禁）
> - `docs/plan/frontend-deepseek-harness-optimization-plan.md`（composer 触发菜单 P1-4 子片 3 的来源方案）
>
> 取证方法：静态阅读 `frontend/src/lib/composer-*.ts`、`frontend/src/hooks/workspace/composer/*`、`frontend/src/components/workspace/workspace-shell/main-section.tsx`、`frontend/src/api/runtime/fs-*.ts`、`backend/internal/filebrowse/*`、`backend/internal/fsscope/*`、`backend/internal/api/skills/fs_browser_*.go`；未运行服务端。

---

## 0. 结论摘要

1. **现状问题定位准确**：composer 的 `@` 候选只来自 `selectedThread.artifacts`（线程交付物），而当前会话的交付物几乎只有 `session-history-<sessionId>.json`（以及少量 `turn-*.json`），所以用户看到"只有一个 session 文件"。这不是工作区文件列表能力缺失，而是**数据源接错了**（`frontend/src/components/workspace/workspace-shell/main-section.tsx:194-201`）。
2. **可复用的现成能力充足**：仓库已有完整的文件浏览后端与前端客户端——
   - 后端：`GET /api/runtime/fs/roots`（作用域根枚举，支持 `session_id` 桥接会话工作目录）、`GET /fs/list`（单层目录、游标分页、服务端排序、隐藏/内部项过滤）、`/fs/stat`、`/fs/preview`（`backend/internal/api/skills/fs_browser_routes.go:38-49`）；
   - 前端：`fetchFsRoots` / `pickDefaultRoot`（优先 `session:<id>` 根）、`fetchFsListing`（归一化 + `cursor_invalid` 判定）、`useFileBrowser`（分页与竞态状态机，可直接借鉴）（`frontend/src/api/runtime/fs-roots.ts:91-130`、`frontend/src/api/runtime/fs-list.ts:118-144`、`frontend/src/hooks/workspace/use-file-browser.ts:146-214`）。
3. **两个真实缺口**：
   - **递归搜索缺口**：`fs/list` 只列**一层**，没有跨目录的文件搜索；一次列全仓不可行（`node_modules`、`dist`、`.git` 等）。
   - **异步数据源缺口**：composer 菜单模型（`lib/composer-menu.ts`）是纯同步函数 + 客户端 `includes` 子串过滤（`rankMatch`，`composer-menu.ts:96-105`），且查询串持有在 `useComposerMenu` 内部（`use-composer-menu.ts:187-206`），宿主组件拿不到 `@` 后面的输入，无法"输入即搜"。
4. **方案主线（两步走，符合"先小批量、再模糊搜索"）**：
   - **P0 首屏小批量（纯前端，零后端改动）**：新增 `useComposerFileReferences` hook，`@` 打开瞬间用 `fs/roots` 解析作用域（优先会话根），再用 `fs/list` 拉根目录第一页（`limit≈20`、目录优先、隐藏项过滤），异步注入"工作区文件"分组；原有线程产物分组保留为第二分组/降级兜底。
   - **P1 模糊搜索（后端一个新增只读端点 + 前端接入）**：新增 `GET /api/runtime/fs/search`（`filebrowse` 包内扩展），**有界扫描**（固定忽略目录 + 深度上限 + 扫描条目上限 + 时间预算 + 不跟随符号链接目录）并按打分排序返回 top-N 小批量；前端防抖 + `AbortController` + 请求序号防竞态，菜单模型新增 `serverFiltered` 标记跳过客户端过滤，保住模糊命中（子序列命中不是子串，客户端过滤会误杀）。该端点由**两个消费方**共用：composer `@` 候选（§4.5）与页面右侧文件浏览器（§4.7）；两端共享同一 `fetchFsSearch` 客户端与同一套搜索 hook 纪律。
5. **不做的事**：不建常驻全仓索引 / 文件 watcher（P2 再评估）；不做内容全文搜索（与 grep 能力边界不同）；不把引用展开成文件内容注入提示词（引用仍以 `@相对路径` 文本进入 draft，后端零协议改动）。
6. **完整性审查（v2）**：按 11 个维度逐项复核（目标/契约/扫描排序/安全/错误/限额/竞态/降级/测试/排期/文档同步），补齐 10 处缺口（`q` 上限与字面匹配、`truncated` 与 `has_more` 语义拆分、隐藏目录下钻规则、游标跨变更语义、错误码命名、Unicode 规范化限制、双消费方共享层、面板搜索扩展、超时归因、用户文档同步）；清单与修订记录见 §9。

---

## 1. 现状取证

### 1.1 `@` 触发与引用插入链路（前端）

```text
MessageComposer (message-composer.tsx:132-140)
  └─ useComposerMenu({ value: draft, referenceGroups, ... })
       ├─ detectComposerTrigger(value, caret)        // composer-trigger.ts:30-75，几何判定 @ 查询串
       ├─ buildComposerMenu({ query, referenceGroups, ... })  // composer-menu.ts:263-329，过滤/排序/裁剪
       │     └─ buildReferenceLeafGroup(...)         // composer-menu.ts:193-219，rankMatch 子串 + 每组 ≤8
       └─ selectItem → composerReferenceText(text)   // 含空白加引号
            → applyComposerTriggerInsertion(...)     // composer-trigger.ts:91-100，替换 token 为插入文本
```

| 事实 | 证据位置 |
|---|---|
| `@` 候选的唯一生产来源是线程交付物 `selectedThread.artifacts` | `frontend/src/components/workspace/workspace-shell/main-section.tsx:194-201` |
| 交付物 → 引用组的映射函数只做映射与 `slice(0, 12)` | `frontend/src/lib/composer-references.ts:8,11-29` |
| 会话历史加载时会写入 `session-history-<sessionId>.json` 产物（即用户看到的唯一文件） | `frontend/src/lib/thread-state/history-artifacts.ts:162-169` |
| 菜单每组最多 8 项；参考组叶子构建时做**客户端子串**过滤（`includes` + 前缀加权） | `frontend/src/lib/composer-menu.ts:9,96-105,193-219` |
| 查询串（`@` 后文本）只存在于 `useComposerMenu` 内部 state，宿主不可见 | `frontend/src/hooks/workspace/composer/use-composer-menu.ts:98-104,187-206` |
| 插入文本经 `composerReferenceText` 处理（含空白自动加双引号） | `frontend/src/lib/composer-trigger.ts:77-85`；`use-composer-menu.ts:263-266` |

### 1.2 已有工作区文件能力（可直接复用，勿重复造）

| 能力 | 契约要点 | 证据位置 |
|---|---|---|
| 作用域根枚举 `GET /api/runtime/fs/roots` | 可带 `?session_id=`，把"会话当前工作目录"补成 `kind:"session"` 根；返回 `scope/kind/name/path/exists/is_git_repo` | `backend/internal/filebrowse/service.go:166-192`；`frontend/src/api/runtime/fs-roots.ts:91-130` |
| 单层列表 `GET /api/runtime/fs/list` | `scope,path(相对根),cursor,limit(≤1000),sort,show_hidden,dirs_first`；返回 `dir/entries/next_cursor/has_more/truncated/sort`；内部项 `.aicli-uploads` 永不返回 | `backend/internal/filebrowse/list.go:27-126`；`frontend/src/api/runtime/fs-list.ts:24-140` |
| 默认限额 | `ListLimitDefault=200`、`ListLimitMax=1000`、`ScanMaxEntries=50000`；单层超限置 `truncated=true` 不静默 | `backend/internal/filebrowse/service.go:71-85`；`list.go:129-191` |
| 作用域与路径安全 | scope 仅 `workspace:<id>|session:<id>|cwd`；path 一律相对根、绝对路径拒绝（`path_must_be_relative`）、越界/符号链接逃逸拒绝；错误码机器可读 | `backend/internal/fsscope/scope.go:32-53,148-172` |
| 前端分页状态机（可借鉴） | 请求序号 + `AbortController` 防竞态；`cursor_invalid` 标记重置而非死循环；`truncated` 原样透出 | `frontend/src/hooks/workspace/use-file-browser.ts:24-27,87-93,146-214` |
| 默认作用域选择 | `pickDefaultRoot` 优先 `kind==="session"`，否则首根 | `frontend/src/api/runtime/fs-roots.ts:112-114` |

### 1.3 缺口清单（问题根因）

| # | 缺口 | 影响 |
|---|---|---|
| G1 | `@` 数据源错位：接的是交付物而非工作区文件 | 只有 `session-history-*.json`，功能名不副实 |
| G2 | 无递归搜索端点：`fs/list` 单层，无法跨目录按关键字找文件 | 不能"输入即搜"，只能逐层点目录 |
| G3 | 菜单模型同步且客户端过滤：无 loading/error/empty 表达，`rankMatch` 只认子串 | 异步结果无法渲染状态；模糊命中（子序列）会被客户端二次过滤误杀 |
| G4 | 查询串宿主不可见 | 数据 hook 无法感知输入变化，不能防抖请求 |
| G5 | 全量枚举不可行 | 大仓一次列全文件会造成毫秒级请求膨胀为秒级、响应体 MB 级；必须小批量 + 有界扫描 |

> 备注：`fs/list` 的 `show_hidden=false` 默认已过滤隐藏项，且会跳过 `inaccessible` 单项；其 `truncated` 语义（"本层仅显示前 N 项"）在搜索结果中同样需要保留同类语义。

---

## 2. 目标 / 非目标 / 约束

### 2.1 目标

1. `@` 打开（query 为空）时，**先返回一小批量**工作区文件候选（默认 ≤20 条服务端返回、菜单渲染每组 ≤8 条），不阻塞输入、不做全仓枚举。
2. 继续输入时对**整个作用域（工作区/会话目录）**做模糊搜索：跨目录、按相关度排序、稳定小批量返回；再次输入立即收敛。
3. 作用域正确：优先"会话当前工作目录"根，其次注册工作区根/`cwd`；切换会话时作用域随会话切换并作废旧请求/旧缓存。
4. 失败可降级、可解释：`fs/roots` 不可用（404/405/501/503）或搜索失败时，保留线程产物分组不中断输入，并给出可本地化提示。
5. 安全与限额与既有 `fs/*` 契约完全一致：路径相对作用域根、复用 `fsscope` 校验与错误码、隐藏/内部项纪律不变。
6. **同一搜索端点服务两个入口**：右侧文件浏览器（`file-browser-surface`）获得跨目录搜索能力，且**不破坏**其既有"输入即过滤已加载层"的即时行为（本地过滤保留，远端搜索叠加，见 §4.7）。

### 2.2 非目标（本期明确不做）

- 不建常驻全仓索引、不引入文件系统 watcher / inotify 服务（P2 可选评估）。
- 不做文件**内容**检索（关键字搜正文不属于本功能；grep/检索是另一能力面）。
- 不改 `@` 引用进入提示词的文本协议：插入结果仍是 `@<相对路径>`（含空白时 `@"..."`），后端无需理解新语法。
- 不在本期实现"目录下钻"交互（是否需要一个目录项、选中后做什么，见 §8 开放问题 Q2；评分时可返回目录，但插入语义需要单独决策）。
- 不修改 `fs/list`、`fs/roots` 既有请求/响应字段（只读复用；新增能力另开端点，避免破坏文件浏览器）。
- **不移除**文件浏览器既有本地过滤（`filterText` 只过滤已加载层）：远端搜索是叠加能力，不是替换；`/fs/search` 不可用时自动退回纯本地过滤（§4.7.5）。

### 2.3 约束

- 响应时间预算：首屏小批量 P95 ≤ 300ms（本地单用户）；搜索 P95 ≤ 500ms（含扫描），超预算宁可 `truncated=true` 也不拖死输入。
- 内存/CPU：单次搜索扫描条目上限默认 20000、深度上限 8、时间预算 250ms；扫描过程可被 `context` 取消（前端 abort 即断开）。
- 兼容性：前端不新增运行时依赖；后端只允许标准库（与 `filebrowse` 包现有纪律一致，`service.go:11`）。
- 路径分隔符统一 `/`（沿用 `list.go:203-205` 的 `path.Join` 口径），Windows 盘符/UNC 由 `fsscope` 拦截。
- 双消费方（composer / 右侧文件浏览器）共用同一端点与同一套限额/忽略/截断口径，禁止出现两套规则；前端共享同一 `fetchFsSearch` 客户端与同一套防抖/取消纪律（§4.7.4）。

---

## 3. 方案对比与选型

| 方案 | 做法 | 优点 | 缺点 | 结论 |
|---|---|---|---|---|
| A. 纯前端递归（前端逐层 `fs/list` 拼全量） | hook 递归拉取所有目录再本地过滤 | 后端零改动 | 大仓请求风暴（每层一次 HTTP）、无法限定预算、竞态复杂、与"小批量"目标矛盾 | ❌ 否决 |
| B. 给 `fs/list` 增加 `recursive=true` | 复用现有端点加参数 | 少一个端点 | 破坏单一职责：分页/排序/游标语义要为递归重定义，回归面覆盖文件浏览器主路径；`sort` 键与新评分模型冲突 | ❌ 不推荐 |
| C. 新增 `GET /fs/search`（推荐） | 独立只读端点，有界扫描 + 打分 + 小批量 + 游标 | 语义清晰、限额/降级独立、可灰度；复用 `fsscope` 全套安全校验；不影响既有端点 | 新增一个后端能力与测试面 | ✅ 采纳 |
| D. 复用 skills 搜索（embedding/lexical） | 走 `GET /skills/search` 同类检索 | 已有实现 | 面向技能元数据而非文件路径；embedding 成本与冷启动高；无目录/忽略/深度语义 | ❌ 不适配 |

**P0/P1 拆分选型**：首屏小批量不依赖新端点（复用 `fs/list`），可独立上线验证"数据源接对"；模糊搜索再引入方案 C。这样即使搜索端点延期，`@` 也已从"只有 session 文件"恢复到"工作区文件可见"。

---

## 4. 详细设计

### 4.1 总体架构

```text
MessageComposer ──(新增 onMenuStateChange)──▶ WorkspaceMainSection
                                               │  useComposerFileReferences({ sessionId, query })
                                               │    ├─ fetchFsRoots({sessionId})  → pickDefaultRoot（优先 session 根）
                                               │    ├─ P0: fetchFsListing({scope, path:"", limit:20, dirsFirst:true})
                                               │    └─ P1: fetchFsSearch({scope, q:query, limit:20})（防抖+abort+序号）
                                               ▼
                                   referenceGroups = [workspaceFiles 组, artifacts 组(兜底)]
                                               ▼
                        MessageComposer → useComposerMenu → buildComposerMenu → 菜单渲染
```

- 数据获取（异步、带取消）集中在宿主 hook；菜单模型保持"纯函数"定位。
- `artifacts` 组保留：它承载回合产物（`session-history-*.json`、`turn-*.json`），在根解析失败时是兜底数据源，也避免"功能回退"。

### 4.2 作用域解析（scope）

1. 会话切换时调用一次 `fetchFsRoots({ sessionId })`（会话未登记时 `sessionId` 传空，仅拿注册工作区根）；用 `pickDefaultRoot` 取优先根：
   - `kind === "session"`（会话工作目录）→ 首选；
   - 否则首根（通常是注册工作区根）；
   - `roots` 为空 → 不使用 `cwd` 猜测？`pickDefaultRoot` 已含首根回退；若空则降级为 artifacts 组。
2. `isFsRootsUnavailable`（404/405/501/503）→ 静默降级到 artifacts 组 + 一次轻提示；网络类错误 → 保留上次成功作用域，仅提示。
3. 作用域缓存的键是 `sessionId`（内存 Map + TTL，建议 5min）；`sessionId` 变化立即作废在途请求与结果缓存。
4. 根的可用性以 `root.exists` 为准；`exists=false` 时跳过该根。

> 依据：`fs-roots.ts:91-137` 已是该语义的既有实现；文件浏览器面同样用 `pickDefaultRoot` 选默认作用域（`file-browser-surface.tsx:69-78`）。

### 4.3 P0：首屏小批量（query 为空）

请求（复用 `fs/list`，零后端改动）：

```ts
fetchFsListing({
  scope,                 // §4.2 解析所得
  path: "",              // 根目录；P2 若支持"上次目录"再扩展
  limit: 20,             // 小批量；后端默认 200 但这里显式 20
  sort: "type_then_name",// 目录优先、名称稳定排序
  showHidden: false,
  dirsFirst: true,
})
```

- 响应映射为 `workspaceFiles` 分组：
  - `type === "file"`：`label=name`，`description=path`，`insertText=path`；
  - `type === "dir"`：**P0 不产生可插入项**（见 §8-Q2；先过滤掉目录可避免"选中目录插入什么"的歧义）；
  - `inaccessible/unknown`：过滤；
  - `internal === true`：后端已不返回，前端仍按纪律过滤一次。
- 只有目录、没有文件（如仓库根只有文件夹）时：分组 `empty` 提示"继续输入可搜索子目录文件"（i18n key 见 §4.5.4）。
- `truncated=true` 时在分组描述/页脚提示"仅显示前 N 项"。

### 4.4 P1：后端模糊搜索端点

#### 4.4.1 API 契约

`GET /api/runtime/fs/search`（注册于 `RegisterFSBrowserRoutes`，与 `/fs/list` 同级；`FSBrowserService` 接口新增 `Search` 方法，nil 注入仍统一 503）：

| 参数 | 必填 | 默认 | 说明 |
|---|---|---|---|
| `scope` | ✅ | — | `workspace:<id>` / `session:<id>` / `cwd`；解析与校验复用 `fsscope` |
| `q` | ❌ | `""` | 查询串；空串 = 首屏小批量（可由本端点统一承载，前端 P0 仍走 `fs/list`）。**按字面匹配**（不解释正则/通配符），长度 ≤256 字符，超长 400 `query_too_long`，不做静默截断 |
| `path` | ❌ | `""` | 相对作用域根的基础目录；限定搜索范围（后续支持"仅在当前目录下搜"）。结果 `path` 仍相对作用域根，含该前缀（与 `fs/list` 同口径） |
| `limit` | ❌ | 20 | 1..50，服务端夹紧 |
| `cursor` | ❌ | — | 上一页 `next_cursor` |
| `show_hidden` | ❌ | false | 与 `fs/list` 同口径；`.aicli-uploads` 等内部项始终隐藏 |
| `kinds` | ❌ | `file` | `file` / `dir` / `both`；P1 前端只用 `file` |
| `max_depth` | ❌ | 8 | 1..16，服务端夹紧 |
| `max_scan` | ❌ | 20000 | 扫描条目上限，服务端夹紧（建议硬上限 50000，与 `ScanMaxEntries` 对齐） |
| `budget_ms` | ❌ | 250 | 扫描时间预算，服务端夹紧 ≤1000 |

响应体（全部字段一次性返回，前端归一化纪律见 `fs-list.ts:8-12` 同款：缺数组抛错、未知枚举收口、非有限数值不伪装）：

```json
{
  "scope": "session:sess_20260917080039_At3WjZUQ",
  "query": "compmenu",
  "base": "",
  "items": [
    {
      "name": "composer-menu.ts",
      "path": "frontend/src/lib/composer-menu.ts",
      "type": "file",
      "size": 12345,
      "mtime": 1758000000,
      "ext": ".ts",
      "score": 932,
      "match": { "field": "name", "start": 0, "end": 6 }
    }
  ],
  "next_cursor": "eyJ2IjoyLC…",
  "has_more": false,
  "scanned": 4213,
  "truncated": false,
  "elapsed_ms": 37,
  "limit": 20
}
```

错误：复用 `fsscope` 机器码（`scope_invalid` / `scope_not_found` / `path_not_found` / `path_permission_denied` …）+ 既有 `cursor_invalid` + 新增 `fs_search_failed`（500，命名对齐既有的 `fs_list_failed`，`service.go:29`）与 `query_too_long`（400）。

补充语义（v2 补齐）：

- **`truncated` 与 `has_more` 正交**：`truncated` 表示"因深度/扫描量/时间预算/单目录枚举上限导致本次结果可能不完备"；`has_more` 表示"按相关度还有下一页可翻"。四象限都必须能如实表达（`truncated=true` 且 `has_more=false` 是合法组合，UI 提示"结果可能不完整"而不是"到底了"）。
- `truncated_reason`（可选，`string[]`，`omitempty`）：归因枚举 `depth|scan|budget|dir_entries`，用于区分"预算耗尽"与"深度截断"；本期 UI 只消费布尔 `truncated`，归因字段先用于可观测性与测试断言。
- `scanned` / `elapsed_ms` 仅供可观测性与测试断言，不参与前端逻辑。

#### 4.4.2 扫描与忽略规则

- **遍历策略**：自基础目录起的 BFS（浅层优先）。BFS 保证时间预算优先花在浅层文件上，且空 `q` 时天然产出"浅层优先"的小批量。
- **固定忽略目录**（不进入，大小写不敏感；后续可改为配置）：
  `.git` `.hg` `.svn` `node_modules` `vendor` `dist` `build` `out` `target` `.venv` `venv` `__pycache__` `.next` `.turbo` `.cache` `coverage` `.aicli-uploads`（内部项）。
- **隐藏项**：名字以 `.` 开头的条目在 `show_hidden=false` 时跳过（与 `fs/list` 的 `IsHiddenName` 口径一致）。
- **隐藏目录不进入**：`show_hidden=false` 时，名字以 `.` 开头的**目录**同样不递归进入——否则会返回"父目录不可见、子文件可见"的命中，导航语义断裂；`show_hidden=true` 时允许进入（固定忽略名单仍优先）。
- **符号链接目录**：一律不跟随（避免环、避免越过 `fsscope` 语义边界）；符号链接文件可按 `stat` 结果按需返回（与 `list.go:164-181` 同口径）。
- **单目录条目上限**：单目录超过 2000 项时按名称序截断该目录的枚举（防单个巨量目录吃满预算）。仅当被截掉的条目里存在**可见**条目（非内部项、非隐藏项、非忽略目录）时才置 `truncated=true`；整目录都是隐藏/忽略项时不算遗漏，不误报。
- **`max_depth` 截断归因**：仅当位于深度边界的目录**非空**（用 `ReadDir(1)` 轻量探针判定）时才置 `depth`；边界层空目录不会遗漏任何条目，不误报。归因去重，命中一次后不再重复探测。
- **取消与计时**：每处理 256 个条目检查一次 `ctx.Err()`；`budget_ms` 自"作用域解析完成、开始枚举"起计时（不含路径校验），超预算立即收尾并返回已收集结果；前端 abort 后服务端尽快退出。

#### 4.4.3 打分与排序（模糊匹配）

大小写不敏感（比较前统一 `strings.ToLower`；不做 Unicode NFC/NFD 归一化，标准库限制，见 §7-R9）；路径分隔符 `/` 与 `-`/`_`/`.` 作为词边界。打分维度（从高到低，建议初始权重，实施时以测试表固化）：

| 命中形态 | 分值 | 说明 |
|---|---|---|
| 文件名完整相等 | 1000 | `q == name` |
| 文件名前缀 | 900 − 位置惩罚 | `composer-…` 命中 `composer` |
| 文件名词边界前缀（camelCase/`-`/`_` 分段） | 850 | `useComposerMenu` 命中 `menu` 的段 |
| 文件名子串 | 700 − 位置惩罚 | |
| 路径段前缀/段相等 | 600 | 中间目录名命中 |
| 路径子串 | 500 | |
| 文件名子序列（fzf 式） | 300 × 覆盖率 − 间隔惩罚 | 覆盖率高、间隔小者优先 |
| 路径子序列 | 200 × 覆盖率 − 间隔惩罚 | |
| 无命中 | 丢弃 | 不做"猜" |

- 调节项（实现口径）：查询越短、命中位置越靠前加分；不做独立的"路径深度/总长"调节项——路径总长只通过子序列档位的**覆盖率**（`权重 × 覆盖率 − 间隔惩罚`）体现。
- 排序稳定性：`score desc → 类型(file 优先) → path asc`（路径比较大小写不敏感 + 原串兜底，构成全序），保证同一目录树两次请求结果一致、游标可复现；`mtime` **不参与**非空 `q` 的排序（只作空 `q` 的次键）。
- 空 `q`：不做打分，按 `(depth asc, mtime desc, path asc)` 返回浅层文件小批量，只扫描到收集满 `limit × 4` 或 `depth ≤ 2` 即停，不触发全量扫描。

#### 4.4.4 游标与分页

- 游标为 base64url(JSON) 的**复合键**：`{ v:2, scope, q, base, last:{score,path,depth,mtime,type} }`，`v` 版本化（对齐 `list.go` 的 `listCursorVersion` 纪律）。`last` 字段与排序键一一对应，因此树在两次请求之间变化、游标项消失时，续页仍按同一全序定位起点，不会因次要键误判而整段漏项；旧版本游标一律 400 `cursor_invalid`（前端按「重置到第一页」处理，见下条）。
- 翻页 = 重扫 + 跳过 `last` 之前的已返回项（树未变化时结果稳定），**不承诺快照一致性**：树在两次请求之间变化允许重复或遗漏，重复项由前端按 `path` 去重（对齐 `applyListingPage` 的去重口径，`use-file-browser.ts:57-76`）。
- 游标与当前 `scope/q/base` 不匹配（用户换了查询或作用域却复用旧游标）→ 400 `cursor_invalid`；前端重置到第一页而不是用同一游标重试。
- composer 场景**不需要**"加载更多"交互：菜单每组最多渲染 8 条，`has_more=true` 只用于页脚提示"结果较多，继续输入以缩小范围"；面板场景需要"加载更多"（§4.7.4）。

#### 4.4.5 限额与性能

| 项 | 默认 | 上限 | 备注 |
|---|---|---|---|
| `limit` | 20 | 50 | 前端请求 20 |
| `max_scan` | 20000 | 50000 | 对齐 `ScanMaxEntries` |
| `max_depth` | 8 | 16 | BFS 浅层优先 |
| `budget_ms` | 250 | 1000 | 超预算置 `truncated=true` 提前返回 |
| 单目录枚举 | 2000 | — | 超限截断该目录并标记 |
| 结果缓存（P2 可选） | 目录列表 TTL 10–30s / LRU 2048 目录 | — | 本期先"无状态扫描 + 预算兜底"，避免索引一致性问题 |

- 扫描全是本地 `os.ReadDir`，无网络；`stat` 只在需要打分/展示 `mtime/size` 时对候选做（或直接复用 `DirEntry.Info()`）。
- 前端防抖 200ms + `AbortController`：连续输入只会有最后一次请求真正完成。
- **并发决策**：不引入全局扫描锁（避免 head-of-line blocking）；同一前端请求串行（防抖 + abort），服务端以 `ctx` 取消与三重上限兜底。多标签同时搜索是"多份有界扫描"，不共享状态、互不影响；若真实使用中出现叠加压力，P2 再评估按作用域的在途闸。

#### 4.4.6 安全校验

- 复用 `fsscope.Resolver.ResolvePath`（README 级顺序：URL 解码 → 统一分隔符 → Clean → Join → 越界校验），绝对路径直接 400 `path_must_be_relative`。
- 递归枚举**不跟随符号链接目录**；返回的 `path` 一律相对作用域根。
- 错误响应统一走既有 `fsWriteReadError` 渲染（`fs_browser_handlers.go`），不新造错误体格式。

### 4.5 前端接入设计

#### 4.5.1 新增 hook：`useComposerFileReferences`

建议位置：`frontend/src/hooks/workspace/composer/use-composer-file-references.ts`（与 composer 其余 hook 同目录）。

```ts
export type ComposerFileReferencesOptions = {
  sessionId?: string;
  /** 当前 `@` 查询串；由菜单状态回调驱动（见 §4.5.2）。 */
  query: string;
  /** 菜单处于 references 模式且打开时才请求；关闭时保留上次结果并停止请求。 */
  enabled: boolean;
};

export type ComposerFileReferencesResult = {
  group: ComposerReferenceGroup | null;   // id="workspace-files"
  status: "idle" | "loading" | "ready" | "error";
  hasMore: boolean;
  truncated: boolean;
  scope: string | null;
  error: unknown;
};
```

行为纪律（对齐 `use-file-browser.ts` 的既有竞态口径）：

1. `sessionId` 变化：解析/复用作用域（§4.2），清空结果，作废在途请求。
2. `query` 变化：200ms 防抖；请求序号 + `AbortController`，只有最新序号的响应写回；`AbortError` 不落 error 态。
3. 请求参数：P0 用 `fs/list`（query 为空），P1 用 `fs/search`（query 非空）；`limit=20`。
4. 失败保留上一次成功结果（输入不闪空），`status="error"` 供 UI 提示；`fs/roots` 不可用时不发搜索请求，`group=null` 交由宿主回退 artifacts 分组。
5. 结果映射：`label=name`、`description=path`、`insertText=path`；过滤 `internal`、`inaccessible`、`unknown`。
6. P1 的远端搜索委托给共享 `useFsSearchQuery`（§4.7.4）：本 hook 只负责作用域解析、参数差异（`limit=20`、`kinds=file`）与结果到引用组的映射；防抖/竞态/分页纪律只有一份实现。

#### 4.5.2 菜单模型的最小扩展（additive，不破坏既有调用方）

1. `ComposerReferenceGroup` 新增可选字段（`lib/composer-menu.ts`）：
   - `status?: "loading" | "ready" | "error"`；
   - `serverFiltered?: boolean`（true = 本组结果已由服务端过滤/排序，**禁止**客户端再做 `rankMatch` 与顺序重排）；
   - `hasMore?: boolean` / `truncated?: boolean`（渲染页脚提示用）。
2. `buildReferenceLeafGroup`：当 `serverFiltered === true` 时跳过 `rankMatch` 过滤与 `sortByRank`，直接按服务端顺序取前 `COMPOSER_MENU_MAX_ITEMS_PER_GROUP` 条（保留空组剔除逻辑）。
3. `buildReferenceLauncherGroup`：允许 `status === "loading"` 的空组进入 launcher（渲染"加载中"），避免"打开菜单瞬间闪空"。
4. `useComposerMenu` 新增 `onMenuStateChange?: (state: { open: boolean; mode: ComposerMenuMode; query: string }) => void`，在 `trigger`/`manual`/`query` 变化时回调（用 `useEffect` 收敛，避免渲染期调 setState）。`MessageComposer` 透传该 prop；`WorkspaceMainSection` 持有 `query` 状态并传给 `useComposerFileReferences`。
5. `WorkspaceMainSection` 组装 `referenceGroups = [workspaceFiles 组（若有）, artifacts 组（兜底）]`；切换会话时 hook 内部已处理作废，无需额外重置。

> 备选：把查询串提升为受控参数（`useComposerMenu({ query })`）。不推荐——会改动既有 draft/触发器单一数据流，回调方案改动面最小。

#### 4.5.3 状态与降级矩阵

| 场景 | 展示 |
|---|---|
| 首屏加载中 | 分组 launcher 显示"工作区文件 · 加载中…"（`status=loading`） |
| 首屏就绪、有文件 | 叶子列表（≤8 条），选中插入 `@relative/path` |
| 首屏目录多、文件少 | 空态文案"输入关键字搜索子目录文件" |
| 搜索中（输入变化） | 保留旧结果 + 轻量 busy 标记（避免列表闪烁） |
| 搜索无命中 | 空态"未找到匹配文件"，不回退为"发送普通消息"（`@` 模式语义不变） |
| `truncated=true` | 页脚"结果已截断，继续输入以缩小范围" |
| `fs/*` 不可用（404/405/501/503） | 文件分组隐藏，仅 artifacts 分组 + 一次性提示 |
| 其它错误 | 保留上次结果 + 错误提示 + 可重试（下次输入自动重试） |

#### 4.5.4 i18n（`frontend/src/i18n/resources/{zh-CN,en-US}/workspace/*`）

| key | zh-CN | en-US |
|---|---|---|
| `composer.references.workspaceFiles` | 工作区文件 | Workspace files |
| `composer.references.workspaceFiles.loading` | 正在读取工作区文件… | Loading workspace files… |
| `composer.references.workspaceFiles.empty` | 未找到匹配文件，可继续输入缩小范围 | No files match; keep typing to narrow down |
| `composer.references.workspaceFiles.error` | 工作区文件不可用 | Workspace files unavailable |
| `composer.references.workspaceFiles.truncated` | 结果已截断，继续输入以缩小范围 | Results truncated; keep typing to narrow down |
| `composer.references.artifacts`（既有组重命名，可选） | 会话产物 | Session artifacts |

### 4.6 与线程产物分组的关系

- `artifacts` 分组继续存在：它是"本次会话产物"（`session-history-*.json`、`turn-*.json`、工具产物），与"工作区文件"是不同语义，UI 上分组隔离即可，不做合并。
- 顺序建议：`workspaceFiles` 在前（高频），`artifacts` 在后（低频/兜底）。
- 现有 `artifactReferenceGroup` 的 `COMPOSER_REFERENCE_ARTIFACT_LIMIT=12` 与 `rankMatch` 客户端过滤对产物组仍然适用（产物数量小），无需改成 `serverFiltered`。

### 4.7 第二消费方：右侧文件浏览器搜索扩展（v2 新增）

> 结论先行：这是**叠加**能力，不是替换。既有 `filterText` 的"即时过滤已加载层"语义原样保留（零请求、离线可用、既有断言不变）；远端搜索结果作为独立数据通道呈现；`/fs/search` 不可用时自动退回现状（§4.7.5）。

#### 4.7.1 现状与约束取证

| 事实 | 证据位置 |
|---|---|
| 过滤词 `filterText` 由面组件持有，注释明确"只作用于已加载层（P4-5/Q3），不发请求、不做全树搜索" | `frontend/src/components/workspace/file-browser-surface.tsx:55-56` |
| 过滤输入框在 `ScopeHeader`；文案"过滤（仅已加载层）…" / "只过滤已加载的目录项：不触发全树搜索…" | `frontend/src/components/workspace/file-browser/scope-header.tsx:154-177`；`frontend/src/i18n/resources/zh-CN/workspace/panels-file-browser.ts:30-33` |
| 过滤在 `buildTreeRows`/`filterEntries` 内做（仅 `entry.name.includes(needle)`），只对已加载层生效 | `frontend/src/lib/file-browser/entry-sort.ts:116-133,175-207` |
| 既有测试固化"过滤收窄已加载层且**不发新请求**" | `frontend/src/components/workspace/file-browser-surface.test.tsx:262-289` |
| 选中/预览只认 `entriesByPath`（已加载层 entry）；取不到就没有预览 | `frontend/src/components/workspace/file-browser-surface.tsx:120-134,176` |
| 预览按 `scope + path` 取数，不依赖树：任意合法文件路径都能预览 | `frontend/src/components/workspace/file-browser/preview-pane.tsx:48-84` |
| `useFileBrowser` 只有"进目录/展开"（`enterDir`/`toggleDir`/`loadListing`），**没有 reveal/定位 API** | `frontend/src/hooks/workspace/use-file-browser.ts:253-281` |
| 面板本身已有 `activeScope`（默认会话根），搜索无需再做一遍作用域解析 | `frontend/src/components/workspace/file-browser-surface.tsx:49,63,65-101` |

#### 4.7.2 交互模型选型

| 方案 | 做法 | 结论 |
|---|---|---|
| A. 结果替换树视图 | query 非空且远端结果就绪后，树区域切换为扁平"搜索结果"列表；清空/返回恢复目录树；loading 与降级期间仍显示本地过滤后的树 | ✅ 采纳（Q7 决策 2026-09-17）：空间利用最好、状态可解释、树与结果互不挤压；P1 最终形态 |
| B. 树 + 独立结果区 | 树上加一段固定高度的结果区 | ❌ 否决（Q7 决策）：右栏是 `3fr 树 / 2fr 预览` 的紧凑网格（`file-browser-surface.tsx:394`），第三区会把树/预览压到不可用 |
| C. 悬浮 popover | 输入框下浮层 | ❌ 右栏宽度 <460px 且 `overflow-hidden`，定位/遮挡/键盘焦点风险高 |
| D. 远端搜索替换本地过滤 | 输入即发请求，本地过滤移除 | ❌ 破坏既有即时语义与 `file-browser-surface.test.tsx:262-289` 断言；离线/端点缺失直接不可用 |

#### 4.7.3 合成规则（本地过滤 vs 远端结果）

| 状态 | 树区域呈现 |
|---|---|
| `query` 为空 | 目录树（现状，完全不变） |
| `query` 非空 + 远端 pending（无上次结果） | 本地过滤后的树 + 顶部"搜索中…"行（零延迟反馈保留） |
| `query` 非空 + 远端就绪 | 扁平搜索结果列表（含目录项）；清除/返回/Esc 恢复树 |
| `query` 非空 + 远端失败/不可用 | 本地过滤后的树 + 一次性提示（不可用时不重试；失败可重试） |
| 远端空结果 | 结果视图空态"未找到匹配文件"（与树的"已加载层无匹配"区分文案） |
| `truncated=true` | 结果区页脚"结果可能不完整（已扫描 N 项）" |
| `has_more=true` | 结果区页脚"加载更多"（游标追加、按 path 去重） |

#### 4.7.4 组件与 hook 改动

1. **新增共享 hook** `frontend/src/hooks/workspace/use-fs-search.ts`（`useFsSearchQuery`），双消费方共用防抖/竞态/分页纪律：

```ts
export type UseFsSearchQueryOptions = {
  scope: string;
  query: string;
  enabled: boolean;
  /** 防抖由调用方选择：composer 200ms（输入快）/ 面板 300ms（结果重）。 */
  debounceMs?: number;
  limit?: number;                          // composer 20 / 面板 50
  kinds?: "file" | "dir" | "both";         // composer file / 面板 both
  showHidden?: boolean;
};

export type UseFsSearchQueryResult = {
  items: FsSearchItem[];
  status: "idle" | "loading" | "ready" | "error";
  hasMore: boolean;
  truncated: boolean;
  nextCursor: string | null;
  error: unknown;
  loadMore: () => void;   // 追加 + 按 path 去重
  retry: () => void;
};
```

   纪律：序号 + `AbortController`（只有最新响应写回，`AbortError` 不落 error）；`scope`/`query`/`showHidden`/`kinds` 变化重置并作废在途；失败保留上次成功结果；`cursor_invalid` 重置第一页而不是死循环。`useComposerFileReferences`（§4.5.1）改为包装该 hook，P0 的 `fs/list` 分支保留。

2. **面板接线**（`file-browser-surface.tsx`）：
   - 新增搜索状态入口：`query = filterText.trim()`；`enabled = query.length > 0`；`kinds="both"`（文件浏览器需要目录结果）；`limit=50`；`showHidden=browser.showHidden`。
   - `ScopeHeader` 文案修订（placeholder/hint 同时说明"即时过滤"与"停顿后全库搜索"）+ 新增"清除搜索/返回目录树"按钮（Esc 亦清空）。
   - 新增 `frontend/src/components/workspace/file-browser/search-results.tsx`：扁平结果列表（图标 + 名称 + 相对路径 + 可选命中高亮）、加载更多、空/错/截断态、键盘 ↑/↓ + Enter；不做虚拟化（≤50/页）。
   - 结果项动作：`file` → 选中并预览——用搜索结果**直接构造 `FsEntry`**（`path/type/size/mtime/ext` 均来自响应），预览按 `scope+path` 取数，**不等待树加载**；`dir` → `enterDir(path)` 并退出搜索；`symlink/inaccessible/unknown` → 只选中并显示类型标注，不伪装成可预览/可进入。
   - 选中来源收口：面组件用 `selection: { entry: FsEntry; source: "tree" | "search" }` 统一预览来源，树内多选/右键菜单行为不变；`scope` 变化时沿用既有清理 effect，一并清空 search 选中与上次结果（`file-browser-surface.tsx:185-189`）。
   - 搜索结果视图内**不接入**多选/右键/传输（避免两套选中集合纠缠）；下载/复制路径入口 P2 再评估。

3. **排序与隐藏开关**：搜索结果始终按相关度（服务端），`sortKey` 不适用——搜索视图内禁用/隐藏排序控件并标注原因，避免"点了排序没变化"；`showHidden` 变化使搜索重发（参数生效）。

4. **`useFileBrowser` 不改**：搜索状态不塞进"目录层状态机"；P1 不做树定位（reveal），搜索选中直接预览（Q8 决策 2026-09-17：直接预览、不动树；reveal API 列入 §5-P2）。

#### 4.7.5 降级矩阵（面板）

| 场景 | 展示 |
|---|---|
| `/fs/search` 404/405/501/503（`isFsSearchUnavailable`） | 保留本地过滤（现状），一次性提示"远端搜索不可用，仅过滤已加载层"；不再重试 |
| 搜索失败（500/网络） | 保留上次结果或退回本地过滤 + 可重试提示 |
| `query` 过短/纯空白 | 视为空查询，只做本地过滤，不发请求 |
| 作用域切换 | 在途请求 abort、结果与选中清空，回到树视图 |

#### 4.7.6 i18n 与用户文档同步

- 新增 `panels.fileBrowser.search.*`：`aria` / `placeholder` / `loading` / `empty` / `error` / `unavailable` / `truncated` / `loadMore` / `exit` / `resultDir`（zh-CN 与 en-US 对称）。
- **修订**既有 `panels.fileBrowser.scope.filterPlaceholder/filterHint`：过滤仍是即时本地过滤，但输入停顿后会触发全库搜索；文案必须同时说明两件事（"说了不做/做了没说"都算缺陷）。
- 用户可见行为变化 → 按 `docs/development-guidelines.md` §10 同步：当前 `docs/user-guide/` 没有 GUI/文件浏览器章节（已取证：仅 CLI 类文档），需要产品确认新增小节或随 release notes 说明；作为 P1-11 交付物跟踪。

#### 4.7.7 影响面清单（回归风险）

| 影响点 | 说明 |
|---|---|
| `file-browser-surface.test.tsx:262-289` | "过滤不发请求"断言**必须继续通过**（本地过滤仍在）；新增用例覆盖"防抖后发搜索请求/结果渲染/降级" |
| `tree-list.tsx` / `entry-sort.ts` | 不改：搜索结果视图是并列新组件，树投影与本地过滤保持原样 |
| `scope-header.tsx` | 只加退出按钮与文案修订；输入仍是受控上抛，不引入请求 |
| `use-file-browser.ts` | 不改：目录层状态机与搜索状态解耦（避免作用域/排序参数与搜索参数互相污染） |
| 预览区 | 预览来源扩为 `tree \| search`；`PreviewPane` 接口不变（仍吃 `FsEntry`） |
| git 角标 / 传输 / 右键菜单 | 不受影响；搜索结果视图本期不显示角标、不接入传输 |

---

## 5. 分阶段落地与任务拆分

### P0：首屏小批量（纯前端，可独立交付）

| 任务 | 内容 | 依赖 |
|---|---|---|
| P0-1 | `useComposerFileReferences`：作用域解析（`fs/roots`+`pickDefaultRoot`，按 sessionId 缓存/作废）+ 首屏 `fs/list` 小批量 + 竞态取消 | — |
| P0-2 | 菜单模型：`ComposerReferenceGroup.status/serverFiltered/hasMore/truncated` + launcher loading 分支（`composer-menu.ts` 及其测试） | — |
| P0-3 | 接线：`UseComposerMenuOptions.onMenuStateChange` → `MessageComposer` prop → `WorkspaceMainSection` 状态；组装双分组 | P0-1/P0-2 |
| P0-4 | i18n 键 + 空态/错误态渲染 + `truncated` 提示 | P0-2/P0-3 |
| P0-5 | 单测：hook（roots 降级/竞态/空目录）、菜单模型（serverFiltered 跳过过滤、loading launcher）、插入文案（含空格引号） | P0-1..P0-4 |

P0 验收：打开 composer 输入 `@`，无需输入关键字即可看到工作区根层文件小批量（不含 session 产物）；断网/端点 503 时输入框仍可用且保留产物分组。

### P1：模糊搜索

| 任务 | 内容 | 依赖 |
|---|---|---|
| P1-1 | 后端 `filebrowse.Search`：BFS 有界扫描 + 忽略目录/深度/扫描量/时间预算 + 打分排序 + 游标 | — |
| P1-2 | 路由与注入：`FSBrowserService` 新增 `Search`，`RegisterFSBrowserRoutes` 注册 `/fs/search`，nil 注入 503 | P1-1 |
| P1-3 | 后端测试：打分表驱动（前缀/子串/子序列/路径命中）、忽略与限额、深度/预算截断、游标翻页稳定性、越界与符号链接、隐藏项 | P1-1/P1-2 |
| P1-4 | 前端 `api/runtime/fs-search.ts` + 类型 + 归一化 + `fetchFsSearch`（与 `fs-list.ts` 同纪律） | P1-2 |
| P1-5 | hook 接入搜索（包装共享 `useFsSearchQuery`，防抖/abort/序号/失败保留），`serverFiltered=true` 分组下发 | P1-4 |
| P1-6 | UI 状态（搜索中/无命中/截断页脚）+ i18n + 单测 | P1-5 |
| P1-7 | E2E（Playwright）：`@` → 首屏批量 → 输入关键字 → 结果收敛 → 选中插入正确路径 | P0/P1-6 |
| P1-8 | 共享 hook `useFsSearchQuery`（防抖/abort/序号/分页/去重），composer 与面板搜索共用（§4.7.4） | P1-4 |
| P1-9 | 面板搜索视图：`ScopeHeader` 文案/退出按钮 + `search-results.tsx` + 选中预览/进入目录 + 与本地过滤的合成规则（§4.7.2/§4.7.3） | P1-8 |
| P1-10 | 面板降级：`/fs/search` 不可用回退"仅本地过滤"+ 一次性提示；`truncated/has_more` 页脚（§4.7.5） | P1-9 |
| P1-11 | 面板 i18n（zh/en）+ 用户文档/文案同步（§4.7.6）+ 面板单测（§6.2） | P1-9 |

P1 验收：输入 `compmenu` 能命中 `frontend/src/lib/composer-menu.ts`（子序列/子串均可）；扫描受限时响应仍 ≤ 预算并显式 `truncated=true`；前端输入期间无重复渲染整列表的闪烁。面板验收：右栏文件浏览器输入关键字 → 停顿后出现跨目录结果（含子目录文件）→ 选中文件出现预览；清空/退出/Esc 恢复目录树；`/fs/search` 未实现时行为与现状一致（仅本地过滤）且无报错噪音。

### P2（可选，另行立项评审）

- 目录项下钻 / "最近使用文件" / 按 git 变更加权（工作区已具备 `/git/status`）。
- 目录列表 TTL 缓存或符号索引（需先解决一致性与内存上限）。
- 读取 `.gitignore` 作为忽略规则（替代固定名单）。
- `@` 引用在后端展开为受控文件上下文（提示词工程范畴，需单独评审）。
- 搜索结果 reveal：点击结果时把目录树定位/展开到该文件（需给 `useFileBrowser` 新增 reveal API，含多级展开与竞态清理）——Q8 决策后列入。

---

## 6. 测试计划

### 6.1 后端

- 单元（`filebrowse` 包，沿用 `service_test.go` 的临时目录夹具风格）：
  - 打分：表驱动断言顺序（精确名 > 前缀 > 词边界 > 子串 > 子序列；路径命中降权）；
  - 忽略：`node_modules`/`.git` 等不进入；`show_hidden=false` 跳过点文件；`.aicli-uploads` 永不返回；
  - 限额：`max_depth`、`max_scan`、`budget_ms`、单目录 2000 截断 → `truncated=true`；
  - `q` 字面匹配：含 `*`/`(`/`[` 等字符不触发正则/通配语义；`q` 超 256 → 400 `query_too_long`；
  - 隐藏目录：`show_hidden=false` 时 `.config` 下的文件不被命中（父目录不可见则子文件不返回）；
  - `truncated_reason` 归因：`depth`/`scan`/`budget`/`dir_entries` 各构造一例；
  - `kinds=both`：目录命中正常返回，且与文件命中按统一打分排序；
  - 游标：同树重复请求结果一致；`cursor` 篡改/版本不符 → `cursor_invalid`；
  - 安全：`path=../..`、绝对路径、编码绕过、符号链接逃逸 → 既有 `fsscope` 错误码；
  - 空 `q` 首屏：浅层优先、不做全量扫描。
- Handler：参数夹紧（`limit>50`、`budget_ms>1000`）、nil 注入 503、错误体格式与 `/fs/list` 一致。

### 6.2 前端

- `fs-search` 归一化：缺 `items` 抛错、`next_cursor` 非串归 null、`has_more` 缺省 false、未知 `type` 收口 `unknown`（与 `fs-list.ts` 同款用例）。
- `useComposerFileReferences`：roots 不可用降级；sessionId 切换作废；连续输入只有最后响应写回；abort 不落 error；失败保留旧结果。
- `useFsSearchQuery`：防抖合并（连续输入只有最后响应写回）、abort 不落 error、分页追加按 `path` 去重、`cursor_invalid` 重置第一页、`scope/showHidden/kinds` 变化重置在途。
- `composer-menu`：`serverFiltered` 组跳过本地过滤与重排；loading launcher；空组剔除；`hasMore/truncated` 透传。
- 插入：`@path`、含空格 `@"path with space"`（既有 `composerReferenceText` 用例补齐引用组场景）。
- 组件：`message-composer` 的 `onMenuStateChange` 回调时序（打开/输入/关闭）。
- 面板：既有"过滤不发请求"用例保持通过；新增"停顿后发 `/fs/search` 并渲染结果/空态/截断/has_more/降级"用例；搜索结果选中进入预览（不依赖树加载）；目录结果进入目录并退出搜索；`scope` 切换清空搜索状态与在途请求。

### 6.3 E2E

Playwright（参考 `frontend/e2e` 既有工作区用例）：新会话 → 输入 `@` → 断言首屏候选来自工作区（非 `session-history-*.json` 独占）→ 输入关键字 → 选中 → 断言草稿文本为 `@<相对路径>`。

面板场景：右栏文件浏览器输入关键字 → 跨目录结果 → 选中文件出现预览 → 清空恢复树；端点 503/未实现时输入过滤仍生效（无请求、无报错噪音）。

---

## 7. 风险与缓解

| # | 风险 | 缓解 |
|---|---|---|
| R1 | 大仓扫描超时/CPU 抖动 | BFS + 深度/扫描量/时间三重上限；超限显式 `truncated=true`；前端 abort 即时取消 |
| R2 | 菜单打开瞬间闪空/闪烁 | loading launcher 占位 + 搜索期间保留旧结果 + 200ms 防抖 |
| R3 | `serverFiltered` 改动引入菜单回归 | additive 字段，缺省行为与现状完全一致；为两分支各补单测 |
| R4 | 作用域解析错误（列到非预期目录） | 只信 `fs/roots` 返回的 `scope`；不拼接绝对路径；`exists=false` 跳过；错误码可观测 |
| R5 | 搜索命中排序不符合直觉 | 打分表固化到测试；先实现简单权重，按真实仓库反馈迭代（不改协议） |
| R6 | Windows 路径/大小写差异 | 统一 `/` 输出；比较逻辑大小写不敏感（与 `fsscope.pathWithin` 的 Windows 口径一致）；测试覆盖盘符与含空格路径 |
| R7 | 结果里包含敏感文件名（如 `.env`） | 默认隐藏点文件；`show_hidden` 仅前端显式开启（本期不开启）；路径不回显绝对路径 |
| R8 | 面板"本地过滤 + 远端搜索"双重语义造成用户困惑 | 文案同时说明两件事（§4.7.6）；搜索结果视图提供显式"返回目录树"；本地过滤行为不变，避免"输入变卡/闪空" |
| R9 | Unicode 规范化：标准库无 NFC/NFD 归一化，macOS 等 NFD 文件名可能与 NFC 输入不匹配 | 匹配前统一 `ToLower`；已知限制写入测试注释与文档；P2 再评估（当前 `filebrowse` 包纪律不允许引入 `x/text`） |
| R10 | 双消费方同时搜索造成扫描叠加 | 两端各自有界（预算/扫描量/深度）+ abort；不做全局锁避免排队；必要时 P2 增每作用域在途闸 |

---

## 8. 开放问题（实施前需确认；Q7/Q8 已决策）

> Q1–Q6 仍待实施前确认；Q7/Q8 已于 2026-09-17 决策（采纳下文推荐项），条目保留题面与结论以留痕。

1. **Q1 首屏"小批量"的选取策略**：根层文件（本方案默认）还是"最近修改的浅层文件"？若仓库根几乎无文件，首屏可能接近空——可接受（继续输入即搜）或补"最近文件"。
2. **Q2 目录项语义**：是否需要目录下钻（选中目录 = 限定后续搜索范围）？若需要，建议新增菜单 action kind（如 `{kind:"browse-dir"}`）并单独设计键盘路径；本期默认目录不进入候选。
3. **Q3 忽略规则来源**：固定名单（本方案）vs 读取 `.gitignore`（P2）。若仓库依赖目录命名特殊（如 `backend/dist`），固定名单 + 单目录截断已能兜底。
4. **Q4 产物分组命名**：`composer.references.files` 当前指向产物组；引入"工作区文件"后建议把产物组改名（§4.5.4），是否接受用户可见文案变化？
5. **Q5 搜索是否需要"最近会话文件"权重**（例如优先展示本会话工具刚读写过的文件）？这需要前端记录或后端事件关联，属 P2。
6. **Q6 端点命名**：`/fs/search` 与既有 `/skills/search` 命名一致；若担心与"内容搜索"混淆，可用 `/fs/find`。本方案暂定 `/fs/search`。
7. **Q7 面板搜索结果呈现**：✅ **已决策（2026-09-17）**——采纳"结果替换树视图 + 显式返回"（§4.7.2-A）；P1 不做分屏/独立结果区（右栏 `3fr 树 / 2fr 预览` 紧凑网格不具备第三区空间）。
8. **Q8 搜索结果与树的联动**：✅ **已决策（2026-09-17）**——P1 不做树定位/展开联动、不新增 reveal API；点击文件结果直接构造 `FsEntry` 进入预览（预览按 `scope+path` 取数、不依赖树）；reveal 定位列入 P2 评估（§4.7.4、§5-P2）。

---

## 9. 完整性审查与 v2 修订记录（v2 新增）

### 9.1 审查方法

对照 11 个维度逐项复核，每条结论必须能落到契约字段、代码位置或测试用例：目标/非目标、接口契约、扫描与排序、安全与权限、错误模型、限额与性能、竞态与取消、降级矩阵、测试计划、任务拆分与依赖、文档同步（`docs/development-guidelines.md` §10）。

### 9.2 复核结论

**已覆盖**：作用域解析与复用（§4.2）、首屏小批量（§4.3）、端点契约（§4.4.1）、扫描/忽略/预算（§4.4.2）、打分排序（§4.4.3）、游标分页（§4.4.4）、安全校验（§4.4.6）、composer 前端接入与状态矩阵（§4.5）、产物分组关系（§4.6）、测试与风险（§6/§7）。

**v2 补齐的缺口（G1–G10）**：

| # | 缺口 | 修订位置 |
|---|---|---|
| G1 | `q` 无长度上限、未声明字面匹配（正则/通配歧义） | §4.4.1（≤256、`query_too_long`、字面匹配） |
| G2 | `truncated` 与 `has_more` 语义未拆分，UI 无法区分"不完整"与"到底" | §4.4.1 补充语义、§4.7.3 |
| G3 | 隐藏目录是否下钻未定义 | §4.4.2 |
| G4 | 游标跨变更语义（重复/遗漏、去重责任）与"加载更多"适用范围未写明 | §4.4.4、§4.7.4 |
| G5 | 新错误码命名与既有 `fs_list_failed` 不一致 | §4.4.1（`fs_search_failed`） |
| G6 | Unicode 规范化限制（NFD/NFC）未识别 | §4.4.3、§7-R9 |
| G7 | 双消费方缺少共享层设计，易出现两套竞态/防抖实现 | §4.7.4（`useFsSearchQuery`）、§5-P1-8 |
| G8 | 未设计右侧文件浏览器搜索扩展（本次用户要求） | §4.7 全节、§5-P1-9..P1-11 |
| G9 | 扫描截断/超时的归因不可观测 | §4.4.1（`truncated_reason`） |
| G10 | 用户可见行为变化未安排文档/文案同步 | §4.7.6、§5-P1-11、附录 B |

### 9.3 v2 修订记录（相对 v1）

1. 新增 §4.7（右侧文件浏览器搜索扩展）与 §9（本审查）。
2. §4.4 契约补充：`q` 上限与字面匹配、`truncated/has_more` 正交语义、`truncated_reason`、`fs_search_failed`/`query_too_long`。
3. §4.4.2/§4.4.4/§4.4.5 补充隐藏目录、游标漂移与去重、取消粒度（每 256 项）与并发决策。
4. §5 增补 P1-8..P1-11；§6/§7/§8/附录 A/B 同步扩充。
5. 明确"一端点两消费方"的共享纪律（§2.3、§4.7.4）。

### 9.4 实施前复核清单（gate）

- [x] Q7/Q8 已决策（2026-09-17，§8）；Q1–Q6 中与本迭代相关的决策仍待确认。
- [ ] `fs/search` 契约评审通过（含错误码与限额），且未改动既有 `/fs/*` 契约。
- [ ] 面板既有断言（"过滤不发请求"）与新用例同时在位。
- [ ] 用户文档/文案同步方案已确认（§4.7.6）。
- [ ] 性能预算在本仓库真实规模上抽测（`frontend` + `backend` 目录）并记录 `elapsed_ms/scanned`。

### 9.5 Q7/Q8 决策回写（v2.1，2026-09-17）

1. **Q7（结果呈现）**：采纳 §4.7.2-A"结果替换树视图 + 显式返回"；不采用分屏/独立结果区（右栏紧凑网格无第三区空间）。
2. **Q8（树联动）**：P1 不做定位/展开联动、不新增 reveal API；点击文件结果直接构造 `FsEntry` 预览（预览按 `scope+path` 取数、不依赖树）；reveal 列入 §5-P2。
3. 同步位置：头部版本 v2.1、§4.7.2、§4.7.4、§5-P2、§8、§9.4 gate。

---

## 附录 A：关键代码索引

| 位置 | 作用 |
|---|---|
| `frontend/src/components/workspace/workspace-shell/main-section.tsx:194-201` | 当前 `@` 分组唯一装配点（P0 接线处） |
| `frontend/src/lib/composer-references.ts:8,11-29` | 产物 → 引用组映射（保留为 artifacts 组） |
| `frontend/src/lib/composer-menu.ts:9,96-105,173-219,263-329` | 菜单模型：每组 8 条、子串过滤、launcher/leaf 构建（P0-2 改造点） |
| `frontend/src/lib/composer-trigger.ts:30-75,77-100` | `@` 几何判定与插入（不改） |
| `frontend/src/hooks/workspace/composer/use-composer-menu.ts:98-206,220-266` | 菜单 controller；查询串持有处（P0-3 增加回调） |
| `frontend/src/components/workspace/message-composer.tsx:132-140` | 菜单 hook 装配（透传回调） |
| `frontend/src/api/runtime/fs-roots.ts:91-137` | 作用域根客户端与选择策略（复用） |
| `frontend/src/api/runtime/fs-list.ts:24-144` | 单层列表客户端与归一化纪律（复用/P0 数据源） |
| `frontend/src/hooks/workspace/use-file-browser.ts:146-214` | 竞态/游标状态机（P0-1 借鉴） |
| `frontend/src/components/workspace/file-browser-surface.tsx:49,55-56,63,120-134,176,185-189` | 面板过滤词/作用域/选中预览收口（§4.7 改造点） |
| `frontend/src/components/workspace/file-browser/scope-header.tsx:154-177` | 过滤输入框（文案与"退出搜索"改造点） |
| `frontend/src/lib/file-browser/entry-sort.ts:116-133,182-207` | 面板本地过滤实现（保持不动的既有语义） |
| `frontend/src/components/workspace/file-browser-surface.test.tsx:262-289` | "过滤不发请求"既有断言（必须保持通过） |
| `frontend/src/i18n/resources/zh-CN/workspace/panels-file-browser.ts:30-33` | 过滤文案（需随行为变化修订） |
| `frontend/src/hooks/workspace/use-fs-search.ts`（新增） | 双消费方共享搜索 hook（P1-8） |
| `frontend/src/components/workspace/file-browser/search-results.tsx`（新增） | 面板搜索结果视图（P1-9） |
| `backend/internal/api/skills/fs_browser_routes.go:33-49` | `/fs/*` 路由注册与注入接口（P1-2 扩展点） |
| `backend/internal/filebrowse/list.go:27-126` | 单层列表实现（P1-1 的结构参考） |
| `backend/internal/filebrowse/service.go:55-85,128-152` | 限额与 Service 构造（P1-1 复用） |
| `backend/internal/fsscope/scope.go:32-53,148-172` | 作用域/路径安全与错误码（P1-1 复用） |

## 附录 B：与既有文档/契约的边界

- 本方案**不修改** `workspace-right-panel-file-browser-and-git-diff-plan.md` 定义的 `/fs/list`、`/fs/roots`、`/fs/stat`、`/fs/preview`（含传输类端点）的请求/响应契约；只新增 `/fs/search` 只读端点。
- 本方案**不改变** `@` 引用作为纯文本进入消息的协议；不新增后端"引用解析"依赖。
- 右侧文件浏览器既有 `filterText`（仅过滤已加载层）行为**保留**；本方案只叠加远端搜索，属于**用户可见行为扩展**——需同步修订 i18n 文案与用户文档（§4.7.6），既有"过滤不发请求"断言不得回归。
- 若后续决定做"引用展开注入文件内容"，必须单独出方案并评审提示词预算/安全边界（见 P2）。

---

## 10. 实施记录（2026-09-17）

> **状态更新**：§5 的 P0 与 P1（P1-1..P1-11）已实施；P2 仍未立项。下面按任务记录落地位置与**实测**验证证据（命令与输出均为真实执行结果，未执行的项不写）。

### 10.1 后端（P1-1..P1-3）

| 任务 | 落地位置 | 验证证据 |
|---|---|---|
| P1-1 | `backend/internal/filebrowse/search.go`：BFS 有界扫描 + 忽略名单 + `max_depth`/`max_scan`/`budget_ms`/单目录 2000 截断 + 打分（精确名 > 前缀 > 词边界 > 子串 > 子序列，路径命中降权）+ v2 复合游标（`last:{score,path,depth,mtime,type}`，base64url 版本化，见 §10.5） | `go test ./internal/filebrowse/...` → `ok github.com/wwsheng009/ai-agent-runtime/internal/filebrowse 2.790s` |
| P1-2 | `FSBrowserService` 新增 `Search`（`filebrowse/service.go`）；`backend/internal/api/skills/fs_browser_routes.go` 注册 `GET /api/runtime/fs/search`（nil 注入沿用 503）；参数夹紧与错误体复用既有 `fsWriteReadError` | `backend/internal/api/skills/fs_browser_search_handlers_test.go`；`go test ./internal/api/skills/...` → `ok ... 21.999s`；`go build ./...`（workdir `backend/`）退出码 0 |
| P1-3 | `backend/internal/filebrowse/search_test.go`：打分表驱动、忽略与限额、`truncated_reason` 归因、游标翻页一致性/篡改、越界与符号链接、隐藏项 | 同上两条测试命令 |

### 10.2 前端 composer（P0、P1-4/P1-5/P1-6/P1-7/P1-8）

| 任务 | 落地位置 | 验证证据 |
|---|---|---|
| P0-1 | `frontend/src/hooks/workspace/composer/use-composer-file-references.ts`：`fs/roots` → `pickDefaultRoot` 解析作用域（按 sessionId 缓存 5min、切换作废）+ `fs/list` 根层小批量（`limit=20`）+ 请求序号/AbortController 竞态纪律 | `use-composer-file-references.test.tsx`（13 tests） |
| P0-2 | `frontend/src/lib/composer-menu.ts`：`ComposerReferenceGroup.status/serverFiltered/hasMore/truncated` + launcher loading 分支（`serverFiltered` 组跳过客户端 `rankMatch` 与重排） | `composer-menu.test.ts`（19 tests） |
| P0-3 | `use-composer-menu.ts` 的 `onMenuStateChange` → `message-composer.tsx` prop → `workspace-shell/main-section.tsx` 状态；组装「工作区文件（前）+ 线程交付物（后兜底）」双分组 | `message-composer-menu.test.tsx`（14 tests）、`message-composer.test.tsx`（12 tests） |
| P0-4 | i18n `composer.references.workspaceFiles*`（zh-CN/en-US `workspace/base.ts`）+ 空态/错误/截断渲染 | E2E 用例 2（空态文案）；`composer-trigger.test.ts`（11 tests，含空格引用加引号） |
| P1-4 | `frontend/src/api/runtime/fs-search.ts`：类型 + 归一化（缺 `items` 抛错、`next_cursor` 非串归 null、未知 `type` 收口 `unknown`）+ `fetchFsSearch`（scope 空直接拒绝、可选参数省略空串） | `fs-search.test.ts`（12 tests） |
| P1-8 | `frontend/src/hooks/workspace/use-fs-search.ts`：防抖/abort/序号/分页去重/`isFsSearchUnavailable` 粘性降级判定，composer 与面板共用 | `use-fs-search.test.tsx`（11 tests） |
| P1-5 | composer hook 接入共享搜索：query 非空走 `fs/search`（防抖 200ms、`limit=COMPOSER_FILE_REFERENCE_LIMIT`、`kinds=file`），远端结果以 `serverFiltered=true` 下发；端点不可用粘性回退 P0 首屏 + 客户端过滤（首屏每作用域只补拉一次） | `use-composer-file-references.test.tsx` 4 条 P1 用例（请求参数/慢响应后到被丢弃/失败保留旧结果并置 error/404 粘性降级） |
| P1-6 | 菜单 UI 状态：搜索中（`data-composer-menu-group-status`）、无命中（`data-composer-menu-group-note`）、截断页脚（`data-composer-menu-group-truncated`）与 i18n 文案 | `message-composer-menu.test.tsx`；E2E 用例 2 断言无命中空态文案且不伪造候选 |
| P1-7 | `frontend/e2e/composer-file-reference.spec.ts`（2 用例）+ `e2e/mock-server.mjs` 的 `/api/runtime/fs/search` 夹具（命中刻意落在子目录 `src/lib/composer-menu.ts`，并支持游标翻页） | `npx playwright test composer-file-reference.spec.ts` → **2 passed (8.6s)**：`@` 首屏给 `workspace-files` 分组 → 输入关键字**真实打到** `fs/search`（断言 `scope`/`kinds=file`/空 query 不打端点）→ 根层未命中项消失 → 点选后草稿为 `@src/lib/composer-menu.ts`、菜单收起 |

### 10.3 前端文件浏览器面板（P1-9..P1-11）

| 任务 | 落地位置 | 验证证据 |
|---|---|---|
| P1-9 | `frontend/src/components/workspace/file-browser/search-results.tsx` 受控扁平结果列表（命中高亮按 rune 偏移切分、↑/↓+Enter、加载更多、空/错/不可用/截断态，不虚拟化）；`.../search-match.ts`（切分工具独立成文件——组件文件不得导出工具函数）；`.../search-view.tsx`（结果视图外壳：标题、降级/提示条、退出入口） | `search-results.test.tsx`（10 tests：rune 偏移、命中列、键盘、目录行走、Esc、空/错/不可用、`truncated` 与 `hasMore` 正交、选中态） |
| P1-10 | `.../use-browser-search.ts`（搜索状态与降级判据，复用 `isFsSearchUnavailable` / `FS_SEARCH_LIMIT_PANEL` / `FS_SEARCH_DEBOUNCE_PANEL_MS`）；`file-browser-surface.tsx` 按 §4.7.4 接线（`query=filterText.trim()`、`enabled=query.length>0`、`kinds="both"`、`showHidden` 透传、防抖 300ms；选中收口 `{ entry, source: "tree" \| "search" }`——`file` 用响应直构 `FsEntry` 预览不等树、`dir` → `enterDir`+退出搜索、`symlink/inaccessible/unknown` 只选中并标注）；`file-browser/scope-header.tsx` 清除搜索/返回目录树 + Esc + 排序禁用与 `sortHint` | `file-browser-surface.test.tsx`（15 tests，既有「过滤不发请求」保留）；`npx vitest run src/components/workspace/file-browser src/components/workspace/file-browser-surface.test.tsx` → **5 files / 50 passed** |
| P1-11 | i18n `panels.fileBrowser.search.*`（`zh-CN`/`en-US` 对称 17 键：`aria/placeholder/loading/empty/error/errorUnknown/unavailable/truncated/loadMore/loadingMore/retry/exit/resultDir/resultSymlink/resultInaccessible/resultUnknown/sortHint`；en-US 由 `satisfies DeepStringShape` 编译期对齐） | `npx tsc --noEmit -p tsconfig.json` 退出码 0（形状对齐）；面板用例断言降级/空态/截断文案 |
| 降级与边界 | 404/405/501/503 → 退回本地过滤 + 一次性提示，**不再发第二次请求**且不给重试入口；其它失败（500）可重试并在重试后恢复；结果视图按方案不接多选/右键/传输（`selectedPaths` 只对树行有意义） | 面级用例：不可用（404）与可重试（500）两条路径 |

> 实施偏差（已在代码注释登记）：① 为守住单文件 500 非空行上限，把批量下载/复制路径动作与右键菜单项从 surface 抽出为 `use-browser-file-actions.ts` / `use-row-menu-items.ts`，面级测试共享脚手架抽到 `frontend/src/test/file-browser-surface-harness.tsx`；② `retained` 上传重试入口由 `useMemo(() => readRetainedUploads(), [retainedToken, transfer.uploads])` 改为渲染期直读 `readRetainedUploads()`（消除新版 `react-hooks` 的「多余依赖」告警，`dismissUpload` 内部 `setUploads` 触发重渲染，行为等价）。

### 10.4 回归口径（本次执行）

- 前端单测：`npx vitest run`（workdir `frontend/`）全量 **296 files / 2411 tests passed**。
- 前端静态检查：`npx tsc --noEmit -p tsconfig.json` 与 `npm run build`（`tsc -b && vite build`）退出码均 0；`npx eslint .` → 0 error / 2 warning（两条均为既有文件的 `react-hooks/exhaustive-deps`：`artifact-detail-dialog.tsx:50`、`composer-model-panel.tsx:317`，非本次改动）。
- E2E：`npx playwright test composer-file-reference.spec.ts` → 2 passed (12.0s)。
- 后端：`go build ./...` 退出码 0；`go test ./internal/filebrowse/... ./internal/api/skills/... -count=1` → `ok filebrowse 2.877s` / `ok api/skills 22.081s`。

### 10.5 缺口修复记录（2026-09-17，第二轮：独立审查发现项）

> 触发：对本方案的独立只读审查（结论：无 blocker / 无 major）列出的 minor、nit 与测试缺口。以下为逐项落地。

| 发现项 | 级别 | 落地 |
|---|---|---|
| 游标续页比较键与排序键不一致：空 q / 同分文件-目录边界下，游标项消失（树变化）时可能整段漏项 | minor（正确性边界） | `afterSearchCursor` 复用与 `sortSearchCandidates` 一一对应的比较键（`searchCandidateAfterCursor`）；游标升 v2（`last:{score,path,depth,mtime,type}`），v1 一律 400 `cursor_invalid`（§4.4.4 已同步） |
| 截断归因假阳性：`dir_entries` 在过滤前判定，整目录都是隐藏/忽略项也报截断 | minor（噪声） | 仅当被丢弃条目里存在可见条目（`anyPossiblyVisibleDropped`）才置位（§4.4.2 已同步） |
| 截断归因假阳性：`depth` 对深度边界层的**空**目录也置位 | minor（噪声） | 边界层目录用 `ReadDir(1)` 轻量探针判非空后才置位；归因去重避免重复探测（§4.4.2 已同步） |
| 文档漂移：`mtime` tie-break 与"深度/路径长度调节项"与实现不符 | nit（文档） | §4.4.3 改写为"实现口径"：`mtime` 只作空 q 次键；不做独立深度/长度调节项（路径总长经子序列覆盖率体现） |
| 游标编码失败时悄然产出 `has_more=true` 且无游标 | nit（契约自洽） | 编码失败即不置 `has_more`，对外不产生矛盾组合 |
| `scanned` 跨页累加语义、`rootCache` TTL 兜底、E2E 语言依赖 | nit | 代码/测试注释登记（不改变行为） |

新增回归用例（`backend/internal/filebrowse/search_test.go`）：

- `TestSearchCandidateAfterCursorOrdering`：续页比较键逐条对齐排序键（空 q：depth/mtime/path；非空 q：score/文件优先/path）。
- `TestSearchEmptyQueryCursorSurvivesRemovedItem`：游标项被删除后仍按同一全序续页（旧实现会跳过 `aa.txt`）。
- `TestSearchBaseSubPathScopesResults`：`path` 限定子树 + 结果 `path` 仍相对作用域根（含 base 前缀）+ 游标与 base 绑定（换 base → `cursor_invalid`）。
- `TestSearchDepthTruncationIgnoresEmptyBoundaryDir` / `TestSearchDirEntriesTruncationSkipsInvisibleOverflow`：两条假阳性回归。
- `TestSearchCursorInvalidAndStable` 改 v2 载荷，新增 `negative depth` / `invalid item type` 拒绝分支。

验证（本轮全量门禁）：

- 后端：`gofmt -l internal/filebrowse` 空输出、`go vet ./internal/filebrowse` 退出码 0、`go build ./...` 退出码 0、`go test ./internal/filebrowse ./internal/api/skills -count=1` → `ok filebrowse 2.637s` / `ok api/skills 19.444s`。
- 前端：`npx tsc --noEmit -p tsconfig.json` 退出码 0、`npx eslint .` → 0 error / 2 warning（两条均为既有文件，非本次改动）、`npm run build` 退出码 0、`npx vitest run` → **297 files / 2413 tests passed**。
- E2E：`npx playwright test composer-file-reference.spec.ts --reporter=list` → 2 passed (9.5s)，用例内 0 console error / 0 failed request。
