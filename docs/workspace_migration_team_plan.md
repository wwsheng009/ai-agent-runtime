# 页面与 Workspace 结构迁移执行方案

更新时间：2026-03-31

本文专门聚焦两个问题：

1. `E:\projects\ai\deer-flow\frontend` 中 `page / workspace` 的结构与布局到底是什么。
2. 在 `ai-agent-runtime/frontend` 已经存在的 Vite 工作台基础上，应该如何按 team 成员边界推进迁移，而不是继续在少数大文件上串行硬拆。

## 1. 本轮已创建的 team 成员

本轮已经按并行分析和迁移执行创建了多个成员，职责如下：

| 成员 | 角色 | 关注点 | 当前结果 |
| --- | --- | --- | --- |
| Team Member A | Landing 结构分析 + 基础质量收口 | 来源 landing 页面骨架、可复用 section、基础组件 lint | 已完成 |
| Team Member B | Runtime API 模块化 | runtime API、SSE、shared fetch helper、兼容导出 | 已完成 |
| Team Member C | Workspace 页面状态拆分 | `workspace-page.tsx` 中 session/runtime/chat 状态 | 第一轮完成 |
| Team Member D | Runtime Teams 拆分 | `runtime-teams.tsx` 的共享类型、排序、格式化、dispatch 模板 | 第一轮完成 |

这些成员已经完成首轮工作，当前 `frontend/pnpm lint` 与 `frontend/pnpm build` 都已通过。

## 2. 来源项目 Landing 结构分析

### 2.1 页面入口组成

来源项目 landing 页入口在 [src/app/page.tsx](E:/projects/ai/deer-flow/frontend/src/app/page.tsx)。

页面结构非常直接：

1. `Header`
2. `Hero`
3. `CaseStudySection`
4. `SkillsSection`
5. `SandboxSection`
6. `WhatsNewSection`
7. `CommunitySection`
8. `Footer`

也就是说，来源 landing 页本质上是：

- 固定顶部 header
- 一个占满首屏的 hero
- 若干标准 section 顺序堆叠
- 底部 footer

### 2.2 landing 的层次结构

主要组件层次如下：

```text
src/app/page.tsx
  Header
  main
    Hero
    CaseStudySection
    SkillsSection
    SandboxSection
    WhatsNewSection
    CommunitySection
  Footer
```

这些 section 绝大多数都通过统一的 [src/components/landing/section.tsx](E:/projects/ai/deer-flow/frontend/src/components/landing/section.tsx) 包装，形成一致的节奏：

- 外层 `section`
- 居中 header
- 标题与副标题
- 下方主体内容区

这意味着 landing 页在结构上不是“每段都完全不同”，而是“统一 section 壳 + 每个 section 内部有自己的视觉组件”。

### 2.3 landing 的共享布局模式

来源 landing 页的共享布局模式有几个明显特征：

1. `Header` 固定在顶部，带轻微毛玻璃和底部分隔线，见 [src/components/landing/header.tsx](E:/projects/ai/deer-flow/frontend/src/components/landing/header.tsx)。
2. `Hero` 是真正的首屏视觉中心，使用全屏高度、居中标题、动态背景、主 CTA，见 [src/components/landing/hero.tsx](E:/projects/ai/deer-flow/frontend/src/components/landing/hero.tsx)。
3. 下面的各个 section 通过统一 `Section` 组件控制标题区样式和纵向节奏。
4. 页面整体更像“产品官网”，不是“文档站”或“控制台壳”。

### 2.4 landing 的视觉骨架

来源项目 landing 的视觉骨架可以概括为：

- 深色背景
- 强视觉 hero
- 明显的动态/动画成分
- Section 之间通过统一标题样式串联
- 内容区块类型包括：
  - 案例卡片网格
  - 技能动画展示
  - runtime/sandbox 说明区
  - bento 功能矩阵
  - 社区 CTA

其中，hero 和 skills section 的视觉密度最高。

### 2.5 哪些 landing 组件适合直接迁移

适合直接迁移或高强度参考的部分：

