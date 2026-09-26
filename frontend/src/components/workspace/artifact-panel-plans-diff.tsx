// 轮次差异视图（报告 §4.4 的前端部分）：把 `GET /plans/{id}/diff` 的统一 diff
// 渲染成带语义着色的面板，并作为**行级评论的行选择器**。
//
// 契约：自包含展示组件，不取数、不裁决、不回灌 —— 数据与开关状态都由「计划归档」面
// 持有，提交评论通过 `onCreateComment` 上抛；`identical` 是判等的唯一依据
// （此时 `text` 只剩两行版本框架）。
//
// 行号口径：可点的行是**新版（`to` 版本）正文行**（`newLine`），删除行只有旧版行号、
// 因此不参与选择——评论锚点必须落在完成提交那一版的正文上。

import { LoaderCircleIcon, XIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  classifyPlanDiffLines,
  formatPlanCommentRange,
  planDiffLineClass,
} from "@/components/workspace/artifact-panel-plans-shared";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { RuntimePlanDiffResult } from "@/types/runtime";

export type ArtifactPlanDiffBlockState = {
  status: "loading" | "ready" | "error";
  result: RuntimePlanDiffResult | null;
  error: string;
};

/** 选中的行区间（新版正文行号，1-based，闭区间）。 */
export type ArtifactPlanDiffSelection = {
  startLine: number;
  endLine: number;
};

