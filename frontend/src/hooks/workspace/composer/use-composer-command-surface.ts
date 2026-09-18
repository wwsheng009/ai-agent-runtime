// P2-7 子片 3：composer `/` 命令面的接线（清单 / 候选 / 弹窗 / 执行器 / 回执文案）。
//
// 边界：本 hook 只做「宿主数据 → 命令行界面 props」的组装，不拉取、不缓存、不推断：
// - `/model` 候选与校验集合都来自宿主持有的运行时目录（`runtimeModels` 唯一事实源）；
// - 执行动作复用常驻座位与侧栏的既有处理器（`onModelChange` / `onRenameSession`），
//   不另建选择通道；
// - 回执文案在这里本地化（执行器只持 i18n key 与插值），组件拿到的就是可渲染文本。
import { useCallback, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  useComposerCommandExecutor,
  type ComposerCommandExecutor,
  type ComposerModelSelectionBridge,
  type ComposerSkillTurnRunner,
} from "@/hooks/workspace/composer/use-composer-command-executor";
import { buildComposerBuiltinCommands } from "@/lib/composer-builtin-commands";
import type { ComposerCommand, ComposerCommandDefinition } from "@/lib/composer-commands";
import {
  composerModelCatalogGroups,
  composerModelCommandOptions,
  composerModelIds,
  type ComposerModelCatalogGroup,
} from "@/lib/composer-model-options";
import {
  composerSkillCommandText,
  composerSkillCommandOptions,
  composerSkillNames,
} from "@/lib/composer-skill-options";
import type { RuntimeModelsResponse, RuntimeSkillCatalog } from "@/types/runtime";

export type ComposerCommandResultBanner = {
  text: string;
  tone: "success" | "error";
};

export type ComposerCommandSurface = {
  /** `/` 菜单与命令行共用的命令清单（`/model` 候选随目录注入）。 */
  commands: readonly ComposerCommandDefinition[];
  /** 已本地化的执行回执（无回执时为 null）。 */
  commandResult: ComposerCommandResultBanner | null;
  /** `/model` 弹窗的按 provider 分组视图。 */
  modelGroups: ComposerModelCatalogGroup[];
  modelDialogOpen: boolean;
  openModelDialog: () => void;
  closeModelDialog: () => void;
  /** `/skill` 弹窗状态。 */
  skillDialogOpen: boolean;
  openSkillDialog: () => void;
  closeSkillDialog: () => void;
  /**
   * 选中 skill 的统一动作（菜单二级候选点选 / 弹窗点选）：
   * 只把 `/skill <name> ` 回填到输入框，不直接执行——执行发生在用户提交时。
   */
  selectSkill: (skillName: string) => void;
  /** 派发命令；`source="pick"` 表示点选候选（`/skill` 点选走回填而非执行）。 */
  onCommand: (
    command: ComposerCommand,
    args: string,
    source: "pick" | "submit",
  ) => boolean;
  onDismissCommandResult: ComposerCommandExecutor["dismissNotice"];
};

export type UseComposerCommandSurfaceOptions = {
  /** 宿主持有的运行时目录；null / 缺省 = 未就绪（不产生候选）。 */
  runtimeModels?: RuntimeModelsResponse | null;
  /** 宿主持有的技能目录；null / 缺省 = 未就绪（不产生候选）。 */
  runtimeSkills?: RuntimeSkillCatalog | null;
  /** 应用模型；与 composer 常驻座位同一处理器。 */
  onModelChange: (modelId: string) => void;
  /** 回填 composer 草稿；`/skill` 点选候选与弹窗选择共用（只回填不执行）。 */
  onDraftChange: (value: string) => void;
  /** 会话重命名处理器（与侧栏同一实现）；缺省时 `/rename` 如实报不可用。 */
  onRenameSession?: (sessionId: string, title: string) => Promise<void>;
  /** 当前会话；新会话未登记时为 undefined。 */
  sessionId?: string;
  /** P2：`/skill` 提交为普通对话回合（宿主接线到 chat turn 提交）；缺省回退 REST。 */
  onRunSkillTurn?: ComposerSkillTurnRunner;
};

export function useComposerCommandSurface({
  runtimeModels,
  runtimeSkills,
  onModelChange,
  onDraftChange,
  onRenameSession,
  sessionId,
  onRunSkillTurn,
}: UseComposerCommandSurfaceOptions): ComposerCommandSurface {
  const { t } = useTranslation("workspace");
  const [modelDialogOpen, setModelDialogOpen] = useState(false);
  const [skillDialogOpen, setSkillDialogOpen] = useState(false);

  const modelOptions = useMemo(
    () => composerModelCommandOptions(runtimeModels),
    [runtimeModels],
  );
  const skillOptions = useMemo(
    () => composerSkillCommandOptions(runtimeSkills),
    [runtimeSkills],
  );
  const commands = useMemo(
    () => buildComposerBuiltinCommands({ modelOptions, skillOptions }),
    [modelOptions, skillOptions],
  );
  const modelGroups = useMemo(
    () => composerModelCatalogGroups(runtimeModels),
    [runtimeModels],
  );
  const openModelDialog = useCallback(() => setModelDialogOpen(true), []);
  const closeModelDialog = useCallback(() => setModelDialogOpen(false), []);
  const openSkillDialog = useCallback(() => setSkillDialogOpen(true), []);
  const closeSkillDialog = useCallback(() => setSkillDialogOpen(false), []);
  const modelSelection = useMemo<ComposerModelSelectionBridge>(
    () => ({
      modelIds: composerModelIds(runtimeModels),
      applyModel: onModelChange,
      openDialog: openModelDialog,
    }),
    [onModelChange, openModelDialog, runtimeModels],
  );

  const selectSkill = useCallback(
    (skillName: string) => {
      if (skillName.trim().length === 0) {
        return;
      }
      onDraftChange(composerSkillCommandText(skillName));
    },
    [onDraftChange],
  );

  const executor = useComposerCommandExecutor({
    modelSelection,
    skillNames: composerSkillNames(runtimeSkills),
    openSkillDialog,
    onRenameSession,
    sessionId,
    onRunSkillTurn,
  });

  // 解构保持稳定引用：React Compiler 要求回调依赖与实际读取的成员一致。
  const runExecutorCommand = executor.run;

  /**
   * 命令派发策略：`/skill` 的点选候选是「选技能」而不是「跑技能」——
   * 回填命令文本交还用户补充 prompt，提交时才进入执行器。
   * 其余命令（含 `/skill` 提交）保持原语义。
   */
  const handleCommand = useCallback(
    (command: ComposerCommand, args: string, source: "pick" | "submit"): boolean => {
      if (command.key === "skill" && source === "pick") {
        selectSkill(args);
        return true;
      }
      return runExecutorCommand(command, args);
    },
    [runExecutorCommand, selectSkill],
  );

  const commandResult = useMemo<ComposerCommandResultBanner | null>(() => {
    const notice = executor.notice;
    if (!notice) {
      return null;
    }
    return {
      text: t(notice.messageKey as never, (notice.values ?? {}) as never) as unknown as string,
      tone: notice.tone,
    };
  }, [executor.notice, t]);

  return {
    commands,
    commandResult,
    modelGroups,
    modelDialogOpen,
    openModelDialog,
    closeModelDialog,
    skillDialogOpen,
    openSkillDialog,
    closeSkillDialog,
    selectSkill,
    onCommand: handleCommand,
    onDismissCommandResult: executor.dismissNotice,
  };
}
