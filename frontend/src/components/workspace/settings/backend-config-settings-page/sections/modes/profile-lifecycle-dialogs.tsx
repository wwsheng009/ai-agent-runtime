// Profile 生命周期对话框：重命名 / 复制 / 迁移层级 / 删除 / 引用清单。
//
// 每个对话框只收集参数并回调宿主，请求与错误处理留在 profiles.tsx；
// 名称类操作共用 profileNamePattern 校验，避免两处规则漂移。

import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import type { RuntimeProfileListEntry } from "@/types/runtime";

import { ProfileDialogShell } from "../../profiles/profile-dialog-shell";
import {
  ProfileField,
  ProfileSelectField,
  ProfileTextField,
  ProfileToggleRow,
} from "../../profiles/profile-form-fields";
import { profileNamePattern } from "../../profiles/profile-draft";
import { profileLayerLabel } from "../../profiles/profile-i18n";
import type { ProfileReferenceItem } from "./profile-list-utils";

const LAYER_VALUES = ["user", "project"] as const;

const LAYER_KEYS = {
  user: "profiles.layers.user",
  project: "profiles.layers.project",
} as const satisfies Record<(typeof LAYER_VALUES)[number], string>;

/** 引用面条目三分类 → 文案键（后端 references 响应的 blocking/warnings/files）。 */
const REFERENCE_KIND_KEYS = {
  blocking: "profiles.lifecycle.referenceKindBlocking",
  warning: "profiles.lifecycle.referenceKindWarning",
  file: "profiles.lifecycle.referenceKindFile",
} as const satisfies Record<ProfileReferenceItem["kind"], string>;

type DialogBaseProps = {
  entry: RuntimeProfileListEntry;
  isSaving: boolean;
  onCancel: () => void;
};

