// Profile 导入对话框（Batch 13 slice 8 / G5 / D28 / D32 / D33）。
//
// 纪律（与后端契约一一对应）：
//   * 先预演后导入：dry_run 只预演、不落盘；预演未通过时确认按钮保持禁用，
//     用户看到的是 issues 清单而不是一条红错（D28-1）；
//   * 导入绝不自动激活：成功文案明确指向 default/apply 两个独立动作（D28）；
//   * 名称可选：留空用包内声明名；填写则必须与之一致（D32，不一致后端 400）；
//   * 请求与错误处理留在 profiles.tsx，本组件只收集参数并回调宿主。

import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import type { RuntimeProfileImportReport } from "@/types/runtime";

import { editorControlClassName } from "../../../editor-control-class";
import { ProfileDialogShell } from "../../profiles/profile-dialog-shell";
import {
  ProfileField,
  ProfileSelectField,
  ProfileTextField,
} from "../../profiles/profile-form-fields";

const LAYER_VALUES = ["user", "project"] as const;

const LAYER_KEYS = {
  user: "profiles.layers.user",
  project: "profiles.layers.project",
} as const satisfies Record<(typeof LAYER_VALUES)[number], string>;

export type ProfileImportDialogProps = {
  isPreviewing: boolean;
  isSaving: boolean;
  /** 最近一次预演/导入的报告；null 表示还没预演过。 */
  preview: RuntimeProfileImportReport | null;
  onCancel: () => void;
  onPreview: (bundle: File, name: string, layer: string) => void;
  onSubmit: (bundle: File, name: string, layer: string) => void;
};

export function ProfileImportDialog({
  isPreviewing,
  isSaving,
  preview,
  onCancel,
  onPreview,
  onSubmit,
}: ProfileImportDialogProps) {
  const { t } = useTranslation("runtimeConfig");
  const [bundle, setBundle] = useState<File | null>(null);
  const [name, setName] = useState("");
  const [layer, setLayer] = useState<string>("user");

  const busy = isPreviewing || isSaving;
  // 预演通过才允许落盘：valid=false 的报告是「拒绝导入」，不是可忽略的警告。
  const canSubmit = bundle !== null && preview?.valid === true && !busy;

  return (
    <ProfileDialogShell
      closeLabel={t("profiles.lifecycle.cancel")}
      description={t("profiles.transfer.importDescription")}
      onClose={onCancel}
      title={t("profiles.transfer.importTitle")}
      footer={
        <div className="flex flex-wrap items-center justify-end gap-2">
          <Button
            disabled={busy}
            size="sm"
            type="button"
            variant="ghost"
            onClick={onCancel}
          >
            {t("profiles.lifecycle.cancel")}
          </Button>
          <Button
            data-testid="profiles-import-preview"
            disabled={bundle === null || busy}
            size="sm"
            type="button"
            variant="secondary"
            onClick={() => {
              if (bundle) {
                onPreview(bundle, name.trim(), layer);
              }
            }}
          >
            {isPreviewing
              ? t("profiles.transfer.importPreviewing")
              : t("profiles.transfer.importPreview")}
          </Button>
          <Button
            data-testid="profiles-dialog-confirm"
            disabled={!canSubmit}
            size="sm"
            type="button"
            onClick={() => {
              if (bundle) {
                onSubmit(bundle, name.trim(), layer);
              }
            }}
          >
            {isSaving ? t("profiles.lifecycle.submitting") : t("profiles.transfer.importConfirm")}
          </Button>
        </div>
      }
    >
      <div className="space-y-3">
        <ProfileField
          description={t("profiles.transfer.importFileHint")}
          label={t("profiles.transfer.importFile")}
        >
          <input
            accept=".zip,application/zip"
            className={cn(editorControlClassName, "disabled:opacity-60")}
            data-testid="profiles-import-file"
            disabled={busy}
            type="file"
            onChange={(event) => {
              setBundle(event.target.files?.[0] ?? null);
            }}
          />
        </ProfileField>
        <ProfileTextField
          description={t("profiles.transfer.importNameHint")}
          disabled={busy}
          label={t("profiles.transfer.importName")}
          value={name}
          onChange={setName}
        />
        <ProfileSelectField
          disabled={busy}
          label={t("profiles.transfer.importLayer")}
          options={LAYER_VALUES.map((value) => ({ value, label: t(LAYER_KEYS[value]) }))}
          value={layer}
          onChange={setLayer}
        />
        {preview ? <ImportPreviewPanel preview={preview} /> : null}
      </div>
    </ProfileDialogShell>
  );
}

/** 预演/导入报告面板：通过时给目标与路径清单；未通过时给 issues（都不落盘）。 */
function ImportPreviewPanel({ preview }: { preview: RuntimeProfileImportReport }) {
  const { t } = useTranslation("runtimeConfig");
  const ok = preview.valid;
  return (
    <div
      className={cn(
        "space-y-2 rounded-field border p-3",
        ok ? "border-border bg-surface-solid" : "border-accent-orange/24 bg-surface-solid",
      )}
      data-testid="profiles-import-preview-result"
    >
      <div
        className={cn("text-xs leading-5", ok ? "text-foreground" : "text-accent-orange")}
        role={ok ? "status" : "alert"}
      >
        {ok
          ? t("profiles.transfer.importPreviewOk", { count: preview.fileCount })
          : t("profiles.transfer.importPreviewInvalid", {
              // 与 profiles.editor.issueCount 同形：这两个占位符的类型化取值是 string。
              errorCount: String(preview.errorCount),
              warningCount: String(preview.warningCount),
            })}
      </div>
      {ok && preview.name ? (
        <div className="break-all font-mono text-xs leading-5 text-muted-foreground">
          {t("profiles.transfer.importTarget", { layer: preview.layer, name: preview.name })}
        </div>
      ) : null}
      {preview.paths.length > 0 ? (
        <div>
          <div className="text-xs font-medium text-foreground">
            {t("profiles.transfer.importPaths")}
          </div>
          <ul className="mt-1 max-h-32 space-y-0.5 overflow-auto font-mono text-xs leading-5 text-muted-foreground">
            {preview.paths.map((path) => (
              <li className="break-all" key={path}>
                {path}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {preview.issues.length > 0 ? (
        <div>
          <div className="text-xs font-medium text-foreground">
            {t("profiles.transfer.importIssues")}
          </div>
          <ul className="mt-1 space-y-1 text-xs leading-5">
            {preview.issues.map((issue, index) => (
              <li
                className={cn(
                  "break-all",
                  issue.severity === "error" ? "text-accent-orange" : "text-muted-foreground",
                )}
                key={`${issue.path}-${String(index)}`}
              >
                {issue.path ? `${issue.path}: ` : ""}
                {issue.message}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {ok ? (
        <p className="text-xs leading-5 text-muted-foreground">
          {t("profiles.transfer.importActivationHint")}
        </p>
      ) : null}
    </div>
  );
}
