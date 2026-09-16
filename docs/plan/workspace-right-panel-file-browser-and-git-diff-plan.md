# 右侧可扩展面板：文件浏览器与 Git 变更浏览器 规划

> 状态：**规划中（尚未实施）** —— 本文只做设计与排期，不含代码改动
> 日期：2026-09-16
> 涉及端：frontend（React 19 + Vite 8 + Tailwind v4 + vitest/Playwright）、backend（Go runtime-server，gorilla/mux，`:8101`）
> 目标页面：`/workspace/chats/new`（右侧栏）
> 关联文档：`workspace-directory-management-implementation-plan.md`（工作目录注册表，`/workspace-directories`）
> 关联既有实现：`use-file-preview.ts`、`api/runtime/files.ts`、`artifact-panel/*`、`internal/filetransport/*`

## 0. 变更记录与阅读导航

| 日期 | 变更 | 章节 |
| --- | --- | --- |
| 2026-09-16 | 规划初稿：需求拆解、已核验现状基线、关键设计决策、前端/后端详细设计、接口契约、分期实施、测试验收、风险、自审 | §1–§11 |
| 2026-09-16 | **v2 修订**：① 右侧栏宽度由「两档预设」升级为**用户可拖拽的可变宽度 + 自适应（auto）默认**（重写 D2、§4.2，新增 §4.2.1 交互细则与 §4.2.2 文件清单）；② §11.2 全部待确认问题**按最优建议拍板**并转写为决策记录；③ 新增宽度相关风险、验收项与代码位置索引 | §0、§1.1、§1.2、D2、§3、§4.2、§4.7、§7 P0-2、§8.2、§9、§10、§11 |
| 2026-09-16 | **v3 修订（未跟踪 / 删除文件处理）**：① 未跟踪文件在 status 中按真实文件内容统计新增行数，读不到结论时用 `-1` + `warnings[]` 如实降级（**禁止** `+0 −0` 伪装）；② `target=working` 下未跟踪文件用 `git diff --no-index -- /dev/null <file>` 合成新增 diff（退出码 1 按「存在差异」正常处理）；③ 请求目标对这条路径无改动而另一侧有改动时回退取数并回报 `target_fallback`（UI 必须标注显示的是哪一侧）；④ 新增未跟踪行数扫描的**单文件上限 + 单次请求总预算** | §0、§5.5、§5.6、§5.8、§5.10、§7 P3-3、§8.2 |
| 2026-09-16 | **v4 修订（diff 行号列合并）**：diff 行号列由「老 + 新」**两列合并为一列**（`add`→新侧、`del`→老侧、`context`→新侧，**不借用对侧数字**）；split 每侧各保留一列；合并只发生在展示层 —— `old_no`/`new_no` 契约、split 配对与行 aria 文案不变（读屏仍能分辨增删行） | §0、§1.1、§4.6、§8.2 |

**阅读导航**

- 要理解「为什么这么设计」：§1 需求 → §2 事实基线 → §3 关键设计决策（D1–D9）
- 要动手实现：§4 前端设计 → §5 后端设计 → §6 契约映射 → §7 分期计划
- 要评审/验收：§7 验收标准 → §8 测试策略 → §9 风险 → §10 代码位置索引 → §11 自审与决策记录（Q0–Q8 已拍板）

---

## 1. 需求与目标

### 1.1 需求拆解（用户原话 → 可交付项）

| 用户原话 | 可交付项 |
| --- | --- |
| 「先放在页面右侧，可以放在现有的右侧栏」 | 复用现有右侧栏（`WorkspaceRightRailSection`），不新开一列 |
| 「需要增加可扩展面板的功能」 | 右侧栏页签体系从硬编码 4 个面升级为**注册表驱动**，新增面不改核心组件 |
| 「右侧栏宽度调整为可变，用户可调整面板宽度，自适应宽度」 | 右栏宽度**连续可调**：拖拽分隔条（含键盘/双击复位）+ 宽度持久化 + **auto 自适应**（按当前面类型与视口计算，用户一旦拖拽即转 manual）；主区最小宽度硬保护，视口不足时降级覆盖层；详见 D2/§4.2 |
| 「文件浏览器…不能一次加载所有文件，像真正文件浏览器一样」 | 目录**逐层懒加载** + 游标分页 + 虚拟滚动；不做全树预取 |
| 「文件上传、下载」 | HTTP 上传（分片）与下载（Range 流式）端点 + 右侧栏进度 UI |
| 「大文件续传」 | 上传：分片 + 服务端 offset 校验 + 断线/刷新后按 offset 续传；下载：`Range`/ETag + 可选 File System Access API 续传 |
| 「不同类型文件，md/文本文件直接渲染预览」 | 类型探测 → 文本/markdown/代码/图片/二进制/超限 分流渲染，复用既有 `file-preview` 解码层 |
| 「git 变更渲染器参考 github.com」 | GitHub 风格变更视图：变更列表（分组 + 状态徽标 + 增删行数）+ unified/split diff + 折叠上下文 + 行号（v4：unified **单列**行号） |
| 「还有对应的后端的相关接口」 | 新增 `/api/runtime/fs/*`（浏览/预览/传输）与 `/api/runtime/git/*` 端点，含契约、错误码、限额 |

### 1.2 目标能力清单

| 能力 | 说明 | 阶段 |
| --- | --- | --- |
| 可扩展面板 | 右侧栏面（surface）注册表：`id / 标题 / 图标 / 懒加载组件 / 所需宽度 / 是否依赖会话` | P0 |
| 可变宽度右栏 | 拖拽调宽（pointer capture + rAF 节流）+ 键盘可调 + 双击复位；`auto` 按面自适应、`manual` 记住用户宽度；主区最小宽度保护 + `<xl` 覆盖层 | P0 |
| 作用域选择 | 文件/Git 面板顶部选择「作用域根」：已注册工作目录、当前会话 `workspace_path`、其 Git 仓库根 | P0 |
| 目录浏览 | 逐层加载、游标分页、排序（目录优先/名称/大小/时间）、隐藏文件开关、面包屑、键盘导航 | P0–P1 |
| 文件预览 | markdown 渲染、文本/代码高亮（prismjs）、图片缩略、二进制与超限如实降级 | P1 |
| 下载 | 流式下载 + `Range`/ETag 支持 + 大文件续传（Chromium 走可续传路径，其他浏览器降级为普通下载） | P2 |
| 上传 | 分片上传、并发可控、暂停/继续、断点续传、整文件校验、冲突策略（失败/覆盖/重命名）、进度与速度 | P2 |
| Git 变更列表 | `git status --porcelain=v2` 解析：暂存/未暂存/未跟踪/冲突分组，重命名、增删行数、二进制标记 | P3 |
| Git diff 渲染 | 结构化 hunks（服务端解析）+ unified/split、折叠上下文、空白忽略、巨大 diff 截断提示 | P3 |
| Git 历史（可选） | 最近提交列表，点击查看该提交 diff | P3（可裁剪） |
| Git 写操作 | stage/unstage（P4，**本期实现**）；commit **本期不做**（Q4） | P4 |

### 1.3 非目标（本期明确不做）

1. **不做多用户权限体系**：runtime-server 仍是单机信任模型；本期的「作用域」是**防误操作与防越界的 UI 安全边界**，不是多租户隔离（详见 §5.2）。
2. **不做文件编辑写回**：文件浏览器一期**只读**（预览/下载）；编辑与保存需要独立设计（冲突检测、外部编辑漂移、未保存草稿），不在本期。
3. **不做文件删除/重命名/新建目录**：P4 之后再评估（涉及 Git 工作区一致性）。
4. **不做 Git 冲突解决 UI**、不做三方合并编辑器、不做 PR/远端仓库浏览。
5. **不做移动端专属交互**：`xl` 以下沿用「隐藏右侧栏」策略，文件/Git 只在大屏可用（与现状一致）。
6. **不做本地缓存/离线**：不缓存文件内容到 IndexedDB（除上传续传需要的元数据）。

---

## 2. 现状事实基线（逐条核验过源码，含位置）

> 本节所有结论均由实际读源码/检索得出，不是推测；实施前如相关文件已变更需重新核对。

### 2.1 前端右侧栏

| 事实 | 证据位置 | 对规划的影响 |
| --- | --- | --- |
| 右侧栏是**单一可折叠列**，内含「条目 / 计划 / 还原 / 会话用量」4 个页签 | `components/workspace/workspace-shell/right-rail-section.tsx:1-2,40-59` | 文件与 Git 面板应作为**新增页签**并入同一列，而非新列 |
| 开合状态：`rightRailOpen = !isNewThread && rightRailManualOpen`，`rightRailManualOpen` 初值来自 `settings.workspace.autoOpenArtifacts`，且该设置变化时同步重置 | `components/workspace/workspace-shell.tsx:157-162,226-230` | 新增页签不得改变既有开合语义（既有测试会回归） |
| 列宽**固定**：`xl:grid-cols-[16rem_minmax(0,1fr)_18rem]`，关闭时退化为两列 | `components/workspace/workspace-shell.tsx:251-257` | 18rem（288px）对 diff 不可用 → 改为 CSS 变量承载的**连续可调宽度**（D2 / §4.2）；默认 `auto` 下内容型面仍为 288px |
| 已有 `ResizeObserver` 在监听 shell 容器尺寸 | `components/workspace/workspace-shell.tsx:193-197` | `auto` 宽度的视口重算**复用**该监听，不新增 observer |
| 列在 `xl` 以下不渲染（`hidden … xl:flex`） | `right-rail-section.tsx:41` | 文件/Git 面板只承诺大屏；小屏降级有先例 |
| 面板体经 `PanelErrorBoundary`（key 含 sessionId）+ `Suspense` 懒加载，加载态有 fallback | `right-rail-section.tsx:42-57` | 新面板必须复用同样的错误边界 + 懒加载，避免新面板异常打穿整列 |
| 顶栏开关按钮：`data-testid="topbar-toggle-right-rail"`，图标 `PanelRight*Icon` | `workspace-shell-topbar.tsx:70-77,311-323` | 可加「复位宽度（回自适应）」入口，但必须保留既有 testid 与开合语义 |

### 2.2 前端面板与页签结构

| 事实 | 证据位置 | 影响 |
| --- | --- | --- |
| 面（surface）是**字面量联合类型硬编码**：`"artifacts" \| "checkpoints" \| "plan" \| "usage"` | `components/workspace/artifact-panel/types.ts:6` | 必须改造成注册表/数据驱动，「可扩展面板」的落点（D1） |
| 页签 id 由 `ArtifactPanelSurfaceTabIds` 显式枚举（8 个 id，含 panel/tab 两两配对） | `artifact-panel/types.ts:18-27` | 扩展后需给出 id 生成规则，避免手写爆炸 |
| 页签栏按 tone 分支写死 4 套样式（artifact/plan/usage/checkpoint 各一段 `cn(...)`） | `artifact-panel/surface-tabs.tsx:20-60+` | tone → 样式需收敛为映射表；新增 file/git 两个 tone |
| 面板按面拆分文件 + 独立懒加载出口 | `artifact-panel.tsx:11-22`、`artifact-panel/lazy-surfaces.tsx`、`artifact-panel-checkpoint-surface.tsx`、`artifact-panel-plan-surface.tsx` | 新面板照此模式：`file-browser-surface.tsx` / `git-surface.tsx` + lazy 出口 |

### 2.3 前端既有文件预览能力（可直接复用，不要重造）

| 事实 | 证据位置 | 影响 |
| --- | --- | --- |
| 已有只读文件读取客户端：`POST /api/runtime/fs/read-file`，`FILE_PREVIEW_MAX_BYTES = 1_000_000`，响应归一化 + `isFileReadUnavailable`（404/405/501/503） | `api/runtime/files.ts:1-27,36-100` | 预览端点可复用同一降级判据；但**整文件 base64 + 1MB 上限**不适合文件浏览器（需要新的分块/截断预览端点） |
| 已有解码层：base64 → 字节，NUL/非 UTF-8 → 二进制，空文件单列，行数口径明确 | `lib/file-preview/decode.ts:1-50` | **直接复用**：新增预览端点只需把「字节来源」换成新响应结构，解码/降级纪律不变 |
| 已有预览状态机：`closed → loading → ready/error`，请求序号 + `AbortController` 防竞态，取消不落错误态，超限 `tooLarge` 不渲染 | `hooks/workspace/use-file-preview.ts:1-70` | 文件浏览器预览**照同一状态机口径**实现，避免二次设计 |
| 已有预览弹层 UI + 专属 i18n 模块（zh-CN/en-US 成对，编译期对齐） | `components/workspace/file-preview-dialog.tsx`、`i18n/resources/{zh-CN,en-US}/workspace/panels-file-preview.ts:1-34` | 文件浏览器内联预览可与弹层共存；新增文案必须两语言同步 |
| 已有 e2e：`frontend/e2e/file-preview.spec.ts` | 同名文件 | 新特性需同等 e2e 覆盖（上传续传、diff 渲染） |
| 高亮依赖 `prismjs`，markdown 依赖 `react-markdown` + `remark-gfm`/`remark-breaks` | `frontend/package.json:19-34` | **无虚拟滚动库、无 diff 库** → 需自研轻量虚拟列表 + diff 渲染器，或显式引入依赖（D8） |

### 2.4 后端 HTTP 与文件传输