function DialogFooter({
  confirmDisabled,
  confirmLabel,
  isSaving,
  onCancel,
  onConfirm,
}: {
  confirmDisabled: boolean;
  confirmLabel: string;
  isSaving: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const { t } = useTranslation("runtimeConfig");
  return (
    <div className="flex flex-wrap items-center justify-end gap-2">
      <Button disabled={isSaving} size="sm" type="button" variant="ghost" onClick={onCancel}>
        {t("profiles.lifecycle.cancel")}
      </Button>
      <Button
        data-testid="profiles-dialog-confirm"
        disabled={isSaving || confirmDisabled}
        size="sm"
        type="button"
        onClick={onConfirm}
      >
        {isSaving ? t("profiles.lifecycle.submitting") : confirmLabel}
      </Button>
    </div>
  );
}

/** 重命名：ref 与文件名都会变，对话框里明确提示既有引用需要同步更新。 */
export function ProfileRenameDialog({
  entry,
  isSaving,
  onCancel,
  onSubmit,
}: DialogBaseProps & { onSubmit: (name: string) => void }) {
  const { t } = useTranslation("runtimeConfig");
  const [name, setName] = useState(entry.name);
  const trimmed = name.trim();
  const valid = profileNamePattern.test(trimmed) && trimmed !== entry.name;

  return (
    <ProfileDialogShell
      closeLabel={t("profiles.lifecycle.cancel")}
      description={t("profiles.lifecycle.renameDescription")}
      onClose={onCancel}
      title={t("profiles.lifecycle.renameTitle")}
      footer={
        <DialogFooter
          confirmDisabled={!valid}
          confirmLabel={t("profiles.lifecycle.confirm")}
          isSaving={isSaving}
          onCancel={onCancel}
          onConfirm={() => {
            onSubmit(trimmed);
          }}
        />
      }
    >
      <div className="space-y-3">
        <ProfileField label={t("profiles.basic.ref")}>
          <div className="select-all break-all font-mono text-xs leading-6 text-muted-foreground">
            {entry.ref}
          </div>
        </ProfileField>
        <ProfileTextField
          description={t("profiles.create.nameHint")}
          disabled={isSaving}
          label={t("profiles.lifecycle.newName")}
          value={name}
          onChange={setName}
        />
        {trimmed && !valid ? (
          <div className="text-xs leading-5 text-accent-orange" role="alert">
            {t("profiles.create.nameInvalid")}
          </div>
        ) : null}
      </div>
    </ProfileDialogShell>
  );
}

/** 复制：以既有 profile 为模板创建新的 profile.yaml（可换层级）。 */
export function ProfileDuplicateDialog({
  entry,
  isSaving,
  onCancel,
  onSubmit,
}: DialogBaseProps & { onSubmit: (name: string, layer: string) => void }) {
  const { t } = useTranslation("runtimeConfig");
  const [name, setName] = useState(`${entry.name}-copy`);
  const [layer, setLayer] = useState<string>(entry.layer || "user");
  const trimmed = name.trim();
  const valid = profileNamePattern.test(trimmed) && trimmed !== entry.name;

  return (
    <ProfileDialogShell
      closeLabel={t("profiles.lifecycle.cancel")}
      description={t("profiles.lifecycle.duplicateDescription", { name: entry.name })}
      onClose={onCancel}
      title={t("profiles.lifecycle.duplicateTitle")}
      footer={
        <DialogFooter
          confirmDisabled={!valid}
          confirmLabel={t("profiles.lifecycle.confirm")}
          isSaving={isSaving}
          onCancel={onCancel}
          onConfirm={() => {
            onSubmit(trimmed, layer);
          }}
        />
      }
    >
      <div className="space-y-3">
        <ProfileTextField
          description={t("profiles.create.nameHint")}
          disabled={isSaving}
          label={t("profiles.lifecycle.duplicateName")}
          value={name}
          onChange={setName}
        />
        {trimmed && !valid ? (
          <div className="text-xs leading-5 text-accent-orange" role="alert">
            {t("profiles.create.nameInvalid")}
          </div>
        ) : null}
        <ProfileSelectField
          disabled={isSaving}
          label={t("profiles.lifecycle.targetLayer")}
          options={LAYER_VALUES.map((value) => ({ value, label: t(LAYER_KEYS[value]) }))}
          value={layer}
          onChange={setLayer}
        />
      </div>
    </ProfileDialogShell>
  );
}

/** 迁移层级：ref 会变，需要提示引用同步。 */
export function ProfileMoveDialog({
  entry,
  isSaving,
  onCancel,
  onSubmit,
}: DialogBaseProps & { onSubmit: (layer: string) => void }) {
  const { t } = useTranslation("runtimeConfig");
  const [layer, setLayer] = useState<string>(
    entry.layer === "user" ? "project" : "user",
  );

  return (
    <ProfileDialogShell
      closeLabel={t("profiles.lifecycle.cancel")}
      description={t("profiles.lifecycle.moveDescription", { name: entry.name })}
      onClose={onCancel}
      title={t("profiles.lifecycle.moveTitle")}
      footer={
        <DialogFooter
          confirmDisabled={layer === entry.layer}
          confirmLabel={t("profiles.lifecycle.confirm")}
          isSaving={isSaving}
          onCancel={onCancel}
          onConfirm={() => {
            onSubmit(layer);
          }}
        />
      }
    >
      <div className="space-y-3">
        <ProfileField label={t("profiles.basic.layer")}>
          <div className="text-sm leading-6 text-foreground">
            {profileLayerLabel(t, entry.layer)}
          </div>
        </ProfileField>
        <ProfileSelectField
          disabled={isSaving}
          label={t("profiles.lifecycle.targetLayer")}
          options={LAYER_VALUES.map((value) => ({ value, label: t(LAYER_KEYS[value]) }))}
          value={layer}
          onChange={setLayer}
        />
      </div>
    </ProfileDialogShell>
  );
}

/**
 * 删除：后端返回「仍有引用且未删除」时宿主会把 referenceCount 传回来，
 * 此时展示强制删除开关，避免用户在无提示的情况下丢掉被引用的 profile。
 */
export function ProfileDeleteDialog({
  entry,
  isSaving,
  onCancel,
  onSubmit,
  referenceCount,
}: DialogBaseProps & {
  onSubmit: (force: boolean) => void;
  referenceCount: number;
}) {
  const { t } = useTranslation("runtimeConfig");
  const [force, setForce] = useState(false);
  const blocked = referenceCount > 0;

  return (
    <ProfileDialogShell
      closeLabel={t("profiles.lifecycle.cancel")}
      description={t("profiles.lifecycle.deleteDescription", { name: entry.name })}
      onClose={onCancel}
      title={t("profiles.lifecycle.deleteTitle")}
      footer={
        <DialogFooter
          confirmDisabled={blocked && !force}
          confirmLabel={t("profiles.lifecycle.confirm")}
          isSaving={isSaving}
          onCancel={onCancel}
          onConfirm={() => {
            onSubmit(force);
          }}
        />
      }
    >
      <div className="space-y-3">
        <ProfileField label={t("profiles.basic.ref")}>
          <div className="select-all break-all font-mono text-xs leading-6 text-muted-foreground">
            {entry.ref}
          </div>
        </ProfileField>
        {blocked ? (
          <ProfileToggleRow
            checked={force}
            description={t("profiles.lifecycle.forceDeleteHint", { count: referenceCount })}
            disabled={isSaving}
            label={t("profiles.lifecycle.forceDelete")}
            onChange={setForce}
          />
        ) : null}
      </div>
    </ProfileDialogShell>
  );
}

/** 引用清单：只读展示仍引用该 profile 的会话/任务。 */
export function ProfileReferencesDialog({
  entry,
  isLoading,
  onCancel,
  references,
}: {
  entry: RuntimeProfileListEntry;
  isLoading: boolean;
  onCancel: () => void;
  references: ProfileReferenceItem[];
}) {
  const { t } = useTranslation("runtimeConfig");

  return (
    <ProfileDialogShell
      closeLabel={t("profiles.lifecycle.cancel")}
      description={t("profiles.lifecycle.referencesDescription", { name: entry.name })}
      onClose={onCancel}
      size="md"
      title={t("profiles.lifecycle.referencesTitle")}
      footer={
        <DialogFooter
          confirmDisabled={false}
          confirmLabel={t("profiles.lifecycle.confirm")}
          isSaving={false}
          onCancel={onCancel}
          onConfirm={onCancel}
        />
      }
    >
      {isLoading ? (
        <div className="text-xs leading-5 text-muted-foreground">
          {t("profiles.list.loading")}
        </div>
      ) : references.length === 0 ? (
        <div className="text-xs leading-5 text-muted-foreground">
          {t("profiles.lifecycle.referencesEmpty")}
        </div>
      ) : (
        <ul className="space-y-1.5">
          {references.map((reference) => (
            <li
              key={`${reference.kind}:${reference.text}`}
              className="rounded-card border border-border bg-surface-solid px-2.5 py-2"
            >
              <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <span
                  className={
                    reference.kind === "blocking"
                      ? "font-semibold text-accent-orange"
                      : "font-semibold text-foreground"
                  }
                >
                  {t(REFERENCE_KIND_KEYS[reference.kind])}
                </span>
                <span className="break-all font-mono">{reference.text}</span>
              </div>
            </li>
          ))}
        </ul>
      )}
    </ProfileDialogShell>
  );
}
