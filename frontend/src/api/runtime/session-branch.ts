/**
 * 会话分支（方案 §6.1）：`POST /api/runtime/sessions/{id}/branch`。
 *
 * 语义与后端一致：把源会话在锚点处的**历史前缀**复制进新会话，源会话零改动。
 * 从 `sessions.ts` 拆出（P0-2：单文件 ≤500 非空行），对外导入路径为
 * `@/api/runtime/session-branch`（barrel 见 `@/api/runtime`）。
 */

import type {
  RuntimeSessionBranchRequest,
  RuntimeSessionBranchResponse,
} from "@/types/runtime";

import { buildRuntimeUrl, fetchRuntimeJson } from "./shared";

/**
 * 会话分支：把源会话在锚点处的历史前缀复制进**新会话**（源会话零改动）。
 * 锚点缺省 = 会话末尾；锚点非「已完成轮次末尾」时后端返回 409。
 */
export async function branchRuntimeSession(
  sessionId: string,
  request: RuntimeSessionBranchRequest = {},
): Promise<RuntimeSessionBranchResponse> {
  return fetchRuntimeJson<RuntimeSessionBranchResponse>(
    buildRuntimeUrl(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/branch`,
    ),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify(request),
    },
  );
}
