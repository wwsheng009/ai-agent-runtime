// 行级评论（报告 §4.4 前端部分）的状态与动作：`GET/POST/DELETE /plans/{id}/comments`。
//
// 从 `use-runtime-plans.ts` 抽出（P0-2 单文件 ≤500 非空行）：归档浏览与评论是两块独立
// 状态机，评论读不到不该让浏览面塌掉，拆开也让两者各自可测。
//
// 口径：**列表按最新归档轮重放**（与 CLI `/plan comments` 一致，`status`/`current_*`
// 直接用后端的投影，前端不重算）；**写侧带发起评论的那一轮**（点击的行号属于该版正文，
// 换一轮就会锚错位置）；删除是幂等的，重复删除同样算成功。

import { useCallback, useRef, useState } from "react";

import {
  createRuntimePlanComment,
  deleteRuntimePlanComment,
  listRuntimePlanComments,
} from "@/lib/runtime-api";
import type {
  RuntimePlanComment,
  RuntimePlanCommentCreateInput,
} from "@/types/runtime";

/**
 * 评论列表的一次读取状态。`revision` 是本次重放的目标修订（当前固定请求最新轮，
 * 由后端回填实际值）。
 */
export type RuntimePlanCommentsState = {
  status: "idle" | "loading" | "ready" | "error";
  planId: string;
  revision: number;
  latestRevision: number;
  comments: RuntimePlanComment[];
  error: string;
};

const IDLE_COMMENTS_STATE: RuntimePlanCommentsState = {
  status: "idle",
  planId: "",
  revision: 0,
  latestRevision: 0,
  comments: [],
  error: "",
};

export type RuntimePlanCommentMutationOutcome = { ok: true } | { ok: false; message: string };

function readErrorMessage(error: unknown, fallback: string) {
  return error instanceof Error && error.message.trim() ? error.message : fallback;
}

export function useRuntimePlanComments() {
  const [commentsState, setCommentsState] =
    useState<RuntimePlanCommentsState>(IDLE_COMMENTS_STATE);
  const requestSeq = useRef(0);

  /** 读取一条归档计划的评论（按最新轮重放锚点）。失败只落在 state，不影响调用方。 */
  const load = useCallback(async (planId: string) => {
    const seq = requestSeq.current + 1;
    requestSeq.current = seq;
    const trimmed = planId.trim();
    setCommentsState((previous) => ({
      ...previous,
      status: "loading",
      planId: trimmed,
      error: "",
    }));

    try {
      const response = await listRuntimePlanComments(trimmed);
      if (seq !== requestSeq.current) {
        return;
      }
      setCommentsState({
        status: "ready",
        planId: response.plan_id || trimmed,
        revision: response.revision,
        latestRevision: response.latest_revision,
        comments: response.comments,
        error: "",
      });
    } catch (error) {
      if (seq !== requestSeq.current) {
        return;
      }
      setCommentsState((previous) => ({
        ...previous,
        status: "error",
        planId: trimmed,
        comments: [],
        error: readErrorMessage(error, "failed to load plan comments"),
      }));
    }
  }, []);

  /**
   * 新建行级评论。`input.revision` 必须是发起评论的那一轮正文（点击的行号属于它），
   * 否则锚点会锚到别的版本上；越界/空正文由后端 400，原样回报给调用方展示。
   */
  const create = useCallback(
    async (
      planId: string,
      input: RuntimePlanCommentCreateInput,
    ): Promise<RuntimePlanCommentMutationOutcome> => {
      const trimmed = planId.trim();
      try {
        const response = await createRuntimePlanComment(trimmed, input);
        requestSeq.current += 1;
        setCommentsState({
          status: "ready",
          planId: response.plan_id || trimmed,
          revision: response.revision,
          latestRevision: response.latest_revision,
          comments: response.comments,
          error: "",
        });
        return { ok: true };
      } catch (error) {
        return { ok: false, message: readErrorMessage(error, "failed to create plan comment") };
      }
    },
    [],
  );

  /** 删除一条评论并刷新列表（后端幂等，重复删除同样算成功）。 */
  const remove = useCallback(
    async (planId: string, commentId: string): Promise<RuntimePlanCommentMutationOutcome> => {
      const trimmed = planId.trim();
      try {
        await deleteRuntimePlanComment(trimmed, commentId);
        await load(trimmed);
        return { ok: true };
      } catch (error) {
        return { ok: false, message: readErrorMessage(error, "failed to delete plan comment") };
      }
    },
    [load],
  );

  /** 清空评论状态（离开详情、切换计划时调用）。 */
  const clear = useCallback(() => {
    requestSeq.current += 1;
    setCommentsState(IDLE_COMMENTS_STATE);
  }, []);

  return {
    clear,
    commentsState,
    create,
    load,
    remove,
  };
}