| 事实 | 证据位置 | 影响 |
| --- | --- | --- |
| runtime API 前缀常量 `canonicalRuntimeEntrypoint = "/api/runtime"`，子路由 `router.PathPrefix(...).Subrouter()` | `internal/api/skills/handler.go:71,650-652` | 新端点挂在同一 subrouter 下 |
| 既有文件端点（全部 `POST` + JSON body）：`/fs/read-file`、`/fs/write-file`、`/fs/append-file` | `handler.go:705-707` | 新浏览/传输端点建议用 REST 风格（`GET` + 查询参数 / `PUT` chunk），与既有点**并存**，不复用命名 |
| `FileTransferService` 接口仅 3 个方法：`ReadFile / WriteFile / AppendFile`（全量字节）；未注入服务时返回 **503 + ErrConfigInvalid** | `internal/api/skills/file_transfer_handlers.go:14-18,36-40` | 新能力需要**新接口 + 新服务**，不要往这个接口里塞分片/diff |
| 读文件响应结构：`{file:{path, data_base64, byte_count}}`；写 200 / 追加 202，`WriteResult` 含 `Action: create\|overwrite` | `file_transfer_handlers.go:57-63,111-113`；`internal/filetransport/service.go:159-164` | 新响应结构沿用「`{file:{...}}` 包裹 + `path` 回传解析后绝对路径」惯例 |
| 路径解析**无根目录约束**：`filepath.Abs(filepath.Clean(path))` | `internal/filetransport/service.go:122-131` | 老端点保持现状（agent/工具链依赖）；**新端点必须加作用域校验**（D3 / §5.2） |
| 写入会 `os.MkdirAll(filepath.Dir(absPath))`，返回 `sizeBefore/created` | `filetransport/service.go:133-157` | 上传落盘可复用「先建父目录 + 报告 create/overwrite」语义 |
| 服务注入点：`handler.SetFileTransferService(filetransport.NewLocalService())` | `cmd/runtime-server/main.go:993` | 新服务同样在 `main.go` 注入 |
| 路由装配：`router := mux.NewRouter(); router.UseEncodedPath(); handler.RegisterRoutes(router)`，最后 `PathPrefix("/")` 兜底到 webui | `main.go:1104-1112` | 新路由必须在 webui 兜底**之前**注册，否则会被静态资源吃掉 |
| 后端测试范式：`httptest` + `mux.NewRouter()` + `handler.RegisterRoutes(router)` + `t.TempDir()` | `internal/api/skills/file_transfer_handlers_test.go:19-53` | 新 handler 测试照此写 |
| 工作目录注册表：`GET/POST/PATCH/DELETE /api/runtime/workspace-directories`，`workspaceregistry.Load()` 懒加载，503 表示注册表不可用 | `internal/api/skills/workspace_directory_handlers.go:36-70` | **作用域根的第一来源**；前端已有 `api/runtime/workspace-directories.ts` 与 `use-runtime-workspace-directories.ts` |

### 2.5 Git 现状：完全空白

| 事实 | 证据 | 影响 |
| --- | --- | --- |
| 后端**没有任何 Git 集成**：全仓检索 `rev-parse` / `git diff` / `git status` 只命中「命令行字符串」与测试夹具（如 `internal/agent/tool_runtime_events_test.go` 里的 `git status` 文本），没有 git 执行模块 | `grep` 结果（`internal/agent/*`、`internal/background/*`） | Git 后端是**全新模块**，需要自建进程执行沙箱、解析、超时与限额 |
| 已有 diff 先例：checkpoint 文件记录了 `Path/Op/BeforeBlobID/AfterBlobID/BeforeHash/AfterHash/DiffText`，blob 按 sha256 去重 | `internal/artifact/checkpoint_files.go:15-57` | 说明「**服务端产出 diff 文本、前端只渲染**」在本仓已有先例；同时提示未来可把 checkpoint diff 与 Git diff 用同一个渲染器 |
| 前端已有 checkpoint 面板与共享工具（文件 op 归一化等） | `components/workspace/artifact-panel-shared.ts:126-133`、`artifact-panel-checkpoint-surface.tsx` | diff 渲染器应做成**可复用组件**，先给 Git 用，后续可替换 checkpoint 的展示 |

### 2.6 仓库工程约束（会直接影响实现方式）

| 约束 | 证据 | 影响 |
| --- | --- | --- |
| **`frontend/src` 单文件 ≤ 500 非空行**，超限即 `npm run lint` 失败 | `frontend/scripts/verify-max-lines.mjs:23-26,60-88` | 文件浏览器与 diff 渲染器必须**按职责切小文件**，禁止单文件堆功能（D9） |
| i18n 有校验脚本，zh-CN/en-US 必须成对 | `frontend/package.json:9-10`（`verify-frontend-i18n.ts`） | 新增文案两语言同时落地 |
| 存在「禁止备份文件」校验 | `frontend/scripts/verify-no-backups.mjs` | 不要用 `.bak` 之类中间产物 |
| Windows 命令长度限制（`cmd.exe` 8191 / CreateProcess 32767，工程上单命令 ≤ 6000 字符） | `AGENTS.md` | 大文件改动用「骨架 + 分块补丁」，不要一次性超长内联命令 |
| 设置持久化：`core/settings/local.ts` 的 `workspace.{density,autoOpenArtifacts,sessionOrder,sessionGrouping}`，含 `normalize*` 容错与默认值 | `core/settings/local.ts:96-100,135-139,205-209,289-293` | 宽度偏好加入同一 schema（本期新增 `rightRailWidthMode` / `rightRailWidthPx`，含 normalize + 默认值），不散落 localStorage |

---

## 3. 关键设计决策（D1–D9）

每条决策给出：结论 → 理由 → 被否决的方案 → 影响面。

### D1 面板扩展机制：注册表 + 懒加载，而不是继续加 `if/else`

- **结论**：把右侧栏「面」抽象为数据：

  ```ts
  // 计划新增：components/workspace/panel-registry.ts
  export type WorkspacePanelSurfaceId =
    | "artifacts" | "checkpoints" | "plan" | "usage"   // 既有
    | "files" | "git";                                  // 新增

  export type WorkspacePanelSurfaceSpec = {
    id: WorkspacePanelSurfaceId;
    /** i18n key（workspace.shell.panelTabs.<id>），不是字面量文案 */
    labelKey: string;
    icon: ComponentType<{ size?: number }>;
    tone: "artifact" | "plan" | "usage" | "checkpoint" | "file" | "git";
    /** 该面是否依赖当前会话（usage/checkpoints 依赖；files/git 只依赖作用域） */
    requiresSession: boolean;
    /** 宽度类别：驱动 auto 模式下的自适应宽度（content = 288px；wide = clamp(0.32×vw, 416, 672)） */
    widthClass: "content" | "wide";
    /** 懒加载出口；返回默认导出组件 */
    load: () => Promise<{ default: ComponentType<WorkspacePanelSurfaceProps> }>;
    /** 未满足前置条件时给出可解释的禁用原因 key（如无会话/无仓库） */
    disabledReasonKey?: string;
  };
  ```

- **理由**：现状是 4 处硬编码（联合类型 + tab id 结构 + 4 段 tone 样式 + 面板分发）。再加文件与 Git 会变成 6 处重复；注册表让「新增一个面」= 新增一个文件 + 注册一行。
- **否决**：① 直接 `if/else` 加两个分支（不可扩展，用户明确要求可扩展）；② 通用插件系统（运行时可插拔、跨仓加载）——过度设计，本仓是单仓前端。
- **影响**：`artifact-panel/types.ts` 保留 `ArtifactPanelSurface` 作兼容别名；`surface-tabs.tsx` 的 tone 分支收敛为 `Record<tone, string>` 映射（顺带降低该文件行数）。

### D2 宽度策略：连续可调（拖拽 + 键盘）+ `auto` 自适应默认，主区最小宽度硬保护

- **结论**：
  1. 宽度是**连续值（px）**，不是档位枚举。单一状态源 `rightRailWidth`，渲染层统一通过 CSS 变量注入：`xl:grid-cols-[16rem_minmax(0,1fr)_var(--right-rail-width,18rem)]`（Tailwind 侧保持静态字面量，运行时只改变量，避免动态类名）。
  2. **两种模式** `mode = "auto" | "manual"`，默认 `auto`：
     - 内容型面（`artifacts` / `plan` / `checkpoint` / `usage`）→ **18rem（等于现状值，零视觉回归）**；
     - 宽内容面（`files` / `git`）→ `clamp(0.32 × viewportWidth, 26rem, 42rem)`：视口越宽面板越宽，但有上下限；
     - 视口尺寸变化时 auto **实时重算**，复用 `workspace-shell.tsx:193-197` 已有的 `ResizeObserver`（不新增监听）。
  3. 用户一旦拖拽 → `mode = "manual"` 并持久化 `widthPx`；**双击手柄 / 手柄上 `Enter` / 设置页「恢复自适应」按钮**回到 `auto`（保留 `widthPx` 不删除，便于再次切回 manual）。
  4. **主区保护**：`maxWidth = min(52rem, viewportWidth − 16rem(左侧栏) − 32rem(主区最小宽))`；`minWidth = 20rem`。无论来源（拖拽 / auto / 历史持久化值 / 视口缩小 / 设置面板输入）**都必须过同一个 `clampRailWidth()`**。当 `maxWidth < minWidth`（极窄视口）时不进入三列网格，直接走覆盖层。
  5. `<xl` 视口：既有「隐藏右栏」策略不变（`right-rail-section.tsx:41` 的 `hidden xl:flex`），但 files/git 面改为**覆盖层抽屉**（`absolute inset-y-0 right-0` + 半透明遮罩 + `Esc` 关闭），宽度 `min(railWidth, 92vw)`，可拖拽但只在覆盖层内生效，不影响主区网格。
- **理由**：
  - 「可变 + 自适应」是明确需求：固定档位在 1366px 笔记本与 4K 屏上体验差异极大，只有「连续值 + 视口函数」能同时满足；
  - 默认 `auto` 且内容型面锁 18rem → 既有 4 个面的视觉与测试基线完全不变（零回归，可安全先行合并）；
  - 宽度只有 `clampRailWidth()` 一个出口 → 不会出现「拖到 900px 把聊天区压没」这类事故。
- **否决**：① 只做两档预设（不满足「可变/自适应」）；② 拖拽时每帧 `setState`（全树重渲染，diff/预览面必然掉帧）；③ 为每个面单独记忆宽度（状态组合爆炸且用户难以预期，改为单一宽度 + auto 按面计算）；④ 本期连左侧会话栏一起做成可调（超出用户诉求与回归面，见 §1.3；同一 hook 后续可复用）。
- **影响**：`workspace-shell.tsx:251-257` 网格列宽、`right-rail-section.tsx` 容器（注入 CSS 变量 + 挂手柄）、`core/settings/local.ts`（`workspace` 段新增两个标量字段 + normalize）、`workspace-shell-topbar.tsx:70-77`（可加「复位宽度」入口）；`right-rail-section.test.tsx`、`workspace-sidebar-responsive.test.tsx` 必须回归通过。

### D3 作用域模型：所有文件/Git 端点必须显式带 `scope`，禁止裸绝对路径

- **结论**：新端点统一接收 `scope`（`workspace:<directory_id>` | `session:<session_id>` | `cwd`）+ 相对 `path`。服务端把 scope 解析为**允许根**，再把相对路径 join + `Clean` + 解析符号链接后校验仍在根内；越界返回 `400 path_outside_scope`。
- **理由**：
  - 老端点 `resolveTransportPath`（`filetransport/service.go:122-131`）是「任意绝对路径」，直接复用到 UI 会让一次前端 bug 变成全盘浏览/覆盖；
  - 但必须诚实定位：本机 runtime 是**单用户信任模型**，agent 本来就有 shell，所以这层不是强隔离，而是「**能力收敛 + 防误操作 + 让 API 语义可审计**」；
  - 相对路径 + scope 还天然解决了跨平台路径展示问题（Windows 盘符/反斜杠不进 URL）。
- **否决**：① 直接复用裸绝对路径（不可接受）；② 引入 chroot/容器级隔离（超出本期，且与单机模型不匹配）。
- **影响**：新增 `internal/fsscope`（或 `internal/filebrowse/scope.go`）承担解析、校验、根枚举；`/fs/roots` 返回可用根供前端选择器使用。

### D4 目录浏览：一层一请求 + 游标分页 + 虚拟滚动，服务端不全量扫描

- **结论**：
  - `GET /fs/list` 只返回**一层**，按 `type`/`name`/`mtime`/`size` 排序，`limit` 默认 200、上限 1000，用**不透明游标**（`next_cursor`，内部编码最后一项的排序键）而不是 offset；
  - 返回 `truncated` 与 `has_more`，前端列表**虚拟滚动**（固定行高）并按需追问下一页；
  - 服务端**不**统计子目录项数（避免 `ReadDir` 全树扫描）；父路径用 `..` 计算，不做 expensive 统计。
- **理由**：`node_modules` 之类目录单层可达万级；offset 分页在并发变更下会重复/漏项，游标更稳。
- **否决**：① 一次性返回递归树（用户明确否掉）；② 前端 `limit=100000` 兜底（等于全量）。
- **影响**：`fs/list` 契约（§5.3）；前端 `use-directory-listing` 需处理「加载更多 / 刷新后游标失效 → 从头拉取」。

### D5 传输协议：上传自建分片会话，下载分三层降级

**上传（我们完全可控，全部浏览器可用）**

- `POST /fs/upload/init`（申报 size/目标/冲突策略）→ 服务端创建 `.aicli-uploads/<upload_id>.part` 与 sidecar JSON 状态，返回当前 `offset`（**续传即从这里开始**）；
- `PUT /fs/upload/{upload_id}/chunk`，body = 原始字节，头 `Content-Range: bytes start-end/total` + `X-Chunk-Sha256`；服务端**只接受 `start == 当前 offset`**，否则 `409 upload_offset_mismatch` + `{expected_offset}`；
- `POST /fs/upload/{upload_id}/complete`（校验整文件 sha256 → 原子 rename → 报告 `create/overwrite`）；
- `GET /fs/upload/{upload_id}` 续传探测；`DELETE` 中止并清理；
- 续传跨页面刷新：前端把 `{upload_id, name, size, lastModified, offset}` 存 localStorage，刷新后按 `name+size+lastModified` 匹配用户重选的文件。
- 默认分片 4 MiB（可协商 1–8 MiB），并发上限 3（防止把本地盘打满）。

**下载（浏览器能力受限，必须诚实降级）**

| 层级 | 能力 | 浏览器 | 说明 |
| --- | --- | --- | --- |
| Tier A | 可暂停/续传、进度精确 | Chromium（File System Access API：`showSaveFilePicker` + `createWritable({keepExistingData:true})` + 顺序 `Range` 请求） | 大文件主路径 |
| Tier B | 进度可显示、不可续传 | 全部（`fetch` + `ReadableStream` 累积 → Blob → `a[download]`） | 受内存限制，>256MB 直接走 Tier C |
| Tier C | 交给浏览器原生下载 | 全部（`<a href=download-url download>`） | 无进度/无续传，但最稳 |

服务端职责：`GET /fs/download` 支持 `Range`、返回 `Accept-Ranges: bytes`、`Content-Range`、`Content-Length`、`ETag`（`"<size>-<mtime_unix_nano>"`）；`HEAD` 供续传探测。**ETag 变化 = 文件被外部修改**，续传必须作废重下。

- **否决**：① 只做整文件 base64（既有 `fs/read-file` 方式，1MB 级别就崩）；② 只依赖浏览器原生下载并宣称「支持续传」（不可控，等于不实现需求）。

