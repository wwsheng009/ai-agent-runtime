// 新建 profile 三步向导：命名 → 模板 → 层级与确认。
//
// 只负责收集 RuntimeProfileCreateRequest，不发起请求（宿主统一处理 pending/错误），
// 因此本组件可在 vitest 里静态渲染并断言「名称非法时不能进入下一步」。

import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { RuntimeProfileCreateRequest, RuntimeProfileListEntry } from "@/types/runtime";

import { ProfileDialogShell } from "../../profiles/profile-dialog-shell";
import {
  ProfileField,
  ProfileSelectField,
  ProfileTextField,
} from "../../profiles/profile-form-fields";
import { profileNamePattern } from "../../profiles/profile-draft";

const TEMPLATE_KEYS = {
  minimal: "profiles.create.templateMinimal",
  coding: "profiles.create.templateCoding",
  review: "profiles.create.templateReview",
  fromProfile: "profiles.create.templateFromProfile",
} as const;

type TemplateChoice = keyof typeof TEMPLATE_KEYS;

const TEMPLATE_CHOICES: TemplateChoice[] = ["minimal", "coding", "review", "fromProfile"];

const LAYER_VALUES = ["user", "project"] as const;

/** 层级 → 文案键（显式映射，避免动态拼接键）。 */
const LAYER_KEYS = {
  user: "profiles.layers.user",
  project: "profiles.layers.project",
} as const satisfies Record<(typeof LAYER_VALUES)[number], string>;

export type ProfileCreateDialogProps = {
  isSaving: boolean;
  onCancel: () => void;
  onSubmit: (request: RuntimeProfileCreateRequest) => void;
  /** 既有 profile 列表：选择「复制既有 profile」时作为来源。 */
  profiles: RuntimeProfileListEntry[];
};

export function ProfileCreateDialog({
  isSaving,
  onCancel,
  onSubmit,
  profiles,
}: ProfileCreateDialogProps) {
  const { t } = useTranslation("runtimeConfig");
  const [step, setStep] = useState(0);
  const [name, setName] = useState("");
  const [template, setTemplate] = useState<TemplateChoice>("minimal");
  const [fromRef, setFromRef] = useState("");
  const [layer, setLayer] = useState<string>("user");

  const trimmedName = name.trim();
  const nameValid = profileNamePattern.test(trimmedName);
  const fromProfile = template === "fromProfile";
  const fromRefValid = !fromProfile || fromRef.length > 0;
  const canSubmit = nameValid && fromRefValid && !isSaving;

  const stepLabels = [
    t("profiles.create.stepName"),
    t("profiles.create.stepTemplate"),
    t("profiles.create.stepConfirm"),
  ];

  function submit() {
    if (!canSubmit) {
      return;
    }
    onSubmit({
      name: trimmedName,
      layer,
      template: fromProfile ? "" : template,
      fromRef: fromProfile ? fromRef : "",
    });
  }

  return (
    <ProfileDialogShell
      closeLabel={t("profiles.lifecycle.cancel")}
      description={t("profiles.create.description")}
      onClose={onCancel}
      size="md"
      title={t("profiles.create.title")}
      footer={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex flex-wrap items-center gap-1.5">
            {stepLabels.map((label, index) => (
              <Badge
                key={label}
                className={index === step ? "normal-case text-accent-primary" : "normal-case"}
              >
                {label}
              </Badge>
            ))}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button
              data-testid="profiles-create-cancel"
              disabled={isSaving}
              size="sm"
              type="button"
              variant="ghost"
              onClick={onCancel}
            >
              {t("profiles.lifecycle.cancel")}
            </Button>
            {step > 0 ? (
              <Button
                data-testid="profiles-create-back"
                disabled={isSaving}
                size="sm"
                type="button"
                variant="secondary"
                onClick={() => {
                  setStep((current) => current - 1);
                }}
              >
                {t("profiles.create.back")}
              </Button>
            ) : null}
            {step < 2 ? (
              <Button
                data-testid="profiles-create-next"
                disabled={step === 0 && !nameValid}
                size="sm"
                type="button"
                onClick={() => {
                  setStep((current) => current + 1);
                }}
              >
                {t("profiles.create.next")}
              </Button>
            ) : (
              <Button
                data-testid="profiles-create-submit"
                disabled={!canSubmit}
                size="sm"
                type="button"
                onClick={submit}
              >
                {isSaving ? t("profiles.create.creating") : t("profiles.create.submit")}
              </Button>
            )}
          </div>
        </div>
      }
    >
      <div className="space-y-3">
        {step === 0 ? (
          <>
            <ProfileTextField
              description={t("profiles.create.nameHint")}
              disabled={isSaving}
              label={t("profiles.create.name")}
              value={name}
              onChange={setName}
            />
            {trimmedName && !nameValid ? (
              <div className="text-xs leading-5 text-accent-orange" role="alert">
                {t("profiles.create.nameInvalid")}
              </div>
            ) : null}
          </>
        ) : null}

        {step === 1 ? (
          <>
            <ProfileSelectField
              description={t("profiles.create.templateHint")}
              disabled={isSaving}
              label={t("profiles.create.template")}
              options={TEMPLATE_CHOICES.map((choice) => ({
                value: choice,
                label: t(TEMPLATE_KEYS[choice]),
              }))}
              value={template}
              onChange={(value) => {
                setTemplate(value as TemplateChoice);
              }}
            />
            {fromProfile ? (
              <ProfileSelectField
                description={t("profiles.create.fromRefHint")}
                disabled={isSaving || profiles.length === 0}
                label={t("profiles.create.fromRef")}
                options={profiles.map((entry) => ({ value: entry.ref, label: entry.name }))}
                value={fromRef}
                onChange={setFromRef}
              />
            ) : null}
          </>
        ) : null}

        {step === 2 ? (
          <>
            <ProfileSelectField
              description={t("profiles.create.layerHint")}
              disabled={isSaving}
              label={t("profiles.create.layer")}
              options={LAYER_VALUES.map((value) => ({
                value,
                label: t(LAYER_KEYS[value]),
              }))}
              value={layer}
              onChange={setLayer}
            />
            <ProfileField label={t("profiles.create.stepConfirm")}>
              <div className="text-sm leading-6 text-foreground" role="status">
                {t("profiles.create.summary", { name: trimmedName, layer })}
              </div>
            </ProfileField>
          </>
        ) : null}
      </div>
    </ProfileDialogShell>
  );
}
