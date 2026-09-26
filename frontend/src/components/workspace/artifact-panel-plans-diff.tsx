// 轮次差异视图（报告 §4.4 的前端部分）：把 `GET /plans/{id}/diff` 的统一 diff 渲染成
// 带语义着色的只读面板。
//
// 契约：自包含展示组件，不取数、不裁决、不回灌 —— 数据与开关状态都由「计划归档」面持有；
// `identical` 是判等的唯一依据（此时 `text` 只剩两行版本框架）。

import { LoaderCircleIcon, XIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  classifyPlanDiffLines,
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

export function ArtifactPlanDiffBlock({
  diffKey,
  state,
  onCollapse,
  onRetry,
}: {
  /** 用于生成 data-testid 的稳定键（`v{from}-v{to}` 或请求中的版本对）。 */
  diffKey: string;
  state: ArtifactPlanDiffBlockState;
  onCollapse: () => void;
  onRetry: () => void;
}) {
  const { t } = useTranslation("workspace");
  const result = state.result;

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
        <pre className="max-h-[24rem] overflow-auto font-mono text-xs leading-5 whitespace-pre">
          {classifyPlanDiffLines(result.text).map((line, index) => (
            <div key={`${index}-${line.kind}`} className={planDiffLineClass(line.kind)}>
              {line.text || " "}
            </div>
          ))}
        </pre>
      ) : null}
    </div>
  );
}