### D6 预览：新增「截断式预览」端点，复用既有解码与状态机

- **结论**：`GET /fs/preview?scope&path&max_bytes=262144&encoding=utf-8`：
  - 文本类（md/txt/json/yaml/代码）：返回 `kind:"text"` + `text` + `truncated` + `line_count`，超限只取头部（并如实告诉前端被截断）；
  - 图片：返回 `kind:"image"` + `mime` + 小尺寸 `data_base64`（服务端不转码，限 2 MiB 以内）；
  - 二进制/非 UTF-8：返回 `kind:"binary"` + `reason`（与 `lib/file-preview/decode.ts` 的 `nul-byte` / `invalid-utf8` 口径**完全一致**）；
  - 超大：`kind:"too_large"` + `size` + `limit`（前端沿用 `tooLarge` 语义，不渲染内容）。
- **理由**：现有 `fs/read-file` 是「整文件 base64 + 前端 1MB 上限」，用于点开单个附件尚可，用于浏览器里随便点文件不可用（内存、延迟、无截断信息）。
- **否决**：把 `fs/read-file` 改造为支持截断（会改变 agent 工具链依赖的语义，风险大于收益）。
- **影响**：`lib/file-preview/decode.ts` 增加「按服务端已给 text」的分支；`use-file-preview.ts` 的状态机口径复用到新 hook `use-file-browser-preview`。

### D7 Git 后端：走 `git` CLI + 严格进程沙箱，不引入 go-git

- **结论**：用 `exec.CommandContext` 调用系统 `git`，参数数组传参（**不**拼 shell 字符串），固定 `-C <root> --no-pager --no-optional-locks`，环境变量：`GIT_OPTIONAL_LOCKS=0`、`GIT_TERMINAL_PROMPT=0`、`LC_ALL=C`、`GIT_PAGER=cat`；每个子命令独立超时（status 5s / diff 15s / log 10s）与输出上限（默认 4 MiB，超限截断并标记）。
- **理由**：① 与用户本地 git 配置、`.gitattributes`、CRLF 处理、hooks、`.gitignore` 语义**完全一致**（go-git 在 diff/rename/ignore 细节上常有偏差，且与「用户看到的 git」不一致会引发争议）；② 仓库本身零 git 依赖（`go.mod` 无需新增）；③ 只读命令风险低。
- **否决**：go-git（语义偏差 + 新增依赖 + 大仓库性能）；`libgit2` cgo（构建复杂度）。
- **风险与对策**：命令注入（→ 参数数组 + 拒绝以 `-` 开头的路径，用 `--` 分隔）；巨型仓库卡死（→ 超时 + context 取消 + 输出截断）；git 不存在（→ 启动时探测一次，`git` 缺失时 `/git/*` 统一返回 `503 git_unavailable`，UI 显示可解释提示，而不是空白）。
- **注意**：`git` 在本开发环境可用（PATH 已确认），但**部署环境可能没有**，必须降级而非报错崩溃。

### D8 diff 渲染与虚拟化：自研轻量实现，不新增依赖（除非实施时证明必要）

- **结论**：
  - **diff 数据面**：服务端解析 unified diff 为结构化 `hunks[]`（`{header, old_start, old_lines, new_start, new_lines, lines:[{type, old_no, new_no, text}]}`），前端**只做渲染**，同时保留 `raw` 原文供「复制原始 diff」；
  - **虚拟化**：以「hunk 行」为虚拟化单位，固定行高 + 窗口化渲染；行数超过阈值（如 2000 行/hunk 或 2 万行/文件）时先渲染视口 + 允许「展开更多」；
  - **高亮**：只对可视行调用 `prismjs`，结果按 `(lang, lineText)` 做 LRU 缓存（复用现有 `code-block` 的高亮就绪判定）。
- **理由**：现存无 `@tanstack/react-virtual`、无 `react-diff-view`；引入 diff 库仍要自己写 GitHub 风格的头部/折叠/空白开关，收益有限。虚拟列表 200 行内可自研（固定行高场景）。
- **否决**：① 直接渲染 `raw` 文本（无行号/无折叠/无法虚拟化）；② 引入 `@git-diff-view/*` 或 `react-diff-view`（体积 + 样式对齐成本 + 与 Tailwind token 体系冲突）。
- **影响**：新增 `components/workspace/diff/`（`virtual-line-list.tsx`、`diff-hunk.tsx`、`diff-file-header.tsx`、`diff-parser.ts` 仅做「服务端结构 → 视图模型」映射）。

### D9 交付纪律：按 500 行预算切文件、按阶段可独立上线

- **结论**：每个新文件目标 ≤ 300 非空行（上限 500，`verify:lines` 卡口）；面板按「出口组件 + 状态 hook + API 客户端 + 子视图」四件套切分；每阶段（P0–P4）结束时功能可用、测试通过、可单独合并。
- **理由**：`verify-max-lines.mjs` 会在 `npm run lint` 阶段直接失败；文件浏览器 + diff 渲染器天然是「大文件重灾区」。
- **影响**：见 §4.9 文件清单与行数预算、§7 分期计划。

---

## 4. 前端详细设计

### 4.1 右侧栏可扩展面板机制

**组件树（改造后）**

```
workspace-shell.tsx                        // 持有 rightRailOpen / width，不变语义
└─ workspace-shell/right-rail-section.tsx   // 容器（hidden xl:flex）+ ErrorBoundary + Suspense
   └─ artifact-panel.tsx                    // 重命名职责：PanelHost（页签栏 + 面分发）
      ├─ artifact-panel/surface-tabs.tsx    // 改为读注册表渲染页签
      ├─ artifact-panel/lazy-surfaces.tsx   // 各面的 lazy 出口（新增 files / git）
      └─ <surface component>                // 各面组件
```

**改造要点**

| 项 | 现状 | 改造 |
| --- | --- | --- |
| 面枚举 | 联合类型硬编码 4 值 | `panel-registry.ts` 导出 `PANEL_SURFACES: WorkspacePanelSurfaceSpec[]`，联合类型由 `typeof` 派生（单一事实来源） |
| tab id | `ArtifactPanelSurfaceTabIds` 手写 8 个字段 | `buildSurfaceTabIds(surfaceId)` 生成 `{panelId, tabId}`；既有 4 个 id 字符串**保持不变**（避免测试与 a11y 属性变化） |
| tone 样式 | 4 段 if | `const SURFACE_TONE_CLASS: Record<Tone, {active: string; idle: string}>` |
| 面分发 | `switch(surface)` | `spec.load()` + `React.lazy`；未激活的面不加载 |
| 会话依赖 | 隐式 | `requiresSession`：无会话时该面显示禁用态 + `disabledReasonKey`，不白屏 |
| 活动面持久化 | 无 | 视需要存 sessionStorage（按会话记忆），失败可忽略（不得因存储异常影响渲染） |

**新增两个面的规格**

| 面 | id | tone | requiresSession | widthClass | 关键依赖 |
| --- | --- | --- | --- | --- | --- |
| 文件浏览器 | `files` | `file` | 否（依赖作用域） | `wide` | `/fs/roots`、`/fs/list`、`/fs/preview`、`/fs/download`、`/fs/upload/*` |
| Git 变更 | `git` | `git` | 否（依赖仓库根） | `wide` | `/git/status`、`/git/diff`、`/git/commits` |

> `auto` 模式的宽度由激活面的 `widthClass` 决定（§4.2）；用户一旦手动拖拽，`widthClass` 不再参与计算，直到双击手柄/`Enter` 复位回自适应。

**界面骨架（两个面共享的「作用域头」）**

```
┌─ 面板页签栏：[条目] [计划] [还原] [用量] [文件] [Git 变更] ─┐
├─ 作用域头： [根选择器 ▾  E:\projects\ai\ai-agent-runtime  🔄 ] ┤   ← 共享组件 ScopeHeader
├─ 工具行：    [过滤…] [排序 ▾] [隐藏文件 ☐] [刷新] [宽度 ⤢]        ┤
├─ 主体：      （文件树+预览 / 变更列表+diff）                      ┤
└─ 底部：      状态行（N 项 / 已选 1 个 / 上传 2 进行中 · 45MB/s）   ┘
```

- `ScopeHeader` 为 `files` 与 `git` 共享：数据来自工作目录注册表（`api/runtime/workspace-directories.ts`）+ 当前会话 `workspace_path`（由 `selectedThread` 提供），并附加由 `/fs/roots` 返回的 `exists`/`is_git_repo` 标记。
- 选中根 + **最近访问目录/文件按会话记忆**（Q8，sessionStorage，按 sessionId 分键，不写全局设置，存储异常静默忽略）；`/fs/roots` 不可用（503）时退化为「只用当前会话工作目录」并提示。

### 4.2 宽度与响应式（用户可调 + 自适应）

**状态模型与计算规则**

| 项 | 设计 |
| --- | --- |
| 状态源 | `settings.workspace.rightRailWidthMode: "auto" \| "manual"`（默认 `auto`）+ `settings.workspace.rightRailWidthPx: number`（默认 `288` = 18rem） |
| 计算出口 | `lib/layout/rail-width.ts`：`resolveRailWidth({ mode, widthPx, surface, viewportWidth })` / `clampRailWidth(px, viewportWidth)`；组件、hook、设置页**共用同一出口**，纯函数可直接单测 |
| auto 规则 | 内容型面（`artifacts`/`plan`/`checkpoint`/`usage`）= `288px`（与现状像素一致）；宽内容面（`files`/`git`）= `clamp(0.32 × vw, 416px, 672px)`（26rem–42rem） |
| manual 规则 | 用 `widthPx` 再过 `clampRailWidth()`：视口变小会被**显示收窄**，但不改写持久化值（回到大屏即恢复用户原本的宽度意图） |
| 渲染注入 | `right-rail-section.tsx` 容器 `style={{ "--right-rail-width": px + "px" }}`；网格用 `xl:grid-cols-[16rem_minmax(0,1fr)_var(--right-rail-width,18rem)]`（Tailwind 侧保持静态字面量） |
| 持久化时机 | 仅在拖拽**结束**（`pointerup`）写 settings：一次写盘、一次 re-render；拖拽过程零 setState |
| 主区保护 | `maxWidth = min(832px, vw − 256px(左栏) − 512px(主区最小))`、`minWidth = 320px`；`maxWidth < minWidth` → 覆盖层模式 |
| 窄视口 | `<xl`：保持不参与网格（不挤压主区），files/git 用**覆盖层抽屉**承载（宽度 `min(railWidth, 92vw)` + 遮罩 + `Esc` 关闭） |
| 回归红线 | ① 关闭右栏仍**不占位**（沿用 2 列网格）；② `topbar-toggle-right-rail` 行为不变；③ 非 `xl` 视口仍不渲染右栏列；④ 内容型面在 `auto` 下的宽度与现状逐像素一致（18rem） |

#### 4.2.1 拖拽交互细则（性能与 a11y 并重）

**手柄**：`components/workspace/workspace-shell/rail-resize-handle.tsx`

- 位置：容器**内部**左边缘的 6px 命中区（`absolute inset-y-0 left-0 z-10 w-1.5 cursor-col-resize touch-none`）；默认不可见，`hover` / `focus-visible` / 拖拽中显示 1px 高亮线（复用既有 border token），不改变容器 `border-l` 的视觉厚度。
- 触控：`touch-none`（`touch-action: none`）确保触摸拖拽不被页面滚动手势抢占；命中区不足时用 `::before` 扩到 12px。

**指针流程**（编号即实现顺序）

1. `onPointerDown`：`setPointerCapture(e.pointerId)`（本仓先例：`use-trajectory-timeline-window.ts:334-337`）；把 `startX` / `startWidth` 写入 ref；置 `dragging`（**仅驱动样式**，不参与几何计算）。
2. `onPointerMove`：**rAF 节流，一帧一次**；`next = clampRailWidth(startWidth − (e.clientX − startX), vw)`；**直接写 DOM**：`railEl.style.setProperty("--right-rail-width", next + "px")`，**不 setState**。
3. 拖拽中：`document.body.style.userSelect = "none"`、`document.body.style.cursor = "col-resize"`，并给容器内容层临时加 `pointer-events-none`。**理由**：pointer capture 只保证事件回流到手柄，**不能**阻止浏览器在图片/文本上发起原生 drag 与文本选区，必须在内容层屏蔽。
4. `onPointerUp` / `onPointerCancel`：释放 capture → 清理 body 样式 → **一次性** `setState` + 写 settings（`mode: "manual"`、`widthPx: next`）。
5. 卸载 / 关闭右栏 / 组件树重建：清理 body 样式与未执行的 rAF，避免页面卡在 `userSelect: none`；关闭右栏时把宽度恢复交给 settings（不写入额外状态）。

**键盘与复位（WAI-ARIA Window Splitter 模式）**

- 手柄属性：`role="separator"`、`aria-orientation="vertical"`、`tabIndex={0}`、`aria-valuemin/valuemax/valuenow`、`aria-label={t("shell.rightRailResizeHandle")}`。
- 按键：`←`/`→` = ±16px（按住 `Shift` = ±64px）；`Home`/`End` = min/max；`Enter` = 回到 `auto`。
- 双击手柄 = 回到 `auto`（与 `Enter` 同义）。

**等价入口（可发现性 + a11y 兜底）**

- 设置 → 外观新增「右侧栏宽度」：滑块（`minWidth`–`maxWidth`）+ 当前值展示 + 「恢复自适应」按钮。
- 理由：① 不便拖拽的用户（触控板/键盘/辅助技术）有等价路径；② 持久化字段有显式重置入口，避免"只能靠拖回去"。

#### 4.2.2 宽度相关新增/改动文件与预算

