// Profiles 文案/错误码映射（纯函数，无 React 依赖）。
//
// 纪律：
//   * 组件不拼接 issue 文案，只把「issue 码 + detail」交给这里映射成 i18n key，
//     保证 profile-draft.ts 保持纯数据、UI 文案集中可测；
//   * 层级名（user/project/builtin/config）来自后端且可扩展，未知层级原样回显，
//     不吞成空串（否则用户看不到后端新增的层）；
//   * 错误提示统一走 formatProfileError：后端 message 优先，缺省回落调用方文案。

import { type TFunction } from "i18next";

import { isRuntimeApiErrorCode } from "@/api/runtime/shared";

import type { ProfileDraftIssue, ProfileDraftIssueCode, ProfilePromptMode } from "./profile-draft";

/** issue 码 → runtimeConfig.profiles.issues.* 的显式键映射（动态模板无法被 i18next 收窄）。 */
export const PROFILE_ISSUE_KEY = {
  nameRequired: "profiles.issues.nameRequired",
  nameInvalid: "profiles.issues.nameInvalid",
  toolConflict: "profiles.issues.toolConflict",
  promptModeInvalid: "profiles.issues.promptModeInvalid",
  overrideKeyRequired: "profiles.issues.overrideKeyRequired",
  overrideValueRequired: "profiles.issues.overrideValueRequired",
  overrideDuplicate: "profiles.issues.overrideDuplicate",
  overrideNotAllowed: "profiles.issues.overrideNotAllowed",
} as const satisfies Record<ProfileDraftIssueCode, string>;

export const PROFILE_LAYER_KEY = {
  user: "profiles.layers.user",
  project: "profiles.layers.project",
  builtin: "profiles.layers.builtin",
  config: "profiles.layers.config",
} as const;

/** 提示词模式 → 文案键（replace 覆盖 / append 追加）。 */
export const PROFILE_PROMPT_MODE_KEY = {
  replace: "profiles.prompts.modeReplace",
  append: "profiles.prompts.modeAppend",
} as const satisfies Record<ProfilePromptMode, string>;

/**
 * 组件传入的是 i18next 的类型化 t（只接受字面量键），这里收敛成 runtimeConfig 命名空间的
 * TFunction，动态键统一由 translateDynamic 放宽，调用方不必到处 `as never`。
 */
export type Translate = TFunction<"runtimeConfig">;

/** 动态键调用：键来自运行时（层级 / issue 码），显式放宽到 string。 */
type DynamicTranslate = (key: string, params?: Record<string, unknown>) => string;

function translateDynamic(
  t: Translate,
  key: string,
  params?: Record<string, unknown>,
): string {
  return (t as unknown as DynamicTranslate)(key, params);
}

/** 层级标签：命中词典用本地化文案，未知层级回显原值，空值给「未知层级」。 */
export function profileLayerLabel(t: Translate, layer: string) {
  const key = PROFILE_LAYER_KEY[layer as keyof typeof PROFILE_LAYER_KEY] as
    | string
    | undefined;
  if (key) {
    return translateDynamic(t, key);
  }
  return layer || t("profiles.list.layerUnknown");
}

/** issue → 人读文案（detail 作为插值参数，缺失时给空串避免出现 undefined）。 */
export function formatProfileIssue(t: Translate, issue: ProfileDraftIssue) {
  return translateDynamic(t, PROFILE_ISSUE_KEY[issue.code], {
    detail: issue.detail ?? "",
  });
}

/** 后端错误：优先展示 message（含 request_id），否则回落调用方文案。 */
export function formatProfileError(error: unknown, fallback: string) {
  const message = error instanceof Error ? error.message.trim() : "";
  return message ? `${fallback}: ${message}` : fallback;
}

/** 生效清单的来源标签：profile / global / default（后端可扩展，未知值原样回显）。 */
export function profileOriginLabel(t: Translate, origin: string) {
  switch (origin) {
    case "profile":
      return t("profiles.tools.originProfile");
    case "global":
      return t("profiles.tools.originGlobal");
    case "default":
      return t("profiles.tools.originDefault");
    default:
      return origin;
  }
}

/**
 * apply 端点在 Batch 8 后端尚未落地时返回 501 not_implemented；
 * 调用方据此把「失败」降级成「当前构建不支持」的提示，而不是报一条红错。
 */
export function isProfileApplyNotImplemented(error: unknown) {
  return isRuntimeApiErrorCode(error, "not_implemented");
}

/** 保存冲突：后端用 mtime/etag 判冲突，前端只按码提示重新加载。 */
export function isProfileWriteConflict(error: unknown) {
  return (
    isRuntimeApiErrorCode(error, "profile_mtime_conflict") ||
    isRuntimeApiErrorCode(error, "conflict")
  );
}
