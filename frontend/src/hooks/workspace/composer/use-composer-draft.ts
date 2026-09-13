import { useCallback, useMemo, useState } from "react";

import {
  readComposerDraft,
  resolveBrowserComposerDraftStorage,
  resolveComposerDraftThreadKey,
  writeComposerDraft,
  type ComposerDraftStorage,
  type ComposerDraftThreadSource,
} from "@/lib/composer-draft";

export type UseComposerDraftOptions = {
  /** 当前会话来源；键取 sessionId 优先、线程 id 兜底。 */
  thread: ComposerDraftThreadSource | undefined;
  /** 测试注入；缺省用 `window.localStorage`（不可用时退化为内存态）。 */
  storage?: ComposerDraftStorage | null;
};

export type ComposerDraftController = {
  draft: string;
  setDraft: (value: string) => void;
  /** 当前草稿归属的会话键；无键（临时线程）时为 null。 */
  threadKey: string | null;
};

/**
 * P1-4 子片 1：会话语义化的草稿状态。
 *
 * - 会话切换时按目标键**同步派生**草稿（渲染期读取，无 effect 闪帧）；
 * - `setDraft` 直接写穿 localStorage（空白即删除条目），无定时器/防抖，
 *   保证卸载、刷新、切会话三条路径下存储与界面一致；
 * - 存储异常静默降级为内存态，不打断输入。
 */
export function useComposerDraft({
  thread,
  storage,
}: UseComposerDraftOptions): ComposerDraftController {
  const threadKey = resolveComposerDraftThreadKey(thread);

  // 存储解析一次：外部注入优先，否则浏览器 localStorage（不可用时内部退化为内存态）。
  const resolvedStorage = useMemo(
    () =>
      storage === undefined ? resolveBrowserComposerDraftStorage() : storage,
    [storage],
  );

  const [state, setState] = useState<{
    threadKey: string | null;
    draft: string;
  }>(() => ({
    threadKey,
    draft: readComposerDraft(resolvedStorage, threadKey),
  }));

  // setDraft 跟随当次渲染的会话键：调用点都在事件处理器/提交路径上，
  // 渲染即重建闭包，切会话后不会写回旧键。
  const setDraft = useCallback(
    (value: string) => {
      writeComposerDraft(resolvedStorage, threadKey, value);
      setState({ threadKey, draft: value });
    },
    [resolvedStorage, threadKey],
  );

  // 键变化时派生目标会话的草稿；键相同则用组件内状态（避免每次渲染重读存储）。
  const draft =
    state.threadKey === threadKey
      ? state.draft
      : readComposerDraft(resolvedStorage, threadKey);

  return { draft, setDraft, threadKey };
}
