# Skill Summary-First 实现状态

Date: 2026-03-18
Scope: 当前仓库 `skill runtime` 的 summary-first / lazy-load 实现状态

## 结论

当前仓库的 `skill runtime` 已经从：

- **full-load registry**

演进为：

- **summary catalog + loaded cache + lazy hydration**

这意味着：

1. 启动期默认只做 discovery
2. `Registry` 持有 summary catalog
3. 读接口和执行接口按需 hydration full skill
4. hydration 结果进入 `Registry` 的 loaded cache
5. `reload / hot reload / persist / delete` 会主动失效缓存

---

## 架构分层

### 1. Discovery 层

职责：

- 扫描 `skill.yaml/skill.yml`
- 解析最小元数据
- 发现 `prompt.md` 路径
- 不加载 prompt 正文

关键位置：

- `internal/runtime/skill/manifest.go`
- `internal/runtime/skill/loader.go`

关键接口：

- `ParseSummaryFile`
- `ParseSummaryBytes`
- `ParseSummaryDir`
- `Discover`
- `DiscoverAll`
- `DiscoverAllWithRegistry`

### 2. Summary Catalog 层

职责：

- 保存轻量 skill 摘要
- 作为上层“技能目录”的稳定来源

关键位置：

- `internal/runtime/skill/summary.go`
- `internal/runtime/skill/registry.go`

关键接口：

- `SkillSummary`
- `SummaryFromSkill`
- `Registry.GetSummary`
- `Registry.ListSummaries`

### 3. Router / Search 层

职责：

- 继续复用 `Skill` stub 完成 keyword / pattern / embedding 路由
- 不要求启动期具备完整 prompt/workflow args 正文

关键位置：

- `internal/runtime/skill/router.go`
- `internal/runtime/skill/embedding_router.go`

### 4. Hydration 层

职责：

- 遇到 discovery stub 时，按 source path 加载完整 skill
- 以 manifest/prompt 的 `mtime + size` 作为失效依据
- 返回 clone，避免调用方污染缓存

关键位置：

- `internal/runtime/skill/hydrate.go`

关键接口：

- `HydrateSkill`
- `HydrateSkillWithRegistry`
- `Registry.Hydrate`
- `Registry.InvalidateLoadedSkill`
- `Registry.ClearLoadedCache`

### 5. Execute / Read 层

职责：

- 在真正执行或对外返回详情前再 hydrate full skill

关键位置：

- `internal/runtime/skill/executor.go`
- `internal/api/skills/handler.go`

---

## 当前已经切换为 summary-first 的入口

### 1. Gateway 全局 skills runtime

- `internal/gateway/router/gin.go`
- `runtimebootstrap.NewManager(... DiscoverOnly: true ...)`

### 2. CLI skill exposure

- `cmd/aicli/commands/skills_integration.go`
- 显式使用 `Registry().ListSummaries()`

### 3. 本地 chat actor host

- `cmd/aicli/commands/chat_actor_host.go`
- 从 summary 转 stub，再注册到 agent

### 4. Profile runtime

- `internal/api/skills/profile_support.go`
- discovery 后把 stub 注册到 registry

### 5. HotReload

- `internal/runtime/skill/hot_reload.go`
- reload 时注册 discovery stub，而不是 full skill

---

## 读/执行路径当前行为

### 1. 执行路径

- `Executor.Execute` 会先检查 skill 是否为 discovery stub
- 如果是，则通过 registry hydration 或 fallback hydration 加载 full skill
- 然后再执行：
  - custom handler
  - workflow
  - default LLM execution

### 2. 读接口

以下 handler 读接口已显式 hydration：

- `ListSkills`
- `GetSkill`
- `SearchSkills`
- `ExportSkills`

### 3. Search 返回

搜索结果中：

- `results`
- `matches[].skill`

都会返回 hydration 后的 full skill 视图。

---

## 缓存与失效

### 1. Registry 内部缓存

`Registry` 当前持有：

- `skills`：router 使用的 stub/full 对象
- `summaries`：轻量目录
- `loadedCache`：hydration 得到的 full skill cache

### 2. 失效触发点

以下操作会清理 loaded cache：

- `HotReload.reloadSkill`
- `HotReload.removeSkillByFile`
- `HotReload.reloadAllSkills`
- `Handler.persistSkill`
- `Handler.deletePersistedSkillFile`
- `Handler.ReloadSkills`

### 3. 自动失效

即使没有显式失效：

- `HydrateSkillWithRegistry` 也会检查 manifest/prompt 文件戳
- 只要 `mtime` 或 `size` 变化，就会重新解析

---

## 当前仍保留的兼容策略

为了避免大面积改动：

- router / embedding router 仍然基于 `Skill` 对象工作
- summary 会转换成 stub skill 进入 registry 与索引
- full skill 只在需要时 hydration

这意味着当前实现是：

- **summary-first**
- 但不是“完全抛弃 Skill stub”

这是有意的兼容层设计。

---

## 推荐后续规范

### 应该优先使用

- `Registry.ListSummaries()`：上层目录、暴露、预览
- `Registry.Hydrate(...)`：读详情或执行前

### 尽量不要再新增

- 启动期 `LoadAllWithRegistry(...)` 作为默认路径
- 上层直接把 `Registry.List()` 当成 catalog 使用

### 允许继续使用

- router / embedding / capability 投影继续读 registry 中的 stub

---

## 现状判断

当前 `skill runtime` 的 summary-first/lazy-load 主链已经基本闭环，后续更适合做的是：

1. 文档和规范收口
2. 新增代码约束尽量走 `ListSummaries`
3. 若未来需要，再把 router 内部索引从 `Skill stub` 进一步下沉到真正的 summary-native 结构

但从工程收益和风险比来看，当前实现已经足够支撑：

- 启动轻量化
- prompt 正文懒加载
- 读接口按需展开
- 执行路径按需解析
- 热更新与缓存失效闭环
