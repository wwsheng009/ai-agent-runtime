// D4「重新扫码=替换凭据」区块（docs/bots/09-multi-instance-binding.md §6）：
// 编辑对话框里的小节（提示 + 入口按钮 + 成功回执）。渲染仅此用途，拆出以控制
// connector-dialog.tsx 的行数预算（P0-2）。

import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";

interface RescanSectionProps {
  /** 是否显示（= 编辑态且通道声明了 kind=qr 动作）。 */
  available: boolean;
  /** 扫码确认并回填后的成功提示（一次性；关闭面板后保留）。 */
  done: boolean;
  onStart: () => void;
}

export function RescanSection({ available, done, onStart }: RescanSectionProps) {
  const { t } = useTranslation("connectors");
  if (!available) {
    return null;
  }
  return (
    <section className="flex flex-col gap-2 rounded-card border border-accent-primary-border bg-accent-primary-soft/40 px-3 py-2.5">
      <p className="text-xs text-muted-foreground">{t("dialog.rescanHint")}</p>
      <Button variant="secondary" size="sm" className="self-start" onClick={onStart}>
        {t("dialog.rescanLogin")}
      </Button>
      {done ? <p className="text-xs text-accent-primary">{t("dialog.rescanDone")}</p> : null}
    </section>
  );
}
