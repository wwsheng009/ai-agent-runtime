// 文本类 skill 的 `$` 提及候选：运行时技能目录 → composer 引用分组。
//
// 与 `/skill` 弹窗同源（同一份 runtimeSkills）；目录未就绪/失败时以状态行
// 如实呈现，不伪造候选；workflow 技能过滤在 lib/composer-skill-mentions。

import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import { type ComposerReferenceGroup } from "@/lib/composer-menu";
import { skillMentionReferenceGroup } from "@/lib/composer-skill-mentions";
import { type RuntimeSkillCatalog } from "@/types/runtime";

export function useComposerSkillGroups(
  runtimeSkills: RuntimeSkillCatalog | null | undefined,
  runtimeSkillsError: string | null,
): ComposerReferenceGroup[] {
  const { t } = useTranslation("workspace");
  return useMemo(() => {
    const status = runtimeSkills ? "ready" : runtimeSkillsError ? "error" : "loading";
    const group = skillMentionReferenceGroup(
      runtimeSkills,
      {
        group: t("composer.references.skills"),
        empty: t("composer.references.skillsEmpty"),
        loading: t("composer.references.skillsLoading"),
        error: t("composer.references.skillsError"),
      },
      status,
    );
    return group ? [group] : [];
  }, [runtimeSkills, runtimeSkillsError, t]);
}