- [src/components/landing/section.tsx](E:/projects/ai/deer-flow/frontend/src/components/landing/section.tsx)
  - 这是典型的浏览器侧展示壳，平台耦合低。
- [src/components/landing/sections/case-study-section.tsx](E:/projects/ai/deer-flow/frontend/src/components/landing/sections/case-study-section.tsx)
  - 其“案例卡片网格 + hover 视觉”适合迁移概念和局部实现，但链接逻辑需改。
- [src/components/landing/sections/whats-new-section.tsx](E:/projects/ai/deer-flow/frontend/src/components/landing/sections/whats-new-section.tsx)
  - 适合复用“功能矩阵展示”的结构思想。
- [src/components/landing/sections/community-section.tsx](E:/projects/ai/deer-flow/frontend/src/components/landing/sections/community-section.tsx)
  - 属于低耦合 CTA 区块。
- [src/components/landing/footer.tsx](E:/projects/ai/deer-flow/frontend/src/components/landing/footer.tsx)
  - 平台依赖很低。

### 2.6 哪些 landing 组件不适合直接迁移

不适合直接搬运，或只能重写式迁移的部分：

- [src/components/landing/header.tsx](E:/projects/ai/deer-flow/frontend/src/components/landing/header.tsx)
  - 有 `env`、GitHub star 统计、服务端 fetch 再验证等来源项目上下文。
- [src/components/landing/hero.tsx](E:/projects/ai/deer-flow/frontend/src/components/landing/hero.tsx)
  - 视觉可以借鉴，但依赖 `next/link`、自定义动画组件、mask 资源、背景组件，直接迁成本高。
- [src/components/landing/progressive-skills-animation.tsx](E:/projects/ai/deer-flow/frontend/src/components/landing/progressive-skills-animation.tsx)
  - 动画复杂、上下文重、实现体量大，适合最后阶段再评估，不适合作为当前迁移优先级。
- `pathOfThread`、demo thread 资源、来源项目图片命名约定
  - 这些都与 Deer Flow 的 thread 与 demo 数据结构绑定。

## 3. 来源项目 Workspace 结构分析

### 3.1 路由骨架

来源项目 workspace 的关键入口包括：

- [src/app/workspace/layout.tsx](E:/projects/ai/deer-flow/frontend/src/app/workspace/layout.tsx)
- [src/app/workspace/page.tsx](E:/projects/ai/deer-flow/frontend/src/app/workspace/page.tsx)
- [src/app/workspace/chats/page.tsx](E:/projects/ai/deer-flow/frontend/src/app/workspace/chats/page.tsx)
- [src/app/workspace/chats/[thread_id]/page.tsx](E:/projects/ai/deer-flow/frontend/src/app/workspace/chats/[thread_id]/page.tsx)

这个结构说明来源项目的 workspace 不是单页，而是一个完整路由域：

- `layout` 负责全局 shell
- `page` 负责 workspace 根路由跳转
- `chats` 负责线程列表页
- `chats/[thread_id]` 负责具体聊天页

### 3.2 workspace 的全局布局骨架

来源项目真正的 workspace 外层壳在 [src/app/workspace/layout.tsx](E:/projects/ai/deer-flow/frontend/src/app/workspace/layout.tsx)：

- `QueryClientProvider`
- `SidebarProvider`
- `WorkspaceSidebar`
- `SidebarInset`
- `CommandPalette`
- `Toaster`

这说明来源项目的 workspace 骨架是：

`Sidebar + Main Inset + 全局命令面板/通知层`

这和当前 Vite 前端的三栏直接铺开结构不同。

### 3.3 workspace sidebar 的结构

sidebar 相关核心文件：

- [src/components/workspace/workspace-sidebar.tsx](E:/projects/ai/deer-flow/frontend/src/components/workspace/workspace-sidebar.tsx)
- [src/components/workspace/workspace-header.tsx](E:/projects/ai/deer-flow/frontend/src/components/workspace/workspace-header.tsx)

来源 sidebar 结构是：

- Header 区
- 导航列表
- Recent Chat List
- Footer 菜单
- 可折叠 rail

