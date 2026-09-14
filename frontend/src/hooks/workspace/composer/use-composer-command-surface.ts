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
} from "@/hooks/workspace/composer/use-composer-command-executor";
import { buildComposerBuiltinCommands } from "@/lib/composer-builtin-commands";
import type { ComposerCommandDefinition } from "@/lib/composer-commands";
import {
  composerModelCatalogGroups,
  composerModelCommandOptions,
  composerModelIds,
  type ComposerModelCatalogGroup,
} from "@/lib/composer-model-options";
import type { RuntimeModelsResponse } from "@/types/runtime";

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
  onCommand: ComposerCommandExecutor["run"];
  onDismissCommandResult: ComposerCommandExecutor["dismissNotice"];
};

export type UseComposerCommandSurfaceOptions = {
  /** 宿主持有的运行时目录；null / 缺省 = 未就绪（不产生候选）。 */
  runtimeModels?: RuntimeModelsResponse | null;
  /** 应用模型；与 composer 常驻座位同一处理器。 */
  onModelChange: (modelId: string) => void;
  /** 会话重命名处理器（与侧栏同一实现）；缺省时 `/rename` 如实报不可用。 */
  onRenameSession?: (sessionId: string, title: string) => Promise<void>;
  /** 当前会话；新会话未登记时为 undefined。 */
  sessionId?: string;
};

export function useComposerCommandSurface({
  runtimeModels,
  onModelChange,
  onRenameSession,
  sessionId,
}: UseComposerCommandSurfaceOptions): ComposerCommandSurface {
  const { t } = useTranslation("workspace");
  const [modelDialogOpen, setModelDialogOpen] = useState(false);

  const modelOptions = useMemo(
    () => composerModelCommandOptions(runtimeModels),
    [runtimeModels],
  );
  const commands = useMemo(
    () => buildComposerBuiltinCommands({ modelOptions }),
    [modelOptions],
  );
  const modelGroups = useMemo(
    () => composerModelCatalogGroups(runtimeModels),
    [runtimeModels],
  );
  const openModelDialog = useCallback(() => setModelDialogOpen(true), []);
  const closeModelDialog = useCallback(() => setModelDialogOpen(false), []);
  const modelSelection = useMemo<ComposerModelSelectionBridge>(
    () => ({
      modelIds: composerModelIds(runtimeModels),
      applyModel: onModelChange,
      openDialog: openModelDialog,
    }),
    [onModelChange, openModelDialog, runtimeModels],
  );

  const executor = useComposerCommandExecutor({
    modelSelection,
    onRenameSession,
    sessionId,
  });

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
    onCommand: executor.run,
    onDismissCommandResult: executor.dismissNotice,
  };
}
