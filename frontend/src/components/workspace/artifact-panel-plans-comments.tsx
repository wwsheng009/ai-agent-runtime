// 行级评论列表（报告 §4.4 的前端部分）：直接消费 `GET /plans/{id}/comments` 的
// 重放投影（`status` + `current_*`）——前端不重算锚点，只负责把三态讲清楚：
// 仍锚定 / 原文已移动（附原锚点）/ 锚点失效（附当时内容）。
//
// 契约：展示组件，不取数。删除通过 `onDelete` 上抛，返回值是给用户看的错误文案。

import { LoaderCircleIcon, Trash2Icon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  formatPlanCommentRange,
  planCommentStatusClass,
  planCommentStatusLabel,
} from "@/components/workspace/artifact-panel-plans-shared";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { RuntimePlanComment } from "@/types/runtime";

export type ArtifactPlanCommentsBlockState = {
  status: "idle" | "loading" | "ready" | "error";
  /** 本次重放的目标修订（缺省为最新轮，由后端回填）。 */
  revision: number;
  latestRevision: number;
  comments: RuntimePlanComment[];
  error: string;
};

export function ArtifactPlanCommentsBlock({
  state,
  onDelete,
}: {
  state: ArtifactPlanCommentsBlockState;
  onDelete: (comment: RuntimePlanComment) => Promise<string>;
}) {
  const { t } = useTranslation("workspace");
  const [pendingId, setPendingId] = useState("");
  const [actionError, setActionError] = useState("");

  const remove = async (comment: RuntimePlanComment) => {
    setPendingId(comment.id);
    setActionError("");
    const error = await onDelete(comment);
    setPendingId("");
    setActionError(error);
  };

  return (
    <div className="space-y-1.5" data-testid="plan-comments">
      <div className="flex flex-wrap items-center gap-1.5 text-muted-foreground">
        <span className="app-text-10 uppercase tracking-[0.16em]">
          {t("panels.artifacts.plans.comments.title")}
        </span>
        {state.status === "ready" ? (
          <>
            <Badge>{`${state.comments.length}`}</Badge>
            <span>
              {t("panels.artifacts.plans.comments.replayTarget", {
                version: String(state.revision),
              })}
            </span>
          </>
        ) : null}
      </div>

      {state.status === "loading" ? (
        <div className="inline-flex items-center gap-2 text-muted-foreground">
          <LoaderCircleIcon size={13} className="animate-spin" />
          {t("panels.artifacts.plans.comments.loading")}
        </div>
      ) : state.status === "error" ? (
        <div className="text-accent-orange">
          {t("panels.artifacts.plans.comments.failed")}
          {state.error ? <span className="text-muted-foreground">：{state.error}</span> : null}
        </div>
      ) : state.comments.length === 0 ? (
        <div className="text-muted-foreground">
          {t("panels.artifacts.plans.comments.empty")}
        </div>
      ) : (
        <ul className="space-y-1">
          {state.comments.map((comment) => (
            <li
              className="rounded-card border border-white/8 bg-black/15 px-2.5 py-2"
              data-testid={`plan-comment-${comment.id}`}
              key={comment.id}
            >
              <div className="flex flex-wrap items-center gap-1.5">
                <span className="font-mono text-xs text-foreground/80">
                  {formatPlanCommentRange(comment.current_start_line, comment.current_end_line)}
                </span>
                <Badge className={planCommentStatusClass(comment.status)}>
                  {t(planCommentStatusLabel(comment.status) as never)}
                </Badge>
                <span className="text-muted-foreground">
                  {`v${comment.revision} · ${formatPlanCommentRange(comment.start_line, comment.end_line)}`}
                </span>
                <Button
                  aria-label={t("panels.artifacts.plans.comments.delete")}
                  className="ml-auto"
                  disabled={pendingId === comment.id}
                  onClick={() => {
                    void remove(comment);
                  }}
                  size="sm"
                  title={t("panels.artifacts.plans.comments.delete")}
                  type="button"
                  variant="ghost"
                >
                  <Trash2Icon size={13} />
                </Button>
              </div>
              {comment.excerpt ? (
                <div className="truncate font-mono text-xs text-muted-foreground/80">
                  「{comment.excerpt}」
                </div>
              ) : null}
              <div className="text-xs">{comment.body}</div>
            </li>
          ))}
        </ul>
      )}

      {actionError ? <div className="text-accent-orange">{actionError}</div> : null}
    </div>
  );
}
