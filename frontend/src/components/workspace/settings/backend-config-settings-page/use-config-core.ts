// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { createConfigEditorActions } from "./use-config-actions";
import { useConfigEditorState } from "./use-config-state";


export function useConfigEditorCore() {
  const state = useConfigEditorState();
  const actions = createConfigEditorActions(state);

  return { ...state, ...actions };
}

export type ConfigEditorCore = ReturnType<typeof useConfigEditorCore>;
