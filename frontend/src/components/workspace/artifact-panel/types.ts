// 由 components/workspace/artifact-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Artifact } from "@/data/mock";

import {
  type WorkspacePanelSurfaceId,
  type WorkspacePanelThreadRelation,
} from "@/components/workspace/panel-registry";

/** 兼容别名：面枚举的单一事实来源已收敛到 `components/workspace/panel-registry`。 */
export type ArtifactPanelSurface = WorkspacePanelSurfaceId;

export type ArtifactPanelProps = {
  artifacts: Artifact[];
  isResponding?: boolean;
  lastRuntimeEventType?: string;
  runtimeEventCount?: number;
  onOpenArtifact: (artifactId: string) => void;
  selectedArtifactId: string | null;
  sessionId?: string;
  /** 当前线程 ↔ 运行时会话的关联快照；「会话详情」面据此渲染关联状态区块。 */
  threadRelation?: WorkspacePanelThreadRelation;
  /**
   * 当前会话绑定的工作目录；files/git 面据此给出默认作用域根候选。
   * 缺省时自包含面退化为「只读运行时工作目录（cwd）」并提示。
   */
  workspacePath?: string;
  /** 受控激活面（右栏宽度需要按面型重算）；缺省时面板自管并回调通知。 */
  activeSurface?: WorkspacePanelSurfaceId;
  onActiveSurfaceChange?: (surface: WorkspacePanelSurfaceId) => void;
};

/** 自包含面（files/git）从 PanelHost 收到的上下文；刻意保持小。 */
export type ArtifactPanelSelfContainedProps = {
  sessionId: string;
  workspacePath?: string;
};
