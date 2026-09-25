// 右侧栏「面（surface）」注册表：新增一个面板 = 新增一个组件文件 + 在此注册一行。
//
// 两种面的区别（有意保留，不是遗漏）：
// - 自包含面：注册 `surface`（模块作用域 `lazy()`），由 PanelHost 懒加载并渲染，只接收会话上下文 props。
//   `files` / `git` 走这条路（它们的数据来自作用域根，不依赖面板级会话状态）。
// - 面板级面：`plan` / `checkpoints` 的状态由 PanelHost 里的会话 hook 解析
//   （panel 级 useState/useEffect 不能下沉到懒加载子组件，否则页签徽标与面板会各拉一次数据），
//   因此不注册 `surface`，由 PanelHost 用已解析的 props 显式渲染。新增面请优先做成自包含面。

import { lazy, type ComponentType, type LazyExoticComponent, type ReactNode } from "react";
import {
  FileCode2Icon,
  FileTextIcon,
  FolderTreeIcon,
  GitCompareIcon,
  HistoryIcon,
  IdCardIcon,
  ScrollTextIcon,
} from "lucide-react";
import {
  type WorkspaceThreadRelationKind,
  type WorkspaceThreadTransportKind,
} from "@/components/workspace/workspace-shell-shared";

/** 右侧栏全部面 id（单一事实来源；联合类型由此派生）。 */
export type WorkspacePanelSurfaceId =
  | "artifacts"
  | "checkpoints"
  | "plan"
  | "plans"
  | "files"
  | "git"
  | "sessionDetail";

/** tone 只决定页签配色；没有专属配色的新面复用 `artifact`。 */
export type WorkspacePanelSurfaceTone =
  | "artifact"
  | "plan"
  | "checkpoint"
  | "file"
  | "git";

/**
 * 宽度类别：驱动 `auto` 模式下的自适应宽度（见 `lib/layout/rail-width.ts`）。
 * - `content`：内容型面，auto 下锁 288px（与改造前右栏宽度一致）
 * - `wide`：宽内容面，auto 下 `clamp(0.32 × vw, 416px, 672px)`
 */
export type WorkspacePanelWidthClass = "content" | "wide";

/**
 * 当前本地线程 ↔ 运行时会话的关联快照（由 shell 侧的纯判据派生一次，向下传值）。
 * 供「会话详情」这类需要解释「这个聊天和运行时会话是什么关系」的面使用。
 */
export type WorkspacePanelThreadRelation = {
  kind: WorkspaceThreadRelationKind;
  transport: WorkspaceThreadTransportKind;
};

/** 自包含面从 PanelHost 收到的上下文（刻意保持小：不引入面板内部状态）。 */
export type WorkspacePanelSurfaceProps = {
  /** 当前会话 id（无会话时为空字符串）。 */
  sessionId: string;
  /** 当前会话绑定的工作目录（作用域根的候选之一）；无则 undefined。 */
  workspacePath?: string;
  /** 当前线程与会话的关联快照；未提供时面不渲染关联状态区块。 */
  threadRelation?: WorkspacePanelThreadRelation;
  /**
   * 当前线程最新运行时事件（与面板级 hook 同源，见 `artifact-panel.tsx`）。
   * 自包含面用它做「事件 → 重载」，避免各自再建一条订阅通道。
   */
  lastRuntimeEventType?: string;
  /** 当前线程已消费的运行时事件计数，与 lastRuntimeEventType 组成重载键。 */
  runtimeEventCount?: number;
};

export type WorkspacePanelSurfaceSpec = {
  id: WorkspacePanelSurfaceId;
  /** i18n key（`workspace` 命名空间），不是字面量文案。 */
  labelKey: string;
  icon: ComponentType<{ size?: number }>;
  tone: WorkspacePanelSurfaceTone;
  /** 该面是否依赖当前会话；为 true 且无会话时禁用页签并展示禁用原因。 */
  requiresSession: boolean;
  widthClass: WorkspacePanelWidthClass;
  /** 无会话（或其他前置条件未满足）时的可解释原因 key。 */
  disabledReasonKey?: string;
  /**
   * 自包含面的懒加载组件（**模块作用域**创建，见下方 `lazySurface`）；缺省表示由 PanelHost 用面板级状态渲染。
   *
   * 为什么不存 `load: () => import(...)`：组件必须在渲染期之外创建，否则每次渲染都会
   * `lazy()` 出新身份（`react-hooks/static-components`），且会触发卸载重挂 / 重复请求。
   */
  surface?: LazyExoticComponent<ComponentType<WorkspacePanelSurfaceProps>>;
};

