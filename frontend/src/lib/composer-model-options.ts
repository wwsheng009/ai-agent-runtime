// P2-7 子片 3：`/model` 的候选项组装（宿主真实运行时目录 → 菜单 / 弹窗可搬运的数据）。
//
// 边界：这里只做「目录 → 候选」的纯映射，不读网络、不缓存、不推断模型能力：
// - 目录未就绪（null / 无 provider / 无模型）时返回空集合，菜单与弹窗据此进入空态，
//   **不补占位项、不伪造默认模型**；
// - 模型名与 provider 名一律保持数据原文（不翻译、不美化、不截断）；
// - 弹窗按 provider 分组保留全部行；命令候选是扁平列表，若同一模型 id 出现在多个
//   provider 下则只保留首条——候选项的身份就是模型 id（选中后作为命令参数），
//   重复项会在菜单里产生同一 id。差异不隐藏：弹窗仍按 provider 完整列出行。

import type { ComposerCommandOption } from "@/lib/composer-commands";
import type { RuntimeModelsResponse } from "@/lib/runtime-api";

export type ComposerModelCatalogGroup = {
  /** provider 名（数据原文）。 */
  provider: string;
  /** 该 provider 下的模型名（去空行、去组内重复，保持目录顺序）。 */
  models: readonly string[];
};

/**
 * 目录 → 按 provider 分组的只读视图。
 * 空 provider（无模型）不产出空分组；provider 顺序沿用目录顺序。
 */
export function composerModelCatalogGroups(
  catalog: RuntimeModelsResponse | null | undefined,
): ComposerModelCatalogGroup[] {
  if (!catalog) {
    return [];
  }
  return catalog.providers
    .map((provider) => ({
      provider: provider.name,
      models: [...new Set(provider.models.filter((model) => model.trim().length > 0))],
    }))
    .filter((group) => group.models.length > 0);
}

/**
 * 目录 → `/model` 的第二级候选（扁平）。
 * `value` 是模型 id（选中后作为命令参数回填执行器），`description` 是所属 provider。
 */
export function composerModelCommandOptions(
  catalog: RuntimeModelsResponse | null | undefined,
): ComposerCommandOption[] {
  const seen = new Set<string>();
  const options: ComposerCommandOption[] = [];
  for (const group of composerModelCatalogGroups(catalog)) {
    for (const model of group.models) {
      if (seen.has(model)) {
        continue;
      }
      seen.add(model);
      options.push({
        value: model,
        label: model,
        description: group.provider,
      });
    }
  }
  return options;
}

/**
 * 目录中的全部模型 id（`/model <id>` 的校验集合）。
 * 与候选项口径一致：只报目录里真实存在的模型，不做前缀 / 模糊推断。
 */
export function composerModelIds(
  catalog: RuntimeModelsResponse | null | undefined,
): string[] {
  return [...new Set(composerModelCatalogGroups(catalog).flatMap((group) => group.models))];
}
