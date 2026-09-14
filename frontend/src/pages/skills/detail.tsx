// P2-1A：技能市场详情面板。
//
// 选中技能后以 `GET /api/runtime/skills/{name}` 拉取最新定义（列表里的是同一份
// hydrated 结构，但详情可能已被热重载替换），拉取失败时如实报错并保留列表行数据，
// 不回退到「假装成功」的本地缓存。

import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { getRuntimeSkillDetail } from "@/api/runtime/skills";
import { cn } from "@/lib/utils";
import type { RuntimeSkill } from "@/types/runtime";

import { classifySkillsError, describeSkillSource, skillSubtitle } from "./shared";

type SkillDetailPanelProps = {
  skill: RuntimeSkill;
  onClose: () => void;
};

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <dt className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 break-words text-sm leading-6">{value}</dd>
    </div>
  );
}

function ChipList({ label, values }: { label: string; values: string[] }) {
  if (values.length === 0) {
    return null;
  }
  return (
    <div className="min-w-0">
      <dt className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">{label}</dt>
      <dd className="mt-1 flex flex-wrap gap-1.5">
        {values.map((value) => (
          <span
            key={value}
            className="rounded-full border border-border bg-surface-softer px-2 py-0.5 text-xs text-muted-foreground"
          >
            {value}
          </span>
        ))}
      </dd>
    </div>
  );
}

export function SkillDetailPanel({ skill, onClose }: SkillDetailPanelProps) {
  const { t } = useTranslation("skills");
  // 调用方按技能名 key 重挂载，因此这里以列表行为初值即可，无需在 effect 里回写。
  const [detail, setDetail] = useState<RuntimeSkill>(skill);
  const [state, setState] = useState<{ status: "loading" | "ready" | "error"; error: unknown }>({
    status: "loading",
    error: null,
  });
  const [reloadKey, setReloadKey] = useState(0);
  const seqRef = useRef(0);
  const { status, error } = state;

  useEffect(() => {
    const controller = new AbortController();
    const seq = ++seqRef.current;

    void getRuntimeSkillDetail(skill.name, { signal: controller.signal })
      .then((fresh) => {
        if (seqRef.current !== seq) {
          return;
        }
        setDetail(fresh);
        setState({ status: "ready", error: null });
      })
      .catch((cause: unknown) => {
        if (seqRef.current !== seq) {
          return;
        }
        if (cause instanceof DOMException && cause.name === "AbortError") {
          return;
        }
        setState({ status: "error", error: cause });
      });

    return () => {
      controller.abort();
    };
  }, [skill.name, reloadKey]);

  const reload = () => {
    setState({ status: "loading", error: null });
    setReloadKey((key) => key + 1);
  };

  const source = describeSkillSource(detail);
  const subtitle = skillSubtitle(detail);
  const errorKind = classifySkillsError(error);

  return (
    <section
      className="surface-panel min-w-0 rounded-panel border border-border px-3 py-3 sm:px-4"
      aria-label={t("detail.title")}
      data-testid="skills-detail"
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h3 className="truncate text-sm font-semibold" data-testid="skills-detail-name">
            {detail.name}
          </h3>
          {subtitle ? (
            <p className="mt-0.5 truncate text-xs text-muted-foreground">{subtitle}</p>
          ) : null}
        </div>
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="sm" onClick={reload}>
            {t("actions.refreshDetail")}
          </Button>
          <Button variant="secondary" size="sm" onClick={onClose}>
            {t("actions.closeDetail")}
          </Button>
        </div>
      </div>

      {status === "loading" ? (
        <p className="mt-2 text-xs text-muted-foreground" data-testid="skills-detail-loading">
          {t("detail.loading")}
        </p>
      ) : null}

      {status === "error" ? (
        <div
          className="mt-2 rounded-panel border border-analytics-warning-border bg-analytics-warning-soft px-3 py-2 text-xs text-analytics-warning"
          data-testid="skills-detail-error"
        >
          {t(`errors.${errorKind}`)}
        </div>
      ) : null}

      {detail.description ? (
        <p className="mt-2 whitespace-pre-wrap break-words text-sm leading-6">
          {detail.description}
        </p>
      ) : null}

      <dl className="mt-3 grid gap-3 sm:grid-cols-2">
        <Field label={t("detail.category")} value={detail.category || t("detail.absent")} />
        <Field label={t("detail.version")} value={detail.version || t("detail.absent")} />
        <Field label={t("detail.source")} value={source || t("detail.absent")} />
        <Field
          label={t("detail.promptPath")}
          value={detail.source?.promptPath || t("detail.absent")}
        />
        <ChipList label={t("detail.tags")} values={detail.tags} />
        <ChipList label={t("detail.capabilities")} values={detail.capabilities} />
        <ChipList label={t("detail.tools")} values={detail.tools} />
        <ChipList label={t("detail.permissions")} values={detail.permissions} />
      </dl>

      {detail.triggers.length > 0 ? (
        <div className="mt-3">
          <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
            {t("detail.triggers")}
          </div>
          <ul className="mt-1 space-y-1">
            {detail.triggers.map((trigger, index) => (
              <li
                key={`${trigger.type}-${index}`}
                className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground"
              >
                <span className="rounded border border-border px-1.5 py-0.5">{trigger.type}</span>
                <span className="break-all">{trigger.values.join(", ")}</span>
                {trigger.weight !== null ? (
                  <span className="tabular-nums">
                    {t("detail.weight", { value: String(trigger.weight) })}
                  </span>
                ) : null}
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      {detail.workflowSteps.length > 0 ? (
        <div className="mt-3">
          <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
            {t("detail.workflow")}
          </div>
          <ol className="mt-1 space-y-1" data-testid="skills-detail-workflow">
            {detail.workflowSteps.map((step, index) => (
              <li key={step.id || index} className="text-xs leading-5 text-muted-foreground">
                <span className="text-foreground">{step.id || step.name || `#${index + 1}`}</span>
                {step.tool ? <span> · {step.tool}</span> : null}
                {step.dependsOn.length > 0 ? (
                  <span> · {t("detail.dependsOn", { value: step.dependsOn.join(", ") })}</span>
                ) : null}
              </li>
            ))}
          </ol>
        </div>
      ) : null}

      {detail.systemPrompt || detail.userPrompt ? (
        <div className="mt-3 space-y-2">
          {detail.systemPrompt ? (
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
                {t("detail.systemPrompt")}
              </div>
              <pre className="mt-1 max-h-48 overflow-auto whitespace-pre-wrap break-words rounded-panel border border-border bg-surface-softer px-2 py-2 text-xs leading-5">
                {detail.systemPrompt}
              </pre>
            </div>
          ) : null}
          {detail.userPrompt ? (
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
                {t("detail.userPrompt")}
              </div>
              <pre className="mt-1 max-h-48 overflow-auto whitespace-pre-wrap break-words rounded-panel border border-border bg-surface-softer px-2 py-2 text-xs leading-5">
                {detail.userPrompt}
              </pre>
            </div>
          ) : null}
        </div>
      ) : null}

      <div
        className={cn(
          "mt-3 text-xs text-muted-foreground",
          status === "error" && "text-analytics-warning",
        )}
      >
        {detail.contextFiles.length > 0
          ? t("detail.contextFiles", { value: detail.contextFiles.join(", ") })
          : null}
      </div>
    </section>
  );
}
