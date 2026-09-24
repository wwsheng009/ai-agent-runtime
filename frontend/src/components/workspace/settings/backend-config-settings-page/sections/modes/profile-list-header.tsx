// Profiles 列表页头（纯展示）：标题 / 汇总胶囊 / 刷新与新建 / 过滤输入。
//
// 抽出的理由：profiles.tsx 只保留状态机（列表数据、对话框、编辑器），
// 页头拆成无状态组件后既压缩宿主文件，也让「汇总数字口径」集中一处。

import { RefreshCcwIcon, UserCogIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";

import { SettingsAddButton } from "../../../settings-add-button";
import { SummaryPill } from "../../primitives";
import { ProfileTextField } from "../../profiles/profile-form-fields";

export type ProfileListHeaderProps = {
  /** 列表总条数（不是过滤后的可见条数）。 */
  count: number;
  /** 当前默认 profile 名（空串表示没有默认）。 */
  defaultProfile: string;
  /** 校验失败（valid=false）的条数；0 时不显示告警胶囊。 */
  errorCount: number;
  filter: string;
  busy: boolean;
  isLoading: boolean;
  onCreate: () => void;
  onFilterChange: (value: string) => void;
  onRefresh: () => void;
};

export function ProfileListHeader({
  busy,
  count,
  defaultProfile,
  errorCount,
  filter,
  isLoading,
  onCreate,
  onFilterChange,
  onRefresh,
}: ProfileListHeaderProps) {
  const { t } = useTranslation("runtimeConfig");

  return (
    <div className="rounded-panel border border-border bg-surface-softer p-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="inline-flex size-6 items-center justify-center rounded-field border border-border bg-surface-solid text-accent-primary">
              <UserCogIcon size={13} />
            </span>
            <div className="text-base font-semibold text-foreground">{t("profiles.title")}</div>
            <SummaryPill label={t("profiles.title")} value={t("profiles.list.count", { count })} />
            {defaultProfile ? (
              <SummaryPill label={t("profiles.list.default")} value={defaultProfile} />
            ) : null}
            {errorCount > 0 ? (
              <SummaryPill label={t("profiles.list.statusError")} value={String(errorCount)} />
            ) : null}
          </div>
          <p className="mt-1.5 max-w-[46rem] text-xs leading-5 text-muted-foreground">
            {t("profiles.description")}
          </p>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <Button
            disabled={isLoading || busy}
            size="sm"
            type="button"
            variant="secondary"
            onClick={onRefresh}
          >
            <RefreshCcwIcon size={14} />
            {t("profiles.list.refresh")}
          </Button>
          <SettingsAddButton
            data-testid="profiles-create-open"
            disabled={busy}
            label={t("profiles.list.create")}
            size="sm"
            type="button"
            onClick={onCreate}
          />
        </div>
      </div>

      <div className="mt-3 max-w-sm">
        <ProfileTextField
          description={t("profiles.list.filterHint")}
          label={t("profiles.list.filter")}
          value={filter}
          onChange={onFilterChange}
        />
      </div>
    </div>
  );
}
