// S2b（docs/bots/13-event-subscription-routing-design.md §3.4/§6.1）：声明式事件
// 订阅编辑器——会话选择器 × 事件类型（支持族通配）× 投递形态。
//
// 结构（2026-10-15 与微前端同口径，见 backend/cmd/aicli/commands/web/js/connectors.js）：
//   * 事件类型按投递形态分组多选（types↔deliver 关联由结构表达，折叠面板汇总已选）；
//   * 投递形态由所选类型自动推导：唯一解锁定、多形态收敛选项、跨形态提示一键拆分；
//   * 相容性告警（形态不匹配会被后端静默忽略）；勾选任一类型自动打开总开关。
//
// 纯受控组件（value + onChange），提交合并在 ./shared.mergeEventsIntoPayload；
// 编辑态专属（与 D6 路由同一约定：策略在创建后配置）。ids/自定义类型采用
// 「草稿文本 + 差分合并」：输入过程中不会因即时解析吞掉正在输入的分隔符，
// 也不会丢失既有词表外值（往返不丢）。

import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import type { ConnectorEventSubscriptionView } from "@/types/runtime";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

import {
  EVENT_TYPE_FAMILY,
  WILDCARD_FAMILY,
  customPanelTypes,
  mergeCustomTokens,
  resolveSubscriptionDeliver,
  scanSubscriptionTypes,
  splitSubscriptionModel,
  toggleType,
  typesSummary,
  visibleEventTypeSections,
  withResolvedDeliver,
  type SubscriptionDeliver,
} from "./subscription-forms";
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

const DELIVER_LABEL_KEYS = {
  notice: "connectorDialog.eventsDeliverNotice",
  content: "connectorDialog.eventsDeliverContent",
  digest: "connectorDialog.eventsDeliverDigest",
  stream: "connectorDialog.eventsDeliverStream",
} as const satisfies Record<SubscriptionDeliver, string>;

/** 形态分组标题（键 = EVENT_TYPE_FORMS 的值 join("/")；空串 = 未接线）。 */
type SectionLabelKey =
  | "connectorDialog.eventsGroupContent"
  | "connectorDialog.eventsGroupStream"
  | "connectorDialog.eventsGroupNotice"
  | "connectorDialog.eventsGroupNoticeDigest"
  | "connectorDialog.eventsGroupDigest"
  | "connectorDialog.eventsGroupUnwired";

const SECTION_LABEL_KEYS: Record<string, SectionLabelKey | undefined> = {
  content: "connectorDialog.eventsGroupContent",
  stream: "connectorDialog.eventsGroupStream",
  notice: "connectorDialog.eventsGroupNotice",
  "notice/digest": "connectorDialog.eventsGroupNoticeDigest",
  digest: "connectorDialog.eventsGroupDigest",
  "": "connectorDialog.eventsGroupUnwired",
};

interface TypeOptionProps {
  name: string;
  hint?: string;
  checked: boolean;
  onToggle: (checked: boolean) => void;
}

/** 单个多选项：原生 checkbox（右侧小字 = 族名/词表外）。 */
function TypeOption({ name, hint, checked, onToggle }: TypeOptionProps) {
  return (
    <label className="flex items-center gap-2 text-xs text-foreground">
      <input
        type="checkbox"
        aria-label={name}
        checked={checked}
        onChange={(event) => onToggle(event.target.checked)}
      />
      <span>{name}</span>
      {hint ? (
        <span className="ml-auto text-[11px] text-muted-foreground">{hint}</span>
      ) : null}
    </label>
  );
}