| 文件 | 职责 | 预算 |
| --- | --- | --- |
| `lib/layout/rail-width.ts`（新增） | `clampRailWidth()` / `resolveRailWidth()` / 常量表（min/max/系数/面分类） | ≤ 120 行 |
| `hooks/workspace/use-right-rail-width.ts`（新增） | 模式解析、视口重算、拖拽几何 ref、提交持久化、清理 | ≤ 220 行 |
| `components/workspace/workspace-shell/rail-resize-handle.tsx`（新增） | 手柄（pointer + 键盘 + 双击 + a11y 属性） | ≤ 160 行 |
| `components/workspace/workspace-shell/right-rail-section.tsx`（改造） | 注入 CSS 变量、挂手柄、覆盖层模式 | 59 → ≤ 140 行 |
| `components/workspace/workspace-shell.tsx`（改造） | 网格列宽改为 CSS 变量；宽度状态移入 hook | 局部改动 |
| `core/settings/local.ts`（改造） | `rightRailWidthMode` / `rightRailWidthPx` + `normalize*` | 局部改动 |
| `components/workspace/settings/appearance-settings-page/rail-width-section.tsx`（新增） | 宽度滑块 + 复位按钮 | ≤ 140 行 |
| `lib/layout/rail-width.test.ts`、`hooks/workspace/use-right-rail-width.test.ts`、`workspace-shell/rail-resize-handle.test.tsx`（新增） | 纯函数边界 / 拖拽只提交一次 / 键盘与 a11y | 测试不计入行数卡口 |

> 回归红线：`right-rail-section.test.tsx`、`workspace-sidebar-responsive.test.tsx` 本期**不能变红**。P0-2 动手前先补一条「auto 下默认宽度 = 288px」的断言保护现状，再改实现。

### 4.3 文件浏览器

**交互分层**

| 区域 | 内容 | 关键实现 |
| --- | --- | --- |
| 面包屑 | 根名 / 相对路径分段（可点击回跳，支持「复制绝对路径」） | 由相对 path 直接切分，不额外请求 |
| 树/列表 | 单层目录项：图标、名称、大小、mtime、git 状态角标（**已确认做（Q6）**：来自 `/git/status` 的 path→status 映射；非仓库/无 git 时整体跳过，不显示空角标） | 虚拟滚动（固定行高 28px）；目录点击**原地展开**（树模式）或**进入**（列表模式） |
| 工具栏 | 过滤（客户端前缀/模糊匹配当前已加载层）、排序、隐藏文件开关、刷新、全部折叠 | 过滤**只作用于已加载层**（不触发全树搜索）；全树搜索明确标注为 P5 可选项 |
| 预览区 | 点击文件时：右侧栏内**内联预览**（P1）或打开既有 `FilePreviewDialog`（P1 弹层入口保留） | 复用 `use-file-preview` 状态机口径 + `lib/file-preview/decode.ts` 判定 |
| 底部状态行 | 当前层项数 / 已加载层数 / 选中项 / 传输进度摘要 | 传输入口常驻，切面不丢状态 |

**状态模型（`use-file-browser.ts`，三段式 reducer）**

```ts
type ListingKey = string;               // `${scope}|${absDirPath}`
type ListingState = {
  status: "idle" | "loading" | "ready" | "loading_more" | "error";
  entries: FsEntry[];                   // 已加载（≤ limit × n）
  nextCursor: string | null;
  hasMore: boolean;
  error: unknown;
  truncated: boolean;                   // 服务端截断（单层项数超上限）
  loadedAt: number;                     // 用于「过期重载」
};
type FileBrowserState = {
  scope: FsScopeSelection;
  listings: Record<ListingKey, ListingState>;   // 每层独立缓存，展开/折叠不重复请求
  expanded: Set<string>;                        // 树模式展开集合
  selectedPath: string | null;
  filter: { text: string; showHidden: boolean; sort: SortKey };
};
```

**纪律（沿用本仓既有惯例）**

1. **请求序号 + `AbortController`**：每次进入目录递增序号，只有最新结果写回；切换目录/关闭面板中止在途请求；主动取消不落 error 态（照 `use-file-preview.ts`）。
2. **不臆造数据**：`next_cursor`/`has_more` 缺失按「无更多」处理并记录为什么；不做「假定已全量」的客户端全排序（排序参数下推给服务端，避免只排了前 200 项却显示为全局有序）。
3. **不伪造加载态**：`truncated=true` 必须显式提示「本层仅显示前 N 项」。
4. **不缓存残留**：外部变更通过「刷新按钮 + 进入目录时 TTL 校验」处理，TTL 建议 5s（避免频繁点击重复请求，也避免长期脏数据）。

### 4.4 上传 / 下载 / 大文件续传（前端）

**上传状态机（`use-file-upload.ts`）**

```
idle → hashing?(可选) → init → transferring ⇄ paused ⇄ retrying → completing → done
                                   ↘ aborted / error(可重试)
```

- 分片：`file.slice(start, end)`，默认 4 MiB，并发 3；每片失败按**指数退避重试 3 次**（仅对网络/5xx 重试；`409 offset mismatch` 不重试，而是重新 `GET /fs/upload/{id}` 拉真实 offset 后继续）。
- 进度：按**已确认落盘字节**（服务端返回的 `received`）计算，而不是「已发送字节」，避免进度虚高。
- 暂停/继续：`AbortController` 中止在途片；继续时先探测 offset → 从 `offset` 续传（同一 `upload_id`）。
- 刷新恢复：`localStorage["aicli.uploads"]` 存 `[{upload_id, scope, dir, name, size, lastModified, offset}]`（**不存文件内容**）；面板重开后显示「发现未完成上传」，用户重选同一文件（按 name+size+lastModified 匹配）即可续传；不匹配则提示而不是静默新建。
- 冲突策略：`fail`（默认，返回 409 并在 UI 询问）/ `overwrite` / `rename`（`name (1).ext`）。策略选择必须显式（不做「默认真覆盖」这种危险默认）。
- 取消：`DELETE /fs/upload/{id}` 清理 `.part` 与 sidecar；面板关闭不等于取消（用户可能只是切面），但提供「清理全部未完成上传」。

**下载**

| 场景 | 实现 | 备注 |
| --- | --- | --- |
| 小文件（< 8 MiB）/ 用户点「下载」 | Tier C：`<a href="/api/runtime/fs/download?...">` 原生下载 | 最简单，浏览器自带进度 |
| 大文件 + Chromium | Tier A：`showSaveFilePicker` 拿 `FileSystemFileHandle` → 顺序 `Range` 请求写入 → 支持暂停/续传 | 需用户手势触发；取消保存对话框 = 用户取消，不落错误态 |
| 大文件 + 非 Chromium | Tier B：`fetch` + `ReadableStream` + 进度事件 → Blob 下载（>256MiB 禁用该层并提示走 Tier C） | 明确提示「本浏览器不支持断点续传」 |
| 续传校验 | 记录 `ETag`；续传前 `HEAD` 比对，不一致 → 作废重下并提示「文件已被修改」 | 与上传的 offset 校验对称 |

> 一句话总结诚实边界：**上传的断点续传是我们自己实现的，全浏览器可用；下载的断点续传依赖 Chromium 的 File System Access API，其他浏览器只能降级为普通下载**。规划文档、UI 文案与测试都按这个事实写，不做「全平台续传」的承诺。

### 4.5 预览渲染（分流表）

| 类型判定 | 渲染 | 组件 |
| --- | --- | --- |
| `text` + `md`/`markdown` 扩展名 | `react-markdown`（+`remark-gfm`/`remark-breaks`），相对链接与图片**不加载外部资源**，仅文本渲染；超长文档分块渲染（首批 2000 行 + 「加载更多」） | 复用 `message-markdown` 的渲染能力，独立轻量组件 |
| `text` + 其他（代码/配置/日志） | 行号 + `prismjs` 高亮（语言由扩展名推断，未知则纯文本）；> 5000 行只渲染可视窗口 | `diff` 目录之外的 `text-viewer.tsx`（与 diff 共享 `virtual-line-list.tsx`） |
| `image`（png/jpg/gif/webp/svg） | `<img>`，限制显示尺寸；SVG 以 `<img src=data:...>` 载入（**不**内联注入 DOM，避免脚本面） | `image-preview.tsx` |
| `binary` | 只显示字节数 + 判定原因（`nul-byte` / `invalid-utf8`），不渲染文本 | 复用 `panels-file-preview.ts` 既有文案口径 |
| `too_large` | 显示真实大小与上限，提供「下载」入口 | 同上 |

- 新增 i18n 模块：`i18n/resources/{zh-CN,en-US}/workspace/panels-file-browser.ts`、`panels-git.ts`（zh/en 成对，`verify-frontend-i18n.ts` 卡口）。
- 预览纪律：**不做类型伪装**（二进制不当文本、超限不当空文件、截断必须标注截断），与 `lib/file-preview/decode.ts:1-7` 的既有纪律一致。

### 4.6 Git 变更浏览器（GitHub 风格）

**布局（宽内容面：`auto` 下的自适应宽度，或用户手动拖宽）**

```
┌ 作用域头：E:\projects\ai\ai-agent-runtime   ⎇ main ↑2 ↓0   [刷新] ┐
├ 视图：[变更] [提交]        显示：[树 ▾] [空白：忽略 ▾] [⊞ 全部展开] ┤
├──────────────┬──────────────────────────────────────────────────┤
│ 变更列表      │  Diff 视图                                        │
│ ▾ 已暂存 (2)  │  ┌ 文件头：src/a.ts   M   +12 −3   [unified|split] │
│   M src/a.ts  │  │ @@ -1,7 +1,9 @@   ⋯ 上下文折叠 ⋯              │
│ ▾ 变更 (3)    │  │  - 旧行                                        │
│   M src/b.ts  │  │  + 新行                                        │
│ ▾ 未跟踪 (1)  │  └ …（虚拟滚动）                                  │
│   ? new.txt   │                                                  │
└──────────────┴──────────────────────────────────────────────────┘
```

**列表区（对齐 GitHub）**

| 元素 | 设计 |
| --- | --- |
| 分组 | 已暂存 / 未暂存 / 未跟踪 / 冲突（GitHub 桌面端口径；顺序固定） |
| 状态徽标 | `M` 改（黄）、`A` 增（绿）、`D` 删（红）、`R` 重命名（紫）、`U` 未跟踪（灰）、`!` 冲突（橙）；重命名显示 `old → new` |
| 行内信息 | 文件名（含目录弱化显示）、`+N −M` 计数（`--numstat` 提供）、二进制标记 |
| 树/平铺切换 | 树模式按目录聚合 + 可折叠；平铺模式按 path 排序 |
| 过滤 | 路径子串过滤（客户端，作用于已加载的 status 结果） |
| 选择 | 单选文件 → 右侧显示 diff；键盘 ↑/↓ 切换文件、`j/k` 可选 |

**Diff 视图**

| 能力 | 设计 |
| --- | --- |
| unified / split | 同一份结构化 hunks，两种布局；split 需要按行对齐（服务端已给 old/new 行号，前端做配对） |
| 折叠上下文 | 每 hunk 上下各显示 3 行（默认）；「展开上方/下方 N 行」再请求一次带更大 `context` 的 diff（**服务端不返回全文件**，避免巨大载荷）；「展开整个文件」= `context=999999` 但受行数上限保护 |
| 行 | **单列行号**（老/新两列已合并：`add`→新侧、`del`→老侧、`context`→新侧；缺失侧渲染空串，**不借对侧数字**）+ `+`/`-`/空格 前缀 + 代码（可选中复制）；split 每侧各一列行号；`\ No newline at end of file` 显式渲染 |
| 白色空白 | 开关：忽略空白变更（服务端 `--ignore-all-space`），开关变化需要重新取 diff（**不**在前端假装过滤） |
| 二进制 | 显示「二进制文件，已变更」+ 大小变化，不渲染内容 |
| 超大 diff | 服务端 `truncated=true` + `truncated_reason`（行数/字节）；前端显示「已截断，仅显示前 N 行」并提供「下载原始 diff」 |
| 语法高亮 | 按扩展名 → prism 语言；仅可视行；LRU 缓存 |
| 性能 | hunk 级虚拟化；单文件渲染行数上限（默认 2000，可「继续加载」）；切换文件用请求序号防竞态 |
| 复制/导出 | 「复制文件路径」「复制原始 diff」「下载 .patch」（服务端提供 `format=raw` 文本） |

**提交视图（P3，可裁剪）**：最近提交列表（sha 短、作者、相对时间、标题、refs 徽标）→ 点击查看该提交的 diff（`target=commit:<sha>`）。

**只读优先**：P3 全部只读。P4 才加 `stage/unstage`（按钮 + 乐观更新 + 失败回滚 + 明确文案；**Q2 已确认本期实现**）；**commit 本期不做（Q4）** —— 本仓是 agent 工作区，前端提交会与 agent 行为竞争，收益与风险不匹配。

### 4.7 前端数据层与新增文件清单（含行数预算）

**API 客户端（`api/runtime/`，照既有惯例：一个模块 = 一组端点 + 归一化函数 + `*.test.ts`）**

| 新文件 | 端点 | 说明 |
| --- | --- | --- |
| `api/runtime/fs-roots.ts` | `GET /fs/roots` | 作用域根列表归一化 |
| `api/runtime/fs-list.ts` | `GET /fs/list` | 目录项归一化（含 `next_cursor`/`has_more`/`truncated` 缺字段容错） |
| `api/runtime/fs-preview.ts` | `GET /fs/preview` | 预览载荷归一化（复用 `decodeBase64Bytes`） |
| `api/runtime/fs-transfer.ts` | `HEAD/GET /fs/download`、`/fs/upload/*` | 传输（含 `UploadOffsetMismatchError` 之类**具名错误**，供重试策略分支） |
| `api/runtime/git.ts` | `GET /git/status`、`/git/diff`、`/git/commits` | Git 归一化 + 结构化 diff 类型校验 |

**状态与视图组件**

