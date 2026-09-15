import { useEffect, useRef, type RefObject } from "react";

import { getLiveStreamEntry, subscribeLiveStreamText } from "@/lib/live-stream-text";

/**
 * 订阅 live 文本，但**不触发渲染**：把最新文本写进 ref，交给打字机的帧循环读取。
 *
 * 为什么需要它：live 增量（≈30–60 次/秒）与打字机（≈30 次/秒）若各自驱动一次渲染，
 * 同一条消息的 markdown 尾块每个节拍会被重解析两遍——实测（`e2e/zz-profile-audit.mjs`，
 * plain / 507 帧）ScriptDur 1.95s、LayoutCount 442，而单驱动基线为 1.05s / 201。
 * 让打字机当唯一渲染驱动后，增量在下一帧被「顺带」揭示，渲染次数直接砍半。
 *
 * ref 在订阅回调里同步更新：增量到达 → 下一帧读到新目标，中间不产生任何 React 更新。
 * 订阅前先同步一次当前值，保证「挂载时 store 里已有增量」的场景不漏。
 */
export function useLiveStreamTextRef(
  messageId: string | null,
): RefObject<string | null> {
  const ref = useRef<string | null>(null);

  useEffect(() => {
    if (!messageId) {
      ref.current = null;
      return;
    }
    const sync = () => {
      ref.current = getLiveStreamEntry(messageId)?.text ?? null;
    };
    sync();
    return subscribeLiveStreamText(sync);
  }, [messageId]);

  return ref;
}
