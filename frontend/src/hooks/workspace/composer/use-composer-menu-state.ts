// useComposerMenu 的内部状态形状（trigger / dismissedKey / 层级 / 高亮）。

import type { ComposerMenuLevel } from "@/lib/composer-menu";
import type { ComposerTrigger } from "@/lib/composer-trigger";

export type ComposerMenuInternalState = {
  manual: boolean;
  trigger: ComposerTrigger | null;
  dismissedKey: string | null;
  level: ComposerMenuLevel;
  activeId: string | null;
};

export const INITIAL_COMPOSER_MENU_STATE: ComposerMenuInternalState = {
  manual: false,
  trigger: null,
  dismissedKey: null,
  level: { kind: "root" },
  activeId: null,
};