这意味着来源项目的 sidebar 是一个“导航容器”，不是当前 Vite 项目里那种“线程列表 + runtime teams 控制面”混合体。

### 3.4 thread 页面主布局

具体聊天页在 [src/app/workspace/chats/[thread_id]/page.tsx](E:/projects/ai/deer-flow/frontend/src/app/workspace/chats/[thread_id]/page.tsx)。

它的主布局可概括为：

- 顶部悬浮 thread header
- 中央 message list
- 底部悬浮 input box
- 输入区上方叠加 todo list
- artifact 通过 trigger 打开，不是固定右栏

这点非常关键：

来源项目的 artifact 不是三栏固定 rail，而更像“独立的 artifact 面板 / drawer / detail 视图”。

### 3.5 workspace 中消息与制品的布局关系

来源项目的消息区和制品区是分离的：

- 消息区在 [src/components/workspace/messages/message-list.tsx](E:/projects/ai/deer-flow/frontend/src/components/workspace/messages/message-list.tsx)
- 制品列表与详情在
  - [src/components/workspace/artifacts/artifact-file-list.tsx](E:/projects/ai/deer-flow/frontend/src/components/workspace/artifacts/artifact-file-list.tsx)
  - [src/components/workspace/artifacts/artifact-file-detail.tsx](E:/projects/ai/deer-flow/frontend/src/components/workspace/artifacts/artifact-file-detail.tsx)

来源设计重点是：

- 消息流先行
- 文件/制品由消息中的 present-files 或触发器打开
- 制品详情支持 `code / preview` 切换

这和当前 Vite 前端“右侧永远固定一个 artifact rail”是不同交互模型。

### 3.6 workspace 输入区的结构

输入区核心在 [src/components/workspace/input-box.tsx](E:/projects/ai/deer-flow/frontend/src/components/workspace/input-box.tsx)。

来源输入区不是一个简单 textarea，而是完整的 prompt input 系统，带有：

- 模型选择
- attachment
- action menu
- context
- 状态切换
- stop / submit 控制
- 新线程欢迎态

这部分功能明显比当前 Vite 前端的 [src/components/workspace/message-composer.tsx](E:/projects/ai/ai-agent-runtime/frontend/src/components/workspace/message-composer.tsx) 丰富得多。

## 4. 当前 Vite 前端的对应结构

当前项目前端的页面骨架在：

- [src/App.tsx](E:/projects/ai/ai-agent-runtime/frontend/src/App.tsx)
- [src/pages/landing-page.tsx](E:/projects/ai/ai-agent-runtime/frontend/src/pages/landing-page.tsx)
- [src/pages/workspace-page.tsx](E:/projects/ai/ai-agent-runtime/frontend/src/pages/workspace-page.tsx)

当前结构特点：

- 只有两个本地路由：`/` 与 `/workspace`
- landing 已经简化为 `header + hero + feature-grid + footer`
- workspace 是一个单页控制台，不是完整路由域
- workspace 使用固定三栏：
  - 左栏 sidebar
  - 中栏消息面
  - 右栏 artifact rail

workspace 三栏外壳在 [src/components/workspace/workspace-shell.tsx](E:/projects/ai/ai-agent-runtime/frontend/src/components/workspace/workspace-shell.tsx)。

## 5. 来源 workspace 与当前 workspace 的关键差异

这是当前迁移中最需要明确的部分。

### 5.1 来源是“路由域 + 抽屉式 artifact”，当前是“单页三栏控制台”

来源 Deer Flow：

- sidebar 是导航
- main 是消息
- artifact 是触发式 detail 视图
- 输入框是浮在底部的独立控制层

当前 Vite 前端：

- sidebar 兼具线程列表和 runtime team 面板
- center 是消息区
- right rail 固定显示 artifact
- 输入框固定在消息面板底部

结论：

不能直接把 Deer Flow 的 workspace 结构原样搬进来。否则会和当前已经建立的 runtime 控制台布局冲突。

### 5.2 当前三栏方案更适合本项目当前阶段

当前项目前端已经把 runtime teams、path claims、mailbox、tasks 等面板能力放进 sidebar。  
这说明当前 workspace 的目标不仅是“聊天”，还是“运行时观察与操作控制台”。

