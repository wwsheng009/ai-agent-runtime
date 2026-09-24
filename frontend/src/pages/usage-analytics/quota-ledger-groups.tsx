// FR-13：账本「按 Profile 分组」子视图（从 quota.tsx 拆出，守住 500 行上限）。
//
// 口径边界：
// - `groups === null` 表示响应未携带分组数据（未请求分组 / 旧后端忽略 group_by），
//   如实提示「未返回分组」，不伪造空分组；
// - `groups === []` 是真实空态（当前筛选下无可聚合分组），两者呈现必须不同；
// - 组顺序保持后端排序（total_tokens 降序 → profile 升序），前端不重排；
// - `profile === ""` 为「未归属」组（历史行 / 未绑定 profile 的会话）。

import type { UsageLedgerGroup } from "@/types/runtime";
import { useTranslation } from "react-i18next";

import { formatNumber } from "./format";

export function LedgerProfileGroups({
  groups,
  groupedTotal,
}: {
  groups: UsageLedgerGroup[] | null;
  groupedTotal: number | null;
}) {
  const { t } = useTranslation("usageAnalytics");

  if (groups === null) {
    return (
      <p className="mt-3 text-xs text-muted-foreground">{t("quota.ledger.groups.unavailable")}</p>
    );
  }

  return (
    <div className="mt-3">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h4 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          {t("quota.ledger.groups.title")}
        </h4>
        {groupedTotal !== null ? (
          <p className="text-xs text-muted-foreground">
            {t("quota.ledger.groups.subtitle", { value: formatNumber(groupedTotal) })}
          </p>
        ) : null}
      </div>
      {groups.length > 0 ? (
        <div className="mt-2 overflow-x-auto rounded-panel border border-border">
          <table className="w-full text-xs">
            <thead className="bg-surface-softer text-left text-muted-foreground">
              <tr>
                <th scope="col" className="px-2 py-1.5 font-medium">
                  {t("quota.ledger.groups.columns.profile")}
                </th>
                <th scope="col" className="px-2 py-1.5 font-medium">
                  {t("quota.ledger.groups.columns.requests")}
                </th>
                <th scope="col" className="px-2 py-1.5 font-medium">
                  {t("quota.ledger.groups.columns.failures")}
                </th>
                <th scope="col" className="px-2 py-1.5 font-medium">
                  {t("quota.ledger.groups.columns.inputTokens")}
                </th>
                <th scope="col" className="px-2 py-1.5 font-medium">
                  {t("quota.ledger.groups.columns.outputTokens")}
                </th>
                <th scope="col" className="px-2 py-1.5 font-medium">
                  {t("quota.ledger.groups.columns.totalTokens")}
                </th>
              </tr>
            </thead>
            <tbody>
              {groups.map((group) => (
                <tr key={group.profile} className="border-t border-border">
                  <td className="px-2 py-1.5">
                    {group.profile || (
                      <span className="text-muted-foreground">
                        {t("quota.ledger.groups.unassigned")}
                      </span>
                    )}
                  </td>
                  <td className="px-2 py-1.5 tabular-nums">{formatNumber(group.requests)}</td>
                  <td className="px-2 py-1.5 tabular-nums">{formatNumber(group.failures)}</td>
                  <td className="px-2 py-1.5 tabular-nums">{formatNumber(group.input_tokens)}</td>
                  <td className="px-2 py-1.5 tabular-nums">{formatNumber(group.output_tokens)}</td>
                  <td className="px-2 py-1.5 tabular-nums">{formatNumber(group.total_tokens)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <p className="mt-2 text-xs text-muted-foreground">{t("quota.ledger.groups.empty")}</p>
      )}
    </div>
  );
}