/** 注册表内自包含面的统一出口：模块加载时创建一次，身份终身稳定。 */
function lazySurface(
  load: () => Promise<{ default: ComponentType<WorkspacePanelSurfaceProps> }>,
) {
  return lazy(load);
}

/** 渲染顺序即数组顺序；既有 4 个面的顺序与改造前保持一致。 */
export const WORKSPACE_PANEL_SURFACES: readonly WorkspacePanelSurfaceSpec[] = [
  {
    id: "artifacts",
    labelKey: "panels.artifacts.tabs.items",
    icon: FileCode2Icon,
    tone: "artifact",
    requiresSession: false,
    widthClass: "content",
  },
  {
    id: "plan",
    labelKey: "panels.artifacts.tabs.plan",
    icon: ScrollTextIcon,
    tone: "plan",
    requiresSession: true,
    widthClass: "content",
    disabledReasonKey: "panels.shell.panelTabs.disabledNoSession",
  },
  {
    id: "checkpoints",
    labelKey: "panels.artifacts.tabs.restore",
    icon: HistoryIcon,
    tone: "checkpoint",
    requiresSession: true,
    widthClass: "content",
    disabledReasonKey: "panels.shell.panelTabs.disabledNoSession",
  },
  {
    id: "files",
    labelKey: "panels.shell.panelTabs.files",
    icon: FolderTreeIcon,
    tone: "file",
    requiresSession: false,
    widthClass: "wide",
    surface: lazySurface(() =>
      import("@/components/workspace/file-browser-surface").then((module) => ({
        default: module.FileBrowserSurface,
      })),
    ),
  },
  {
    id: "git",
    labelKey: "panels.shell.panelTabs.git",
    icon: GitCompareIcon,
    tone: "git",
    requiresSession: false,
    widthClass: "wide",
    surface: lazySurface(() =>
      import("@/components/workspace/git-surface").then((module) => ({
        default: module.GitSurface,
      })),
    ),
  },
  {
    id: "sessionDetail",
    labelKey: "panels.artifacts.tabs.sessionDetail",
    icon: IdCardIcon,
    tone: "artifact",
    requiresSession: true,
    widthClass: "content",
    disabledReasonKey: "panels.shell.panelTabs.disabledNoSession",
    surface: lazySurface(() =>
      import("@/components/workspace/session-detail-surface").then((module) => ({
        default: module.SessionDetailSurface,
      })),
    ),
  },
  {
    // 归档计划浏览器（报告 §4.5）：全局资源、不依赖当前会话，故注册为自包含面。
    // 排在数组末尾：tab 顺序与 PanelHost 里「面板级面 + 自包含面」的 DOM 顺序保持一致。
    id: "plans",
    labelKey: "panels.artifacts.tabs.plans",
    icon: FileTextIcon,
    tone: "plan",
    requiresSession: false,
    widthClass: "wide",
    surface: lazySurface(() =>
      import("@/components/workspace/artifact-panel-plans-surface").then((module) => ({
        default: module.ArtifactPanelPlansSurface,
      })),
    ),
  },
];

export type WorkspacePanelSurfaceTabIds = {
  panelId: string;
  tabId: string;
};

/**
 * 由单个 `useId()` 基值派生 tab/panel 的 id。
 * 一个 useId 覆盖全部面，避免「面数量增加 → hooks 数量变化」。
 */
export function buildSurfaceTabIds(
  baseId: string,
  surfaceId: WorkspacePanelSurfaceId,
): WorkspacePanelSurfaceTabIds {
  return {
    panelId: `${baseId}-panel-${surfaceId}`,
    tabId: `${baseId}-tab-${surfaceId}`,
  };
}

/** 页签徽标（数量/状态胶囊）；由 PanelHost 决定内容，注册表不承载数据。 */
export type WorkspacePanelSurfaceBadges = Partial<
  Record<WorkspacePanelSurfaceId, ReactNode>
>;