| 新文件（建议路径） | 职责 | 预算 |
| --- | --- | --- |
| `components/workspace/panel-registry.ts` | 面注册表 + 类型 | ≤ 120 行 |
| `components/workspace/artifact-panel/surface-tabs.tsx`（改造） | 读注册表渲染页签 + tone 映射表 | ≤ 200 行 |
| `components/workspace/file-browser-surface.tsx` | 文件面出口（布局编排） | ≤ 250 行 |
| `components/workspace/file-browser/tree-list.tsx` | 虚拟列表 + 展开/选中 | ≤ 300 行 |
| `components/workspace/file-browser/scope-header.tsx` | 作用域头（与 git 共享） | ≤ 150 行 |
| `components/workspace/file-browser/preview-pane.tsx` | 预览分流 | ≤ 250 行 |
| `components/workspace/file-browser/text-viewer.tsx` | 文本/代码视图 | ≤ 250 行 |
| `components/workspace/file-browser/transfer-tray.tsx` | 上传/下载进度托盘 | ≤ 250 行 |
| `components/workspace/git-surface.tsx` | Git 面出口 | ≤ 250 行 |
| `components/workspace/git/change-list.tsx` | 变更列表（分组/树/过滤） | ≤ 300 行 |
| `components/workspace/git/diff-view.tsx` | diff 视图（unified/split 切换） | ≤ 300 行 |
| `components/workspace/git/diff-hunk.tsx` | 单 hunk 渲染 + 折叠 | ≤ 250 行 |
| `components/workspace/diff/virtual-line-list.tsx` | 共享虚拟行列表（文本/diff 共用） | ≤ 250 行 |
| `components/workspace/git/commit-list.tsx` | 提交列表（P3 可选） | ≤ 180 行 |
| `hooks/workspace/use-file-browser.ts` | 目录层状态机 | ≤ 350 行 |
| `hooks/workspace/use-file-transfer.ts` | 上传/下载状态机 | ≤ 350 行 |
| `hooks/workspace/use-git-changes.ts` | status/diff 拉取 + 竞态控制 | ≤ 300 行 |
| `types/runtime/fs-browser.ts` | 类型定义（新增，与 `types/runtime/checkpoints.ts` 同目录惯例） | ≤ 150 行 |
| `lib/file-browser/entry-sort.ts`、`lib/file-browser/path-utils.ts` | 纯函数（排序/路径拼接/面包屑） | ≤ 200 行 |
| `lib/git/diff-view-model.ts` | 结构化 hunks → 视图模型（split 配对、折叠区间） | ≤ 300 行 |
| `lib/layout/rail-width.ts`、`hooks/workspace/use-right-rail-width.ts`、`workspace-shell/rail-resize-handle.tsx`、`appearance-settings-page/rail-width-section.tsx` | 可变宽度右栏（详见 §4.2.2） | 新增合计 ≤ 640 行（另 `right-rail-section.tsx` 改造后 ≤ 140 行） |

> 若实测某个文件必然超 300 行，先按「渲染 / 状态 / 纯函数」再拆，而不是放宽预算 —— `verify:lines` 的 500 行是硬卡口。

### 4.8 i18n、样式与无障碍

- **i18n**：新增 `panels-file-browser.ts` / `panels-git.ts`（zh-CN + en-US 成对）；键路径建议 `workspace.panels.fileBrowser.*`、`workspace.panels.git.*`；面板页签标题放 `workspace.shell.panelTabs.{files,git}`。**注册表里存 key，不存文案**。
- **样式**：复用既有 token（`--workspace-sidebar-bg`、`border-white/8`、`bg-white/4`、`text-muted-foreground`、`accent-gold` 等）；diff 需**新增一组语义色 token**（新增 `--diff-add-bg/-fg`、`--diff-del-bg/-fg`，在明暗主题下各自定义），避免在组件里写死十六进制。
- **无障碍**：
  - 页签走 `role="tablist"`/`role="tab"`/`aria-selected`（现状 `surface-tabs.tsx` 已有 button + focus ring 基础，需补齐 aria 关联）；
  - 目录树用 `role="tree"`/`treeitem`/`aria-expanded`/`aria-level`，虚拟滚动下**必须**保留 `aria-setsize`/`aria-posinset`；
  - diff 行用 `<table>` 语义（GitHub 同款）不利于虚拟化 → 采用 `role="row"`/`role="cell"` 的等价结构 + `aria-label` 描述增删；
  - 所有图标按钮必须有 `aria-label` + `title`（既有 `topbar-toggle-right-rail` 是范例）。
- **键盘**：面板内 `Esc` 关抽屉；树 `↑↓←→`/`Home`/`End`/`Enter`；列表 `↑↓` 切文件；diff `n/p` 跳下一个/上一个文件（可选）。

### 4.9 与既有功能的边界（避免重复造轮子）

| 既有能力 | 本规划如何处理 |
| --- | --- |
| `fs/read-file` + `FilePreviewDialog`（工具行点击文件预览） | **保持不动**；新面板用新端点。二者互不影响，但共享 `decode.ts` 判定与文案口径 |
| `artifact-panel` 的条目/还原面板 | 不动；仅在 `surface-tabs` 层做注册表改造（顺带降低该文件行数） |
| checkpoint 的 diff 文本 | 本期不改；`diff/` 渲染组件设计为可复用，后续可作为 checkpoint 视图的升级路径 |
| agent 的 `append_write`/`shell` 写入 | 不做写锁；文件浏览器只读，上传目标冲突用显式策略解决；预览遇到 mtime 变化时提示「内容已变化，点击刷新」 |

---

## 5. 后端详细设计

### 5.1 路由总览（全部挂在 `/api/runtime` 子路由，注册于 `handler.go` 的 `RegisterRoutes`）

| 方法 | 路径 | 用途 | 阶段 |
| --- | --- | --- | --- |
| GET | `/fs/roots` | 可用作用域根（工作目录注册表 + 会话 CWD + 仓库标记） | P0 |
| GET | `/fs/list` | 列一层目录（游标分页、排序、过滤） | P0 |
| GET | `/fs/stat` | 单路径元信息（大小/mtime/类型/mime/是否文本） | P0 |
| GET | `/fs/preview` | 截断式预览（text/image/binary/too_large） | P1 |
| GET | `/fs/download` | 流式下载，支持 `Range` | P2 |
| HEAD | `/fs/download` | 续传探测（`Content-Length` + `ETag` + `Accept-Ranges`） | P2 |
| POST | `/fs/upload/init` | 创建上传会话，返回起始 offset | P2 |
| PUT | `/fs/upload/{upload_id}/chunk` | 写一个分片（raw body + `Content-Range`） | P2 |
| GET | `/fs/upload/{upload_id}` | 续传探测（当前 received/offset/chunk_size） | P2 |
| POST | `/fs/upload/{upload_id}/complete` | 校验 + 原子落盘 | P2 |
| DELETE | `/fs/upload/{upload_id}` | 中止并清理 | P2 |
| GET | `/git/status` | 仓库状态（分支/上游/分组变更 + numstat） | P3 |
| GET | `/git/diff` | 结构化 diff（file / target / context / whitespace） | P3 |
| GET | `/git/commits` | 最近提交列表 | P3（可裁剪） |
| POST | `/git/stage` | stage/unstage 一组文件 | P4 |

**命名与兼容**：既有 `POST /fs/read-file|write-file|append-file` 保持不变（agent 工具链与既有 e2e 依赖）；新增端点用 GET/HEAD/PUT/DELETE 的 REST 语义，并在响应中**不含** base64 全量字节（除图片预览）。

### 5.2 作用域与安全模型（新增包 `internal/fsscope`）

```
请求参数： scope=workspace:<directory_id> | session:<session_id> | cwd
          path=<相对作用域根的路径>（空/"/" = 根）

解析流程：
1. 解析 scope → 候选根路径（绝对路径）
     workspace:<id> → workspaceregistry 查记录（不存在 → 404 scope_not_found）
     session:<id>   → 会话 metadata.context.workspace_path（缺失 → 400 scope_has_no_root）
     cwd            → runtime 进程工作目录
2. 根做一次 EvalSymlinks（失败则用 Abs 结果）作为"允许根"
3. 目标 = Abs(Join(允许根, Clean(path)))
4. 校验目标在允许根之下（Windows 大小写不敏感 + 路径分隔符严格比较：
   target == root || strings.HasPrefix(target, root + sep)）
5. 若目标已存在，再 EvalSymlinks 后重复第 4 步（防符号链接逃逸）
6. 越界 → 400 path_outside_scope（不回显服务端绝对路径以外的信息）
```

**边界与诚实说明**

- 这是一层**能力收敛**：它让「前端只能访问被显式选中的根」，而不是引入真正的多租户隔离。runtime-server 单机信任模型不变 —— agent 依然拥有 shell，越权者本来就能读任意路径（见 §1.3 非目标 1）。
- **禁止给 path 传绝对路径**：绝对路径一律 `400 path_must_be_relative`，从接口层面杜绝「前端拼错一个变量就逛全盘」。
- 上传目标必须是**目录内新建/覆盖文件**：不允许目标是已存在的目录（复用现有 `pathKindMismatchError` 提示风格）。
- URL 编码：`path` 走查询参数（`url.QueryEscape`），`router.UseEncodedPath()` 已开启；后端必须 `url.QueryUnescape` 后处理，避免 `%2e%2e` 绕过（**先解码再 Clean 再校验**，顺序不能颠倒）。
- 新增一层的审计：所有写操作（上传/complete/stage）记录一条结构化日志（`scope`、`path`、`size`、`action`），便于排查「谁覆盖了什么」。

### 5.3 文件浏览接口

**`GET /fs/roots`**

```json
{
  "roots": [
    { "scope": "workspace:wd_ab12", "kind": "workspace", "name": "ai-agent-runtime",
      "path": "E:\\projects\\ai\\ai-agent-runtime", "exists": true,
      "is_git_repo": true, "git_root": "E:\\projects\\ai\\ai-agent-runtime" },
    { "scope": "session:sess_9f", "kind": "session", "name": "会话工作目录",
      "path": "E:\\projects\\demo", "exists": true, "is_git_repo": false }
  ],
  "count": 2
}
```

- `is_git_repo` 由「仓库探测」（§5.5）得出，**失败不报错**，仅置 `false` + `probe_error` 字段（可选）。

**`GET /fs/list?scope=…&path=src&cursor=…&limit=200&sort=name_asc&show_hidden=false&dirs_first=true`**

```json
{
  "dir": { "path": "src", "abs_path": "E:\\…\\src", "parent": "", "is_root": false },
  "entries": [
    { "name": "api", "path": "src/api", "type": "dir", "mtime": 1758000000 },
    { "name": "main.ts", "path": "src/main.ts", "type": "file", "size": 1234,
      "mtime": 1758000001, "ext": ".ts", "is_text": true, "is_symlink": false }
  ],
  "next_cursor": "eyJ2IjoibWFpbi50cyJ9",
  "has_more": true,
  "truncated": false,
  "sort": "name_asc"
}
```

| 参数 | 约束 |
| --- | --- |
| `limit` | 默认 200，上限 1000（超出 → 400 或静默夹取；**建议静默夹取并在响应回显实际 limit**） |
| `cursor` | 不透明字符串；解析失败 → 400 `cursor_invalid`（不静默回退到第一页，避免前端死循环） |
| `sort` | `name_asc\|name_desc\|mtime_desc\|size_desc\|type_then_name`（默认 `type_then_name`） |
| `show_hidden` | 默认 `false`；判定规则：`.` 前缀（Windows 另加 `FILE_ATTRIBUTE_HIDDEN`） |
| 单层出错 | 子项权限不足（EPERM/EACCES）时**跳过该项并置 `"type":"inaccessible"`**，不整层失败 |

- **实现要点**：`os.ReadDir`（返回 `DirEntry`，避免每个文件 `Stat`）→ 需要 size/mtime 时对 file 项 `Info()`（失败置 `-1`）。排序在服务端完成（因为只发一页）。游标位置 = 排序键，`has_more` 通过「多读一项」判定，天然避免 `len == limit` 的歧义。

### 5.4 传输接口

**下载 `GET|HEAD /fs/download?scope=…&path=…`**

| 项 | 设计 |
| --- | --- |
| 响应头 | `Accept-Ranges: bytes`、`Content-Length`、`Content-Type`（按扩展名，默认 `application/octet-stream`）、`ETag: "<size>-<mtimeUnixNano>"`、`Last-Modified`、`Content-Disposition: attachment; filename*=UTF-8''<encoded>` |
| Range | 支持单区间 `Range: bytes=start-end`、`bytes=start-`、`bytes=-suffix`；多区间**不支持**（返回 200 全量或 416，建议 416 + `Content-Range: bytes */size`） |
| 416 | 越界 → `416` + `Content-Range: bytes */<size>`（前端据此重置续传） |
| 实现 | `http.ServeContent` 直接可用（自动处理 Range/If-Range/Last-Modified/ETag 语义），**优先用它**而不是手写 Range 解析 |
| 大文件 | 不落内存（`ServeContent` 走 `io.Copy`）；需要 `http.ServeContent` 的 `ReadSeeker` = `os.Open` |
| 一致性 | 服务端打开文件后即固定 inode/mtime；并发外部修改时 `ServeContent` 仍按打开时的内容读（可接受），但 `ETag` 变化会被续传探测捕获 |

**上传会话**

临时目录：`<目标目录>/.aicli-uploads/<upload_id>.part` + `<upload_id>.json`（sidecar：`upload_id, scope, dir, name, size, offset, sha256_prefix?, chunk_size, expires_at, conflict_policy, created_by_session`）。

- 放在目标目录下是为了**同一卷**，`complete` 时 `os.Rename` 才是原子操作（跨卷 rename 会失败）。
- `.aicli-uploads` 与 `*.part` 在 `/fs/list` 中被视为隐藏（即使 `show_hidden=true` 也建议隐藏，或明确标 `internal:true`，避免用户误删正在上传的分片）。
- 过期：`expires_at` 默认 24h；`/fs/list` 或 `/fs/roots` 被调用时**顺带懒清理**过期会话（不做后台定时器，保持无状态），并在日志中记录清理。

```jsonc
// POST /fs/upload/init
{ "scope": "workspace:wd_ab12", "dir": "docs/plan", "name": "大文件.zip",
  "size": 5368709120, "sha256": "可选，整文件预校验",
  "chunk_size": 4194304, "conflict_policy": "fail" }

// 200
{ "upload_id": "up_2f1c…", "offset": 0, "received": 0, "chunk_size": 4194304,
  "expires_at": 1758600000, "target": { "path": "docs/plan/大文件.zip", "exists": false } }

// 目标已存在且 hash 相同 → 秒传
{ "upload_id": "", "completed": true, "deduplicated": true,
  "target": { "path": "docs/plan/大文件.zip", "exists": true, "sha256": "…" } }

// 命名冲突且 conflict_policy=fail → 409
{ "error": { "code": "target_exists", "message": "…" },
  "target": { "path": "docs/plan/大文件.zip", "exists": true, "size": 123 } }
```

```jsonc
// PUT /fs/upload/{id}/chunk     body = 原始字节（不 base64，避免 33% 膨胀）
// headers: Content-Range: bytes 4194304-8388607/5368709120
//          X-Chunk-Sha256: <可选>
// 200
{ "received": 8388608, "offset": 8388608 }
// 409（offset 不连续 / 长度不匹配）
{ "error": { "code": "upload_offset_mismatch", "message": "…" }, "expected_offset": 4194304 }
```

