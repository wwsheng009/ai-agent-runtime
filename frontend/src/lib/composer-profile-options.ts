// Batch 12：`/profile` 的候选项组装（运行时 profile 目录 → 菜单 / 弹窗可搬运的数据）。
//
// 边界：纯映射目录到命令候选，不读网络、不缓存、不推断：
// - 目录未就绪（null / 空数组）时返回空集合，菜单与弹窗进入空态；
// - 菜单候选只收**可解析**（`valid`）的 profile——把解析失败的 profile 放进
//   菜单等于承诺一次注定失败的切换；它们仍参与存在性判定（`candidates` 全集），
//   用户手写 ref 时会得到「存在但不可用 + 原因」的如实回执，而不是「未找到」；
// - `ref` 是发给后端的值（稳定引用），`label` 是目录里的可读名，`layer` 是数据原文。

import type { ComposerCommandOption } from "@/lib/composer-commands";
import type { RuntimeProfileListResponse } from "@/types/runtime";

/** 一个候选 profile 的只读投影（目录原文，不翻译、不美化）。 */
export type ComposerProfileCandidate = {
  /** 稳定引用（发给 `set_profile` 的值）。 */
  ref: string;
  /** 目录里的可读名（回执展示用；缺失时回落 ref）。 */
  label: string;
  /** 所在层（user / project / builtin …，数据原文）。 */
  layer: string;
  /** 是否为全局默认 profile。 */
  isDefault: boolean;
  /** profile.yaml 可解析；false 时 `invalidReason` 给出原因。 */
  valid: boolean;
  /** 不可解析的原因（`valid=true` 时为空串）。 */
  invalidReason: string;
};

/** 目录 → 候选全集（含不可解析项，按目录顺序，ref 去重）。 */
export function composerProfileCandidates(
  catalog: RuntimeProfileListResponse | null | undefined,
): ComposerProfileCandidate[] {
  if (!catalog || !Array.isArray(catalog.profiles)) {
    return [];
  }
  const seen = new Set<string>();
  const candidates: ComposerProfileCandidate[] = [];
  for (const entry of catalog.profiles) {
    const ref = (entry.ref ?? "").trim();
    if (ref.length === 0 || seen.has(ref)) {
      continue;
    }
    seen.add(ref);
    const valid = entry.valid !== false;
    candidates.push({
      ref,
      label: (entry.name ?? "").trim() || ref,
      layer: (entry.layer ?? "").trim(),
      isDefault: entry.isDefault === true,
      valid,
      invalidReason: valid ? "" : (entry.error ?? "").trim(),
    });
  }
  return candidates;
}

/**
 * 目录 → `/profile` 的第二级候选（扁平；只含可解析项）。
 * `value` 是 ref（选中后作为命令参数交给执行器），`label` 是可读名，
 * `description` 是层名（数据原文）。
 */
export function composerProfileCommandOptions(
  catalog: RuntimeProfileListResponse | null | undefined,
): ComposerCommandOption[] {
  return composerProfileCandidates(catalog)
    .filter((candidate) => candidate.valid)
    .map((candidate) => ({
      value: candidate.ref,
      label: candidate.label,
      ...(candidate.layer ? { description: candidate.layer } : {}),
    }));
}

/**
 * ref 解析：精确优先；否则唯一的大小写不敏感匹配；歧义 / 未命中返回 null。
 * 与 `/model` 的 `resolveModelId` 同口径——不猜用户意图，不把歧义静默择一。
 */
export function resolveComposerProfileRef(
  candidates: readonly ComposerProfileCandidate[],
  requested: string,
): ComposerProfileCandidate | null {
  const needle = requested.trim();
  if (needle.length === 0) {
    return null;
  }
  const exact = candidates.find((candidate) => candidate.ref === needle);
  if (exact) {
    return exact;
  }
  const lowered = needle.toLowerCase();
  const matches = candidates.filter(
    (candidate) => candidate.ref.toLowerCase() === lowered,
  );
  return matches.length === 1 ? matches[0] : null;
}