export function SubscriptionEditor({ value, onChange }: SubscriptionEditorProps) {
  const { t } = useTranslation("connectors");
  // 输入草稿（按行索引）：解析后的数组不丢失用户正在输入的分隔符。
  const [idsDraft, setIdsDraft] = useState<Record<number, string>>({});
  const [customDraft, setCustomDraft] = useState<Record<number, string>>({});

  // 回填/外部变更时把「当前形态不被所选类型允许」的既有值收敛到推导结果
  // （与微前端 renderEventSubRows 的 refreshDeliverSelect 同语义）；无变化不回写。
  useEffect(() => {
    const next = value.subscriptions.map(withResolvedDeliver);
    if (next.some((item, index) => item !== value.subscriptions[index])) {
      onChange({ ...value, subscriptions: next });
    }
  }, [value, onChange]);

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

  /** 类型集合变更：写回 types 并收敛形态；勾选类型时总开关未开则自动打开。 */
  function applyTypes(index: number, types: string[], autoEnable: boolean) {
    const subscriptions = value.subscriptions.map((item, i) =>
      i === index ? withResolvedDeliver({ ...item, types }) : item,
    );
    onChange({ enabled: autoEnable ? true : value.enabled, subscriptions });
  }

  function toggleRowType(index: number, name: string, checked: boolean) {
    const sub = value.subscriptions[index];
    applyTypes(index, toggleType(sub.types, name, checked), checked);
  }

  function changeCustomTypes(index: number, text: string) {
    const sub = value.subscriptions[index];
    const previous = parseSubscriptionList(customDraft[index] ?? "");
    const next = parseSubscriptionList(text);
    setCustomDraft((state) => ({ ...state, [index]: text }));
    applyTypes(index, mergeCustomTokens(sub.types, previous, next), false);
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
    setCustomDraft({});
    replaceSubscriptions(value.subscriptions.filter((_, i) => i !== index));
  }

  /** 一键拆分：按形态分组，词表外/未接线类型留在首条（id 追加 -2/-3）。 */
  function splitSubscription(index: number) {
    const parts = splitSubscriptionModel(value.subscriptions[index]);
    if (parts.length <= 1) {
      return;
    }
    setIdsDraft({});
    setCustomDraft({});
    replaceSubscriptions([
      ...value.subscriptions.slice(0, index),
      ...parts,
      ...value.subscriptions.slice(index + 1),
    ]);
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
      {value.subscriptions.length === 0 ? (
        <p className="text-xs text-muted-foreground">{t("connectorDialog.eventsEmpty")}</p>
      ) : null}
      {value.subscriptions.map((sub, index) => {
        const deliver = sub.deliver ?? "notice";
        const resolved = resolveSubscriptionDeliver(sub.types, deliver);
        const visibleSections = visibleEventTypeSections(resolved.deliver, sub.types);
        const scan = scanSubscriptionTypes(sub.types, deliver);
        const issues = [
          ...scan.conflicts.map((item) =>
            t("connectorDialog.eventsConflictNeeds", {
              type: item.type,
              forms: item.forms.join("/"),
            }),
          ),
          ...scan.unwired.map((type) => t("connectorDialog.eventsUnwired", { type })),
        ];
        const summary = typesSummary(sub.types);
        const summaryText =
          summary.total === 0
            ? t("connectorDialog.eventsTypesSummaryNone")
            : t("connectorDialog.eventsTypesSummarySelected", {
                total: String(summary.total),
                names: summary.shown.join(t("connectorDialog.eventsTypesSummarySep")),
                more: summary.more ? t("connectorDialog.eventsTypesSummaryMore") : "",
              });
        const customTypes = customPanelTypes(sub.types);
        const mode = sub.sessions?.mode ?? "bound";
        return (
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
                {t("connectorDialog.eventsMode")}
                <select
                  aria-label={t("connectorDialog.eventsMode")}
                  className={inputClassName}
                  value={mode}
                  onChange={(event) =>
                    updateSessions(index, {
                      mode: event.target.value as NonNullable<
                        ConnectorEventSubscriptionView["sessions"]
                      >["mode"],
                    })
                  }
                >
                  {MODES.map((item) => (
                    <option key={item} value={item}>
                      {item === "bound"
                        ? t("connectorDialog.eventsModeBound")
                        : item === "ids"
                          ? t("connectorDialog.eventsModeIds")
                          : t("connectorDialog.eventsModeAll")}
                    </option>
                  ))}
                </select>
              </label>
              <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                {t("connectorDialog.eventsDeliver")}
                <select
                  aria-label={t("connectorDialog.eventsDeliver")}
                  className={inputClassName}
                  value={resolved.deliver}
                  disabled={resolved.locked}
                  onChange={(event) =>
                    updateAt(index, { deliver: event.target.value as SubscriptionDeliver })
                  }
                >
                  {resolved.options.map((option) => (
                    <option key={option} value={option}>
                      {t(DELIVER_LABEL_KEYS[option])}
                    </option>
                  ))}
                </select>
                <span className="text-[11px] text-muted-foreground">
                  {resolved.locked
                    ? t("connectorDialog.eventsDeliverDerived")
                    : resolved.free
                      ? ""
                      : t("connectorDialog.eventsDeliverOptions", {
                          forms: resolved.options.join("/"),
                        })}
                </span>
              </label>
            </div>
            <div className="grid gap-2 sm:grid-cols-2">
              <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                {t("connectorDialog.eventsIds")}
                <input
                  type="text"
                  aria-label={t("connectorDialog.eventsIds")}
                  className={inputClassName}
                  disabled={mode !== "ids"}
                  placeholder={
                    mode === "ids"
                      ? t("connectorDialog.eventsIdsPlaceholder")
                      : t("connectorDialog.eventsIdsInactive")
                  }
                  value={idsDraft[index] ?? formatSubscriptionList(sub.sessions?.ids)}
                  onChange={(event) => {
                    setIdsDraft((prev) => ({ ...prev, [index]: event.target.value }));
                    updateSessions(index, { ids: parseSubscriptionList(event.target.value) });
                  }}
                />
              </label>
              <div className="flex items-end">
                <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
                  <input
                    type="checkbox"
                    aria-label={t("connectorDialog.eventsRowEnabled")}
                    checked={sub.enabled !== false}
                    onChange={(event) => updateAt(index, { enabled: event.target.checked })}
                  />
                  {t("connectorDialog.eventsRowEnabled")}
                </label>
              </div>
            </div>
            <div className="flex flex-col gap-1.5">
              <span className="text-xs text-muted-foreground">
                {t("connectorDialog.eventsTypes")}
              </span>
              <details className="rounded-control border border-border bg-surface px-2.5 py-2">
                <summary className="cursor-pointer select-none text-xs text-foreground">
                  {summaryText}
                </summary>
                <div className="mt-2 flex max-h-56 flex-col gap-1.5 overflow-y-auto">
                  {visibleSections.map((section) => (
                    <div key={section.form} className="flex flex-col gap-1">
                      <span className="text-[11px] font-medium uppercase tracking-[0.12em] text-muted-foreground">
                        {t(SECTION_LABEL_KEYS[section.form] ?? "connectorDialog.eventsGroupUnwired")}
                      </span>
                      {section.types.map((name) => {
                        const family = EVENT_TYPE_FAMILY[name];
                        return (
                          <TypeOption
                            key={name}
                            name={name}
                            hint={
                              family === WILDCARD_FAMILY
                                ? t("connectorDialog.eventsFamilyWildcard")
                                : family
                            }
                            checked={(sub.types ?? []).includes(name)}
                            onToggle={(checked) => toggleRowType(index, name, checked)}
                          />
                        );
                      })}
                    </div>
                  ))}
                  {customTypes.length ? (
                    <div className="flex flex-col gap-1">
                      <span className="text-[11px] font-medium uppercase tracking-[0.12em] text-muted-foreground">
                        {t("connectorDialog.eventsGroupCustom")}
                      </span>
                      {customTypes.map((name) => (
                        <TypeOption
                          key={name}
                          name={name}
                          hint={t("connectorDialog.eventsFamilyUnknown")}
                          checked
                          onToggle={(checked) => toggleRowType(index, name, checked)}
                        />
                      ))}
                    </div>
                  ) : null}
                </div>
              </details>
              <input
                type="text"
                aria-label={t("connectorDialog.eventsCustomLabel")}
                className={inputClassName}
                placeholder={t("connectorDialog.eventsCustomPlaceholder")}
                value={customDraft[index] ?? ""}
                onChange={(event) => changeCustomTypes(index, event.target.value)}
              />
              {issues.length ? (
                <span className="text-xs text-accent-orange">{"⚠ " + issues.join("; ")}</span>
              ) : null}
              <span className="text-xs text-muted-foreground">
                {t("connectorDialog.eventsTypesPickHint")}
              </span>
            </div>
            <div className="flex items-center justify-between gap-2">
              {scan.conflicts.length > 0 ? (
                <Button variant="secondary" size="sm" onClick={() => splitSubscription(index)}>
                  {t("connectorDialog.eventsSplit")}
                </Button>
              ) : (
                <span />
              )}
              <Button variant="ghost" size="sm" onClick={() => removeSubscription(index)}>
                {t("connectorDialog.eventsRemove")}
              </Button>
            </div>
          </div>
        );
      })}
      <Button variant="secondary" size="sm" className="self-start" onClick={addSubscription}>
        {t("connectorDialog.eventsAdd")}
      </Button>
    </section>
  );
}