```jsonc
// POST /fs/upload/{id}/complete
{ "sha256": "<整文件计算值>" }           // 可选：由服务端计算并回传
// 200
{ "file": { "path": "docs/plan/大文件.zip", "size": 5368709120 },
  "action": "create", "sha256": "…", "elapsed_ms": 2143 }
// 400（校验失败，临时文件保留以便重传末片）
{ "error": { "code": "upload_checksum_mismatch", "message": "…" } }
```

**上传实现纪律**

1. 分片写入必须 `start == 当前文件 size`，用 `O_APPEND` 打开 + 校验 `Stat().Size()` 双重确认（防并发错乱）；
2. `complete` 前校验整文件 `sha256`（若 `init` 提供了 `sha256` 则比对；未提供则计算并回报）；
3. `complete` 用 `os.Rename`（Windows 上目标存在时需先 `os.Remove`，或用 `os.Rename` + 失败回退「复制后删除」；**说明**：Windows `MoveFile` 不能覆盖，必须显式处理，这是最容易踩的坑）；
4. 并发保护：同一 `upload_id` 的分片请求用 `sync.Map` + 互斥锁串行化（否则并发 PUT 会交错写坏文件）；
5. `size` 上限：默认 **10 GiB（可配置）**，超限 → 400 `upload_too_large`；分片**默认 4 MiB**，可配置区间 1–8 MiB（Q5 决策）。

### 5.5 Git 接口（新增包 `internal/gitbrowse`）

**仓库探测与缓存**

- `git -C <path> rev-parse --show-toplevel`（同时 `--is-inside-work-tree` 判定是否在 worktree 内；`.git` 目录/裸库不提供 diff）；
- `--show-superproject-working-tree` 可选（submodule 场景）；
- 结果按 `absPath` 缓存 30s（或按 `.git/index` 的 mtime 失效）；探测失败 → `repo_not_found`（**不是** 500）；
- 可发现「作用域根不是仓库、但某子目录是仓库」的情况：`/git/status` 允许 `path` 指向子目录并按 `--show-toplevel` 上溯到仓库根；前端提示「已上溯到仓库根 X」。

**`GET /git/status?scope=…&path=…`**

```json
{
  "repo": { "root": "E:\\projects\\ai\\ai-agent-runtime", "branch": "main",
            "detached": false, "head": "a1b2c3d", "upstream": "origin/main",
            "ahead": 2, "behind": 0, "is_bare": false },
  "clean": false,
  "staged":   [ { "path": "src/a.ts", "status": "M", "insertions": 12, "deletions": 3, "binary": false } ],
  "unstaged": [ { "path": "src/b.ts", "status": "M", "insertions": 4, "deletions": 1, "binary": false } ],
  "untracked":[ { "path": "new.txt", "status": "U", "insertions": 3, "deletions": 0, "binary": false } ],
  "conflicts":[ { "path": "src/c.ts", "status": "!" } ],
  "renames":  [ { "from": "old.ts", "to": "new.ts", "status": "R" } ],
  "generated_at": 1758000000
}
```

- 数据来源：`git status --porcelain=v2 --branch -z --untracked-files=all`（v2 稳定、含 rename/XY 分离，**必须 `-z` 解析**以避免文件名中的空格/换行/引号问题），增删行数来自 `git diff --numstat -z` 与 `git diff --cached --numstat -z`；
- 解析纪律：`-z` 输出按 `\x00` 切分；遇到**无法识别的 porcelain 记录类型**（v2 的 `#`/`1`/`2`/`u`/`?`/`!`）时按「无法解析 → 记录到 `warnings[]` 并跳过」，不猜测语义（与前端 `decode.ts` 不臆造数据同一纪律）；
- 二进制文件在 numstat 中表现为 `-\t-` → `binary: true`，**不要**当成 0/0。
- 未跟踪文件不在 numstat 里：服务端按**读取文件**统计行数（`\n` 计数，末行无换行也算一行；探测到 NUL 字节 → `binary: true`；符号链接按 git 口径算 1 行）。
  未跟踪文件只能逐个读，因此统计有**单文件上限**（`MaxOutputBytes`，默认 4 MiB）与**单次 status 的总扫描预算**（`MaxUntrackedScanBytes`，默认 16 MiB，见 §5.8）；读不到结论时（非常规文件、超过单文件上限或总预算、读取失败）用 `insertions = deletions = -1` 表示「统计不可用」并追加 `warnings[]`，**不得**回落到 `+0 −0`（那会把「未知」显示成「没有改动」）。

**`GET /git/diff?scope=…&path=…&file=src/a.ts&target=working|staged|commit:<sha>&context=3&whitespace=show|ignore_all`**

- 命令：`git diff --no-color --no-ext-diff --find-renames [--cached|<sha>] --unified=<context> [--ignore-all-space] -- <file>`
- **未跟踪文件**（仅 `target=working`）：`git diff -- <file>` 对未跟踪路径没有任何输出，照抄会显示成「没有差异」。这类文件改用 `git diff … --no-index -- /dev/null <file>` 合成「新增文件」diff（进程根取作用域根，路径口径与 `file` 参数一致）。注意 `--no-index` 的退出码 1 有两种含义：存在差异（stdout 有内容、stderr 为空）与路径读不到（stdout 为空、stderr 有 `error:` 行），只有前者算成功，后者按 `git_failed` 报错——绝不能渲染成「新增的空文件」。
- 空的新文件没有任何 hunk，但结论仍是 `status: "A"`（不是「与目标一致」）：前端据此显示「新增的空文件」而不是「没有差异」。
- **目标回退（改动在另一侧）**：变更列表按「改动在哪一侧」分组，而 `target` 由用户在「工作区 / 已暂存」里选。当请求目标对这条路径没有任何改动、而另一侧有改动时（典型：未跟踪或已暂存的新文件 + `target=working`，或未暂存的删除 + `target=staged`），服务端**回退**到另一侧取 diff，并在响应里如实回报 `effective_target`（实际使用的目标）与 `target_fallback: true`；前端**必须**说明「当前对比目标没有该文件的改动，以下显示的是另一侧的改动」，不得默默换源。`target=commit:<sha>` 没有「另一侧」（用户显式选了历史版本），不回退。
- 判定「该目标对这条路径到底有没有改动」用 `git diff --name-status -z -- <file>`（空的新文件、被删除的空文件、仅模式变更在 unified diff 里可能连文件头都没有，无法从文本得出结论）：有记录就用它的状态字母补 `file.status`（`A`/`D`/`R`），没有记录才考虑回退。
- 服务端把 unified diff 解析为结构化 hunks（见 §5.6），响应同时带 `raw`（原文，供复制/下载）与 `hunks`（供渲染）；
- 解析失败（非标准输出，如子模块或 git 版本差异）→ 返回 `{ "hunks": [], "raw": "...", "parse_error": "..." }`，让前端降级为纯文本展示，**不要** 500；
- 子模块变更：`git diff --submodule=log`；本期可只标 `is_submodule: true` 并显示「子模块变更」。

**`GET /git/commits?scope=…&path=…&limit=50&cursor=<sha>`**

- `git log --max-count=<limit+1> --skip=0 --pretty=format:%H%x1f%h%x1f%an%x1f%aI%x1f%s%x1f%D` + `--first-parent`（可选）；分页用 `cursor=<最后一条 sha>` + `--skip=1`（或用 `sha~1` 作为起点，避免 `--skip` 大偏移性能问题）；
- 提交 diff 复用 `/git/diff?target=commit:<sha>`（`git diff <sha>^ <sha>` 或 `git show --format= --find-renames <sha>`）。

### 5.6 结构化 diff 契约（前后端唯一事实来源）

```jsonc
{
  "file": { "path": "src/a.ts", "abs_path": "E:\\…\\src\\a.ts",
            "old_path": null, "status": "M", "is_binary": false, "is_submodule": false },
  "target": "working",
  "effective_target": "working",
  "target_fallback": false,
  "context": 3,
  "whitespace": "show",
  "insertions": 12, "deletions": 3,
  "hunks": [
    {
      "header": "@@ -1,7 +1,9 @@ function main() {",
      "old_start": 1, "old_lines": 7,
      "new_start": 1, "new_lines": 9,
      "lines": [
        { "type": "context", "old_no": 1, "new_no": 1, "text": "import { x } from \"y\";" },
        { "type": "del",     "old_no": 2, "new_no": null, "text": "const a = 1;" },
        { "type": "add",     "old_no": null, "new_no": 2, "text": "const a = 2;" },
        { "type": "nonewline", "text": "\\ No newline at end of file" }
      ]
    }
  ],
  "raw": "diff --git a/src/a.ts b/src/a.ts\n…",
  "parse_error": "",
  "truncated": false,
  "truncated_reason": "",
  "generated_at": 1758000000
}
```

**契约纪律**

- `line.type` 只允许 `context | add | del | nonewline`；`old_no`/`new_no` 用 `null` 而不是 `0`（`0` 是合法的行号语义歧义源）；
- 行文本**不做 HTML 转义**（JSON 层面），前端渲染用 React 文本节点（天然转义），**禁止** `dangerouslySetInnerHTML`；
- `truncated=true` 必须是服务端主动截断（行数 > 20000 或字节 > 4 MiB），且 `truncated_reason` 说明触发的是哪个阈值；
- 契约新增字段一律 additive；前端归一化层对未知字段忽略、对缺失字段有默认（照 `api/runtime/files.ts:36-61` 的归一化风格）。

### 5.7 错误模型

沿用既有的「`h.writeError(w, status, errors.New(errors.ErrXxx, msg))`」风格（见 `file_transfer_handlers.go:37-49`），但新端点需要**机器可读的错误码**，因此响应体统一为：

```json
{ "error": { "code": "path_outside_scope", "message": "path escapes the selected root" },
  "request_id": "req_…" }
```

| HTTP | code | 触发条件 | 前端处理 |
| --- | --- | --- | --- |
| 400 | `scope_invalid` / `scope_has_no_root` | scope 格式错 / 会话无工作目录 | 提示并回到根选择器 |
| 400 | `path_must_be_relative` / `path_outside_scope` / `path_invalid` | 越界或非法路径 | 提示「路径不可访问」，不重试 |
| 400 | `cursor_invalid` | 游标无法解析 | 丢弃游标，从第一页重载 |
| 400 | `upload_too_large` / `chunk_size_invalid` | 限额 | 提示并禁用入口 |
| 404 | `scope_not_found` / `path_not_found` / `repo_not_found` | 注册目录被删 / 路径不存在 / 非 Git 仓库 | 根选择器刷新；Git 面显示「不是 Git 仓库」空态 |
| 409 | `target_exists` | 上传冲突且策略为 fail | 弹冲突选择（覆盖/重命名/取消） |
| 409 | `upload_offset_mismatch` | 分片起点与已落盘 offset 不一致 | **拉取真实 offset 后续传**（不盲重试） |
| 409 | `upload_checksum_mismatch` | 整文件校验失败 | 保留临时文件，提供「重传最后一片」 |
| 410 | `upload_expired` | 会话过期 | 清理本地记录 + 重新 init |
| 416 | （无 body，标准 Range 语义） | 下载 Range 越界 | 重置下载续传状态 |
| 500 | `fs_read_failed` / `git_failed` | 磁盘 IO / git 非零退出 | 展示 `message`（含 stderr 尾部），提供重试 |
| 503 | `git_unavailable` | 系统无 `git` | Git 面显示降级说明（**不崩溃、不白屏**） |
| 503 | `service_unavailable` | 新服务未注入（与既有 503 口径一致） | 与既有 `isFileReadUnavailable` 同款降级 |

**说明**：`git_failed` 必须携带 `exit_code` 与 stderr 的**截断尾部**（≤ 2KB），因为 git 的人类可读错误是排障的关键信息；同时**不得**回显服务端凭据类内容（git 命令不含凭据，风险低，但仍做长度截断）。

### 5.8 限额、超时与性能

| 项 | 默认值 | 可配置 | 理由 |
| --- | --- | --- | --- |
| `fs/list` limit | 200（上限 1000） | 是 | 单层万级目录不至于把一页撑爆 |
| 单层目录项数扫描上限 | 50000 | 是 | 超过则 `truncated=true`（防极端目录拖死服务端） |
| `fs/preview` 文本字节 | 256 KiB | 是 | 文本预览不需要更多；超限截断 |
| `fs/preview` 图片字节 | 2 MiB | 是 | base64 膨胀 33%，再大不适合内联 |
| diff 输出上限 | 4 MiB / 20000 行 | 是 | 巨型 lock 文件/生成代码 diff |
| 未跟踪文件行数扫描预算 | 16 MiB / 次 status（单文件另有 `MaxOutputBytes`，默认 4 MiB） | 是 | 未跟踪文件只能逐个读，防止一次列表请求变成无上限磁盘读取；超预算按 `insertions = -1` + `warnings[]` 如实降级 |
| `git status` 超时 | 5s | 是 | 大仓库 `--untracked-files=all` 可能慢 |
| `git diff` / `git log` 超时 | 15s / 10s | 是 | 慢命令必须可中断（`exec.CommandContext` + kill） |
| 上传并发分片 | 客户端 3；服务端同一 upload_id 串行 | 是 | 磁盘随机写 + 顺序一致性 |
| 上传会话 TTL | 24h | 是 | 详见 §5.4 |
| 传输日志 | 写操作全量 1 条；读操作不记录 | — | 避免日志爆炸 |

**并发与取消**：所有 git 与 fs 命令都接收 `r.Context()`；客户端断开（切面/关抽屉/换目录）应立刻终止子进程（避免 `git status` 在后台堆积）。为此需要**每个请求独立的 timeout context**（不要用 `context.Background()`）。

**不做缓存的东西**：不做 Git 状态缓存（除 30s 探测缓存）、不做目录列表缓存（前端已有层缓存）——因为工作区会被 agent 频繁改动，缓存的错误率高于收益。

### 5.9 后端新增文件清单与服务注入

