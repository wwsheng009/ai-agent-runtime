// P1-9 会话整理动作（Fork / 删除）的纯逻辑：不依赖 React，便于单测。
//
// 边界：后端无「会话克隆」API（`/sessions/{id}` 仅 archive/activate/close/delete），
// 因此 Fork 语义 = 以同一标题基名与工作目录新开**独立会话**，不复制消息历史
// （不伪造分支历史）；目录归属通过 `workspace_path` 继承，未绑定会话回落「未分组」。

import { resolveRuntimeSessionDirectory } from "@/components/workspace/workspace-sidebar-shared";
import {
  type RuntimeCreateSessionRequest,
  type RuntimeSessionRecord,
} from "@/lib/runtime-api";

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
 * 构造 Fork 的新会话请求：标题带本地化分支后缀，用户与工作目录继承源会话。
 * 源会话未绑定目录时不带 `workspace_path`，新会话进入「未分组」而非误挂其他目录。
 */
export function buildForkSessionRequest(input: {
  session: RuntimeSessionRecord;
  /** 侧栏展示口径的标题（线程标题优先，其次 metadata.title，最后 session.id）。 */
  sourceTitle: string;
  userId: string;
  /** 本地化分支后缀（如「（分支）」/ " (branch)"）。 */
  branchSuffix: string;
}): RuntimeCreateSessionRequest {
  const userId = input.userId.trim();
  const directory = resolveRuntimeSessionDirectory(input.session);

  return {
    title: buildForkSessionTitle(
      input.sourceTitle,
      input.session.id,
      input.branchSuffix,
    ),
    ...(userId ? { user_id: userId } : {}),
    ...(directory.fullPath ? { workspace_path: directory.fullPath } : {}),
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
