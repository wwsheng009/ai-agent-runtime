// 会话手动压缩区块：模式选择 + 执行 + 结果提示，嵌在上下文面板底部。
//
// 为什么状态机放这里而不是控件里：面板关闭即卸载，压缩结论/错误随之归零，
// 下次打开看到的必然是当前状态而不是上一次的旧结论（控件侧不必再写 reset effect）。

import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  compactSessionContext,
  resolveSessionCompactMode,
  type SessionCompactMode,
  type SessionCompactOutcome,
} from "@/api/runtime";
import { formatContextTokens } from "@/components/workspace/composer-context-usage-shared";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { cn } from "@/lib/utils";

/** 手动压缩模式：auto = 交运行时按会话能力解析；local/remote 显式指定执行方。 */
const COMPACT_MODE_OPTIONS: readonly SessionCompactMode[] = ["auto", "local", "remote"];

type CompactNotice = {
  tone: "error" | "success" | "muted";
  text: string;
};

function compactModeLabelKey(mode: SessionCompactMode): string {
  switch (mode) {
    case "local":
      return "composer.contextUsage.compact.modeLocal";
    case "remote":
      return "composer.contextUsage.compact.modeRemote";
    default:
      return "composer.contextUsage.compact.modeAuto";
  }
}

export type ComposerContextCompactSectionProps = {
  /** 非 null = 当前不可压缩（会话未落地 / 响应中），文案直接展示给用户。 */
  disabledReason: string | null;
  /** 压缩成功（确实替换了历史）时上报结果：宿主用它立刻覆盖环上的占用显示。 */
  onCompacted: (outcome: SessionCompactOutcome) => void;
  /** 空 = 草稿会话，不发请求。 */
  sessionId: string;
};

export function ComposerContextCompactSection({
  disabledReason,
  onCompacted,
  sessionId,
}: ComposerContextCompactSectionProps) {
  const { t } = useTranslation("workspace");
  // i18next 的类型收窄只认字面量键，模式键是按枚举拼出来的：沿用仓库既有的收口写法
  // （见 composer-permission-mode-control / artifact-panel）。
  const translate = (key: string) => t(key as never) as string;
  const [mode, setMode] = useState<SessionCompactMode>("auto");
  const [pending, setPending] = useState(false);
  const [outcome, setOutcome] = useState<SessionCompactOutcome | null>(null);
  const [error, setError] = useState<string | null>(null);

  const disabled = disabledReason !== null || pending;

  async function runCompact() {
    if (!sessionId || disabled) {
      return;
    }

    setPending(true);
    setError(null);
    try {
      const next = await compactSessionContext(sessionId, { mode });
      setOutcome(next);
      // 只有真正替换了历史才回传：skipped 时环上应继续显示实时占用。
      if (next.result) {
        onCompacted(next);
      }
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setPending(false);
    }
  }

  const result = outcome?.result ?? null;
  const notice: CompactNotice | null = error
    ? { tone: "error", text: error }
    : outcome && !result
      ? {
          tone: "muted",
          text: outcome.status.reason
            ? t("composer.contextUsage.compact.skipped", {
                reason: outcome.status.reason,
              })
            : t("composer.contextUsage.compact.skippedUnknown"),
        }
      : result
        ? {
            tone: "success",
            text: t("composer.contextUsage.compact.done", {
              // 非 count 的插值位在类型上按字符串收窄，数字先显式转字符串。
              messages: String(result.compactedMessages),
              before: formatContextTokens(result.tokenBefore),
              after: formatContextTokens(result.tokenAfter),
            }),
          }
        : null;

  return (
    <div className="mt-0.5 border-t border-border pt-3">
      <h4 className="text-xs font-semibold text-foreground">
        {t("composer.contextUsage.compact.title")}
      </h4>
      <p className="mt-1 text-[11px] leading-5 text-muted-foreground">
        {t("composer.contextUsage.compact.description")}
      </p>
      <div className="mt-2 flex items-center gap-2">
        <Select
          ariaLabel={t("composer.contextUsage.compact.mode")}
          className="min-w-0 flex-1"
          disabled={disabled}
          onChange={(value) => setMode(resolveSessionCompactMode(value) || "auto")}
          options={COMPACT_MODE_OPTIONS.map((option) => ({
            label: translate(compactModeLabelKey(option)),
            value: option,
          }))}
          side="top"
          value={mode}
        />
        <Button
          className="shrink-0"
          disabled={disabled}
          onClick={() => void runCompact()}
          size="sm"
          title={disabledReason ?? undefined}
          type="button"
          variant="secondary"
          data-testid="composer-context-compact-action"
        >
          {pending
            ? t("composer.contextUsage.compact.pending")
            : t("composer.contextUsage.compact.action")}
        </Button>
      </div>
      {disabledReason ? (
        <p className="mt-1.5 text-[11px] leading-5 text-muted-foreground">
          {disabledReason}
        </p>
      ) : null}
      {notice ? (
        <p
          className={cn(
            "mt-1.5 text-[11px] leading-5",
            notice.tone === "error"
              ? "text-accent-orange"
              : notice.tone === "success"
                ? "text-accent-teal"
                : "text-muted-foreground",
          )}
          role={notice.tone === "error" ? "alert" : "status"}
        >
          {notice.text}
        </p>
      ) : null}
      {result && result.checkpointIds.length > 0 ? (
        <p className="mt-1 text-[11px] leading-5 text-muted-foreground">
          {t("composer.contextUsage.compact.checkpoints", {
            count: result.checkpointIds.length,
          })}
        </p>
      ) : null}
    </div>
  );
}