| 新文件（建议路径） | 职责 | 预算 |
| --- | --- | --- |
| `internal/fsscope/scope.go` | scope 解析、根校验、越界判定 | ≤ 250 行 |
| `internal/fsscope/scope_test.go` | 越界/符号链接/大小写/编码用例 | ≤ 300 行 |
| `internal/filebrowse/service.go` | list/stat/preview 服务（`os.ReadDir`、mime、文本判定） | ≤ 350 行 |
| `internal/filebrowse/upload.go` | 上传会话：init/chunk/complete/abort/清理 | ≤ 400 行 |
| `internal/filebrowse/upload_state.go` | sidecar JSON 读写 + 并发锁 | ≤ 200 行 |
| `internal/gitbrowse/exec.go` | git 进程执行沙箱（超时、限额、参数数组） | ≤ 250 行 |
| `internal/gitbrowse/status.go` | porcelain v2 / numstat 解析 | ≤ 350 行 |
| `internal/gitbrowse/diff.go` | unified diff → 结构化 hunks | ≤ 400 行 |
| `internal/api/skills/fs_browser_handlers.go` | `/fs/roots|list|stat|preview` handler + 注册函数 | ≤ 350 行 |
| `internal/api/skills/fs_transfer_handlers.go` | `/fs/download|upload/*` handler | ≤ 400 行 |
| `internal/api/skills/git_handlers.go` | `/git/*` handler | ≤ 300 行 |
| `internal/api/skills/*_test.go` | 对应测试（照 `file_transfer_handlers_test.go` 范式） | 各 ≤ 400 行 |

**注入方式**（照 `main.go:993` 既有写法）：

```go
handler.SetFileBrowseService(filebrowse.NewLocalService(cfg.FileBrowse))
handler.SetGitBrowseService(gitbrowse.NewLocalService())   // git 缺失时内部降级为 ErrUnavailable
```

