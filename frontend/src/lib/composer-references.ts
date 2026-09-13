// P1-4 子片 3：`@` 引用候选的来源适配（宿主数据 → 菜单模型）。
// 只做映射与裁剪，不做过滤/排序（那属于 `lib/composer-menu.ts`）。

import { type Artifact } from "@/data/mock";
import { type ComposerReferenceGroup } from "@/lib/composer-menu";

/** 单组候选上限：超出部分由输入过滤收敛，避免一次性渲染整仓文件。 */
export const COMPOSER_REFERENCE_ARTIFACT_LIMIT = 12;

/** 当前线程交付物 → `@` 文件引用组；无交付物返回 null（空分组不进入菜单）。 */
export function artifactReferenceGroup(
  artifacts: readonly Artifact[],
  label: string,
): ComposerReferenceGroup | null {
  if (artifacts.length === 0) {
    return null;
  }
  return {
    id: "artifacts",
    label,
    items: artifacts
      .slice(0, COMPOSER_REFERENCE_ARTIFACT_LIMIT)
      .map((artifact) => ({
        id: artifact.id,
        label: artifact.name,
        insertText: artifact.path,
        description: artifact.path,
      })),
  };
}
