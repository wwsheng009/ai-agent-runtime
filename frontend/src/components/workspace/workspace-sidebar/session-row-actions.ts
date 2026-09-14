// P1-9 会话整理动作（分支 / 删除）的纯逻辑：不依赖 React，便于单测。
//
// 语义（2026-09 分支能力）：侧栏 Fork = 以「会话末尾」为锚点的整会话分支
// （`POST /sessions/{id}/branch`，锚点缺省）；历史前缀由服务端复制，源会话零改动。
// 目录归属由服务端从源会话 `metadata.context.workspace_path` 继承（方案 §5.2 R4），
// 未绑定目录的源会话其分支同样回落「未分组」。

import type { RuntimeSessionBranchRequest } from "@/lib/runtime-api";

/** Fork 标题：源标题为空时回落到会话 id，避免产生「空标题 + 后缀」的新会话。 */
export function buildForkSessionTitle(
  sourceTitle: string,
  fallbackSessionId: string,
  suffix: string,
): string {
  const base = sourceTitle.trim() || fallbackSessionId.trim();
  return `${base}${suffix}`;
}

/**
 * 构造分支的新会话请求：标题带本地化分支后缀，用户继承源会话。
 * 带 `anchorMessageId` = 消息级分支（锚点 = 该条轮末尾消息）；缺省 = 整会话分支。
 */
export function buildBranchSessionRequest(input: {
  /** 源会话 id（同时作为标题为空时的回落基名）。 */
  sessionId: string;
  /** 侧栏展示口径的标题（线程标题优先，其次 metadata.title，最后 session.id）。 */
  sourceTitle: string;
  userId: string;
  /** 本地化分支后缀（如「（分支）」/ " (branch)"）。 */
  branchSuffix: string;
  /** 消息级分支的锚点消息 id；缺省 = 会话末尾（整会话分支）。 */
  anchorMessageId?: string;
}): RuntimeSessionBranchRequest {
  const userId = input.userId.trim();
  const anchorMessageId = input.anchorMessageId?.trim() ?? "";

  return {
    title: buildForkSessionTitle(
      input.sourceTitle,
      input.sessionId,
      input.branchSuffix,
    ),
    ...(userId ? { user_id: userId } : {}),
    ...(anchorMessageId
      ? { anchor_message_id: anchorMessageId, include_anchor: true }
      : {}),
  };
}

/**
 * 删除后的选中会话回落：被删除会话恰好是当前会话时返回 null（调用方跳回工作台首页），
 * 否则保持原选中不变。用于避免删除后路由停留在已不存在的会话上。
 */
export function resolveSelectionAfterSessionDelete(
  selectedThreadId: string,
  deletedSessionId: string,
): string | null {
  return selectedThreadId === deletedSessionId ? null : selectedThreadId;
}