因此，至少在当前阶段，更合适的策略是：

- 保留三栏控制台
- 从 Deer Flow 迁消息渲染、输入体验、artifact detail 模式
- 不直接迁 Deer Flow 的整体 workspace 路由壳

### 5.3 可迁移的是“子结构”和“交互模式”，不是整套布局

适合借鉴/迁移的 workspace 子结构：

- 消息分组与特定消息块渲染模式
- artifact detail 的 `preview / code` 双视图模式
- 输入区 richer controls 的拆分思路
- 线程标题区和工具区的交互节奏

不适合直接迁移的部分：

- `Next.js` 路由壳
- `core/*` hooks 与状态模型
- `ThreadContext`、`useThreadStream`、`useThreads`
- `i18n`、`settings`、`models`、`uploads`

## 6. 建议的迁移边界

基于上面的结构分析，推荐按以下边界推进：

### 6.1 Landing 团队

负责范围：

- `Header / Hero / CaseStudy 风格 / Features / CTA / Footer`

原则：

- 迁“官网展示骨架”
- 不迁来源项目的服务端统计和 demo thread 跳转逻辑

### 6.2 Workspace Shell 团队

负责范围：

- 顶部 header
- 左栏信息层级
- 中栏消息区骨架
- 右栏 artifact rail 的整体布局

原则：

- 保留当前三栏结构
- 只借鉴 Deer Flow 的 spacing、header/tool strip、区块层级

### 6.3 Conversation & Artifact 团队

负责范围：

- 消息组渲染
- artifact list/detail
- `preview / source` 双视图
- todo/subtask 展示模式

原则：

- 优先迁移交互模式
- 不直接引入 Deer Flow 的 thread/core 依赖

### 6.4 Runtime Integration 团队

负责范围：

- `agent/chat` SSE
- session history
- runtime stream
- runtime teams
- 结果聚合与 artifact 拉取

原则：

- 所有数据契约以本项目后端为准
- 只复用 Deer Flow 的展示方式，不复用其协议模型

## 7. 下一轮建议的 team 任务

### Team 1: Landing & Visual

下一步任务：

1. 评估是否将 `CaseStudySection` 和 `CommunitySection` 的低耦合展示壳迁入当前 landing。
2. 明确 `Hero` 是否保留现有 Vite 版本，还是引入 Deer Flow 中更强的视觉背景。
3. 保持 landing 不绑定来源项目 thread/demo 资源。

### Team 2: Workspace Shell

下一步任务：

1. 将当前 `workspace-shell` 的 header 和 panel 分层进一步对齐 Deer Flow 的信息节奏。
2. 评估是否把 `thread title + token usage + artifact trigger` 的来源交互方式映射到当前顶部栏。
3. 不改变当前三栏总体格局。

### Team 3: Conversation & Artifact

下一步任务：

1. 从 Deer Flow 中挑选可独立迁移的消息分组/特定消息块模式。
2. 补强当前 artifact rail，逐步从“右栏文件列表”升级到“列表 + detail 视图”。
3. 评估是否引入更丰富的 markdown/code/file 呈现方式。

### Team 4: Runtime Integration

下一步任务：

1. 继续把 `workspace-page.tsx` 的聊天提交流、artifact 生成和状态机逻辑下沉。
2. 为多 team 执行结果聚合设计单独的数据层。
3. 为真实 artifact 拉取和预览建立 API 层与组件边界。

## 8. 结论

本次结构分析最重要的结论只有两条：

1. Deer Flow 的 landing 可以按 section 级别选择性迁移，复用空间较大。
2. Deer Flow 的 workspace 不能整套照搬，因为它的核心是“多路由聊天产品壳”，而当前项目已经形成“runtime 控制台三栏壳”。

因此，后续迁移的正确方式不是“把 Deer Flow workspace 搬过来”，而是：

- 保留当前三栏 runtime 控制台结构
- 逐步吸收 Deer Flow 中更成熟的消息、artifact、输入和展示模式
- 所有数据契约与状态模型继续以 `ai-agent-runtime` 后端为准
