// P2-1B：技能详情对话框（工作台「技能」页签）。
//
// 复用独立页面（/runtime/skills）的 `SkillDetailPanel`：详情由
// `GET /api/runtime/skills/{name}` 现取，拉取失败如实报错、不回退到列表快照。
// 外壳沿用设置域对话框约定：portal 挂载、Esc / 遮罩关闭、关闭后焦点回位。

import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { useDialogLifecycle } from "@/components/ui/use-dialog-lifecycle";
import { useFocusRestore } from "@/hooks/workspace/use-focus-restore";
import { SkillDetailPanel } from "@/pages/skills/detail";
import type { RuntimeSkill } from "@/types/runtime";

type WorkspaceSkillDetailDialogProps = {
  skill: RuntimeSkill;
  onClose: () => void;
};

export function WorkspaceSkillDetailDialog({
  skill,
  onClose,
}: WorkspaceSkillDetailDialogProps) {
  const { t } = useTranslation("skills");

  useDialogLifecycle(true, onClose);
  useFocusRestore(true);

  if (typeof document === "undefined") {
    return null;
  }

  return createPortal(
    <DialogOverlay className="z-[110]" onDismiss={onClose}>
      <DialogPanel
        aria-label={t("workspaceTab.dialogAriaLabel")}
        aria-modal="true"
        className="max-w-[46rem]"
        data-testid="workspace-skills-dialog"
        elevation="lg"
        role="dialog"
      >
        <div className="flex items-center justify-between border-b border-border px-3 py-2.5 sm:px-4">
          <h2 className="text-sm font-semibold">{t("detail.title")}</h2>
          <Button
            data-testid="workspace-skills-dialog-close"
            size="sm"
            variant="secondary"
            onClick={onClose}
          >
            {t("workspaceTab.closeDialog")}
          </Button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-3 py-3 sm:px-4">
          <SkillDetailPanel key={skill.name} skill={skill} onClose={onClose} />
        </div>
      </DialogPanel>
    </DialogOverlay>,
    document.body,
  );
}
