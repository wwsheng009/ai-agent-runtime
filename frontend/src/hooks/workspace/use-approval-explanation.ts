/**
 * §4.13 按需解释（只读）：对某个会话的 pending 审批做一次摘要。
 *
 * 单独成 hook 而不是内联 `fetch`：与仓库约定一致（组件不直接 import API），
 * 也便于上层在测试里 mock；调用方自带 loading/错误展示，这里不做重试与状态。
 *
 * 之所以不在 `usePendingInteractions` 上暴露：主会话待办条与子代理弹层各自
 * 已持有条目（含 sessionId）与请求生命周期，这里只共享「怎么调」这一件事。
 */
import { useCallback } from "react";

import {
  explainSessionApproval,
  type SessionApprovalExplanation,
} from "@/api/runtime/sessions";

export function useApprovalExplanation(): (
  sessionId: string,
  requestId: string,
) => Promise<SessionApprovalExplanation> {
  return useCallback(
    (sessionId: string, requestId: string) =>
      explainSessionApproval(sessionId, requestId),
    [],
  );
}
