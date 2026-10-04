// `@` 引用候选分组：工作区文件（异步，可能为空/错误态）+ 线程交付物兜底。
//
// 从 main-section 提取（500 非空行门禁）：宿主只关心「拼好的一组引用」，
// 文案本地化与产物组映射在这里收口。

import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import { type Artifact } from "@/data/mock";
import { type ComposerReferenceGroup } from "@/lib/composer-menu";
import { artifactReferenceGroup } from "@/lib/composer-references";

export function useComposerReferenceGroups(
  fileGroup: ComposerReferenceGroup | null,
  artifacts: readonly Artifact[],
): ComposerReferenceGroup[] {
  const { t } = useTranslation("workspace");
  return useMemo(() => {
    const groups: ComposerReferenceGroup[] = [];
    if (fileGroup) {
      groups.push(fileGroup);
    }
    const files = artifactReferenceGroup(artifacts, t("composer.references.files"));
    if (files) {
      groups.push(files);
    }
    return groups;
  }, [artifacts, fileGroup, t]);
}
