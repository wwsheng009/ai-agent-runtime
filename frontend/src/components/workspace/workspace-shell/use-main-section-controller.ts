// WorkspaceMainSection 的运行时装配（状态 / 派生 / 命令面）抽离文件。
//
// 为什么单独成文件：main-section.tsx 只保留渲染骨架；这里集中 `@` 引用候选、
// 后台任务、子代理目录、文件预览与 `/` 命令面的接线，仅搬迁不改语义。

import { useCallback, useMemo, useState } from "react";

import { agentDisplayName } from "@/components/workspace/session-agents-panel-shared";
import { type SubagentSessionTarget } from "@/components/workspace/trajectory/subagent-session-target";
import { useBackgroundJobs } from "@/hooks/workspace/use-background-jobs";
import { useFilePreview } from "@/hooks/workspace/use-file-preview";
import { useComposerFileReferences } from "@/hooks/workspace/composer/use-composer-file-references";
import { useComposerReferenceGroups } from "@/hooks/workspace/composer/use-composer-reference-groups";
import { useComposerSkillGroups } from "@/hooks/workspace/composer/use-composer-skill-groups";
import { type ComposerMenuState } from "@/hooks/workspace/composer/use-composer-menu";
import { useComposerCommandSurface } from "@/hooks/workspace/composer/use-composer-command-surface";
import { useComposerProfileSwitch } from "@/hooks/workspace/composer/use-composer-profile-command";
import { createComposerSkillTurnRunner } from "@/hooks/workspace/composer/composer-skill-turn";
import { useRuntimeProfileCatalog } from "@/hooks/workspace/composer/use-runtime-profile-catalog";
import { useSessionAgents } from "@/hooks/use-session-agents";
import { useParkedTurnView } from "@/hooks/workspace/use-parked-turns";
import { imageSubmitNoticeBanner } from "@/hooks/workspace/agent-chat-turn/image-prompt-turn";

import { type WorkspaceMainSectionProps } from "./main-section-props";

