# 右侧栏文件浏览器升级：多页签文件管理器 实施记录

> 状态：**已实施并验证**
> 日期：2026-09-19
> 关联：`docs/plan/workspace-right-panel-file-browser-and-git-diff-plan.md`（该文为更早的「文件浏览器 + Git 变更」总体规划，仍处规划中；本文只记录本次已落地的多页签升级范围）

## 1. 需求

用户提出（2026-09-18）：

1. 主页面「对话 / 技能 / 轨迹」几个页签升级为通用 tab 组件；点击右侧文件浏览器中的文件时，在该组件上新建页签显示文件，取代右下角的小预览窗口。
2. 调整目标：右侧栏文件浏览器本身升级为多页签文件管理器——点击文件名即追加页签，形如 `/文件浏览器/文件1/文件2/文件3…`。

## 2. 落地形态

- 通用页签条组件：`frontend/src/components/ui/tab-strip.tsx`（测试 id 工厂在 `tab-strip-shared.ts`，遵循组件文件只导出组件的 react-refresh 约定），供文件管理器复用；条目 / 计划 / 还原 / 会话用量等既有面板页签保持原实现不变。
- 文件管理器多页签：
  - 纯模型：`frontend/src/components/workspace/file-browser/file-manager-tabs.ts`（根页签常驻、点击文件追加页签、去重、关闭后回落邻近页签）。
  - Hook：`frontend/src/components/workspace/file-browser/use-file-manager-tabs.ts`（模型 ↔ `WorkspaceTabStripItem` 适配）。
  - 视图：`frontend/src/components/workspace/file-browser/file-tab.tsx`（文件页签 pane，预览内容复用既有文件预览解码链）。
  - 接线：`frontend/src/components/workspace/file-browser-surface.tsx` 引入以上三者。
- 删除旧右下角小预览窗：`frontend/src/components/workspace/file-browser/preview-pane.tsx`（连同其测试）移除，由文件页签替代。
- e2e：`frontend/e2e/file-manager-tabs.spec.ts`（2 条，断言可视盒与滚动真实发生，口径同 `file-browser-panel.spec.ts`）。

## 3. 验证记录（2026-09-19 晨）

| 项 | 结果 |
| --- | --- |
| `pnpm build`（`tsc -b` + vite build） | 通过（仅既有 INEFFECTIVE_DYNAMIC_IMPORT 告警） |
| vitest 全量 | 2547/2547 全绿（2026-09-19 凌晨口径，含本次新增用例） |
| Playwright 全量（102 用例，14 失败） | 14 失败全部在纯 HEAD 基线（独立 worktree + 自带 dist）复现同一失败集，判定预存、与本次改动无关；失败分布：trajectory 4、sidebar-session-actions 4、workspace-chat 2、usage-observability 2、design-tokens 1、sidebar-directory-indent 1 |
| 本次新增 e2e 复验（本记录补齐项） | `file-manager-tabs.spec.ts` + `file-preview.spec.ts` 共 8/8 通过（2026-09-19 07:2x，新鲜 dist） |
| 说明 | 全量 Playwright 轮次的新 spec 覆盖缺口已按上条闭合：`file-manager-tabs.spec.ts` 创建于 01:03，晚于该轮 dist 构建（00:09），故单独复跑 |

## 4. 范围边界

- 需求 1 的前半部分（主页面「对话 / 技能 / 轨迹」页签升级为通用组件）**未在本次实施**：主页签 `workspace-shell/view-tab-bar.tsx` 仍为裸 `role="tab"` 按钮实现（最后修改 09-14，不在本次改动集），后续升级为 `WorkspaceTabStrip` 时可复用本文档口径。

## 5. 已知边界

- 根页签即目录浏览视图；文件页签内容为只读预览（同旧预览面板能力：markdown 渲染 / 文本 / 二进制与超限如实降级）。
- 页签为前端会话内状态，刷新后回落根页签（与旧预览行为一致，未做持久化）。
