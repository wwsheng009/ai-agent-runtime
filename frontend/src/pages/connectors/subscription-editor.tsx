// S2（docs/bots/13-event-subscription-routing-design.md §3.4/§6.1）：声明式事件
// 订阅编辑器——sessions 选择器 × 事件类型（支持族通配）× 投递形态。
//
// 纯受控组件（value + onChange），提交合并在 ./shared.mergeEventsIntoPayload；
// 编辑态专属（与 D6 路由同一约定：策略在创建后配置）。ids/types 采用
// 「草稿文本 + 解析回值」：输入过程中不会因即时解析吞掉正在输入的分隔符。

import { useState } from "react";
import { useTranslation } from "react-i18next";

import type { ConnectorEventSubscriptionView } from "@/types/runtime";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

import { formatSubscriptionList, parseSubscriptionList } from "./subscription-list";

const inputClassName =
  "h-9 w-full rounded-control border border-border bg-surface-soft px-2.5 text-sm text-foreground outline-none placeholder:text-muted-foreground focus:border-accent-primary disabled:opacity-60";

export interface SubscriptionEditorValue {
  enabled: boolean;
  subscriptions: ConnectorEventSubscriptionView[];
}

interface SubscriptionEditorProps {
  value: SubscriptionEditorValue;
  onChange: (value: SubscriptionEditorValue) => void;
}

const MODES = ["bound", "ids", "all"] as const;
const DELIVERS = ["notice", "content", "digest", "stream"] as const;

const DELIVER_LABEL_KEYS: Record<(typeof DELIVERS)[number], string> = {
  notice: "connectorDialog.eventsDeliverNotice",
  content: "connectorDialog.eventsDeliverContent",
  digest: "connectorDialog.eventsDeliverDigest",
  stream: "connectorDialog.eventsDeliverStream",
};

