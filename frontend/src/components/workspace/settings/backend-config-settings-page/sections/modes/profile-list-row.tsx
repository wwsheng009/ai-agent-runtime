// Profiles 列表行（纯展示）：徽章组 + 操作按钮组 + 描述/错误/句柄信息。
//
// 纪律：
//   * 所有「能不能点」的判断都在这里按 entry 自身字段（writable/valid）收敛，
//     宿主只负责 busy / applyUnavailable / 是否命中默认 profile 这三件外部状态；
//   * 写操作（编辑/设默认/复制/改名/迁移/删除）仅对 writable 条目开放，
//     只读条目（builtin/config）按钮禁用而不是隐藏，用户能看到「为什么不能改」。

import {
  ArrowRightLeftIcon,
  CheckIcon,
  CopyIcon,
  LinkIcon,
  PencilIcon,
  PencilLineIcon,
  Trash2Icon,
  ZapIcon,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import type { RuntimeProfileListEntry } from "@/types/runtime";

import { SettingsIconActionButton } from "../../../settings-action-group";
import { profileLayerLabel } from "../../profiles/profile-i18n";

export type ProfileListRowProps = {
  entry: RuntimeProfileListEntry;
  /** 任一生命周期操作进行中：整行按钮禁用，避免并发写。 */
  busy: boolean;
  /** apply 端点返回 501 后置位（Batch 12 未落地）。 */
  applyUnavailable: boolean;
  /** 该行对应的编辑器已展开。 */
  selected: boolean;
  /** 该行就是当前默认 profile（按 name 或 ref 命中）。 */
  isDefaultTarget: boolean;
  onApply: (entry: RuntimeProfileListEntry) => void;
  onDelete: (entry: RuntimeProfileListEntry) => void;
  onDuplicate: (entry: RuntimeProfileListEntry) => void;
  onMove: (entry: RuntimeProfileListEntry) => void;
  onOpen: (entry: RuntimeProfileListEntry) => void;
  onRename: (entry: RuntimeProfileListEntry) => void;
  onSetDefault: (entry: RuntimeProfileListEntry) => void;
  onShowReferences: (entry: RuntimeProfileListEntry) => void;
};

export function ProfileListRow({
  applyUnavailable,
  busy,
  entry,
  isDefaultTarget,
  selected,
  onApply,
  onDelete,
  onDuplicate,
  onMove,
  onOpen,
  onRename,
  onSetDefault,
  onShowReferences,
}: ProfileListRowProps) {
  const { t } = useTranslation("runtimeConfig");

  return (
    <div
      className={cn(
        "rounded-panel border p-3",
        selected
          ? "border-accent-primary-border bg-accent-primary-soft"
          : "border-border bg-surface-softer",
      )}
      data-profile-ref={entry.ref}
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <span className="text-sm font-semibold text-foreground">{entry.name}</span>
          <Badge className="normal-case">{profileLayerLabel(t, entry.layer)}</Badge>
          <Badge
            className={cn(
              "normal-case",
              entry.valid ? "text-accent-primary" : "text-accent-orange",
            )}
          >
            {entry.valid ? t("profiles.list.statusOk") : t("profiles.list.statusError")}
          </Badge>
          {entry.isDefault ? (
            <Badge className="normal-case">{t("profiles.list.default")}</Badge>
          ) : null}
          {!entry.writable ? (
            <Badge className="normal-case">{t("profiles.list.readOnly")}</Badge>
          ) : null}
        </div>

        <div className="flex flex-nowrap items-center gap-1">
          <SettingsIconActionButton
            data-testid={`profiles-action-edit-${entry.ref}`}
            disabled={busy || !entry.writable}
            label={t("profiles.list.open")}
            onClick={() => {
              onOpen(entry);
            }}
          >
            <PencilIcon size={13} />
          </SettingsIconActionButton>
          <SettingsIconActionButton
            data-testid={`profiles-action-apply-${entry.ref}`}
            disabled={busy || applyUnavailable}
            label={
              applyUnavailable ? t("profiles.list.applyDisabled") : t("profiles.list.apply")
            }
            onClick={() => {
              onApply(entry);
            }}
          >
            <ZapIcon size={13} />
          </SettingsIconActionButton>
          <SettingsIconActionButton
            data-testid={`profiles-action-default-${entry.ref}`}
            disabled={busy || !entry.writable || isDefaultTarget}
            label={t("profiles.list.setDefault")}
            onClick={() => {
              onSetDefault(entry);
            }}
          >
            <CheckIcon size={13} />
          </SettingsIconActionButton>
          <SettingsIconActionButton
            data-testid={`profiles-action-duplicate-${entry.ref}`}
            disabled={busy || !entry.writable}
            label={t("profiles.list.duplicate")}
            onClick={() => {
              onDuplicate(entry);
            }}
          >
            <CopyIcon size={13} />
          </SettingsIconActionButton>
          <SettingsIconActionButton
            data-testid={`profiles-action-rename-${entry.ref}`}
            disabled={busy || !entry.writable}
            label={t("profiles.list.rename")}
            onClick={() => {
              onRename(entry);
            }}
          >
            <PencilLineIcon size={13} />
          </SettingsIconActionButton>
          <SettingsIconActionButton
            data-testid={`profiles-action-move-${entry.ref}`}
            disabled={busy || !entry.writable}
            label={t("profiles.list.move")}
            onClick={() => {
              onMove(entry);
            }}
          >
            <ArrowRightLeftIcon size={13} />
          </SettingsIconActionButton>
          <SettingsIconActionButton
            data-testid={`profiles-action-references-${entry.ref}`}
            disabled={busy}
            label={t("profiles.lifecycle.referencesLoad")}
            onClick={() => {
              onShowReferences(entry);
            }}
          >
            <LinkIcon size={13} />
          </SettingsIconActionButton>
          <SettingsIconActionButton
            data-testid={`profiles-action-delete-${entry.ref}`}
            disabled={busy || !entry.writable}
            label={t("profiles.list.remove")}
            onClick={() => {
              onDelete(entry);
            }}
          >
            <Trash2Icon size={13} />
          </SettingsIconActionButton>
        </div>
      </div>

      {entry.description ? (
        <p className="mt-1.5 max-w-[46rem] text-xs leading-5 text-muted-foreground">
          {entry.description}
        </p>
      ) : null}
      {!entry.valid && entry.error ? (
        <p className="mt-1 text-xs leading-5 text-accent-orange" role="alert">
          {entry.error}
        </p>
      ) : null}
      <div className="mt-1.5 flex flex-wrap items-center gap-3 text-xs leading-5 text-muted-foreground">
        <span className="break-all font-mono">
          {t("profiles.list.refLabel")}: {entry.ref}
        </span>
        <span className="break-all font-mono">
          {t("profiles.list.pathLabel")}: {entry.path || "-"}
        </span>
      </div>
      {selected ? (
        <div className="mt-1.5 text-xs leading-5 text-accent-primary">
          {t("profiles.list.selectedHint", { name: entry.name })}
        </div>
      ) : null}
    </div>
  );
}