export function useWorkspaceMainSectionController(
  props: WorkspaceMainSectionProps,
) {
  const {
    imageSubmitFeedback,
    isNewThread,
    onDraftChange,
    onModelChange,
    onRenameRuntimeSession,
    onSubmit,
    parkedTurn,
    runtimeModels,
    runtimeSkills,
    runtimeSkillsError,
    selectedThread,
    selectedWorkspaceDirectoryId,
    t,
    trajectoryStore,
    viewMode,
    workspaceDirectories,
    workspaceDirectoriesError,
    workspaceDirectoriesLoading,
  } = props;

  // P0：`@` 引用候选 = 工作区文件（fs/roots 解析作用域 + fs/list 首屏小批量）+ 线程交付物兜底。
  // 菜单查询串只在 useComposerMenu 内部持有，这里通过 onMenuStateChange 回调同步出来。
  const [composerMenuState, setComposerMenuState] = useState<ComposerMenuState>(
    {
      open: false,
      mode: "references",
      query: "",
    },
  );
  const handleComposerMenuStateChange = useCallback(
    (next: ComposerMenuState) => {
      setComposerMenuState((previous) =>
        previous.open === next.open &&
        previous.mode === next.mode &&
        previous.query === next.query
          ? previous
          : next,
      );
    },
    [],
  );
  const fileReferences = useComposerFileReferences({
    sessionId: selectedThread.sessionId,
    query: composerMenuState.query,
    enabled: composerMenuState.open && composerMenuState.mode === "references",
    labels: {
      group: t("composer.references.workspaceFiles"),
      loading: t("composer.references.workspaceFilesLoading"),
      empty: t("composer.references.workspaceFilesEmpty"),
      error: t("composer.references.workspaceFilesError"),
      truncated: t("composer.references.workspaceFilesTruncated"),
    },
  });

  // P1-4 子片 3：`@` 引用候选分组（工作区文件在前，线程交付物在后兜底）。
  const composerReferenceGroups = useComposerReferenceGroups(
    fileReferences.group,
    selectedThread.artifacts,
  );
  // 文本类 skill 的 `$` 提及候选（与 `/skill` 弹窗同源的运行时技能目录）。
  const composerSkillGroups = useComposerSkillGroups(
    runtimeSkills,
    runtimeSkillsError,
  );
  const workspaceSelection = isNewThread
    ? {
        label: t("composer.menu.workspace"),
        options: workspaceDirectories.map((directory) => ({
          id: directory.id,
          label: directory.name?.trim() || directory.path,
          path: directory.path,
        })),
        selectedId: selectedWorkspaceDirectoryId ?? null,
        clearLabel: t("composer.menu.workspaceClear"),
        clearDescription: t("composer.menu.workspaceClearDescription"),
        ...(workspaceDirectoriesLoading
          ? { statusText: t("composer.menu.workspaceLoading") }
          : workspaceDirectoriesError
            ? {
                statusText: t("composer.menu.workspaceError", {
                  message: workspaceDirectoriesError,
                }),
              }
            : workspaceDirectories.length === 0
              ? { emptyText: t("composer.menu.workspaceEmpty") }
              : {}),
      }
    : undefined;

  // P2-1A / P2-9：后台任务（Jobs）——shell owner 单例加载一次，
  // 顶栏常驻状态条与弹层共用同一份数据（计数天然一致，不做第二次拉取）。
  const jobsSessionId = selectedThread.sessionId?.trim() ?? "";
  const [jobsPanelOpen, setJobsPanelOpen] = useState(false);
  const jobs = useBackgroundJobs({
    enabled: Boolean(jobsSessionId),
    lastRuntimeEventType: selectedThread.lastRuntimeEventType,
    runtimeEventCount: selectedThread.runtimeEventCount,
    sessionId: jobsSessionId,
  });

  // P2-1A：子代理控制面（AgentControl 身份图）——顶栏 lineage 面包屑与弹层共用一次加载。
  const agentsSessionId = selectedThread.sessionId?.trim() ?? "";
  const sessionAgents = useSessionAgents({ sessionId: agentsSessionId });
  const [agentsPanelOpen, setAgentsPanelOpen] = useState(false);
  // §6.8 托管挂起：事件快照 + 任务投影（同一份 AgentControl 目录，挂起边沿补刷一次）。
  const parkedTurnView = useParkedTurnView(
    parkedTurn,
    sessionAgents.tree.descendants,
    sessionAgents.refresh,
  );
  // G8：会话 agents 面板的只读下钻目标（复用子会话 transcript 对话框）。
  const [agentTranscriptTarget, setAgentTranscriptTarget] =
    useState<SubagentSessionTarget | null>(null);
  const agentBreadcrumb = useMemo(
    () =>
      sessionAgents.tree.lineage.map((agent) => ({
        label: agentDisplayName(agent),
        path: agent.agentPath,
      })),
    [sessionAgents.tree.lineage],
  );

  // P2-1A：工具行文件路径的运行时预览（fs/read-file，只读）；未命中关联产物时兜底。
  const filePreview = useFilePreview({ sessionId: selectedThread.sessionId });

  // P2-7：composer `/` 命令面（清单 / `/model` 候选与弹窗 / 执行器 / 回执文案）；`/export`、
  // `/rename`、`/model` 复用轨迹导出、侧栏重命名与常驻座位选择器的同一实现，`/feedback`
  // 写本地日志（log-only）。目录未就绪时菜单无候选、弹窗如实显示空/失败态，不伪造模型名。
  // P2：`/skill` 回合化（构造器已抽到 composer-skill-turn.ts，行数门禁）。
  const handleRunSkillTurn = useMemo(
    () => createComposerSkillTurnRunner(onSubmit),
    [onSubmit],
  );

  // Batch 12：`/profile` 运行时目录（宿主数据源，一次拉取）+ 切换动作（单一处理器：
  // 命令行提交、弹窗点选与常驻切换器都经它派发，回执文案一致）。
  const runtimeProfileCatalog = useRuntimeProfileCatalog();
  const handleProfileSwitch = useComposerProfileSwitch(
    selectedThread.sessionId,
  );
  const composerCommandSurface = useComposerCommandSurface({
    runtimeModels,
    runtimeSkills,
    runtimeProfiles: runtimeProfileCatalog.catalog,
    onProfileSwitch: handleProfileSwitch,
    onDraftChange,
    onModelChange,
    onRenameSession: onRenameRuntimeSession,
    sessionId: selectedThread.sessionId,
    onRunSkillTurn: handleRunSkillTurn,
  });

  // S5：带图发送（submit_prompt.images）回执与命令回执共用同一通知条，前者优先。
  const composerNotice =
    imageSubmitNoticeBanner(imageSubmitFeedback?.notice, t) ??
    composerCommandSurface.commandResult;
  // 仅当带图回执真正在显示时才优先它；控制器对象恒存在，不能拿它当「是否在显示」的判据。
  const dismissComposerNotice = imageSubmitFeedback?.notice
    ? imageSubmitFeedback.dismissNotice
    : composerCommandSurface.onDismissCommandResult;

  // P2-1B：中部视图页签（对话 / 技能 / 轨迹）；技能与轨迹页签不承载输入框，只有对话面保留 composer。
  const skillsSurfaceVisible = !isNewThread && viewMode === "skills";
  const chatSurfaceVisible =
    !skillsSurfaceVisible &&
    (isNewThread || viewMode === "chat" || !trajectoryStore);

  return {
    agentBreadcrumb,
    agentTranscriptTarget,
    agentsPanelOpen,
    agentsSessionId,
    chatSurfaceVisible,
    composerCommandSurface,
    composerNotice,
    composerReferenceGroups,
    composerSkillGroups,
    dismissComposerNotice,
    filePreview,
    handleComposerMenuStateChange,
    handleProfileSwitch,
    jobs,
    jobsPanelOpen,
    jobsSessionId,
    parkedTurnView,
    runtimeProfileCatalog,
    sessionAgents,
    setAgentTranscriptTarget,
    setAgentsPanelOpen,
    setJobsPanelOpen,
    skillsSurfaceVisible,
    workspaceSelection,
  };
}