- 接口定义放在 `internal/api/skills`（与 `FileTransferService` 同级，便于测试注入 fake）；
- 注册函数按功能拆分（如 `RegisterFileBrowserRoutes(runtimeRouter)`、`RegisterGitRoutes(runtimeRouter)`），在 `RegisterRoutes` 内调用 —— 现状是 `RegisterWorkspaceDirectoryRoutes(router)` 已有此先例（`workspace_directory_handlers.go:63-70`），且 handler.go 已接近需要拆分路由注册的体量。
- **Windows 特有注意**：`os.Rename` 覆盖、路径大小写、`\\?\` 长路径（> 260 字符）在 `EvalSymlinks` 后可能变成 UNC 前缀 —— scope 比较必须在**同一规范化形态**下进行（统一先 `filepath.Clean` + `EvalSymlinks`，再比较，且比较前对两边都做同样处理）。

### 5.10 后端测试要点（含必须覆盖的负例）

| 类别 | 必测用例 |
| --- | --- |
| 越界 | `path=../..`、`path=..%2f..%2fetc`（编码绕过）、指向根外的符号链接、Windows 大小写变体（`C:\Windows` vs `c:\windows`）→ 一律 400 |
| 绝对路径 | `path=E:\Windows` → 400 `path_must_be_relative` |
| 目录 | 对目录 `stat`/`download` → 明确错误（复用目录/文件类型不匹配提示风格） |
| 分页 | 1000 项临时目录：`limit=200` 翻页不重不漏；`cursor` 非法 → 400；排序稳定（同名不同大小写场景） |
| 上传 | 顺序分片成功；乱序 → 409 + expected_offset；断点重连后从 offset 续传；checksum 不符 → 409；`conflict_policy=fail` → 409；`overwrite` 成功；abort 后临时文件消失 |
| 下载 | `Range` 单区间正确；越界 → 416；`HEAD` 返回 ETag/Accept-Ranges；大文件不 OOM（用 `httptest` + 流式校验前 N 字节） |
| Git | 临时仓库（`git init` + commit + 改文件）：status 分组正确；重命名（`git mv`）解析；二进制文件 `binary=true`；非仓库 → `repo_not_found`；**git 不存在**（PATH 置空）→ `git_unavailable` 而非崩溃；未跟踪文件行数统计（文本按真实行数含无尾换行；二进制标 `binary=true`；超单文件上限/总预算 → `-1` + `warnings[]`） |
| diff 解析 | 新增/删除文件、`\ No newline at end of file`、纯空白变更、超限截断（`truncated=true`）、CRLF 文件、未跟踪文件合成 `--no-index` 新增 diff（空文件 `status="A"` / 二进制 / 无尾换行 / 点开前被删除 → `git_failed`）、请求目标无改动时回退并回报 `target_fallback` |

---

## 6. 契约映射表（端点 ↔ 前端函数 ↔ 测试）

| 端点 | 前端函数（`api/runtime/*`） | 归一化/降级约定 | 测试 |
| --- | --- | --- | --- |
| `GET /fs/roots` | `fetchFsRoots()` | `roots` 缺失 → 抛错；`exists/is_git_repo` 缺省 `false` | `fs-roots.test.ts` |
| `GET /fs/list` | `fetchFsListing(params)` | `entries` 非数组 → 抛错；`next_cursor` 非字符串 → `null`；`has_more` 缺省 `false` | `fs-list.test.ts` |
| `GET /fs/preview` | `fetchFsPreview(params)` | `kind` 白名单校验；`text` 与 `kind` 不匹配 → 抛错 | `fs-preview.test.ts` |
| `HEAD /fs/download` | `probeDownload(params)` | 无 `ETag` → 视为不可续传 | `fs-transfer.test.ts` |
| `GET /fs/download` | `downloadRange(params, start, end)` | 206/200 分支；416 → 抛 `RangeNotSatisfiableError` | 同上 |
| `POST /fs/upload/init` | `initUpload(req)` | `completed=true` → 秒传；`upload_id` 空但未完成 → 抛错 | 同上 |
| `PUT /fs/upload/{id}/chunk` | `putUploadChunk(id, blob, start, total)` | 409 + `expected_offset` → 抛 `OffsetMismatchError{expected}` | 同上 |
| `POST /fs/upload/{id}/complete` | `completeUpload(id)` | `action` 白名单校验 | 同上 |
| `GET /git/status` | `fetchGitStatus(params)` | 四类分组缺失 → 按空数组；rename 缺 `from/to` → 跳过并记录 | `git.test.ts` |
| `GET /git/diff` | `fetchGitDiff(params)` | `hunks` 非法结构 → 抛错并提示（**不静默当空 diff**，避免"文件没改动"的假象） | `git.test.ts` |
| `GET /git/commits` | `fetchGitCommits(params)` | 同上 | `git.test.ts` |

> 归一化纪律沿用 `api/runtime/files.ts` 的既有注释风格：把「后端契约 + 归一化规则 + 降级判据」写在文件头部注释里，作为该模块的契约文档。

---

## 7. 分期实施计划

每期结束都必须：`npm run lint`（含 i18n/行数校验）通过、`npm test` 通过、`go test ./...` 通过、功能可独立演示。**每期的产出可单独合并**。

### P0 — 面板机制 + 目录浏览（骨架打通）

| 任务 | 交付物 | 验收 |
| --- | --- | --- |
| P0-1 面板注册表 | `panel-registry.ts`、`surface-tabs.tsx` 改造、`ArtifactPanelSurface` 兼容别名 | 既有 4 个页签行为、a11y、`right-rail-section.test.tsx` 全绿；新增 2 个页签可点（先显示占位） |
| P0-2 可变宽度右栏 | `lib/layout/rail-width.ts`、`use-right-rail-width.ts`、`rail-resize-handle.tsx`、settings 两字段 + 外观页滑块、CSS 变量列宽、覆盖层抽屉 | ① `auto` 下内容型面宽度 = 288px（与现状逐像素一致）；② 拖拽/键盘/双击复位均生效，刷新后 manual 宽度保留；③ 视口缩到 `xl` 附近主区仍 ≥ 512px、聊天区不被压坏；④ `<xl` 不渲染右栏列；⑤ 关闭右栏不占位；⑥ 既有响应式与右栏测试全绿 |
| P0-3 后端作用域 | `internal/fsscope` + `/fs/roots` | 越界/编码/符号链接负例全部 400（§5.10 表第一、二行） |
| P0-4 后端列表 | `/fs/list`、`/fs/stat` + `filebrowse` 服务与注入 | 1000 项目录分页不重不漏；排序稳定；超大目录 `truncated` |
| P0-5 前端目录树 | `use-file-browser.ts`、`tree-list.tsx`、`scope-header.tsx`、虚拟滚动 + Git 状态角标（Q6） | 万级目录滚动不掉帧（实测记录）；展开/折叠不重复请求；竞态用例（快速连点）不串层；非仓库时不显示角标 |
| P0-6 i18n 骨架 | `panels-file-browser.ts`（zh/en） | `npm run lint:i18n` 通过 |

**P0 结束态**：右侧栏宽度可拖拽/键盘可调并持久化（默认 `auto` 与现状一致），能切到「文件」页签，选作用域根，逐层浏览目录，看到名称/大小/时间与 Git 状态角标，能刷新与过滤。**尚不能预览、不能传输、Git 页签为空态。**

### P1 — 预览（md/文本/图片/二进制分流）

| 任务 | 交付物 | 验收 |
| --- | --- | --- |
| P1-1 `/fs/preview` | `filebrowse` 预览实现（文本截断/图片/二进制判定） | 二进制与超限语义与 `decode.ts` 完全一致（对照测试） |
| P1-2 前端预览 | `preview-pane.tsx`、`text-viewer.tsx`（prism 高亮 + 虚拟化） | 1MB 日志、5000 行代码、二进制、超限文件 4 类用例表现正确且不卡 |
| P1-3 markdown | md 渲染（复用 react-markdown 能力） | 不加载外部资源；超长 md 分块 |
| P1-4 与既有弹层共存 | `file-preview-dialog` 保留，面板内预览并行 | 既有 `e2e/file-preview.spec.ts` 不回归 |

### P2 — 传输（上传分片续传 + 下载 Range）

| 任务 | 交付物 | 验收 |
| --- | --- | --- |
| P2-1 下载后端 | `/fs/download`（`http.ServeContent`）+ `HEAD` | Range/416/ETag 用例全过（§5.10） |
| P2-2 上传后端 | `/fs/upload/*` 全套 + 过期清理 + 审计日志 | 断点续传、乱序 409、checksum 失败、abort 清理全部通过 |
| P2-3 前端上传 | `use-file-transfer.ts` + `transfer-tray.tsx` | 5GB 文件暂停/继续/刷新恢复（Chromium）；`localStorage` 记录不残留 |
| P2-4 前端下载 | Tier A/B/C 三层策略 + 进度 | 非 Chromium 明确提示「不支持续传」；ETag 变化作废续传 |
| P2-5 Windows 落盘 | `os.Rename` 覆盖处理 | Windows 覆盖已存在文件成功（不出现 `Access is denied`） |

### P3 — Git 只读（变更列表 + diff 渲染）

| 任务 | 交付物 | 验收 |
| --- | --- | --- |
| P3-1 git 沙箱 | `gitbrowse/exec.go`（超时/限额/降级） | PATH 无 git → `git_unavailable`；15s 卡死命令被 kill |
| P3-2 status | porcelain v2 解析 + numstat | 分组/重命名/二进制/冲突用例（临时仓库）全过 |
| P3-3 diff | unified → hunks 解析 + 截断 | 新增/删除/无尾换行/CRLF/纯空白/超限用例全过；未跟踪文件合成新增 diff（点击可见内容）与目标回退（`target_fallback`）用例全过 |
| P3-4 变更列表 | `change-list.tsx`（分组、树/平铺、状态徽标、计数） | 与 `git status` 输出逐项对照一致 |
| P3-5 diff 视图 | `diff-view.tsx`/`diff-hunk.tsx`/`virtual-line-list.tsx` | unified/split 切换、折叠展开、空白忽略（重新取 diff）、复制原文 |
| P3-6 提交视图（可选裁剪） | `commit-list.tsx` | 点击提交看 diff |
| P3-7 大 diff 性能 | 视口渲染 + 高亮缓存 | 2 万行 diff 首屏 < 500ms（本机实测） |

### P4 — Git 写操作（stage/unstage，commit 不做）与打磨

| 任务 | 说明 |
| --- | --- |
| P4-1 stage/unstage | `POST /git/stage`；乐观更新 + 失败回滚 + 明确文案（**不自动提交**）；**已确认本期实现**（Q2） |
| P4-2 commit | **本期确认不做**（Q4）；如需启用必须二次确认 + 提交信息输入 + 独立评审 |
| P4-3 拖拽上传 | 拖到面板内上传到当前目录；视觉反馈 + 冲突提示 |
| P4-4 右键菜单/多选 | 多选下载、复制路径 |
| P4-5 全树搜索（可选） | 本期只做**已加载层前端过滤**（Q3）；服务端 `rg` 全树搜索留作后续独立评估（本仓已有 rg 使用先例），需独立限流设计 |

**排期建议（单人全职口径，含测试）**：P0 ≈ 4–5 天（新增「可变宽度右栏」含拖拽/a11y/设置入口，约 +1 天）；P1 ≈ 3 天；P2 ≈ 5–6 天（后端续传与 Windows 落盘是最耗时项）；P3 ≈ 5 天；P4 ≈ 2–3 天（含 P4-1 stage/unstage）。合计 ≈ **19–22 天**（不含 P4-5 全树搜索）。

---

## 8. 测试与验收

### 8.1 自动化测试

| 层 | 工具 | 覆盖 |
| --- | --- | --- |
| 后端单测 | `go test ./...`（`httptest` + `mux.NewRouter()` + `RegisterRoutes`，照 `file_transfer_handlers_test.go`） | 作用域越界、分页、上传续传、Range 下载、git 解析、diff 解析、降级（无 git / 非仓库 / 无权限） |
| 后端 git 测试 | `t.TempDir()` + `git init/commit`（环境有 git；缺失则 `t.Skip`） | status 分组、rename、二进制、diff 各种边角 |
| 前端单测 | `vitest run` | API 归一化（每模块 `*.test.ts`）、状态机（竞态/取消/重试/续传分支）、纯函数（排序/面包屑/split 配对） |
| 前端 e2e | `playwright test` | ① 目录浏览 → 预览 md/文本；② 上传小文件 + 暂停/继续；③ Git 变更列表 → diff 渲染 → 切换 unified/split |

**后端必测负例清单**见 §5.10；**契约测试**要求「端点 ↔ 前端函数」一一对应（§6 表），避免只测正常路径。

### 8.2 手工验收清单（每期结束跑一遍）

1. 打开 `/workspace/chats/new`，右侧栏切「文件」：能选根 → 展开 3 层 → 面包屑回跳正确 → 刷新可见外部新增文件；
2. **右栏宽度**：默认（`auto`）在内容型面下与改造前像素一致（288px）；拖到 640px 松开后刷新，宽度保留；双击手柄回到自适应；`Tab` 聚焦手柄后用 `←`/`→`/`Home`/`End` 调整、`Enter` 复位；窗口从 2560px 缩到 1280px 时右栏收窄且主区 ≥ 512px；缩到 `xl` 以下右栏列消失；
3. 打开一个包含 5000+ 文件的目录：滚动流畅、不一次性请求全量（DevTools Network 核对请求次数与 `limit`）；
4. 预览：`.md`（中文 + 表格 + 代码块）、`.ts`（高亮 + 行号）、`.png`、`.exe`（二进制提示）、2GB 文件（超限提示 + 下载入口）；
5. 上传：500MB 文件 → 中途暂停 10s → 继续 → 完成后比对 sha256；上传同名文件 → 冲突提示三种策略各试一次；上传中断网 → 恢复后续传；
6. 下载：Chromium 用 Tier A 暂停/继续；Firefox 用 Tier C 得到「不支持续传」的说明；
7. Git：改 3 个文件（含重命名、含二进制）→ 变更列表计数与 `git status` 一致 → diff 与 `git diff` 一致（含 `\ No newline`）→ 空白忽略开关行为正确；行号只有**一列**（unified 不再并排老/新两列，split 每侧各一列）；另加**未跟踪新文件**与**已删除文件**：计数为真实新增行数（读不到时显示「统计不可用」而非 `+0 −0`）、点击可见新增/删除内容、对比目标不覆盖改动侧时自动回退并标注；
8. 在非 Git 目录选 Git 页签 → 显示「不是 Git 仓库」空态，不报错；
9. 面板切换不丢上传进度；关闭右侧栏后重新打开，文件/Git 面状态、作用域与宽度保持。

### 8.3 「完成」的定义（DoD）

- 所有新增端点有契约文档（§5）+ 归一化测试 + 至少一个负例测试；
- `npm run lint`（含 `verify-frontend-i18n` / `verify-max-lines` / `verify-no-backups`）与 `npm test`、`go test ./...` 全绿；
- 无 `dangerouslySetInnerHTML`；无新增 `any`；无 `console.log` 残留；
- 前端新文件 ≤ 500 非空行（目标 ≤ 300）；
- 降级路径有测试（git 缺失、服务未注入、端点 404/503）。

---

## 9. 风险、边界与回滚

| 风险 | 等级 | 对策 |
| --- | --- | --- |
| 右侧栏宽度改造影响既有布局与测试 | 中 | 默认 `auto` 且内容型面锁 288px（与现状同值）；CSS 变量注入、网格字面量不变；先补「默认宽度 288px」断言保护再改实现，并回归 `right-rail-section.test.tsx`、`workspace-sidebar-responsive.test.tsx` |
| 拖拽调宽时每帧重渲染导致卡顿（尤其 diff/预览面） | 中 | 拖拽期间**只写 CSS 变量**（rAF 节流、零 setState），`pointerup` 才提交一次；P0-2 验收含「拖拽时 diff/预览面不掉帧」实测 |
| 拖拽在图片/文本上触发原生 drag 或文本选区，手柄「跟丢」 | 中 | `setPointerCapture` + body `userSelect:none` + 内容层临时 `pointer-events-none`（§4.2.1 步骤 3）；手柄加 `touch-none` |
| 持久化的宽宽度在窄屏把主区压垮 | 中 | 所有入口（拖拽/auto/设置/历史值/视口变化）统一走 `clampRailWidth()`；显示宽度可收窄但不改写用户持久化值；`maxWidth < minWidth` 降级覆盖层 |
| 拖拽中组件卸载导致页面卡在 `userSelect:none` | 低 | `pointerup` / `pointercancel` / `unmount` 三处清理 + rAF 取消；hook 单测覆盖卸载分支 |
| 大目录/大 diff 卡顿或打爆内存 | 高 | 服务端截断 + 游标分页 + 客户端虚拟化 + 高亮仅可视行 + 实测记录（P0-5/P3-7 验收项） |
| Windows `os.Rename` 覆盖失败 | 高 | P2-5 专项；显式 remove-then-rename + 回退复制删除；测试在 Windows 环境跑 |
| 上传临时文件污染工作区 | 中 | 固定隐藏目录 + 列表隐藏 + TTL 清理 + 审计日志；UI 提供「清理未完成上传」 |
| 前端「可扩展面板」改造触碰既有面板 | 中 | 注册表先包一层，既有 4 面的 tab id/aria/testid **字符串保持不变**；分两步提交（先重构后新增） |
| git 命令注入 / 路径以 `-` 开头 | 中 | 参数数组 + `--` 分隔 + 拒绝以 `-` 开头的 path；不接受任何 shell 拼接 |
| `frontend/src` 500 行卡口导致返工 | 中 | 按 §4.7 预算先建骨架再填逻辑；大组件先拆容器与视图 |
| 作用域被误解为安全隔离 | 中 | 文档与 UI 文案明确「单机信任模型下限权，不是多租户隔离」（§5.2） |
| 与 agent 并发写文件导致读到中间态 | 低 | 只读优先；预览带 `mtime`，变化时提示刷新；不做锁 |
| Git 面依赖系统 git | 低 | 启动探测 + `git_unavailable` 降级 + 空态说明 |
| 新增依赖引入体积 | 低 | 默认零新依赖（自研虚拟列表/高亮复用 prism）；若实施证明必要，单独提交并说明体积影响 |

**回滚策略**：所有改动**追加式**——新端点、新文件、新页签；不修改既有端点的语义。回滚 = 从注册表移除 2 个面 + 后端不注入新服务（端点消失 → 前端按 404 降级），无需回滚数据。唯一例外是 `right-rail-section.tsx` / `surface-tabs.tsx` / `workspace-shell.tsx` 的改造（P0-2 前必须先有回归测试保护）。

---

## 10. 关键代码位置索引

**现状（必须理解/改动的既有文件）**

| 位置 | 作用 |
| --- | --- |
| `frontend/src/components/workspace/workspace-shell/right-rail-section.tsx:1-59` | 右侧栏容器（开合、错误边界、懒加载） |
| `frontend/src/components/workspace/workspace-shell.tsx:157-162,193-197,251-257,364-372` | 右侧栏状态、既有 `ResizeObserver`（auto 宽度重算复用点）、网格列宽、面 props |
| `frontend/src/components/workspace/workspace-shell-topbar.tsx:70-77,311-323` | 顶栏右侧栏开关（`topbar-toggle-right-rail`） |
| `frontend/src/components/workspace/artifact-panel/types.ts:6,18-27` | 面枚举与 tab id（扩展点） |
| `frontend/src/components/workspace/artifact-panel/surface-tabs.tsx:20-60+` | 页签渲染与 tone 样式（收敛点） |
| `frontend/src/api/runtime/files.ts:1-100` | 既有文件读取契约与降级判据（范式） |
| `frontend/src/lib/file-preview/decode.ts:1-50` | 字节→展示内容解码纪律（复用） |
| `frontend/src/hooks/workspace/use-file-preview.ts:1-70` | 预览状态机口径（复用） |
| `frontend/src/core/settings/local.ts:96-100,135-139,205-209,289-293` | `workspace` 设置段：类型定义 / 默认值 / normalize 助手范式 / normalize 调用点（`rightRailWidthMode`、`rightRailWidthPx` 的落点） |
| `frontend/src/components/workspace/trajectory/use-trajectory-timeline-window.ts:330-340` | `setPointerCapture` + ref 几何拖拽先例（右栏手柄照此实现） |
| `frontend/scripts/verify-max-lines.mjs:23-26` | 500 非空行硬卡口 |
| `backend/internal/api/skills/handler.go:71,650-652,705-707` | runtime 路由前缀、注册入口、既有 fs 端点 |
| `backend/internal/api/skills/file_transfer_handlers.go:14-18,36-64,74-113` | 既有传输接口/响应结构与 503 口径（范式） |
| `backend/internal/filetransport/service.go:36-131,133-164` | 本地读写实现与**无根约束**的路径解析（对比项） |
| `backend/internal/api/skills/workspace_directory_handlers.go:36-70` | 工作目录注册表（作用域根来源） |
| `backend/cmd/runtime-server/main.go:993,1104-1112` | 服务注入与路由装配顺序 |
| `backend/internal/artifact/checkpoint_files.go:15-57` | diff 先例（DiffText + blob 去重） |
| `backend/internal/api/skills/file_transfer_handlers_test.go:19-53` | 后端 handler 测试范式 |

**规划新增（目录一览，详见 §4.7 / §5.9）**

```
frontend/src/
  components/workspace/panel-registry.ts
  components/workspace/file-browser-surface.tsx
  components/workspace/file-browser/{tree-list,scope-header,preview-pane,text-viewer,image-preview,transfer-tray}.tsx
  components/workspace/git-surface.tsx
  components/workspace/git/{change-list,diff-view,diff-hunk,commit-list}.tsx
  components/workspace/diff/virtual-line-list.tsx
  hooks/workspace/{use-file-browser,use-file-transfer,use-git-changes}.ts
  api/runtime/{fs-roots,fs-list,fs-preview,fs-transfer,git}.ts
  types/runtime/fs-browser.ts
  lib/file-browser/{entry-sort,path-utils}.ts
  lib/git/diff-view-model.ts
  i18n/resources/{zh-CN,en-US}/workspace/{panels-file-browser,panels-git}.ts

backend/internal/
  fsscope/{scope.go,scope_test.go}
  filebrowse/{service.go,upload.go,upload_state.go}
  gitbrowse/{exec.go,status.go,diff.go}
  api/skills/{fs_browser_handlers.go,fs_transfer_handlers.go,git_handlers.go}
```

---

## 11. 自审与决策记录

### 11.1 已在本文档内自审、给出结论的点

| 问题 | 结论 |
| --- | --- |
| 文件浏览器要不要支持编辑/删除？ | 一期只读（§1.3-2/3）。理由是冲突检测与外部漂移会显著抬高复杂度，且 agent 本身在写工作区 |
| 要不要把既有 `fs/read-file` 改造成支持截断？ | 不改（D6）。会破坏 agent 工具链依赖的语义 |
| 目录排序在前端还是后端？ | 后端（D4）。前端只排已加载的一页会产生「假的全局有序」 |
| 空白忽略在 diff 前端过滤还是重取？ | 重取（§4.6）。前端过滤无法还原被折叠的上下文 |
| 是否引入虚拟滚动/diff 依赖？ | 默认不引入（D8），实施中若证明必要再单独评估 |
| git 用 CLI 还是库？ | CLI（D7），语义与用户本地一致，且零新依赖 |
| 宽度是「少数档位」还是「连续可调」？ | **连续可调 + `auto` 自适应默认**（用户 2026-09-16 明确要求「可变 / 用户可调 / 自适应」）；用户拖拽后转 `manual` 并记忆，双击/`Enter` 回 `auto`（D2、§4.2） |
| 右栏宽度是否需要「重置」以外的持久化边界？ | 显示宽度永远过 `clampRailWidth()`（视口变化可显示收窄），但**持久化值不被视口改写**；`<xl` 退化为覆盖层（§4.2 状态模型表） |

### 11.2 已拍板决策（2026-09-16，用户确认「其余问题按最优建议执行」）

> 说明：原 Q1–Q8 的备选与推荐理由保留在「原备选/说明」列，**最终以「决策」列为准**，实施阶段不再回头讨论；Q0 为用户本轮新增指示。

| # | 决策（已生效） | 落地位置 | 原备选/说明 |
| --- | --- | --- | --- |
| Q0（新增） | 右栏宽度**连续可调 + `auto` 自适应默认**：拖拽分隔条 / 键盘可调 / 双击与 `Enter` 复位；持久化「模式 + 像素」；主区最小宽度硬保护；`<xl` 退覆盖层 | D2、§4.2、§4.2.1、§4.2.2、P0-2 | 用户 2026-09-16 明确指示，取代原「两档宽度（narrow/wide）」设计；变更记录见 §0 |
| Q1 | 文件/Git 面在 `auto` 下按面自适应：`clamp(0.32 × vw, 416px, 672px)`；内容型面锁 288px；用户拖拽后转 `manual` | §4.2 状态模型 | 原建议「进入即 wide、可手动回 narrow」被 Q0 取代 |
| Q2 | **本期实现 stage/unstage（P4-1）**；commit 不进入本期 | §7 P4 | 同原建议 ②（commit 单独评估） |
| Q3 | 本期只做**已加载层的前端过滤**；全树 `rg` 搜索列为后续独立评估（需限流设计） | §7 P4-5 | 同原建议 ③ + ②后续 |
| Q4 | **不允许前端发起 commit** | §7 P4-2（保留描述但标注未启用） | 同原建议 ①（agent 工作区场景收益与风险不匹配） |
| Q5 | 上传上限 **10GiB**、分片 **4MiB**，两者均可配置 | §5.4 | 同原建议 ① |
| Q6 | 文件浏览器**显示 Git 状态角标**：一次 `/git/status` 建映射，非仓库时跳过 | §4.3、§4.6 | 同原建议 ① |
| Q7 | 预览**以面板内联为主**，同时保留既有 `FilePreviewDialog` 弹层入口 | §4.5 | 同原建议 ① |
| Q8 | 最近访问目录/文件**按会话记忆**（sessionStorage，不写全局设置） | §4.3 | 同原建议 ② |

### 11.3 实施时的工程纪律提醒（来自本仓既有教训）

1. **大文件分块落地**：前端新组件先写空壳骨架，再按 section 分块补丁；单次补丁不要逼近 Windows 命令长度上限（`AGENTS.md`：跨 shell 链路按 8191 预算、单命令 ≤ 6000 字符）。
2. **先有测试再重构既有面板**（P0-1/P0-2 顺序不能颠倒）。
3. **新增端点必须写负例测试**，尤其是越界与降级路径 —— 本仓既有测试风格（`file_transfer_handlers_test.go`）已证明这条路可行。
4. **不要把「不可用」包装成「空数据」**：git 缺失、端点 503、路径不可访问，都必须如实呈现（这是 `api/runtime/files.ts:86-100` 与 `decode.ts:1-7` 已经建立的仓内纪律）。
5. **拖拽类交互不要每帧 `setState`**：本仓 `use-conversation-scroll.ts` 已建立「rAF 节流 + 帧内几何缓存」纪律，右栏宽度沿用（§4.2.1）：拖拽期间只写 CSS 变量，`pointerup` 才提交一次。
6. **改既有面板前先加保护性断言**：P0-2 先补「`auto` 下默认宽度 = 288px」再改实现；P0-1 先补既有 4 个页签的行为断言再重构注册表。两条都要求「先绿后改」，避免边重构边排障。