export function SubscriptionEditor({ value, onChange }: SubscriptionEditorProps) {
  const { t } = useTranslation("connectors");
  // 输入草稿（按行索引）：解析后的数组不丢失用户正在输入的分隔符。
  const [idsDraft, setIdsDraft] = useState<Record<number, string>>({});
  const [typesDraft, setTypesDraft] = useState<Record<number, string>>({});

  function replaceSubscriptions(subscriptions: ConnectorEventSubscriptionView[]) {
    onChange({ ...value, subscriptions });
  }

  function updateAt(index: number, patch: Partial<ConnectorEventSubscriptionView>) {
    replaceSubscriptions(
      value.subscriptions.map((item, i) => (i === index ? { ...item, ...patch } : item)),
    );
  }

  function updateSessions(
    index: number,
    patch: Partial<NonNullable<ConnectorEventSubscriptionView["sessions"]>>,
  ) {
    const current = value.subscriptions[index]?.sessions ?? {};
    updateAt(index, { sessions: { ...current, ...patch } });
  }

  function addSubscription() {
    replaceSubscriptions([
      ...value.subscriptions,
      { id: "", sessions: { mode: "bound" }, types: [], deliver: "notice" },
    ]);
  }

  function removeSubscription(index: number) {
    // 行索引重排后草稿必须作废（否则会显示到相邻行）。
    setIdsDraft({});
    setTypesDraft({});
    replaceSubscriptions(value.subscriptions.filter((_, i) => i !== index));
  }

  return (
    <section className="flex flex-col gap-2.5 rounded-card border border-border px-3 py-3">
      <div className="flex items-center justify-between gap-2">
        <span className="text-xs font-medium uppercase tracking-[0.14em] text-muted-foreground">
          {t("connectorDialog.eventsTitle")}
        </span>
        <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <input
            type="checkbox"
            aria-label={t("connectorDialog.eventsEnabled")}
            checked={value.enabled}
            onChange={(event) => onChange({ ...value, enabled: event.target.checked })}
          />
          {t("connectorDialog.eventsEnabled")}
        </label>
      </div>
      <p className="text-xs text-muted-foreground">{t("connectorDialog.eventsHint")}</p>
      {value.enabled ? (
        <>
          {value.subscriptions.length === 0 ? (
            <p className="text-xs text-muted-foreground">{t("connectorDialog.eventsEmpty")}</p>
          ) : null}
          {value.subscriptions.map((sub, index) => (
            <div
              key={index}
              className={cn(
                "flex flex-col gap-2 rounded-control border border-border bg-surface-soft px-2.5 py-2.5",
                sub.enabled === false && "opacity-60",
              )}
            >
              <div className="grid gap-2 sm:grid-cols-3">
                <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                  {t("connectorDialog.eventsId")}
                  <input
                    type="text"
                    aria-label={t("connectorDialog.eventsId")}
                    className={inputClassName}
                    value={sub.id ?? ""}
                    onChange={(event) => updateAt(index, { id: event.target.value })}
                  />
                </label>
                <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                  {t("connectorDialog.eventsDeliver")}
                  <select
                    aria-label={t("connectorDialog.eventsDeliver")}
                    className={inputClassName}
                    value={sub.deliver ?? "notice"}
                    onChange={(event) =>
                      updateAt(index, {
                        deliver: event.target.value as ConnectorEventSubscriptionView["deliver"],
                      })
                    }
                  >
                    {DELIVERS.map((deliver) => (
                      <option key={deliver} value={deliver}>
                        {t(DELIVER_LABEL_KEYS[deliver])}
                      </option>
                    ))}
                  </select>
                </label>
                <div className="flex items-end justify-between gap-2">
                  <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
                    <input
                      type="checkbox"
                      aria-label={t("connectorDialog.eventsRowEnabled")}
                      checked={sub.enabled !== false}
                      onChange={(event) => updateAt(index, { enabled: event.target.checked })}
                    />
                    {t("connectorDialog.eventsRowEnabled")}
                  </label>
                  <Button variant="ghost" size="sm" onClick={() => removeSubscription(index)}>
                    {t("connectorDialog.eventsRemove")}
                  </Button>
                </div>
              </div>
              <div className="grid gap-2 sm:grid-cols-2">
                <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                  {t("connectorDialog.eventsMode")}
                  <select
                    aria-label={t("connectorDialog.eventsMode")}
                    className={inputClassName}
                    value={sub.sessions?.mode ?? "bound"}
                    onChange={(event) =>
                      updateSessions(index, {
                        mode: event.target.value as NonNullable<
                          ConnectorEventSubscriptionView["sessions"]
                        >["mode"],
                      })
                    }
                  >
                    {MODES.map((mode) => (
                      <option key={mode} value={mode}>
                        {mode === "bound"
                          ? t("connectorDialog.eventsModeBound")
                          : mode === "ids"
                            ? t("connectorDialog.eventsModeIds")
                            : t("connectorDialog.eventsModeAll")}
                      </option>
                    ))}
                  </select>
                </label>
                {sub.sessions?.mode === "ids" ? (
                  <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                    {t("connectorDialog.eventsIds")}
                    <input
                      type="text"
                      aria-label={t("connectorDialog.eventsIds")}
                      className={inputClassName}
                      value={idsDraft[index] ?? formatSubscriptionList(sub.sessions?.ids)}
                      onChange={(event) => {
                        setIdsDraft((prev) => ({ ...prev, [index]: event.target.value }));
                        updateSessions(index, { ids: parseSubscriptionList(event.target.value) });
                      }}
                    />
                  </label>
                ) : null}
              </div>
              <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                {t("connectorDialog.eventsTypes")}
                <input
                  type="text"
                  aria-label={t("connectorDialog.eventsTypes")}
                  className={inputClassName}
                  placeholder={t("connectorDialog.eventsTypesPlaceholder")}
                  value={typesDraft[index] ?? formatSubscriptionList(sub.types)}
                  onChange={(event) => {
                    setTypesDraft((prev) => ({ ...prev, [index]: event.target.value }));
                    updateAt(index, { types: parseSubscriptionList(event.target.value) });
                  }}
                />
              </label>
              <span className="text-xs text-muted-foreground">
                {t("connectorDialog.eventsTypesHint")}
              </span>
            </div>
          ))}
          <Button variant="secondary" size="sm" className="self-start" onClick={addSubscription}>
            {t("connectorDialog.eventsAdd")}
          </Button>
        </>
      ) : null}
    </section>
  );
}