export function ArtifactPlanDiffBlock({
  diffKey,
  state,
  commentRevision,
  onCreateComment,
  onCollapse,
  onRetry,
}: {
  /** 用于生成 data-testid 的稳定键（`v{from}-v{to}` 或请求中的版本对）。 */
  diffKey: string;
  state: ArtifactPlanDiffBlockState;
  /** 评论锚定的轮次：即 diff 的 `to` 版本（点击的行号属于它）。 */
  commentRevision: number;
  /** 提交评论；返回空串表示成功，否则是给用户看的错误文案。 */
  onCreateComment: (selection: ArtifactPlanDiffSelection, body: string) => Promise<string>;
  onCollapse: () => void;
  onRetry: () => void;
}) {
  const { t } = useTranslation("workspace");
  const result = state.result;
  const [selection, setSelection] = useState<ArtifactPlanDiffSelection | null>(null);
  const [draft, setDraft] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState("");

  const selectLine = (lineNumber: number, extend: boolean) => {
    setSelection((previous) =>
      previous && extend
        ? {
            startLine: Math.min(previous.startLine, lineNumber),
            endLine: Math.max(previous.endLine, lineNumber),
          }
        : { startLine: lineNumber, endLine: lineNumber },
    );
    setSubmitError("");
  };

  const submit = async () => {
    if (!selection) {
      return;
    }
    const body = draft.trim();
    if (body === "") {
      setSubmitError(t("panels.artifacts.plans.comments.diff.emptyBody"));
      return;
    }
    setSubmitting(true);
    setSubmitError("");
    const error = await onCreateComment(selection, body);
    setSubmitting(false);
    if (error) {
      setSubmitError(error);
      return;
    }
    setDraft("");
    setSelection(null);
  };

  return (
    <div
      className="space-y-1.5 rounded-card border border-white/8 bg-black/15 px-2.5 py-2"
      data-testid={`plan-diff-${diffKey}`}
    >
      <div className="flex flex-wrap items-center gap-1.5 text-muted-foreground">
        <span className="app-text-10 uppercase tracking-[0.16em]">
          {t("panels.artifacts.plans.diff.title")}
        </span>
        {result ? (
          <>
            <Badge>{`v${result.from_version} → v${result.to_version}`}</Badge>
            <span className="text-accent-teal">+{result.added}</span>
            <span className="text-accent-orange">-{result.removed}</span>
            {result.identical ? (
              <Badge>{t("panels.artifacts.plans.diff.identical")}</Badge>
            ) : null}
            {result.coarse ? (
              <Badge>{t("panels.artifacts.plans.diff.coarse")}</Badge>
            ) : null}
            {result.truncated ? (
              <Badge>{t("panels.artifacts.plans.diff.truncated")}</Badge>
            ) : null}
          </>
        ) : null}
        <span className="ml-auto inline-flex gap-1">
          {state.status === "error" ? (
            <Button onClick={onRetry} size="sm" type="button" variant="ghost">
              {t("panels.artifacts.plans.retry")}
            </Button>
          ) : null}
          <Button
            aria-label={t("panels.artifacts.plans.diff.collapse")}
            onClick={onCollapse}
            size="sm"
            title={t("panels.artifacts.plans.diff.collapse")}
            type="button"
            variant="ghost"
          >
            <XIcon size={13} />
          </Button>
        </span>
      </div>

      {state.status === "loading" ? (
        <div className="inline-flex items-center gap-2 text-muted-foreground">
          <LoaderCircleIcon size={13} className="animate-spin" />
          {t("panels.artifacts.plans.diff.loading")}
        </div>
      ) : state.status === "error" ? (
        <div className="text-accent-orange">
          {t("panels.artifacts.plans.diff.failed")}
          {state.error ? <span className="text-muted-foreground">：{state.error}</span> : null}
        </div>
      ) : result && result.identical ? (
        <div className="text-muted-foreground">
          {t("panels.artifacts.plans.diff.identicalHint")}
        </div>
      ) : result ? (
        <div className="space-y-1">
          <div className="text-muted-foreground">
            {t("panels.artifacts.plans.comments.diff.selectHint")}
          </div>
          <pre className="max-h-[24rem] overflow-auto font-mono text-xs leading-5 whitespace-pre">
            {classifyPlanDiffLines(result.text).map((line, index) => {
              const selectable = line.newLine !== undefined;
              const current = line.newLine;
              const selected =
                selectable &&
                selection !== null &&
                current !== undefined &&
                current >= selection.startLine &&
                current <= selection.endLine;
              return (
                <div
                  key={`${index}-${line.kind}`}
                  className={`${planDiffLineClass(line.kind)}${
                    selected ? " bg-accent-teal/15" : ""
                  }`}
                >
                  <span className="mr-2 inline-block w-8 select-none text-right text-muted-foreground/60">
                    {line.newLine ?? line.oldLine ?? ""}
                  </span>
                  {selectable ? (
                    <button
                      className={`cursor-pointer text-left hover:bg-white/5${
                        selected ? " underline decoration-dotted" : ""
                      }`}
                      data-testid={`plan-diff-line-${line.newLine}`}
                      onClick={(event) => selectLine(line.newLine as number, event.shiftKey)}
                      type="button"
                    >
                      {line.text || " "}
                    </button>
                  ) : (
                    <span>{line.text || " "}</span>
                  )}
                </div>
              );
            })}
          </pre>

          <div className="flex flex-wrap items-center gap-1.5">
            <span className="text-muted-foreground">
              {selection
                ? t("panels.artifacts.plans.comments.diff.selected", {
                    range: formatPlanCommentRange(selection.startLine, selection.endLine),
                    revision: String(commentRevision),
                  })
                : t("panels.artifacts.plans.comments.diff.noSelection")}
            </span>
          </div>
          {selection ? (
            <div className="space-y-1">
              <textarea
                className="min-h-[3.5rem] w-full rounded-card border border-white/10 bg-black/20 px-2 py-1.5 font-mono text-xs"
                data-testid="plan-comment-draft"
                onChange={(event) => setDraft(event.target.value)}
                placeholder={t("panels.artifacts.plans.comments.diff.placeholder")}
                value={draft}
              />
              <div className="flex items-center gap-1.5">
                <Button
                  disabled={submitting}
                  onClick={() => {
                    void submit();
                  }}
                  size="sm"
                  type="button"
                >
                  {submitting
                    ? t("panels.artifacts.plans.comments.diff.submitting")
                    : t("panels.artifacts.plans.comments.diff.submit")}
                </Button>
                <Button
                  disabled={submitting}
                  onClick={() => {
                    setSelection(null);
                    setDraft("");
                    setSubmitError("");
                  }}
                  size="sm"
                  type="button"
                  variant="ghost"
                >
                  {t("panels.artifacts.plans.comments.diff.cancel")}
                </Button>
                {submitError ? (
                  <span className="text-accent-orange">{submitError}</span>
                ) : null}
              </div>
            </div>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
